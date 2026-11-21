#!/bin/sh
# Runs the VPN servers for the "vpn" compose service (named "openvpn" before).
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
# - scripts of each server (presets and own start / stop scripts, see
#   internal/agent/scripts.go): run as root while the server's interface is
#   up; a change runs the old stop scripts, then the new start scripts.
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
OVPN_SERVERS_DIR=${OVPN_SERVERS_DIR:-$(dirname "$CONF")/servers}

# Servers stopped in trapdoor have a "stopped" marker (OpenVPN:
# trapdoor.stopped in its directory, WireGuard: <id>.stopped). Autostart off
# ("noautostart" marker): they stay stopped when this container starts.
for d in "$(dirname "$CONF")" "$OVPN_SERVERS_DIR"/*; do
	if [ -f "$d/trapdoor.noautostart" ]; then
		touch "$d/trapdoor.stopped"
		echo "$(basename "$d"): autostart off, stays stopped"
	fi
done
for f in "$WG_DIR"/*.noautostart "$AWG_DIR"/*.noautostart; do
	if [ -f "$f" ]; then
		touch "${f%.noautostart}.stopped"
		echo "$(basename "${f%.noautostart}"): autostart off, stays stopped"
	fi
done
wg_iface() { ip route | awk '/^default/ { print $5; exit }'; }
wg_nat() { # add|del <address/bits>
	[ -n "$2" ] || return 0
	ifc=${VPN_NAT_IFACE:-$(wg_iface)}
	if [ -z "$ifc" ]; then # no default route (e.g. some host networks): say it once
		if [ ! -f "$WG_STATE_ROOT/no-nat-iface" ]; then
			mkdir -p "$WG_STATE_ROOT" && touch "$WG_STATE_ROOT/no-nat-iface"
			echo "vpn: no default route to NAT WireGuard / AmneziaWG clients through; set VPN_NAT_IFACE"
		fi
		return 0
	fi
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
		if [ -f "$dir/$id.stopped" ]; then # stopped in trapdoor: down, nothing reported
			if ip link show "$id" >/dev/null 2>&1; then
				"$quick" down "$conf" >/dev/null 2>&1 || ip link del "$id" 2>/dev/null || true
				wg_nat del "$(cat "$WG_STATE/$id.addr" 2>/dev/null)"
				echo "$label $id: stopped"
			fi
			rm -f "$WG_STATE/$id.sum" "$WG_STATE/$id.addr" "$WG_STATE/$id.failed" "$dir/$id.dump" "$dir/$id.error"
			continue
		fi
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

# Scripts of each server: <base>scripts-up.sh (presets) and <base>up.sh (own)
# while the interface named in <base>scripts.env is up; <base>down.sh and
# <base>scripts-down.sh when it goes down, the files change, the server is
# removed or this container stops. <base> is <dir>/trapdoor- (OpenVPN) or
# <dir>/<id>. (WireGuard, AmneziaWG). Answer in <base>scripts.status.
SC_STATE=/run/trapdoor-scripts
rm -rf "$SC_STATE"
mkdir -p "$SC_STATE"
sc_dev() { # <iface>: the device ("tun+" = the first tun<N> there is)
	case $1 in
	*+) ip -o link show | awk -F': ' '{ split($2, a, "@"); print a[1] }' | grep -E "^${1%+}[0-9]+\$" | head -1 ;;
	*) ip link show "$1" >/dev/null 2>&1 && echo "$1" ;;
	esac
}
sc_exec() { # <env file> <script>: runs it with the server's variables, 30 s at most
	(
		SERVER_ID=$(sed -n 's/^SERVER_ID=//p' "$1")
		TYPE=$(sed -n 's/^TYPE=//p' "$1")
		IFACE=$(sed -n 's/^IFACE=//p' "$1")
		dev=$(sc_dev "$IFACE")
		SUBNET=$(ip -4 route show dev "${dev:-none}" proto kernel scope link 2>/dev/null | awk 'NR == 1 { print $1 }')
		NAT_IFACE=${VPN_NAT_IFACE:-$(wg_iface)}
		export SERVER_ID TYPE IFACE SUBNET NAT_IFACE
		cd "$(dirname "$2")"
		timeout 30 sh "$2"
	) 2>&1
}
sc_down() { # <state dir>: the stop scripts saved when the start scripts ran
	[ -f "$1/env" ] || return 0
	for f in down.sh scripts-down.sh; do
		[ -f "$1/$f" ] || continue
		o=$(sc_exec "$1/env" "$1/$f") || echo "$(basename "$1"): $f failed: $(echo "$o" | tail -2 | tr '\n' ' ')"
	done
	rm -f "$1/down.sh" "$1/scripts-down.sh" "$1/sum"
}
sc_status() { # <base> <line>: written only when it changes
	[ "$(cat "${1}scripts.status" 2>/dev/null)" = "$2" ] && return 0
	echo "$2" >"${1}scripts.status.tmp" && mv "${1}scripts.status.tmp" "${1}scripts.status"
}
scripts_sync() {
	for env in "$(dirname "$CONF")/trapdoor-scripts.env" "$OVPN_SERVERS_DIR"/*/trapdoor-scripts.env "$WG_DIR"/*.scripts.env "$AWG_DIR"/*.scripts.env; do
		[ -f "$env" ] || continue
		base=${env%scripts.env}
		sid=$(sed -n 's/^SERVER_ID=//p' "$env")
		[ -n "$sid" ] || continue
		st=$SC_STATE/$sid
		mkdir -p "$st"
		echo "$base" >"$st/base"
		sum=$(cat "${base}scripts-up.sh" "${base}scripts-down.sh" "${base}up.sh" "${base}down.sh" "$env" 2>/dev/null | sha256sum | cut -d' ' -f1)
		if [ -n "$(sc_dev "$(sed -n 's/^IFACE=//p' "$env")")" ]; then
			[ "$sum" = "$(cat "$st/sum" 2>/dev/null)" ] && continue
			sc_down "$st"
			cp "$env" "$st/env"
			for f in scripts-down.sh down.sh; do
				if [ -f "${base}$f" ]; then cp "${base}$f" "$st/$f"; fi
			done
			err=
			for f in scripts-up.sh up.sh; do
				[ -f "${base}$f" ] || continue
				if ! o=$(sc_exec "$st/env" "${base}$f"); then
					err="$f: $(echo "$o" | tail -3 | tr '\n' ' ')"
					break
				fi
			done
			if [ -z "$err" ]; then
				sc_status "$base" "ok $sum"
				echo "$sid: scripts applied"
			else
				sc_status "$base" "error $sum $err"
				echo "$sid: scripts failed: $err"
			fi
			echo "$sum" >"$st/sum"
		else
			if [ -f "$st/sum" ]; then
				sc_down "$st"
				echo "$sid: scripts stopped (interface down)"
			fi
			sc_status "$base" inactive
		fi
	done
	# Servers whose scripts are gone (removed, or every script cleared): stop once.
	for st in "$SC_STATE"/*; do
		[ -f "$st/base" ] || continue
		[ -f "$(cat "$st/base")scripts.env" ] && continue
		sc_down "$st"
		rm -rf "$st"
	done
}

# Speed limits of each server (TD_FIREWALL=file): <base>shaping.sh, made by
# the agent (tc commands, see internal/shaping), run with the server's
# device while it is up, and again when the file changes or the device was
# recreated (a restart drops its rules). Answered in <base>shaping.status:
# "ok|waiting|error <sha256> [message]". When the file is gone, the limits
# are taken out.
SH_STATE=/run/trapdoor-shaping
rm -rf "$SH_STATE"
mkdir -p "$SH_STATE"
sh_status() { # <file> <line>: written only when it changes
	[ "$(cat "$1" 2>/dev/null)" = "$2" ] && return 0
	echo "$2" >"$1.tmp" && mv "$1.tmp" "$1"
}
sh_clear() { # <state prefix>
	dev=$(cat "$1.dev" 2>/dev/null || true)
	ifb=$(cat "$1.ifb" 2>/dev/null || true)
	if [ -n "$dev" ]; then
		tc qdisc del dev "$dev" root 2>/dev/null || true
		tc qdisc del dev "$dev" ingress 2>/dev/null || true
	fi
	if [ -n "$ifb" ]; then ip link del "$ifb" 2>/dev/null || true; fi
	rm -f "$1".*
}
shaping_sync() {
	command -v tc >/dev/null || return 0
	for f in "$(dirname "$CONF")/trapdoor-shaping.sh" "$OVPN_SERVERS_DIR"/*/trapdoor-shaping.sh "$WG_DIR"/*.shaping.sh "$AWG_DIR"/*.shaping.sh; do
		[ -f "$f" ] || continue
		base=${f%shaping.sh}
		st=$SH_STATE/$(echo "$f" | sha256sum | cut -c1-16)
		sum=$(sha256sum "$f" | cut -d' ' -f1)
		dev=$(sc_dev "$(sed -n 's/^# IFACE=//p' "$f" | head -1)")
		if [ -z "$dev" ]; then
			sh_status "${base}shaping.status" "waiting $sum"
			rm -f "$st.applied"
			continue
		fi
		mark="$sum $dev $(cat "/sys/class/net/$dev/ifindex" 2>/dev/null)"
		[ "$mark" = "$(cat "$st.applied" 2>/dev/null)" ] && continue
		echo "$f" >"$st.file"
		echo "$dev" >"$st.dev"
		sed -n 's/^IFB=//p' "$f" | head -1 >"$st.ifb"
		if out=$(sh "$f" "$dev" 2>&1); then
			sh_status "${base}shaping.status" "ok $sum"
			echo "$dev: speed limits applied"
		else
			sh_status "${base}shaping.status" "error $sum $(echo "$out" | tail -2 | tr '\n' ' ')"
			echo "$dev: speed limits failed: $out"
		fi
		echo "$mark" >"$st.applied"
	done
	for f in "$SH_STATE"/*.file; do
		[ -f "$f" ] || continue
		[ -f "$(cat "$f")" ] && continue
		echo "$(cat "${f%.file}.dev" 2>/dev/null): speed limits removed"
		sh_clear "${f%.file}"
	done
}

(while :; do
	[ -f "$FW_RULES" ] && fw_apply
	wg_sync "$WG_DIR" wg wg-quick
	wg_sync "$AWG_DIR" awg awg-quick
	scripts_sync
	shaping_sync
	sleep 1
done) &
WG_LOOP=$!

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
	stop_marker=$(dirname "$conf")/trapdoor.stopped
	while [ -f "$conf" ] && [ ! -f "$stop_marker" ]; do
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
		if [ "$stopping" = 1 ]; then # docker stop, or stopped in trapdoor (main loop)
			if [ -f "$stop_marker" ]; then echo "$name: stopped"; fi
			exit 0
		fi
		if [ ! -f "$conf" ] || [ -f "$stop_marker" ]; then
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
	if [ -f "$stop_marker" ] && [ -f "$conf" ]; then
		echo "$name: stopped"
	else
		echo "$name: removed, stopped"
	fi
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
trap 'stopping=1; kill "$WG_LOOP" 2>/dev/null; for f in "$OVPN_STATE"/*.pid; do [ -f "$f" ] && kill -TERM "$(cat "$f")" 2>/dev/null; done; true' TERM INT

waited=0
while [ "$stopping" = 0 ]; do
	if [ -f "$CONF" ] && ! [ -f "$OVPN_STATE/openvpn.pid" ] && ! [ -f "$(dirname "$CONF")/trapdoor.stopped" ]; then
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
		[ -f "$dir/trapdoor.stopped" ] && continue
		echo "$id: starting OpenVPN"
		ovpn_supervise "$id" "$conf" &
		echo $! >"$OVPN_STATE/$id.pid"
	done
	# Supervisors that ended (removed or stopped servers): forget them and
	# their rules. A stopped server whose OpenVPN did not exit (management
	# unreachable) is stopped here.
	for f in "$OVPN_STATE"/*.pid; do
		[ -f "$f" ] || continue
		id=$(basename "$f" .pid)
		sdir=$OVPN_SERVERS_DIR/$id
		[ "$id" = openvpn ] && sdir=$(dirname "$CONF")
		if kill -0 "$(cat "$f")" 2>/dev/null; then
			if [ -f "$sdir/trapdoor.stopped" ]; then kill -TERM "$(cat "$f")" 2>/dev/null || true; fi
			continue
		fi
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
# The servers' stop scripts (their rules would outlive the container on the
# host's network).
for st in "$SC_STATE"/*; do
	[ -d "$st" ] && sc_down "$st"
done
# Speed limits: the IFB devices would outlive the container on the host's network.
for f in "$SH_STATE"/*.file; do
	[ -f "$f" ] && sh_clear "${f%.file}"
done
# Take down the WireGuard / AmneziaWG interfaces brought up here, with their NAT and
# rules: on the host's network (network_mode: host) they would outlive the container.
for f in "$WG_STATE_ROOT"/*/*.addr; do
	[ -f "$f" ] || continue
	id=$(basename "$f" .addr)
	ip link del "$id" 2>/dev/null || true
	wg_nat del "$(cat "$f")"
	for c in INPUT FORWARD; do iptables -D "$c" -i "$id" -j "TD-$id" 2>/dev/null || true; done
	iptables -F "TD-$id" 2>/dev/null && iptables -X "TD-$id" 2>/dev/null || true
	echo "$id: down (container stopping)"
done
exit 0
