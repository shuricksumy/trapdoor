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
VPN_PORT=11194
WEB_BIND=127.0.0.1:18080
ENV

log "image runs as non-root and reports its version"
docker run --rm "$IMAGE" version
[[ $(docker run --rm --entrypoint id "$IMAGE" -u) == 10001 ]] || fail "image does not run as uid 10001"

log "compose up (openvpn waits for server.conf)"
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
dc logs openvpn | grep -q "NAT 10.77.0.0/255.255.255.0" || fail "NAT not derived from server.conf"
sudo grep -q "^remote 127.0.0.1 11194$" "$WORK/openvpn/client-template.txt" || fail "client template"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
	-d '{"public_host":"127.0.0.1"}' "$WEB/api/v1/servers/openvpn/setup")
[[ $code == 409 ]] || fail "second setup answered $code, want 409"

log "agent reaches OpenVPN's management interface (shared network namespace)"
for _ in $(seq 60); do api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' && break; sleep 1; done
api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' || fail "agent cannot reach management"
dc exec -T openvpn sh -c 'netstat -ltn 2>/dev/null || ss -ltn' | grep -q '127.0.0.1:2080' || fail "management not on 127.0.0.1"

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
dc logs openvpn | grep -q "starting again (restart requested)" || fail "supervisor did not restart OpenVPN"
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

log "access rules: written by the agent, applied by the openvpn container (TD_FIREWALL=file)"
api -X PUT -H "Content-Type: application/json" -m 30 \
	-d '{"enabled":true,"default":"deny","rules":[{"to":"10.77.0.1","proto":"icmp","action":"allow"}]}' \
	"$WEB/api/v1/servers/openvpn/firewall" >"$WORK/fw.json" || fail "save access rules"
grep -q '"applied":true' "$WORK/fw.json" || fail "rules not applied: $(cat "$WORK/fw.json")"
dc exec -T openvpn iptables -S TD-FWD | grep -q -- "-d 10.77.0.1/32 -p icmp -j ACCEPT" || fail "rule not in the container"
dc exec -T openvpn iptables -C FORWARD -i tun+ -j TD-FWD || fail "no FORWARD jump"
dc logs openvpn | grep -q "access rules applied" || fail "no apply log"
! dc exec -T agent iptables -N TD-PROBE >/dev/null 2>&1 || fail "agent container can change the firewall"

log "WireGuard: created through the web API, run by the openvpn container (TD_WIREGUARD=file)"
api -H "Content-Type: application/json" \
	-d '{"type":"wireguard","id":"wg0","name":"Smoke","port":15182,"public_host":"127.0.0.1","subnet":"10.78.0.0/24","allowed_ips":["10.78.0.0/24"]}' \
	"$WEB/api/v1/servers" >"$WORK/wg.json" || fail "create WireGuard server"
wgrunning() { api "$WEB/api/v1/status" | grep -q '"id":"wg0"[^}]*"running":true'; }
for _ in $(seq 20); do wgrunning && break; sleep 1; done
wgrunning || fail "wg0 not running: $(api "$WEB/api/v1/status")"
dc logs openvpn | grep -q "wireguard wg0: up (10.78.0.1/24)" || fail "no wg0 up log"
sudo head -1 "$WORK/wireguard/wg0.dump" | grep -q '^(hidden)' || fail "private key in the dump file"
api -H "Content-Type: application/json" -d '{"name":"wgsmoke"}' "$WEB/api/v1/servers/wg0/peers" >/dev/null || fail "add WireGuard client"
sudo mkdir -p /etc/wireguard
api "$WEB/api/v1/servers/wg0/peers/wgsmoke/conf" | sudo sh -c 'umask 077; cat >/etc/wireguard/tdsmoke.conf'
sudo wg-quick up /etc/wireguard/tdsmoke.conf >/dev/null 2>&1 || fail "host WireGuard client"
wgping() { ping -c 1 -W 1 10.78.0.1 >/dev/null 2>&1; }
for _ in $(seq 15); do wgping && break; sleep 1; done
wgping || fail "no ping through WireGuard: $(dc logs --tail 20 openvpn)"
! sudo awk -F'\t' 'NR > 1 && $2 != "(hidden)"' "$WORK/wireguard/wg0.dump" | grep -q . || fail "preshared key in the dump file"
for _ in $(seq 10); do api "$WEB/api/v1/servers/wg0/sessions" | grep -q '"name":"wgsmoke"' && break; sleep 1; done
api "$WEB/api/v1/servers/wg0/sessions" | grep -q '"name":"wgsmoke"' || fail "wgsmoke not in live sessions"
api -X PUT -H "Content-Type: application/json" -d '{"disabled":true}' "$WEB/api/v1/servers/wg0/peers/wgsmoke" >/dev/null
for _ in $(seq 10); do dc logs openvpn | grep -q "wireguard wg0: peers updated" && break; sleep 1; done
sleep 1
! wgping || fail "disabled WireGuard client still gets through"
sudo wg-quick down /etc/wireguard/tdsmoke.conf >/dev/null 2>&1 || true
api -X DELETE "$WEB/api/v1/servers/wg0" || fail "remove WireGuard server"
for _ in $(seq 10); do dc exec -T openvpn ip link show wg0 >/dev/null 2>&1 || break; sleep 1; done
! dc exec -T openvpn ip link show wg0 >/dev/null 2>&1 || fail "wg0 still up after remove"
log "WireGuard client connected through the container; disable and remove work"

log "web container is locked down"
[[ $(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' tdsmoke-web-1) == true ]] || fail "web rootfs not read-only"
[[ $(dc exec -T web id -u) == 10001 ]] || fail "web not running as uid 10001"
[[ $(docker inspect -f '{{json .HostConfig.PortBindings}}' tdsmoke-openvpn-1) != *2080* ]] || fail "management port published"

log "PASS"
