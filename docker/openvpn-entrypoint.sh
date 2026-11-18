#!/bin/sh
# Runs the OpenVPN server for the "openvpn" compose service.
# - waits for the config instead of crash-looping (fresh installs)
# - NAT for VPN clients: VPN_NAT_SUBNET=auto (the "server" line of
#   server.conf, checked at every start) or a CIDR; empty = none.
#   [VPN_NAT_IFACE=eth0]
# - supervises OpenVPN: when it exits (tunnelward restarts it with SIGTERM
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
