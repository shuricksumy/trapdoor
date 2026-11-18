#!/usr/bin/env bash
# Docker smoke test: the real compose file with a locally built image.
# A fresh OpenVPN setup is created, the web UI/API is used to issue
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
dc() { docker compose -f "$WORK/docker-compose.yml" -p twsmoke "$@"; }
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
	dc down -v --remove-orphans >/dev/null 2>&1 || true
	sudo rm -rf "$WORK"
}
trap cleanup EXIT

cp "$REPO/docker-compose.yml" "$WORK/"
mkdir -p "$WORK/openvpn"
cat >"$WORK/.env" <<ENV
TW_IMAGE=$IMAGE
TW_AGENT_TOKEN=$(openssl rand -hex 16)
TW_WEB_API_TOKEN=$TOKEN
TW_COOKIE_SECURE=false
TW_TZ=Europe/Dublin
OPENVPN_DIR=$WORK/openvpn
VPN_PORT=11194
VPN_NAT_SUBNET=10.77.0.0/24
WEB_BIND=127.0.0.1:18080
ENV

log "image runs as non-root and reports its version"
docker run --rm "$IMAGE" version
[[ $(docker run --rm --entrypoint id "$IMAGE" -u) == 10001 ]] || fail "image does not run as uid 10001"

log "new PKI and tls-crypt-v2 key (one-off containers)"
docker run --rm -u root -v "$WORK/openvpn:/etc/openvpn" "$IMAGE" init-pki
docker run --rm -u root -v "$WORK/openvpn:/etc/openvpn" --entrypoint openvpn "$IMAGE" \
	--genkey tls-crypt-v2-server /etc/openvpn/tls-crypt-v2.key

log "compose up (openvpn waits for server.conf)"
(cd "$WORK" && dc up -d)
for _ in $(seq 60); do curl -fs "$WEB/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fs "$WEB/healthz" >/dev/null || fail "web not healthy"
curl -fs "$WEB/" | grep -q 'id="app"' || fail "UI not served"

log "server certificate via the web API, then server.conf"
api -H "Content-Type: application/json" -d '{"name":"server","kind":"server","validity_days":3650}' "$WEB/api/v1/certs" >/dev/null
sudo tee "$WORK/openvpn/server.conf" >/dev/null <<CONF
port 1194
proto udp
dev tun
topology subnet
server 10.77.0.0 255.255.255.0
ca easy-rsa/pki/ca.crt
cert easy-rsa/pki/issued/server.crt
key easy-rsa/pki/private/server.key
dh none
tls-crypt-v2 tls-crypt-v2.key
crl-verify crl.pem
keepalive 2 6
management 127.0.0.1 2080
verb 3
CONF
sudo tee "$WORK/openvpn/client-template.txt" >/dev/null <<CONF
client
proto udp
remote 127.0.0.1 11194
dev tun
nobind
remote-cert-tls server
verb 3
CONF

log "agent reaches OpenVPN's management interface (shared network namespace)"
for _ in $(seq 60); do api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' && break; sleep 1; done
api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' || fail "agent cannot reach management"
dc exec -T openvpn sh -c 'netstat -ltn 2>/dev/null || ss -ltn' | grep -q '127.0.0.1:2080' || fail "management not on 127.0.0.1"

log "admin user via CLI in the web container, login with cookie"
echo "smoke-admin-password" | dc exec -T web tunnelward user add admin >/dev/null
JAR=$WORK/jar
curl -fsS -c "$JAR" -H "Content-Type: application/json" -d '{"username":"admin","password":"smoke-admin-password"}' "$WEB/api/v1/auth/login" >/dev/null || fail "login"

log "issue alice through the UI's API and connect from the host"
curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -d '{"name":"alice"}' "$WEB/api/v1/certs" >/dev/null
curl -fsS -b "$JAR" "$WEB/api/v1/certs/alice/ovpn" >"$WORK/alice.ovpn" || fail "download profile"
sudo openvpn --config "$WORK/alice.ovpn" >"$WORK/client.log" 2>&1 &
CLIENT_PID=$!
for _ in $(seq 30); do grep -q "Initialization Sequence Completed" "$WORK/client.log" 2>/dev/null && break; sleep 1; done
grep -q "Initialization Sequence Completed" "$WORK/client.log" || fail "alice could not connect"
for _ in $(seq 15); do curl -fsS -b "$JAR" "$WEB/api/v1/sessions" | grep -q '"name":"alice"' && break; sleep 1; done
curl -fsS -b "$JAR" "$WEB/api/v1/sessions" | grep -q '"name":"alice"' || fail "alice not in live sessions"
ping -c 2 -W 2 10.77.0.1 >/dev/null || fail "no traffic through the tunnel"

log "revoke disconnects alice"
curl -fsS -b "$JAR" -H "Origin: $WEB" -X POST "$WEB/api/v1/certs/alice/revoke" >/dev/null
for _ in $(seq 10); do curl -fsS -b "$JAR" "$WEB/api/v1/sessions" | grep -q '"name":"alice"' || break; sleep 1; done
! curl -fsS -b "$JAR" "$WEB/api/v1/sessions" | grep -q '"name":"alice"' || fail "revoked alice still online"

log "server config: change via the web API, restart through management (supervised SIGTERM)"
wj() { curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" "$@"; }
wj "$WEB/api/v1/server/config" >"$WORK/sc.json"
python3 - "$WORK/sc.json" >"$WORK/sc-new.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
s = c["settings"]
s["dns"] = ["9.9.9.9"]
print(json.dumps({"base_version": c["version"], "settings": s}))
PY
wj -X PUT -d @"$WORK/sc-new.json" "$WEB/api/v1/server/config" >/dev/null || fail "save server config"
wj -X POST -d '{"rollback":true}' "$WEB/api/v1/server/restart" >"$WORK/restart.json"
grep -q '"ok":true' "$WORK/restart.json" || fail "restart: $(cat "$WORK/restart.json")"
dc logs openvpn | grep -q "starting again (restart requested)" || fail "supervisor did not restart OpenVPN"
api "$WEB/api/v1/status" | grep -q '"openvpn_version":"OpenVPN' || fail "agent lost OpenVPN after restart (network namespace)"

log "a config that passes the checks but cannot start is rolled back"
wj "$WEB/api/v1/server/config" >"$WORK/sc2.json"
python3 - "$WORK/sc2.json" >"$WORK/sc-bad.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
print(json.dumps({"base_version": c["version"], "raw": c["raw"] + "local 203.0.113.99\n"}))
PY
wj -X PUT -d @"$WORK/sc-bad.json" "$WEB/api/v1/server/config" >/dev/null || fail "save bad-at-start config"
curl -sS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -m 150 -X POST -d '{"rollback":true}' "$WEB/api/v1/server/restart" >"$WORK/rollback.json"
grep -q '"rolled_back":true' "$WORK/rollback.json" || fail "no rollback: $(cat "$WORK/rollback.json")"
grep -q "running again" "$WORK/rollback.json" || fail "OpenVPN not back after rollback: $(cat "$WORK/rollback.json")"
! sudo grep -q "203.0.113.99" "$WORK/openvpn/server.conf" || fail "bad config still in place"
grep -q "9.9.9.9" "$WORK/openvpn/server.conf" || fail "rolled back to the wrong version"

log "web container is locked down"
[[ $(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' twsmoke-web-1) == true ]] || fail "web rootfs not read-only"
[[ $(dc exec -T web id -u) == 10001 ]] || fail "web not running as uid 10001"
[[ $(docker inspect -f '{{json .HostConfig.PortBindings}}' twsmoke-openvpn-1) != *2080* ]] || fail "management port published"

log "PASS"
