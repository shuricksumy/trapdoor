#!/bin/sh
# Runs the OpenVPN server for the "openvpn" compose service.
# - waits for the config instead of crash-looping (fresh installs)
# - optional NAT for VPN clients: VPN_NAT_SUBNET=10.8.0.0/24 [VPN_NAT_IFACE=eth0]
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

if [ -n "${VPN_NAT_SUBNET:-}" ]; then
	IFACE=${VPN_NAT_IFACE:-$(ip route | awk '/^default/ { print $5; exit }')}
	if ! iptables -t nat -C POSTROUTING -s "$VPN_NAT_SUBNET" -o "$IFACE" -j MASQUERADE 2>/dev/null; then
		iptables -t nat -A POSTROUTING -s "$VPN_NAT_SUBNET" -o "$IFACE" -j MASQUERADE
	fi
	echo "openvpn: NAT $VPN_NAT_SUBNET via $IFACE"
fi

cd "$(dirname "$CONF")"

stopping=0
pid=
trap 'stopping=1; [ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null || true' TERM INT

while :; do
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
