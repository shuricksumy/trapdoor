#!/usr/bin/env bash
# Docker smoke test: the real compose file with a locally built image.
# A fresh install is set up with the setup wizard's API, the web API issues
# certificates, and a VPN client on the host connects through the container.
#
# Needs: docker compose, openvpn and curl on the host, sudo (tun device).
# Usage: test/docker/smoke.sh <image>
set -euo pipefail

IMAGE=${1:?usage: smoke.sh <image>}
REPO=$(cd "$(dirname "$0")/../.." && pwd)
WORK=$(mktemp -d)
WEB=http://127.0.0.1:18080
TOKEN=smoke-web-token
CLIENT_PID=
BOB_PID=

log() { echo "==> $*"; }
dc() { docker compose -f "$WORK/docker-compose.yml" -p tdsmoke "$@"; }
api() { curl -fsS -H "Authorization: Bearer $TOKEN" "$@"; }
fail() {
	echo "FAIL: $*" >&2
	dc ps -a || true
	dc logs --tail 40 || true
	[[ -f $WORK/client.log ]] && tail -20 "$WORK/client.log"
	exit 1
}
cleanup() {
	[[ -n $CLIENT_PID ]] && sudo kill "$CLIENT_PID" 2>/dev/null || true
	[[ -n $BOB_PID ]] && sudo kill "$BOB_PID" 2>/dev/null || true
	sudo wg-quick down /etc/wireguard/tdsmoke.conf >/dev/null 2>&1 || true
	sudo rm -f /etc/wireguard/tdsmoke.conf
	dc down -v --remove-orphans >/dev/null 2>&1 || true
	sudo rm -rf "$WORK"
}
trap cleanup EXIT

cp "$REPO/docker-compose.yml" "$WORK/"
mkdir -p "$WORK/openvpn"
cat >"$WORK/.env" <<ENV
TD_IMAGE=$IMAGE
TD_AGENT_TOKEN=$(openssl rand -hex 16)
TD_WEB_API_TOKEN=$TOKEN
TD_COOKIE_SECURE=false
TD_TZ=Europe/Dublin
OPENVPN_DIR=$WORK/openvpn
WIREGUARD_DIR=$WORK/wireguard
WG_PORT=15182
AMNEZIAWG_DIR=$WORK/amneziawg
AWG_PORT=15183
VPN_PORT=11194
VPN2_PORT=11195
VPN2_PROTO=tcp
WEB_BIND=127.0.0.1:18080
ENV

log "image runs as non-root and reports its version"
docker run --rm "$IMAGE" version
[[ $(docker run --rm --entrypoint id "$IMAGE" -u) == 10001 ]] || fail "image does not run as uid 10001"

log "compose up (the vpn container waits for server.conf)"
(cd "$WORK" && dc up -d)
for _ in $(seq 60); do curl -fs "$WEB/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fs "$WEB/healthz" >/dev/null || fail "web not healthy"
curl -fs "$WEB/" | grep -q 'id="app"' || fail "UI not served"

log "setup wizard: needed, container port fixed, then create the server"
api "$WEB/api/v1/servers/openvpn/setup" >"$WORK/setup-status.json"
grep -q '"needed":true' "$WORK/setup-status.json" || fail "setup not needed: $(cat "$WORK/setup-status.json")"
grep -q '"fixed_port":1194' "$WORK/setup-status.json" || fail "no fixed port: $(cat "$WORK/setup-status.json")"
grep -q '"public_port":11194' "$WORK/setup-status.json" || fail "public port not from VPN_PORT"
api -H "Content-Type: application/json" -m 90 \
	-d '{"public_host":"127.0.0.1","public_port":11194,"subnet":"10.77.0.0/24","dns":["10.77.0.1"]}' \
	"$WEB/api/v1/servers/openvpn/setup" >"$WORK/setup.json" || fail "setup failed"
grep -q '"running":true' "$WORK/setup.json" || fail "OpenVPN not running after setup: $(cat "$WORK/setup.json")"
dc logs vpn | grep -q "NAT 10.77.0.0/255.255.255.0" || fail "NAT not derived from server.conf"
sudo grep -q "^remote 127.0.0.1 11194$" "$WORK/openvpn/client-template.txt" || fail "client template"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
	-d '{"public_host":"127.0.0.1"}' "$WEB/api/v1/servers/openvpn/setup")
[[ $code == 409 ]] || fail "second setup answered $code, want 409"

log "agent reaches OpenVPN's management interface (shared network namespace)"
for _ in $(seq 60); do api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' && break; sleep 1; done
api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' || fail "agent cannot reach management"
dc exec -T vpn sh -c 'netstat -ltn 2>/dev/null || ss -ltn' | grep -q '127.0.0.1:2080' || fail "management not on 127.0.0.1"

log "admin user via CLI in the web container, login with cookie"
echo "smoke-admin-password" | dc exec -T web trapdoor user add admin >/dev/null
JAR=$WORK/jar
curl -fsS -c "$JAR" -H "Content-Type: application/json" -d '{"username":"admin","password":"smoke-admin-password"}' "$WEB/api/v1/auth/login" >/dev/null || fail "login"

log "issue alice through the UI's API and connect from the host"
curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -d '{"name":"alice"}' "$WEB/api/v1/servers/openvpn/certs" >/dev/null
curl -fsS -b "$JAR" "$WEB/api/v1/servers/openvpn/certs/alice/ovpn" >"$WORK/alice.ovpn" || fail "download profile"
sudo openvpn --config "$WORK/alice.ovpn" >"$WORK/client.log" 2>&1 &
CLIENT_PID=$!
for _ in $(seq 30); do grep -q "Initialization Sequence Completed" "$WORK/client.log" 2>/dev/null && break; sleep 1; done
grep -q "Initialization Sequence Completed" "$WORK/client.log" || fail "alice could not connect"
for _ in $(seq 15); do curl -fsS -b "$JAR" "$WEB/api/v1/servers/openvpn/sessions" | grep -q '"name":"alice"' && break; sleep 1; done
curl -fsS -b "$JAR" "$WEB/api/v1/servers/openvpn/sessions" | grep -q '"name":"alice"' || fail "alice not in live sessions"
ping -c 2 -W 2 10.77.0.1 >/dev/null || fail "no traffic through the tunnel"

log "revoke disconnects alice"
curl -fsS -b "$JAR" -H "Origin: $WEB" -X POST "$WEB/api/v1/servers/openvpn/certs/alice/revoke" >/dev/null
for _ in $(seq 10); do curl -fsS -b "$JAR" "$WEB/api/v1/servers/openvpn/sessions" | grep -q '"name":"alice"' || break; sleep 1; done
! curl -fsS -b "$JAR" "$WEB/api/v1/servers/openvpn/sessions" | grep -q '"name":"alice"' || fail "revoked alice still online"

log "server config: change via the web API, restart through management (supervised SIGTERM)"
wj() { curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" "$@"; }
wj "$WEB/api/v1/servers/openvpn/server/config" >"$WORK/sc.json"
python3 - "$WORK/sc.json" >"$WORK/sc-new.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
s = c["settings"]
s["dns"] = ["9.9.9.9"]
print(json.dumps({"base_version": c["version"], "settings": s}))
PY
wj -X PUT -d @"$WORK/sc-new.json" "$WEB/api/v1/servers/openvpn/server/config" >/dev/null || fail "save server config"
wj -X POST -d '{"rollback":true}' "$WEB/api/v1/servers/openvpn/server/restart" >"$WORK/restart.json"
grep -q '"ok":true' "$WORK/restart.json" || fail "restart: $(cat "$WORK/restart.json")"
dc logs vpn | grep -q "starting again (restart requested)" || fail "supervisor did not restart OpenVPN"
api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' || fail "agent lost OpenVPN after restart (network namespace)"

log "a config that passes the checks but cannot start is rolled back"
wj "$WEB/api/v1/servers/openvpn/server/config" >"$WORK/sc2.json"
python3 - "$WORK/sc2.json" >"$WORK/sc-bad.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
print(json.dumps({"base_version": c["version"], "raw": c["raw"] + "local 203.0.113.99\n"}))
PY
wj -X PUT -d @"$WORK/sc-bad.json" "$WEB/api/v1/servers/openvpn/server/config" >/dev/null || fail "save bad-at-start config"
curl -sS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -m 150 -X POST -d '{"rollback":true}' "$WEB/api/v1/servers/openvpn/server/restart" >"$WORK/rollback.json"
grep -q '"rolled_back":true' "$WORK/rollback.json" || fail "no rollback: $(cat "$WORK/rollback.json")"
grep -q "running again" "$WORK/rollback.json" || fail "OpenVPN not back after rollback: $(cat "$WORK/rollback.json")"
! sudo grep -q "203.0.113.99" "$WORK/openvpn/server.conf" || fail "bad config still in place"
grep -q "9.9.9.9" "$WORK/openvpn/server.conf" || fail "rolled back to the wrong version"

log "access rules: written by the agent, applied by the vpn container (TD_FIREWALL=file)"
api -X PUT -H "Content-Type: application/json" -m 30 \
	-d '{"enabled":true,"default":"deny","rules":[{"to":"10.77.0.1","proto":"icmp","action":"allow"}]}' \
	"$WEB/api/v1/servers/openvpn/firewall" >"$WORK/fw.json" || fail "save access rules"
grep -q '"applied":true' "$WORK/fw.json" || fail "rules not applied: $(cat "$WORK/fw.json")"
dc exec -T vpn iptables -S TD-FWD | grep -q -- "-d 10.77.0.1/32 -p icmp -j ACCEPT" || fail "rule not in the container"
dc exec -T vpn iptables -C FORWARD -i tun+ -j TD-FWD || fail "no FORWARD jump"
dc logs vpn | grep -q "access rules applied" || fail "no apply log"
! dc exec -T agent iptables -N TD-PROBE >/dev/null 2>&1 || fail "agent container can change the firewall"

log "WireGuard: created through the web API, run by the vpn container (TD_WIREGUARD=file)"
api -H "Content-Type: application/json" \
	-d '{"type":"wireguard","id":"wg0","name":"Smoke","port":15182,"public_host":"127.0.0.1","subnet":"10.78.0.0/24","allowed_ips":["10.78.0.0/24"]}' \
	"$WEB/api/v1/servers" >"$WORK/wg.json" || fail "create WireGuard server"
wgrunning() { api "$WEB/api/v1/status" | grep -q '"id":"wg0"[^}]*"running":true'; }
for _ in $(seq 20); do wgrunning && break; sleep 1; done
wgrunning || fail "wg0 not running: $(api "$WEB/api/v1/status")"
dc logs vpn | grep -q "wireguard wg0: up (10.78.0.1/24)" || fail "no wg0 up log"
sudo head -1 "$WORK/wireguard/wg0.dump" | grep -q '^(hidden)' || fail "private key in the dump file"
api -H "Content-Type: application/json" -d '{"name":"wgsmoke"}' "$WEB/api/v1/servers/wg0/peers" >/dev/null || fail "add WireGuard client"
sudo mkdir -p /etc/wireguard
api "$WEB/api/v1/servers/wg0/peers/wgsmoke/conf" | sudo sh -c 'umask 077; cat >/etc/wireguard/tdsmoke.conf'
sudo wg-quick up /etc/wireguard/tdsmoke.conf >/dev/null 2>&1 || fail "host WireGuard client"
wgping() { ping -c 1 -W 1 10.78.0.1 >/dev/null 2>&1; }
for _ in $(seq 15); do wgping && break; sleep 1; done
wgping || fail "no ping through WireGuard: $(dc logs --tail 20 vpn)"
! sudo awk -F'\t' 'NR > 1 && $2 != "(hidden)"' "$WORK/wireguard/wg0.dump" | grep -q . || fail "preshared key in the dump file"
for _ in $(seq 10); do api "$WEB/api/v1/servers/wg0/sessions" | grep -q '"name":"wgsmoke"' && break; sleep 1; done
api "$WEB/api/v1/servers/wg0/sessions" | grep -q '"name":"wgsmoke"' || fail "wgsmoke not in live sessions"
api -X PUT -H "Content-Type: application/json" -m 30 -d '{"enabled":true,"default":"deny","rules":[]}' \
	"$WEB/api/v1/servers/wg0/firewall" >"$WORK/wgfw.json" || fail "save WireGuard rules"
grep -q '"applied":true' "$WORK/wgfw.json" || fail "WireGuard rules not applied by the container: $(cat "$WORK/wgfw.json")"
dc exec -T vpn iptables -C FORWARD -i wg0 -j TD-wg0 || fail "no jump to TD-wg0 in the container"
sleep 1
! wgping || fail "ping passes although the WireGuard rules block everything"
api -X PUT -H "Content-Type: application/json" -m 30 -d '{"enabled":true,"default":"deny","rules":[{"client":"wgsmoke","proto":"icmp","action":"allow"}]}' \
	"$WEB/api/v1/servers/wg0/firewall" | grep -q '"applied":true' || fail "allow rule not applied"
for _ in $(seq 5); do wgping && break; sleep 1; done
wgping || fail "allowed ping blocked"
api -X PUT -H "Content-Type: application/json" -d '{"disabled":true}' "$WEB/api/v1/servers/wg0/peers/wgsmoke" >/dev/null
for _ in $(seq 10); do dc logs vpn | grep -q "wireguard wg0: peers updated" && break; sleep 1; done
sleep 1
! wgping || fail "disabled WireGuard client still gets through"
sudo wg-quick down /etc/wireguard/tdsmoke.conf >/dev/null 2>&1 || true
api -X DELETE "$WEB/api/v1/servers/wg0" || fail "remove WireGuard server"
for _ in $(seq 10); do dc exec -T vpn ip link show wg0 >/dev/null 2>&1 || break; sleep 1; done
! dc exec -T vpn ip link show wg0 >/dev/null 2>&1 || fail "wg0 still up after remove"
! dc exec -T vpn iptables -S TD-wg0 >/dev/null 2>&1 || fail "TD-wg0 left after remove"
log "WireGuard client connected through the container; disable and remove work"

# Each level in turn on the same port: 2.0 (the default) and 3.1 (header
# protection, random trailers, no cookie replies, client timings).
for level in 2.0 3.1; do
	id=awg${level%%.*}
	log "AmneziaWG $level: obfuscated server $id in the container (userspace), a client from the image connects"
	api -H "Content-Type: application/json" \
		-d '{"type":"amneziawg","id":"'"$id"'","port":15183,"public_host":"127.0.0.1","subnet":"10.79.0.0/24","allowed_ips":["10.79.0.0/24"],"awg_level":"'"$level"'"}' \
		"$WEB/api/v1/servers" >"$WORK/awg.json" || fail "create AmneziaWG $level server"
	grep -q '"level":"'"$level"'"' "$WORK/awg.json" || fail "no $level obfuscation: $(cat "$WORK/awg.json")"
	awgrunning() { api "$WEB/api/v1/status" | grep -q '"id":"'"$id"'"[^}]*"running":true'; }
	for _ in $(seq 20); do awgrunning && break; sleep 1; done
	awgrunning || fail "$id not running: $(api "$WEB/api/v1/status"; dc logs --tail 20 vpn)"
	dc exec -T vpn awg show "$id" | grep -q "h4: " || fail "$id has no obfuscation settings"
	api -H "Content-Type: application/json" -d '{"name":"awgsmoke"}' "$WEB/api/v1/servers/$id/peers" >/dev/null || fail "add AmneziaWG client"
	api "$WEB/api/v1/servers/$id/peers/awgsmoke/conf" >"$WORK/awgc.conf"
	grep -q "^S3 = " "$WORK/awgc.conf" && grep -q "^H1 = [0-9]*-[0-9]*$" "$WORK/awgc.conf" || fail "client config lacks obfuscation: $(grep -v Key "$WORK/awgc.conf")"
	if [[ $level == 3.1 ]]; then
		grep -q "^HeaderProtectionKey = " "$WORK/awgc.conf" && grep -q "^RandomTrailers = on$" "$WORK/awgc.conf" &&
			grep -q "^RekeyAfterTime = " "$WORK/awgc.conf" || fail "client config lacks 3.1 settings: $(grep -v Key "$WORK/awgc.conf")"
	fi
	# The image has the AmneziaWG tools: run the client from it, in the host's network.
	docker run --rm --network host --cap-add NET_ADMIN --device /dev/net/tun -u root --entrypoint bash \
		-v "$WORK/awgc.conf:/c.conf:ro" "$IMAGE" -c '
		mkdir -p /etc/amnezia/amneziawg && install -m 600 /c.conf /etc/amnezia/amneziawg/tdawgc.conf
		awg-quick up tdawgc >/dev/null 2>&1 || exit 2
		for i in $(seq 15); do ping -c 1 -W 1 10.79.0.1 >/dev/null 2>&1 && ok=1 && break; done
		awg-quick down tdawgc >/dev/null 2>&1
		[ -n "${ok:-}" ]' || fail "no ping through AmneziaWG $level (rc $?)"
	api -X DELETE "$WEB/api/v1/servers/$id" || fail "remove AmneziaWG server"
	for _ in $(seq 10); do dc exec -T vpn ip link show "$id" >/dev/null 2>&1 || break; sleep 1; done
	! dc exec -T vpn ip link show "$id" >/dev/null 2>&1 || fail "$id still up after remove"
	log "AmneziaWG $level client connected through the obfuscated tunnel; remove works"
done

log "second OpenVPN server: added through the web API, TCP, run by the vpn container"
J=(-H "Content-Type: application/json")
api "${J[@]}" -d '{"type":"openvpn","id":"ovpn-tcp","name":"TCP","port":11195,"proto":"tcp"}' \
	"$WEB/api/v1/servers" >"$WORK/ovpn2.json" || fail "add OpenVPN server"
api "$WEB/api/v1/servers/ovpn-tcp/setup" | grep -q '"needed":true' || fail "setup status of the second server"
api "${J[@]}" -m 60 -d '{"public_host":"127.0.0.1","subnet":"10.80.0.0/24"}' \
	"$WEB/api/v1/servers/ovpn-tcp/setup" >"$WORK/setup2.json" || fail "setup of the second server"
grep -q '"running":true' "$WORK/setup2.json" || fail "second OpenVPN not running: $(cat "$WORK/setup2.json")"
dc logs vpn | grep -q "ovpn-tcp: NAT 10.80.0.0/255.255.255.0" || fail "no NAT for the second server"
dc exec -T vpn ip link show ovpn-tcp >/dev/null || fail "no ovpn-tcp device"
api "${J[@]}" -d '{"name":"bob"}' "$WEB/api/v1/servers/ovpn-tcp/certs" >/dev/null || fail "issue bob"
api "$WEB/api/v1/servers/ovpn-tcp/certs/bob/ovpn" >"$WORK/bob.ovpn" || fail "download bob's profile"
grep -q "^remote 127.0.0.1 11195" "$WORK/bob.ovpn" && grep -q "^proto tcp" "$WORK/bob.ovpn" || fail "bob's profile: $(head -20 "$WORK/bob.ovpn")"
sudo openvpn --config "$WORK/bob.ovpn" >"$WORK/bob.log" 2>&1 &
BOB_PID=$!
for _ in $(seq 30); do grep -q "Initialization Sequence Completed" "$WORK/bob.log" 2>/dev/null && break; sleep 1; done
grep -q "Initialization Sequence Completed" "$WORK/bob.log" || fail "bob could not connect over TCP: $(tail -20 "$WORK/bob.log")"
ping -c 2 -W 2 10.80.0.1 >/dev/null || fail "no traffic through the second server"
for _ in $(seq 15); do api "$WEB/api/v1/servers/ovpn-tcp/sessions" | grep -q '"name":"bob"' && break; sleep 1; done
api "$WEB/api/v1/servers/ovpn-tcp/sessions" | grep -q '"name":"bob"' || fail "bob not in the second server's sessions"
! api "$WEB/api/v1/servers/openvpn/certs" | grep -q '"name":"bob"' || fail "bob in the first server's clients"
api -X PUT "${J[@]}" -m 30 -d '{"enabled":true,"default":"deny","rules":[{"to":"10.80.0.1","proto":"icmp","action":"allow"}]}' \
	"$WEB/api/v1/servers/ovpn-tcp/firewall" >"$WORK/fw2.json" || fail "save access rules of the second server"
grep -q '"applied":true' "$WORK/fw2.json" || fail "second server's rules not applied: $(cat "$WORK/fw2.json")"
dc exec -T vpn iptables -C FORWARD -i ovpn-tcp -j TD-ovpn-tcp || fail "no jump to TD-ovpn-tcp"
sudo kill "$BOB_PID" 2>/dev/null || true
BOB_PID=
api -X DELETE "$WEB/api/v1/servers/ovpn-tcp" || fail "remove the second OpenVPN server"
for _ in $(seq 15); do dc exec -T vpn ip link show ovpn-tcp >/dev/null 2>&1 || break; sleep 1; done
! dc exec -T vpn ip link show ovpn-tcp >/dev/null 2>&1 || fail "ovpn-tcp still up after remove"
for _ in $(seq 10); do dc exec -T vpn iptables -S TD-ovpn-tcp >/dev/null 2>&1 || break; sleep 1; done
! dc exec -T vpn iptables -S TD-ovpn-tcp >/dev/null 2>&1 || fail "TD-ovpn-tcp left after remove"
dc logs vpn | grep -q "ovpn-tcp: removed, stopped" || fail "no remove log"
sudo sh -c "ls -d '$WORK'/openvpn/servers/.removed/ovpn-tcp-*" >/dev/null || fail "removed server's files not kept"
api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' || fail "the first server stopped too"
log "second OpenVPN server: client over TCP, own clients and access rules; remove works"

log "web container is locked down"
[[ $(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' tdsmoke-web-1) == true ]] || fail "web rootfs not read-only"
[[ $(dc exec -T web id -u) == 10001 ]] || fail "web not running as uid 10001"
[[ $(docker inspect -f '{{json .HostConfig.PortBindings}}' tdsmoke-vpn-1) != *2080* ]] || fail "management port published"

log "PASS"
