#!/bin/sh
# Runs the VPN servers for the "openvpn" compose service.
# - WireGuard (TD_WIREGUARD=file on the agent): brings up every interface the
#   agent describes in $WG_DIR (<id>.json + <id>.conf), applies changes with
#   `wg syncconf` (nobody is disconnected), adds NAT for its subnet, and
#   writes `wg show <id> dump` to <id>.dump (<id>.error on failure).
# - waits for the OpenVPN config instead of crash-looping (fresh installs)
# - NAT for VPN clients: VPN_NAT_SUBNET=auto (the "server" line of
#   server.conf, checked at every start) or a CIDR; empty = none.
#   [VPN_NAT_IFACE=eth0]
# - supervises OpenVPN: when it exits (trapdoor restarts it with SIGTERM
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
WG_STATE=/run/trapdoor-wg
rm -rf "$WG_STATE" # a restarted container has no interfaces or rules yet
mkdir -p "$WG_STATE"
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
wg_fail() { # <id> <sum> <message>
	echo "$3" >"$WG_DIR/$1.error"
	echo "$2" >"$WG_STATE/$1.failed"
	echo "wireguard $1: $3"
}
wg_sync() {
	command -v wg >/dev/null || return 0
	for conf in "$WG_DIR"/*.conf; do
		[ -f "$conf" ] || continue
		id=$(basename "$conf" .conf)
		[ -f "$WG_DIR/$id.json" ] || continue # only interfaces trapdoor manages
		sum=$(sha256sum "$conf" | cut -d' ' -f1)
		[ "$sum" = "$(cat "$WG_STATE/$id.failed" 2>/dev/null)" ] && continue # retried when it changes
		addr=$(awk -F' *= *' '$1 == "Address" { print $2; exit }' "$conf")
		old_addr=$(cat "$WG_STATE/$id.addr" 2>/dev/null || true)
		if ! ip link show "$id" >/dev/null 2>&1; then
			out=$(wg-quick up "$conf" 2>&1) || { wg_fail "$id" "$sum" "$(echo "$out" | tail -1)"; continue; }
			echo "wireguard $id: up ($addr)"
		elif [ "$sum" != "$(cat "$WG_STATE/$id.sum" 2>/dev/null)" ]; then
			if [ "$addr" != "$old_addr" ]; then
				wg-quick down "$conf" >/dev/null 2>&1 || ip link del "$id" 2>/dev/null || true
				wg_nat del "$old_addr"
				out=$(wg-quick up "$conf" 2>&1) || { wg_fail "$id" "$sum" "$(echo "$out" | tail -1)"; continue; }
				echo "wireguard $id: restarted ($addr)"
			else
				out=$(wg-quick strip "$conf" >"$WG_STATE/$id.strip" 2>&1 && wg syncconf "$id" "$WG_STATE/$id.strip" 2>&1) ||
					{ wg_fail "$id" "$sum" "$(echo "$out" | tail -1)"; continue; }
				rm -f "$WG_STATE/$id.strip"
				echo "wireguard $id: peers updated"
			fi
		fi
		wg_nat add "$addr" || echo "wireguard $id: NAT setup failed"
		echo "$sum" >"$WG_STATE/$id.sum"
		echo "$addr" >"$WG_STATE/$id.addr"
		rm -f "$WG_STATE/$id.failed" "$WG_DIR/$id.error"
		# Keys stay out of the dump: the private key (interface line) and the
		# preshared keys (second column of the peer lines).
		wg show "$id" dump | awk -F'\t' -v OFS='\t' 'NR == 1 { $1 = "(hidden)" } NR > 1 { $2 = "(hidden)" } { print }' >"$WG_DIR/$id.dump.tmp" &&
			mv "$WG_DIR/$id.dump.tmp" "$WG_DIR/$id.dump"
	done
	# Access rules per WireGuard server (TD_FIREWALL=file): chain TD-<id>,
	# jumped to from INPUT and FORWARD for the interface; answered in
	# <id>.fwstatus like trapdoor-firewall.status.
	for rules in "$WG_DIR"/*.rules; do
		[ -f "$rules" ] || continue
		id=$(basename "$rules" .rules)
		[ -f "$WG_DIR/$id.json" ] || continue
		sum=$(sha256sum "$rules" | cut -d' ' -f1)
		[ "$sum" = "$(cat "$WG_STATE/$id.fwsum" 2>/dev/null)" ] && continue
		if out=$(iptables-restore --noflush <"$rules" 2>&1) &&
			out=$(for c in INPUT FORWARD; do
				iptables -C "$c" -i "$id" -j "TD-$id" 2>/dev/null || iptables -I "$c" 1 -i "$id" -j "TD-$id" || exit 1
			done 2>&1); then
			echo "ok $sum" >"$WG_DIR/$id.fwstatus.tmp"
			echo "wireguard $id: access rules applied"
		else
			echo "error $sum $(echo "$out" | tr '\n' ' ')" >"$WG_DIR/$id.fwstatus.tmp"
			echo "wireguard $id: access rules failed: $out"
		fi
		mv "$WG_DIR/$id.fwstatus.tmp" "$WG_DIR/$id.fwstatus"
		echo "$sum" >"$WG_STATE/$id.fwsum"
	done
	# Servers removed in trapdoor: take their interfaces down.
	for f in "$WG_STATE"/*.addr; do
		[ -f "$f" ] || continue
		id=$(basename "$f" .addr)
		[ -f "$WG_DIR/$id.conf" ] && continue
		ip link del "$id" 2>/dev/null || true
		wg_nat del "$(cat "$f")"
		for c in INPUT FORWARD; do iptables -D "$c" -i "$id" -j "TD-$id" 2>/dev/null || true; done
		iptables -F "TD-$id" 2>/dev/null && iptables -X "TD-$id" 2>/dev/null || true
		rm -f "$WG_STATE/$id".* "$WG_DIR/$id.dump" "$WG_DIR/$id.error" "$WG_DIR/$id.fwstatus"
		echo "wireguard $id: removed"
	done
	# Rules of a server whose interface never came up.
	for f in "$WG_STATE"/*.fwsum; do
		[ -f "$f" ] || continue
		id=$(basename "$f" .fwsum)
		[ -f "$WG_DIR/$id.json" ] && continue
		for c in INPUT FORWARD; do iptables -D "$c" -i "$id" -j "TD-$id" 2>/dev/null || true; done
		iptables -F "TD-$id" 2>/dev/null && iptables -X "TD-$id" 2>/dev/null || true
		rm -f "$f" "$WG_DIR/$id.fwstatus"
	done
}

(while :; do
	[ -f "$FW_RULES" ] && fw_apply
	wg_sync
	sleep 1
done) &

while [ ! -f "$CONF" ]; do
	echo "openvpn: waiting for $CONF"
	sleep 5
done

# The NAT rule follows the VPN subnet; auto re-reads it before every start.
nat_rule=
nat() {
	want=${VPN_NAT_SUBNET:-}
	if [ "$want" = auto ]; then
		want=$(awk '$1 == "server" && NF >= 3 { print $2 "/" $3; exit }' "$CONF")
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
	echo "openvpn: NAT $want via $IFACE"
}

cd "$(dirname "$CONF")"

stopping=0
pid=
trap 'stopping=1; [ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null || true' TERM INT

while :; do
	nat || echo "openvpn: NAT setup failed"
	openvpn --config "$CONF" &
	pid=$!
	set +e
	wait "$pid"
	rc=$?
	# wait returns early when a trapped signal arrives; wait for the real exit.
	while kill -0 "$pid" 2>/dev/null; do wait "$pid"; rc=$?; done
	set -e
	pid=
	[ "$stopping" = 1 ] && exit 0
	if [ "$rc" = 0 ]; then
		echo "openvpn: exited, starting again (restart requested)"
	else
		echo "openvpn: exited with $rc, starting again in 5s"
		sleep 5
	fi
done
