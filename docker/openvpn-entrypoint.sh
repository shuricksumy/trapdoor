#!/bin/sh
# Runs the VPN servers for the "openvpn" compose service.
# - WireGuard and AmneziaWG (TD_WIREGUARD=file on the agent): brings up every
#   interface the agent describes in $WG_DIR or $AWG_DIR (<id>.json + <id>.conf;
#   AmneziaWG runs in userspace with amneziawg-go), applies changes with
#   `wg syncconf` (nobody is disconnected), adds NAT for its subnet, and
#   writes `wg show <id> dump` to <id>.dump (<id>.error on failure).
# - OpenVPN: the first server ($OPENVPN_CONFIG, waited for on fresh installs)
#   and every server added in trapdoor ($OVPN_SERVERS_DIR/<id>/server.conf,
#   started when it appears, stopped when it is removed).
# - NAT for VPN clients: VPN_NAT_SUBNET=auto (the "server" line of each
#   server.conf, checked at every start) or a CIDR for the first server
#   (added servers always use auto); empty = none. [VPN_NAT_IFACE=eth0]
# - supervises each OpenVPN: when it exits (trapdoor restarts it with SIGTERM
#   through the management interface, or it crashes) it is started again in
#   this container, so the agent sharing this network namespace keeps working.
#   docker stop still stops everything.
set -eu

CONF=${OPENVPN_CONFIG:-/etc/openvpn/server.conf}

if [ ! -c /dev/net/tun ]; then
	mkdir -p /dev/net
	mknod /dev/net/tun c 10 200
	chmod 600 /dev/net/tun
fi

# Access rules from trapdoor (TD_FIREWALL=file): the agent writes the
# rules file, this container (the one with NET_ADMIN) applies it and reports
# "ok <sha256>" or "error <sha256> <message>" in the status file.
FW_RULES=$(dirname "$CONF")/trapdoor-firewall.rules
FW_STATUS=$(dirname "$CONF")/trapdoor-firewall.status
fw_sum=
fw_apply() {
	sum=$(sha256sum "$FW_RULES" | cut -d' ' -f1)
	[ "$sum" = "$fw_sum" ] && return 0
	if out=$(iptables-restore --noflush <"$FW_RULES" 2>&1) &&
		out=$(for c in INPUT FORWARD; do
			iptables -C "$c" -i tun+ -j TD-FWD 2>/dev/null || iptables -I "$c" 1 -i tun+ -j TD-FWD || exit 1
		done 2>&1); then
		echo "ok $sum" >"$FW_STATUS.tmp"
		echo "openvpn: access rules applied"
	else
		echo "error $sum $(echo "$out" | tr '\n' ' ')" >"$FW_STATUS.tmp"
		echo "openvpn: access rules failed: $out"
	fi
	mv "$FW_STATUS.tmp" "$FW_STATUS"
	fw_sum=$sum
}

WG_DIR=${WG_DIR:-/etc/wireguard}
AWG_DIR=${AWG_DIR:-/etc/amnezia/amneziawg}
WG_STATE_ROOT=/run/trapdoor-wg
rm -rf "$WG_STATE_ROOT" # a restarted container has no interfaces or rules yet
wg_iface() { ip route | awk '/^default/ { print $5; exit }'; }
wg_nat() { # add|del <address/bits>
	[ -n "$2" ] || return 0
	ifc=${VPN_NAT_IFACE:-$(wg_iface)}
	if [ "$1" = add ]; then
		iptables -t nat -C POSTROUTING -s "$2" -o "$ifc" -j MASQUERADE 2>/dev/null ||
			iptables -t nat -A POSTROUTING -s "$2" -o "$ifc" -j MASQUERADE
	else
		iptables -t nat -D POSTROUTING -s "$2" -o "$ifc" -j MASQUERADE 2>/dev/null || true
	fi
}
wg_fail() { # <dir> <id> <sum> <message>
	echo "$4" >"$1/$2.error"
	echo "$3" >"$WG_STATE/$2.failed"
	echo "$label $2: $4"
}
wg_sync() { # <dir> <wg tool> <wg-quick tool>: WireGuard or AmneziaWG
	dir=$1 wgbin=$2 quick=$3 label=wireguard
	[ "$wgbin" = awg ] && label=amneziawg
	command -v "$wgbin" >/dev/null || return 0
	WG_STATE=$WG_STATE_ROOT/$wgbin
	mkdir -p "$WG_STATE"
	for conf in "$dir"/*.conf; do
		[ -f "$conf" ] || continue
		id=$(basename "$conf" .conf)
		[ -f "$dir/$id.json" ] || continue # only interfaces trapdoor manages
		sum=$(sha256sum "$conf" | cut -d' ' -f1)
		[ "$sum" = "$(cat "$WG_STATE/$id.failed" 2>/dev/null)" ] && continue # retried when it changes
		addr=$(awk -F' *= *' '$1 == "Address" { print $2; exit }' "$conf")
		old_addr=$(cat "$WG_STATE/$id.addr" 2>/dev/null || true)
		if ! ip link show "$id" >/dev/null 2>&1; then
			out=$("$quick" up "$conf" 2>&1) || { wg_fail "$dir" "$id" "$sum" "$(echo "$out" | tail -1)"; continue; }
			echo "$label $id: up ($addr)"
		elif [ "$sum" != "$(cat "$WG_STATE/$id.sum" 2>/dev/null)" ]; then
			if [ "$addr" != "$old_addr" ]; then
				"$quick" down "$conf" >/dev/null 2>&1 || ip link del "$id" 2>/dev/null || true
				wg_nat del "$old_addr"
				out=$("$quick" up "$conf" 2>&1) || { wg_fail "$dir" "$id" "$sum" "$(echo "$out" | tail -1)"; continue; }
				echo "$label $id: restarted ($addr)"
			else
				out=$("$quick" strip "$conf" >"$WG_STATE/$id.strip" 2>&1 && "$wgbin" syncconf "$id" "$WG_STATE/$id.strip" 2>&1) ||
					{ wg_fail "$dir" "$id" "$sum" "$(echo "$out" | tail -1)"; continue; }
				rm -f "$WG_STATE/$id.strip"
				echo "$label $id: peers updated"
			fi
		fi
		wg_nat add "$addr" || echo "$label $id: NAT setup failed"
		echo "$sum" >"$WG_STATE/$id.sum"
		echo "$addr" >"$WG_STATE/$id.addr"
		rm -f "$WG_STATE/$id.failed" "$dir/$id.error"
		# Keys stay out of the dump: the private key (interface line) and the
		# preshared keys (second column of the peer lines).
		"$wgbin" show "$id" dump | awk -F'\t' -v OFS='\t' 'NR == 1 { $1 = "(hidden)" } NR > 1 { $2 = "(hidden)" } { print }' >"$dir/$id.dump.tmp" &&
			mv "$dir/$id.dump.tmp" "$dir/$id.dump"
	done
	# Access rules per WireGuard server (TD_FIREWALL=file): chain TD-<id>,
	# jumped to from INPUT and FORWARD for the interface; answered in
	# <id>.fwstatus like trapdoor-firewall.status.
	for rules in "$dir"/*.rules; do
		[ -f "$rules" ] || continue
		id=$(basename "$rules" .rules)
		[ -f "$dir/$id.json" ] || continue
		sum=$(sha256sum "$rules" | cut -d' ' -f1)
		[ "$sum" = "$(cat "$WG_STATE/$id.fwsum" 2>/dev/null)" ] && continue
		if out=$(iptables-restore --noflush <"$rules" 2>&1) &&
			out=$(for c in INPUT FORWARD; do
				iptables -C "$c" -i "$id" -j "TD-$id" 2>/dev/null || iptables -I "$c" 1 -i "$id" -j "TD-$id" || exit 1
			done 2>&1); then
			echo "ok $sum" >"$dir/$id.fwstatus.tmp"
			echo "$label $id: access rules applied"
		else
			echo "error $sum $(echo "$out" | tr '\n' ' ')" >"$dir/$id.fwstatus.tmp"
			echo "$label $id: access rules failed: $out"
		fi
		mv "$dir/$id.fwstatus.tmp" "$dir/$id.fwstatus"
		echo "$sum" >"$WG_STATE/$id.fwsum"
	done
	# Servers removed in trapdoor: take their interfaces down.
	for f in "$WG_STATE"/*.addr; do
		[ -f "$f" ] || continue
		id=$(basename "$f" .addr)
		[ -f "$dir/$id.conf" ] && continue
		ip link del "$id" 2>/dev/null || true
		wg_nat del "$(cat "$f")"
		for c in INPUT FORWARD; do iptables -D "$c" -i "$id" -j "TD-$id" 2>/dev/null || true; done
		iptables -F "TD-$id" 2>/dev/null && iptables -X "TD-$id" 2>/dev/null || true
		rm -f "$WG_STATE/$id".* "$dir/$id.dump" "$dir/$id.error" "$dir/$id.fwstatus"
		echo "$label $id: removed"
	done
	# Rules of a server whose interface never came up.
	for f in "$WG_STATE"/*.fwsum; do
		[ -f "$f" ] || continue
		id=$(basename "$f" .fwsum)
		[ -f "$dir/$id.json" ] && continue
		for c in INPUT FORWARD; do iptables -D "$c" -i "$id" -j "TD-$id" 2>/dev/null || true; done
		iptables -F "TD-$id" 2>/dev/null && iptables -X "TD-$id" 2>/dev/null || true
		rm -f "$f" "$dir/$id.fwstatus"
	done
}

(while :; do
	[ -f "$FW_RULES" ] && fw_apply
	wg_sync "$WG_DIR" wg wg-quick
	wg_sync "$AWG_DIR" awg awg-quick
	sleep 1
done) &

# OpenVPN servers: the first one ($CONF) and the ones added in trapdoor
# ($OVPN_SERVERS_DIR/<id>/server.conf, each with its own tun device <id>).
OVPN_SERVERS_DIR=${OVPN_SERVERS_DIR:-$(dirname "$CONF")/servers}
OVPN_STATE=/run/trapdoor-ovpn
rm -rf "$OVPN_STATE"
mkdir -p "$OVPN_STATE"

# ovpn_supervise <name> <server.conf>: runs OpenVPN from the config's
# directory and starts it again when it exits (trapdoor restarts it with
# SIGTERM through the management interface). Ends when the config is gone
# (an added server was removed) or the container stops.
ovpn_supervise() {
	name=$1 conf=$2 pid= stopping=0 nat_rule=
	trap 'stopping=1; [ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null || true' TERM INT
	# The NAT rule follows the VPN subnet; auto re-reads it before every start.
	nat() {
		want=${VPN_NAT_SUBNET:-}
		[ "$name" = openvpn ] || [ "$want" = "" ] || want=auto # a CIDR is for the first server
		if [ "$want" = auto ]; then
			want=$(awk '$1 == "server" && NF >= 3 { print $2 "/" $3; exit }' "$conf")
		fi
		[ "$want" = "$nat_rule" ] && return 0
		IFACE=${VPN_NAT_IFACE:-$(ip route | awk '/^default/ { print $5; exit }')}
		if [ -n "$nat_rule" ]; then
			iptables -t nat -D POSTROUTING -s "$nat_rule" -o "$IFACE" -j MASQUERADE 2>/dev/null || true
		fi
		nat_rule=$want
		[ -n "$want" ] || return 0
		if ! iptables -t nat -C POSTROUTING -s "$want" -o "$IFACE" -j MASQUERADE 2>/dev/null; then
			iptables -t nat -A POSTROUTING -s "$want" -o "$IFACE" -j MASQUERADE
		fi
		echo "$name: NAT $want via $IFACE"
	}
	cd "$(dirname "$conf")"
	while [ -f "$conf" ]; do
		nat || echo "$name: NAT setup failed"
		openvpn --config "$conf" &
		pid=$!
		set +e
		wait "$pid"
		rc=$?
		# wait returns early when a trapped signal arrives; wait for the real exit.
		while kill -0 "$pid" 2>/dev/null; do wait "$pid"; rc=$?; done
		set -e
		pid=
		[ "$stopping" = 1 ] && exit 0
		if [ ! -f "$conf" ]; then
			break
		elif [ "$rc" = 0 ]; then
			echo "$name: exited, starting again (restart requested)"
		else
			echo "$name: exited with $rc, starting again in 5s"
			sleep 5
		fi
	done
	if [ -n "$nat_rule" ]; then
		iptables -t nat -D POSTROUTING -s "$nat_rule" -o "${VPN_NAT_IFACE:-$(ip route | awk '/^default/ { print $5; exit }')}" -j MASQUERADE 2>/dev/null || true
	fi
	echo "$name: removed, stopped"
}

# Access rules of an added server: chain TD-<id> for its device <id>,
# from <dir>/trapdoor-firewall.rules like the first server's.
ovpn_fw() { # <id> <dir>
	rules=$2/trapdoor-firewall.rules status=$2/trapdoor-firewall.status
	[ -f "$rules" ] || return 0
	sum=$(sha256sum "$rules" | cut -d' ' -f1)
	[ "$sum" = "$(cat "$OVPN_STATE/$1.fwsum" 2>/dev/null)" ] && return 0
	if out=$(iptables-restore --noflush <"$rules" 2>&1) &&
		out=$(for c in INPUT FORWARD; do
			iptables -C "$c" -i "$1" -j "TD-$1" 2>/dev/null || iptables -I "$c" 1 -i "$1" -j "TD-$1" || exit 1
		done 2>&1); then
		echo "ok $sum" >"$status.tmp"
		echo "$1: access rules applied"
	else
		echo "error $sum $(echo "$out" | tr '\n' ' ')" >"$status.tmp"
		echo "$1: access rules failed: $out"
	fi
	mv "$status.tmp" "$status"
	echo "$sum" >"$OVPN_STATE/$1.fwsum"
}

stopping=0
trap 'stopping=1; for f in "$OVPN_STATE"/*.pid; do [ -f "$f" ] && kill -TERM "$(cat "$f")" 2>/dev/null; done; true' TERM INT

waited=0
while [ "$stopping" = 0 ]; do
	if [ -f "$CONF" ] && ! [ -f "$OVPN_STATE/openvpn.pid" ]; then
		ovpn_supervise openvpn "$CONF" &
		echo $! >"$OVPN_STATE/openvpn.pid"
	elif ! [ -f "$CONF" ] && [ "$waited" = 0 ]; then
		echo "openvpn: waiting for $CONF"
		waited=1
	fi
	for conf in "$OVPN_SERVERS_DIR"/*/server.conf; do
		[ -f "$conf" ] || continue
		dir=$(dirname "$conf")
		id=$(basename "$dir")
		[ -f "$dir/trapdoor.json" ] || continue # only servers trapdoor manages
		[ "$id" = openvpn ] && continue
		ovpn_fw "$id" "$dir"
		[ -f "$OVPN_STATE/$id.pid" ] && continue
		echo "$id: starting OpenVPN"
		ovpn_supervise "$id" "$conf" &
		echo $! >"$OVPN_STATE/$id.pid"
	done
	# Supervisors that ended (removed servers): forget them and their rules.
	for f in "$OVPN_STATE"/*.pid; do
		[ -f "$f" ] || continue
		kill -0 "$(cat "$f")" 2>/dev/null && continue
		id=$(basename "$f" .pid)
		rm -f "$f"
		[ "$id" = openvpn ] && continue
		if [ -f "$OVPN_STATE/$id.fwsum" ]; then
			for c in INPUT FORWARD; do iptables -D "$c" -i "$id" -j "TD-$id" 2>/dev/null || true; done
			iptables -F "TD-$id" 2>/dev/null && iptables -X "TD-$id" 2>/dev/null || true
			rm -f "$OVPN_STATE/$id.fwsum"
		fi
	done
	sleep 2 &
	wait $! || true
done

# Stopping: wait for every OpenVPN to exit.
for f in "$OVPN_STATE"/*.pid; do
	[ -f "$f" ] || continue
	p=$(cat "$f")
	while kill -0 "$p" 2>/dev/null; do wait "$p" 2>/dev/null || sleep 0.2; done
done
exit 0
