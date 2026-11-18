#!/usr/bin/env bash
# End-to-end test: a real OpenVPN server and client on localhost, using only
# certificates, CRL and .ovpn profiles produced by tunnelward.
#
# Needs root (tun device), openvpn and curl. Usage: test/e2e/run.sh [bin]
set -euo pipefail

BIN=$(realpath "${1:-bin/tunnelward}")
WORK=$(mktemp -d)
PORT=11940
TOKEN=e2e
API=http://127.0.0.1:19090
PIDS=()

cleanup() {
	for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
	wait 2>/dev/null || true
	[[ "${KEEP:-}" == 1 ]] || rm -rf "$WORK"
}
trap cleanup EXIT

log() { echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; for f in "$WORK"/*.log; do echo "--- $f"; tail -20 "$f"; done; exit 1; }
api() { curl -fsS -H "X-Tunnelward-Token: $TOKEN" "$@"; }

export TW_AGENT_TOKEN=$TOKEN TW_AGENT_LISTEN=127.0.0.1:19090 \
	TW_OPENVPN_DIR=$WORK TW_PKI_DIR=$WORK/pki TW_CRL_COPIES=- \
	TW_OPENVPN_MGMT=127.0.0.1:12080 \
	TW_WEB_LISTEN=127.0.0.1:18080 TW_AGENT_URL=$API TW_DB_PATH=$WORK/tw.db \
	TW_WEB_API_TOKEN=e2e-web-token TW_POLL_INTERVAL=5s TW_TZ=UTC
WEB=http://127.0.0.1:18080
webapi() { curl -fsS -H "Authorization: Bearer e2e-web-token" "$@"; }

log "init PKI and start agent"
"$BIN" init-pki 2>/dev/null
"$BIN" agent >"$WORK/agent.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 50); do curl -fs "$API/healthz" >/dev/null 2>&1 && break; sleep 0.1; done

log "start web (records session history every 5s)"
"$BIN" web >"$WORK/web.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 50); do curl -fs "$WEB/healthz" >/dev/null 2>&1 && break; sleep 0.1; done
curl -s -o /dev/null -w "%{http_code}" "$WEB/api/v1/stats" | grep -q 401 || fail "web API open without token"

log "issue server and client certificates"
api -d '{"name":"server","kind":"server"}' "$API/v1/certs" >/dev/null
api -d '{"name":"alice"}' "$API/v1/certs" >/dev/null

openvpn --genkey tls-crypt-v2-server "$WORK/tls-crypt-v2.key"
cat >"$WORK/server.conf" <<CONF
port $PORT
proto udp
dev tun
topology subnet
server 10.99.0.0 255.255.255.0
ca pki/ca.crt
cert pki/issued/server.crt
key pki/private/server.key
dh none
tls-crypt-v2 tls-crypt-v2.key
crl-verify pki/crl.pem
keepalive 2 6
management 127.0.0.1 12080
verb 3
CONF
cat >"$WORK/client-template.txt" <<CONF
client
proto udp
remote 127.0.0.1 $PORT
dev tun
nobind
remote-cert-tls server
verify-x509-name server name
connect-retry-max 1
verb 3
CONF

log "start OpenVPN server"
(cd "$WORK" && openvpn --config server.conf >"$WORK/server.log" 2>&1) &
PIDS+=($!)
for _ in $(seq 50); do grep -q "Initialization Sequence Completed" "$WORK/server.log" && break; sleep 0.2; done
grep -q "Initialization Sequence Completed" "$WORK/server.log" || fail "server did not start"

# connect <profile> <log>: true if the client finished initialization
connect() {
	openvpn --config "$1" >"$2" 2>&1 &
	local pid=$!
	for _ in $(seq 75); do
		if grep -q "Initialization Sequence Completed" "$2"; then kill "$pid"; wait "$pid" 2>/dev/null || true; return 0; fi
		kill -0 "$pid" 2>/dev/null || break
		sleep 0.2
	done
	kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true
	return 1
}

log "download alice.ovpn and connect"
api "$API/v1/certs/alice/ovpn" >"$WORK/alice.ovpn"
connect "$WORK/alice.ovpn" "$WORK/client1.log" || fail "alice could not connect"
log "alice connected"

# wait_for <seconds> <command...>: retry until the command succeeds
wait_for() {
	local n=$(($1 * 5)); shift
	for _ in $(seq "$n"); do "$@" && return 0; sleep 0.2; done
	return 1
}
online() { api "$API/v1/sessions" | grep -q "\"name\":\"$1\""; }
offline() { ! online "$1"; }

log "live session: alice shows up, kick disconnects her"
openvpn --config "$WORK/alice.ovpn" >"$WORK/live.log" 2>&1 &
LIVE=$!
PIDS+=($LIVE)
wait_for 15 online alice || fail "alice not in /v1/sessions"
ping -c 3 -i 0.3 -W 1 10.99.0.1 >/dev/null 2>&1 || true # some traffic through the tunnel
sleep 6 # let the web process record at least one poll
api "$API/v1/info" | grep -q '"openvpn_version":"OpenVPN 2' || fail "no openvpn_version in /v1/info"
api -X DELETE "$API/v1/certs/alice/sessions" | grep -q '"killed":1' || fail "kick did not kill 1 session"
wait_for 5 offline alice || fail "alice still online after kick"
log "alice kicked (her client reconnects on its own after the ping timeout)"

log "revoke while connected: session is dropped and cannot come back"
wait_for 30 online alice || fail "alice did not reconnect after kick"
api -X POST "$API/v1/certs/alice/revoke" >/dev/null
wait_for 5 offline alice || fail "revoked alice still online"
wait_for 30 grep -q "CRL CHECK FAILED\|certificate revoked" "$WORK/server.log" || fail "server did not reject her reconnect by CRL"
offline alice || fail "revoked alice came back"
kill "$LIVE" 2>/dev/null || true
log "revoked alice disconnected and rejected by CRL"
api -X POST "$API/v1/certs/alice/unrevoke" >/dev/null

log "revoke alice: connection must be refused"
api -X POST "$API/v1/certs/alice/revoke" >/dev/null
connect "$WORK/alice.ovpn" "$WORK/client2.log" && fail "revoked alice still connected"
grep -q "CRL CHECK FAILED\|certificate revoked" "$WORK/server.log" || fail "server did not reject by CRL"
log "alice rejected by CRL"

log "unrevoke alice and connect again"
api -X POST "$API/v1/certs/alice/unrevoke" >/dev/null
connect "$WORK/alice.ovpn" "$WORK/client3.log" || fail "unrevoked alice could not connect"

log "renew alice: new profile works, old one is rejected"
cp "$WORK/alice.ovpn" "$WORK/alice-old.ovpn"
api -X POST "$API/v1/certs/alice/renew" >/dev/null
api "$API/v1/certs/alice/ovpn" >"$WORK/alice.ovpn"
connect "$WORK/alice.ovpn" "$WORK/client4.log" || fail "renewed alice could not connect"
connect "$WORK/alice-old.ovpn" "$WORK/client5.log" && fail "superseded profile still connected"

log "passphrase key: profile carries the encrypted key"
api -d '{"name":"bob","passphrase":"bobpass1"}' "$API/v1/certs" >/dev/null
api "$API/v1/certs/bob/ovpn" >"$WORK/bob.ovpn"
grep -q "BEGIN ENCRYPTED PRIVATE KEY" "$WORK/bob.ovpn" || fail "bob key not encrypted"
echo bobpass1 >"$WORK/bob.pass"
echo "askpass $WORK/bob.pass" >>"$WORK/bob.ovpn"
connect "$WORK/bob.ovpn" "$WORK/client6.log" || fail "bob could not connect with passphrase"

log "session history: alice's sessions and traffic were recorded"
sleep 6
webapi "$WEB/api/v1/stats?preset=today" >"$WORK/stats.json"
python3 - "$WORK/stats.json" <<'PY' || fail "stats: $(cat "$WORK/stats.json")"
import json, sys
r = json.load(open(sys.argv[1]))
alice = next(u for u in r["users"] if u["name"] == "alice")
assert alice["sessions"] >= 2, alice
assert alice["upload"] > 0 and alice["download"] > 0, alice
assert alice["online_seconds"] > 0, alice
assert r["series"][0]["upload"] == r["total"]["upload"], r
print("    alice:", alice)
PY
webapi "$WEB/api/v1/history/sessions?preset=today&name=alice" | grep -q '"disconnected_at"' || fail "no closed session in history"
webapi "$WEB/api/v1/stats.csv?preset=today" | grep -q "^[0-9-]*,alice," || fail "csv has no alice row"

log "PASS"
