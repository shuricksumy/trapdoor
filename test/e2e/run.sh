#!/usr/bin/env bash
# End-to-end test: a real OpenVPN server and client on localhost, using only
# certificates, CRL and .ovpn profiles produced by trapdoor.
#
# Needs root (tun device), openvpn and curl. Usage: test/e2e/run.sh [bin]
set -euo pipefail

BIN=$(realpath "${1:-bin/trapdoor}")
WORK=$(mktemp -d)
PORT=11940
TOKEN=e2e
API=http://127.0.0.1:19090
PIDS=()

cleanup() {
	for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
	wait 2>/dev/null || true
	ip netns exec twc ip link del tdwc 2>/dev/null || true
	ip link del tdwg0 2>/dev/null || true
	for c in INPUT FORWARD; do iptables -D "$c" -i tdwg0 -j TD-tdwg0 2>/dev/null || true; done
	iptables -F TD-tdwg0 2>/dev/null && iptables -X TD-tdwg0 2>/dev/null || true
	rm -f /etc/wireguard/tdwg0.* /etc/wireguard/tdwc.conf
	ip netns del twc 2>/dev/null || true
	ip link del twh 2>/dev/null || true
	for c in INPUT FORWARD; do iptables -D "$c" -i tun+ -j TD-FWD 2>/dev/null || true; done
	iptables -F TD-FWD 2>/dev/null && iptables -X TD-FWD 2>/dev/null || true
	[[ "${KEEP:-}" == 1 ]] || rm -rf "$WORK"
}
trap cleanup EXIT

log() { echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; for f in "$WORK"/*.log; do echo "--- $f"; tail -20 "$f"; done; exit 1; }
api() { curl -fsS -H "X-Trapdoor-Token: $TOKEN" "$@"; }

export TD_AGENT_TOKEN=$TOKEN TD_AGENT_LISTEN=127.0.0.1:19090 \
	TD_OPENVPN_DIR=$WORK TD_PKI_DIR=$WORK/pki TD_CRL_COPIES=- \
	TD_OPENVPN_MGMT=127.0.0.1:12080 TD_WIREGUARD=direct TD_WIREGUARD_DIR=/etc/wireguard \
	TD_WEB_LISTEN=127.0.0.1:18080 TD_AGENT_URL=$API TD_DB_PATH=$WORK/td.db \
	TD_WEB_API_TOKEN=e2e-web-token TD_POLL_INTERVAL=5s TD_TZ=UTC TD_COOKIE_SECURE=false
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
curl -s -o /dev/null -w "%{http_code}" "$WEB/api/v1/servers/openvpn/stats" | grep -q 401 || fail "web API open without login"
if curl -fsS "$WEB/" | grep -q 'id="app"'; then
	curl -fsSI "$WEB/certificates" | grep -qi "content-security-policy: default-src 'self'" || fail "UI served without CSP"
	log "web UI served (with CSP)"
fi
TD_ADMIN_PASSWORD=e2e-admin-password "$BIN" user add admin >/dev/null
JAR=$WORK/cookies
code=$(curl -s -o /dev/null -w "%{http_code}" -c "$JAR" -H "Content-Type: application/json" \
	-d '{"username":"admin","password":"wrong-password"}' "$WEB/api/v1/auth/login")
[[ $code == 401 ]] || fail "wrong admin password accepted ($code)"
curl -fsS -c "$JAR" -H "Content-Type: application/json" \
	-d '{"username":"admin","password":"e2e-admin-password"}' "$WEB/api/v1/auth/login" >/dev/null || fail "admin login failed"
curl -fsS -b "$JAR" "$WEB/api/v1/auth/me" | grep -q '"username":"admin"' || fail "session cookie not accepted"
log "admin logged in"

log "issue server and client certificates"
api -d '{"name":"server","kind":"server"}' "$API/v1/servers/openvpn/certs" >/dev/null
api -d '{"name":"alice"}' "$API/v1/servers/openvpn/certs" >/dev/null

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
client-config-dir ccd
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

mkdir -p "$WORK/ccd"
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
api "$API/v1/servers/openvpn/certs/alice/ovpn" >"$WORK/alice.ovpn"
connect "$WORK/alice.ovpn" "$WORK/client1.log" || fail "alice could not connect"
log "alice connected"

# wait_for <seconds> <command...>: retry until the command succeeds
wait_for() {
	local n=$(($1 * 5)); shift
	for _ in $(seq "$n"); do "$@" && return 0; sleep 0.2; done
	return 1
}
online() { api "$API/v1/servers/openvpn/sessions" | grep -q "\"name\":\"$1\""; }
offline() { ! online "$1"; }

log "live session: alice shows up, kick disconnects her"
openvpn --config "$WORK/alice.ovpn" >"$WORK/live.log" 2>&1 &
LIVE=$!
PIDS+=($LIVE)
wait_for 15 online alice || fail "alice not in /v1/sessions"
ping -c 3 -i 0.3 -W 1 10.99.0.1 >/dev/null 2>&1 || true # some traffic through the tunnel
sleep 6 # let the web process record at least one poll
api "$API/v1/info" | grep -q '"openvpn_version":"OpenVPN 2' || fail "no openvpn_version in /v1/info"
api -X DELETE "$API/v1/servers/openvpn/certs/alice/sessions" | grep -q '"killed":1' || fail "kick did not kill 1 session"
wait_for 5 offline alice || fail "alice still online after kick"
log "alice kicked (her client reconnects on its own after the ping timeout)"

log "revoke while connected: session is dropped and cannot come back"
wait_for 30 online alice || fail "alice did not reconnect after kick"
api -X POST "$API/v1/servers/openvpn/certs/alice/revoke" >/dev/null
wait_for 5 offline alice || fail "revoked alice still online"
wait_for 30 grep -q "CRL CHECK FAILED\|certificate revoked" "$WORK/server.log" || fail "server did not reject her reconnect by CRL"
offline alice || fail "revoked alice came back"
kill "$LIVE" 2>/dev/null || true
log "revoked alice disconnected and rejected by CRL"
api -X POST "$API/v1/servers/openvpn/certs/alice/unrevoke" >/dev/null

log "revoke alice: connection must be refused"
api -X POST "$API/v1/servers/openvpn/certs/alice/revoke" >/dev/null
connect "$WORK/alice.ovpn" "$WORK/client2.log" && fail "revoked alice still connected"
grep -q "CRL CHECK FAILED\|certificate revoked" "$WORK/server.log" || fail "server did not reject by CRL"
log "alice rejected by CRL"

log "unrevoke alice and connect again"
api -X POST "$API/v1/servers/openvpn/certs/alice/unrevoke" >/dev/null
connect "$WORK/alice.ovpn" "$WORK/client3.log" || fail "unrevoked alice could not connect"

log "renew alice: new profile works, old one is rejected"
cp "$WORK/alice.ovpn" "$WORK/alice-old.ovpn"
api -X POST "$API/v1/servers/openvpn/certs/alice/renew" >/dev/null
api "$API/v1/servers/openvpn/certs/alice/ovpn" >"$WORK/alice.ovpn"
connect "$WORK/alice.ovpn" "$WORK/client4.log" || fail "renewed alice could not connect"
connect "$WORK/alice-old.ovpn" "$WORK/client5.log" && fail "superseded profile still connected"

log "passphrase key: profile carries the encrypted key"
api -d '{"name":"bob","passphrase":"bobpass1"}' "$API/v1/servers/openvpn/certs" >/dev/null
api "$API/v1/servers/openvpn/certs/bob/ovpn" >"$WORK/bob.ovpn"
grep -q "BEGIN ENCRYPTED PRIVATE KEY" "$WORK/bob.ovpn" || fail "bob key not encrypted"
echo bobpass1 >"$WORK/bob.pass"
echo "askpass $WORK/bob.pass" >>"$WORK/bob.ovpn"
connect "$WORK/bob.ovpn" "$WORK/client6.log" || fail "bob could not connect with passphrase"

log "web: admin issues carol and downloads her profile through the web API"
curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -d '{"name":"carol"}' "$WEB/api/v1/servers/openvpn/certs" >/dev/null || fail "web issue failed"
curl -fsS -b "$JAR" "$WEB/api/v1/servers/openvpn/certs/carol/ovpn" >"$WORK/carol.ovpn" || fail "web download failed"
connect "$WORK/carol.ovpn" "$WORK/client7.log" || fail "carol could not connect"
grep -q '"action":"cert.issue"\|action=cert.issue' "$WORK/web.log" || fail "no audit log line"

log "routing: static VPN address and a pushed route for alice (web API)"
curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -X PUT \
	-d '{"static_ip":"10.99.0.50","routes":["192.168.123.0/24"],"lan":[],"full_tunnel":false}' \
	"$WEB/api/v1/servers/openvpn/certs/alice/network" >"$WORK/alice-net.json" || fail "set network"
grep -q '"static_ip":"10.99.0.50"' "$WORK/alice-net.json" || fail "network not saved: $(cat "$WORK/alice-net.json")"
connect "$WORK/alice.ovpn" "$WORK/client-net.log" || fail "alice could not connect with network settings"
grep -q "10.99.0.50" "$WORK/client-net.log" || fail "alice did not get 10.99.0.50"
grep -q "192.168.123.0" "$WORK/client-net.log" || fail "alice did not get the pushed route"
code=$(curl -s -o /dev/null -w "%{http_code}" -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -X PUT \
	-d '{"static_ip":"10.99.0.50","routes":[],"lan":[]}' "$WEB/api/v1/servers/openvpn/certs/carol/network")
[[ $code == 400 ]] || fail "duplicate static address accepted ($code)"
log "alice got 10.99.0.50 and the route; the same address for carol was refused"

log "named network: a route given to alice through a named network (web API)"
curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -X PUT \
	-d '{"networks":["192.168.124.0/24"],"clients":["alice"]}' \
	"$WEB/api/v1/servers/openvpn/named-networks/Lab" >"$WORK/named.json" || fail "save named network"
connect "$WORK/alice.ovpn" "$WORK/client-named.log" || fail "alice could not connect with a named network"
grep -q "192.168.124.0" "$WORK/client-named.log" || fail "alice did not get the named network's route"
grep -q "192.168.123.0" "$WORK/client-named.log" || fail "alice lost her own route"
curl -fsS -b "$JAR" -H "Origin: $WEB" -X DELETE "$WEB/api/v1/servers/openvpn/named-networks/Lab" >/dev/null || fail "remove named network"
! grep -q "192.168.124.0" "$WORK/ccd/alice" || fail "named route left in ccd/alice"
log "alice got the named network's route next to her own; removing it takes it off"

log "access rules: alice in her own network namespace, filtered by real iptables (direct mode)"
ip netns add twc
ip link add twh type veth peer name twn
ip link set twn netns twc
ip addr add 10.200.0.1/24 dev twh
ip link set twh up
ip netns exec twc ip addr add 10.200.0.2/24 dev twn
ip netns exec twc ip link set twn up
ip netns exec twc ip link set lo up
sed "s/^remote .*/remote 10.200.0.1 $PORT/" "$WORK/alice.ovpn" >"$WORK/alice-ns.ovpn"
ip netns exec twc openvpn --config "$WORK/alice-ns.ovpn" >"$WORK/client-ns.log" 2>&1 &
NSCLIENT=$!
PIDS+=($NSCLIENT)
wait_for 20 grep -q "Initialization Sequence Completed" "$WORK/client-ns.log" || fail "alice could not connect from the namespace"
nsping() { ip netns exec twc ping -c 1 -W 1 10.99.0.1 >/dev/null 2>&1; }
wait_for 10 nsping || fail "no ping through the tunnel before any rules"
fw() { curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -X PUT -d "$1" "$WEB/api/v1/servers/openvpn/firewall"; }
fw '{"enabled":true,"default":"deny","rules":[]}' >"$WORK/fw1.json" || fail "save access rules"
grep -q '"applied":true' "$WORK/fw1.json" || fail "rules not applied: $(cat "$WORK/fw1.json")"
iptables -C INPUT -i tun+ -j TD-FWD && iptables -C FORWARD -i tun+ -j TD-FWD || fail "no jump to TD-FWD"
! nsping || fail "ping passes although everything is blocked"
fw '{"enabled":true,"default":"deny","rules":[{"client":"alice","to":"10.99.0.1","proto":"icmp","action":"allow"}]}' >"$WORK/fw2.json"
grep -q '"missing":\[\]' "$WORK/fw2.json" || fail "alice reported without fixed address: $(cat "$WORK/fw2.json")"
iptables -S TD-FWD | grep -q -- "-s 10.99.0.50/32 -d 10.99.0.1/32 -p icmp -j ACCEPT" || fail "alice's rule missing: $(iptables -S TD-FWD)"
wait_for 5 nsping || fail "allowed ping blocked"
fw '{"enabled":true,"default":"allow","rules":[{"client":"alice","proto":"icmp","action":"deny"}]}' >/dev/null
! nsping || fail "ping passes although alice's ICMP is blocked"
fw '{"enabled":false,"default":"deny","rules":[]}' >/dev/null
wait_for 5 nsping || fail "ping blocked although the rules are off"
[[ $(iptables -S TD-FWD | tail -1) == "-A TD-FWD -j RETURN" ]] || fail "disabled chain: $(iptables -S TD-FWD)"
kill "$NSCLIENT"; wait "$NSCLIENT" 2>/dev/null || true
wait_for 15 offline alice || fail "alice still online"
log "rules block and allow alice's real traffic; off means pass-through"

log "server config: push DNS + a route to everyone via the web, restart (SIGHUP), clients get them"
webj() { curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" "$@"; }
webj "$WEB/api/v1/servers/openvpn/server/config" >"$WORK/sc.json"
python3 - "$WORK/sc.json" >"$WORK/sc-new.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
s = c["settings"]
s["dns"] = ["9.9.9.9"]
s["push_routes"] = ["192.168.77.0/24"]
print(json.dumps({"base_version": c["version"], "settings": s}))
PY
webj -X PUT -d @"$WORK/sc-new.json" "$WEB/api/v1/servers/openvpn/server/config" >"$WORK/sc-saved.json" || fail "save server config"
grep -q '"restart_pending":true' "$WORK/sc-saved.json" || fail "no restart pending after save"
grep -q 'dhcp-option DNS 9.9.9.9' "$WORK/server.conf" || fail "server.conf not updated"
ls "$WORK/trapdoor-backups/" | grep -q '^server.conf.' || fail "no backup written"
webj -X POST -d '{"rollback":true}' "$WEB/api/v1/servers/openvpn/server/restart" >"$WORK/restart.json"
grep -q '"ok":true' "$WORK/restart.json" || fail "restart: $(cat "$WORK/restart.json")"
connect "$WORK/alice.ovpn" "$WORK/client-pushed.log" || fail "alice could not connect after restart"
grep -q "dhcp-option DNS 9.9.9.9" "$WORK/client-pushed.log" || fail "DNS not pushed"
grep -q "192.168.77.0" "$WORK/client-pushed.log" || fail "route not pushed"
log "OpenVPN restarted with the new config; alice got DNS 9.9.9.9 and 192.168.77.0/24"
code=$(curl -s -o /dev/null -w "%{http_code}" -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -X PUT \
	-d "{\"base_version\":\"$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$WORK/sc.json")\",\"raw\":\"port 1\\n\"}" "$WEB/api/v1/servers/openvpn/server/config")
[[ $code == 422 || $code == 409 ]] || fail "broken/stale config accepted ($code)"

log "2FA: second server checks logins with trapdoor auth-verify"
mkdir -p "$WORK/ccd"
cat >"$WORK/server-2fa.conf" <<CONF
port 11941
proto udp
dev tun
topology subnet
server 10.98.0.0 255.255.255.0
ca pki/ca.crt
cert pki/issued/server.crt
key pki/private/server.key
dh none
tls-crypt-v2 tls-crypt-v2.key
crl-verify pki/crl.pem
script-security 2
auth-user-pass-verify "$BIN auth-verify --ccd $WORK/ccd" via-file
keepalive 2 6
verb 3
CONF
(cd "$WORK" && openvpn --config server-2fa.conf >"$WORK/server-2fa.log" 2>&1) &
PIDS+=($!)
wait_for 10 grep -q "Initialization Sequence Completed" "$WORK/server-2fa.log" || fail "2FA server did not start"

# totp <key> hex|b32: current 6-digit code (HMAC-SHA256, 30 s), computed independently of trapdoor
totp() {
	python3 - "$1" "$2" <<'PY'
import sys, hmac, hashlib, struct, time, base64
k, fmt = sys.argv[1], sys.argv[2]
key = bytes.fromhex(k) if fmt == "hex" else base64.b32decode(k + "=" * (-len(k) % 8))
h = hmac.new(key, struct.pack(">Q", int(time.time()) // 30), hashlib.sha256).digest()
o = h[-1] & 15
print("%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000))
PY
}
# profile2fa <name> <user> <password>: the name's profile for the 2FA server with credentials
profile2fa() {
	printf '%s\n%s\n' "$2" "$3" >"$WORK/$1-$2.creds"
	sed 's/^remote 127.0.0.1 .*/remote 127.0.0.1 11941/' "$WORK/$1.ovpn" >"$WORK/$1-2fa.ovpn"
	printf 'auth-user-pass %s\nauth-retry none\n' "$WORK/$1-$2.creds" >>"$WORK/$1-2fa.ovpn"
	echo "$WORK/$1-2fa.ovpn"
}

webput() { curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -X PUT -d "$2" "$WEB/api/v1/servers/openvpn/certs/$1/auth"; }
webput carol '{"totp":"enable","static_password":"carolpin"}' >"$WORK/carol-2fa.json" || fail "enable 2FA via web"
SECRET=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["secret"])' "$WORK/carol-2fa.json")
grep -q '"otpauth_uri":"otpauth://totp/' "$WORK/carol-2fa.json" || fail "no otpauth URI"

connect "$(profile2fa carol carol "carolpin$(totp "$SECRET" b32)")" "$WORK/c2fa-ok.log" || fail "carol with password + code could not connect"
log "carol connected with static password + authenticator code"
connect "$(profile2fa carol carol "carolpin000000")" "$WORK/c2fa-bad.log" && fail "wrong code accepted"
grep -q AUTH_FAILED "$WORK/c2fa-bad.log" || fail "wrong code: no AUTH_FAILED"
log "wrong code refused"

curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -d '{"name":"dan"}' "$WEB/api/v1/servers/openvpn/certs" >/dev/null
curl -fsS -b "$JAR" "$WEB/api/v1/servers/openvpn/certs/dan/ovpn" >"$WORK/dan.ovpn"
connect "$(profile2fa dan carol "carolpin$(totp "$SECRET" b32)")" "$WORK/c2fa-cross.log" && fail "carol's credentials worked with dan's certificate"
log "carol's credentials refused on dan's certificate"

HEXKEY=$(python3 -c 'import os; print(os.urandom(32).hex())')
printf '#2FA OTP and/or static pass for auth.\n#2FA_KEY:%s\n#2FA_USER:dan\n#STATIC_PASS:danpin\n' "$HEXKEY" >"$WORK/ccd/dan"
connect "$(profile2fa dan dan "danpin$(totp "$HEXKEY" hex)")" "$WORK/c2fa-legacy.log" || fail "openvpn-ui style ccd login failed"
log "openvpn-ui style 2FA entry (hex key, plain static password) still works"

log "session history: alice's sessions and traffic were recorded"
sleep 6
curl -fsS -b "$JAR" "$WEB/api/v1/servers/openvpn/stats?preset=today" >"$WORK/stats.json" # as logged-in admin
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
webapi "$WEB/api/v1/servers/openvpn/history/sessions?preset=today&name=alice" | grep -q '"disconnected_at"' || fail "no closed session in history"
webapi "$WEB/api/v1/servers/openvpn/stats.csv?preset=today" | grep -q "^[0-9-]*,alice," || fail "csv has no alice row"

if command -v wg >/dev/null && command -v wg-quick >/dev/null; then
	mkdir -p /etc/wireguard
	log "WireGuard: server created through the agent, a client in the namespace connects"
	# 10.97.0.0/24: 10.98 and 10.99 belong to the OpenVPN servers above.
	api -d '{"type":"wireguard","id":"tdwg0","name":"WireGuard e2e","port":51830,"public_host":"10.200.0.1","subnet":"10.97.0.0/24","allowed_ips":["10.97.0.0/24"]}' \
		"$API/v1/servers" >"$WORK/wg-create.json" || fail "create WireGuard server: $(cat "$WORK/wg-create.json")"
	grep -q '"running":true' "$WORK/wg-create.json" || fail "WireGuard interface not up: $(cat "$WORK/wg-create.json")"
	ip link show tdwg0 >/dev/null || fail "no tdwg0 interface"
	api -d '{"name":"wgalice"}' "$API/v1/servers/tdwg0/peers" >/dev/null || fail "add peer"
	! api "$API/v1/servers/tdwg0/info" | grep -q '"error"' || fail "peer not applied: $(api "$API/v1/servers/tdwg0/info")"
	wg show tdwg0 peers | grep -q . || fail "peer not on the interface: $(wgdiag)"
	# Ubuntu's AppArmor profile lets wg-quick read configs only from /etc/wireguard.
	(umask 077 && api "$API/v1/servers/tdwg0/peers/wgalice/conf" >/etc/wireguard/tdwc.conf)
	ip netns exec twc wg-quick up /etc/wireguard/tdwc.conf >"$WORK/wg-client.log" 2>&1 || fail "client wg-quick up"
	wgping() { ip netns exec twc ping -c 1 -W 1 10.97.0.1 >/dev/null 2>&1; }
	wgdiag() {
		for c in "ip -br addr" "ip route" "ip route get 10.97.0.2" "wg show tdwg0 dump" "ip netns exec twc wg show all dump" "iptables -S" "dmesg"; do
			echo "### $c"
			$c 2>&1 | grep -v "^\s*$" | { [[ $c == dmesg ]] && grep -i apparmor | tail -20 || cat; } | cut -c1-220
		done
	}
	wait_for 10 wgping || fail "no ping through the WireGuard tunnel: $(wgdiag)"
	ip netns exec twc ping -c 20 -i 0.2 -s 1000 10.97.0.1 >/dev/null || true
	wgonline() { api "$API/v1/servers/tdwg0/sessions" | grep -q '"name":"wgalice"'; }
	wait_for 10 wgonline || fail "wgalice not in live sessions: $(api "$API/v1/servers/tdwg0/sessions")"
	webapi "$WEB/api/v1/status" | grep -q '"id":"tdwg0"' || fail "web status does not list tdwg0"
	webapi "$WEB/api/v1/servers/tdwg0/sessions" | grep -q '"name":"wgalice"' || fail "web live sessions lack wgalice"
	# The history polls every 5s; let it record the traffic before her session ends.
	sleep 6
	webapi "$WEB/api/v1/servers/tdwg0/stats?preset=today" >"$WORK/wg-stats.json"
	python3 - "$WORK/wg-stats.json" <<'PY' || fail "WireGuard stats: $(cat "$WORK/wg-stats.json")"
import json, sys
r = json.load(open(sys.argv[1]))
a = next(u for u in r["users"] if u["name"] == "wgalice")
assert a["upload"] > 20000 and a["download"] > 20000, a
print("    wgalice:", a)
PY
	log "WireGuard access rules: chain TD-tdwg0 filters wgalice's real traffic"
	wgfw() { curl -fsS -b "$JAR" -H "Origin: $WEB" -H "Content-Type: application/json" -X PUT -d "$1" "$WEB/api/v1/servers/tdwg0/firewall"; }
	wgfw '{"enabled":true,"default":"deny","rules":[]}' >"$WORK/wgfw1.json" || fail "save WireGuard rules"
	grep -q '"applied":true' "$WORK/wgfw1.json" || fail "WireGuard rules not applied: $(cat "$WORK/wgfw1.json")"
	iptables -C INPUT -i tdwg0 -j TD-tdwg0 && iptables -C FORWARD -i tdwg0 -j TD-tdwg0 || fail "no jump to TD-tdwg0"
	! wgping || fail "ping passes although everything is blocked"
	wgfw '{"enabled":true,"default":"deny","rules":[{"client":"wgalice","to":"10.97.0.1","proto":"icmp","action":"allow"}]}' >"$WORK/wgfw2.json"
	iptables -S TD-tdwg0 | grep -q -- "-s 10.97.0.2/32 -d 10.97.0.1/32 -p icmp -j ACCEPT" || fail "wgalice's rule missing: $(iptables -S TD-tdwg0)"
	wait_for 5 wgping || fail "allowed ping blocked"
	wgfw '{"enabled":false,"default":"deny","rules":[]}' >/dev/null
	wait_for 5 wgping || fail "ping blocked although the rules are off"
	iptables -S TD-FWD | grep -q -- "-j RETURN" || fail "OpenVPN chain changed by WireGuard rules"
	log "wgalice connected and shows up live; disable cuts her off without restarting the interface"
	port_before=$(wg show tdwg0 listen-port)
	api -X PUT -d '{"disabled":true}' "$API/v1/servers/tdwg0/peers/wgalice" >/dev/null || fail "disable"
	! wg show tdwg0 peers | grep -q . || fail "disabled peer still on the interface"
	! wgping || fail "disabled client still gets through"
	api -X PUT -d '{"disabled":false}' "$API/v1/servers/tdwg0/peers/wgalice" >/dev/null
	wait_for 30 wgping || fail "enabled client cannot get through" # its next handshake, up to ~15s
	[[ $(wg show tdwg0 listen-port) == "$port_before" ]] || fail "interface was restarted"
	ip netns exec twc wg-quick down /etc/wireguard/tdwc.conf >/dev/null 2>&1 || true
	api -X DELETE "$API/v1/servers/tdwg0" || fail "remove WireGuard server"
	! ip link show tdwg0 >/dev/null 2>&1 || fail "tdwg0 still there after remove"
	! iptables -S TD-tdwg0 >/dev/null 2>&1 || fail "TD-tdwg0 chain left after remove"
	log "traffic recorded per server; removing the server takes the interface down"
else
	log "WireGuard tools not installed: skipping the WireGuard test"
fi

log "PASS"
