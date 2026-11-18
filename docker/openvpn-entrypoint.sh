#!/bin/sh
# Starts the OpenVPN server for the "openvpn" compose service.
# - waits for the config instead of crash-looping (fresh installs, or while
#   the agent shares this container's network namespace)
# - optional NAT for VPN clients: VPN_NAT_SUBNET=10.8.0.0/24 [VPN_NAT_IFACE=eth0]
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
exec openvpn --config "$CONF"
