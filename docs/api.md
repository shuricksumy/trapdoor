# HTTP API

Everything the web UI does goes through the web API, so it can be scripted too.

## Web API

Log in like the browser does (`POST /api/v1/auth/login` sets the session cookie), or set
`TD_WEB_API_TOKEN` and send `Authorization: Bearer <token>`. Errors are JSON
`{"error": "..."}`.

Everything that belongs to one VPN server is under `/api/v1/servers/{server}/`, where
`{server}` is the server's id from `/api/v1/status` (`openvpn` for the first OpenVPN
server). Below, `…` stands for that prefix.

| Method | Path | Result |
|---|---|---|
| POST | `/api/v1/auth/login` | `{"username","password"}` → sets the session cookie |
| POST | `/api/v1/auth/logout` | 204 |
| GET | `/api/v1/auth/me` | current user |
| POST | `/api/v1/auth/password` | `{"current_password","new_password"}` → 204, logs out everywhere |
| GET | `/api/v1/status` | web version, agent reachability, `agent.servers` (id, type, name, PKI and OpenVPN state) |
| GET | `…/sessions` | live sessions (from the agent) |
| GET | `…/stats` | traffic report (below) |
| GET | `…/stats.csv` | same, as CSV: period, name, upload, download, estimated |
| GET | `…/history/sessions` | recorded sessions, newest first (`limit`, default 100) |
| GET, POST | `…/certs` | list / issue certificates (same bodies as the agent API) |
| POST | `…/certs/renew` | bulk renew: `{"names":[…],"validity_days":…}` → result per name |
| GET, DELETE | `…/certs/{name}` | get / delete |
| POST | `…/certs/{name}/revoke`, `/unrevoke`, `/renew` | |
| GET | `…/certs/{name}/ovpn` | download the `.ovpn` profile |
| DELETE | `…/certs/{name}/sessions`, `…/sessions/{client_id}` | disconnect |
| GET | `…/network` | VPN subnet, topology, server routes, static addresses in use |
| GET, PUT | `…/certs/{name}/network` | `{"static_ip","full_tunnel","routes":[cidr],"lan":[cidr]}`, response adds `warnings` |
| GET, PUT | `…/certs/{name}/auth` | 2FA status / change: `{"totp":"enable\|reset\|disable","static_password":"…","remove_static":true}`; a new key's secret and `otpauth://` URI are returned once |
| GET, PUT | `…/server/config` | server.conf as `raw` + `settings`; save with `{"base_version", "raw"\|"settings"}` (422 with `problems`, 409 if changed) |
| POST | `…/server/config/check` | dry run: resulting `raw` + `problems` |
| POST | `…/server/restart` | `{"rollback":true}` → `{"ok","seconds","rolled_back","error"}` |
| GET | `…/server/backups`, `…/server/backups/{name}` | earlier versions |
| GET, POST | `…/setup` | setup status / run the setup (see the agent API) |
| GET, PUT | `…/firewall` | access rules |
| POST | `…/firewall/addresses` | `{"names":[…]}` → fixed VPN addresses for these clients |

Calls are passed to the agent (404 for an unknown server); every change is logged as
`audit server=... action=... target=... by=<user>`.

Report parameters (`…/stats`, `…/stats.csv`): see [the guide](guide.md#traffic-history).

Example:

```
curl -H "Authorization: Bearer $TD_WEB_API_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"alice-phone"}' https://vpn-admin.example.com/api/v1/servers/openvpn/certs
curl -H "Authorization: Bearer $TD_WEB_API_TOKEN" -o alice-phone.ovpn \
  https://vpn-admin.example.com/api/v1/servers/openvpn/certs/alice-phone/ovpn
```

## Agent API (web → agent)

All `/v1/*` requests need the header `X-Trapdoor-Token: <TD_AGENT_TOKEN>`.
Errors are JSON `{"error": "..."}` with 400 / 401 / 404 / 409 / 503. Per-server calls are
under `/v1/servers/{server}/` (`…` below); an unknown server is 404.

| Method | Path | Body | Result |
|---|---|---|---|
| GET | `/healthz` | – | `{"status":"ok"}` (no token) |
| GET | `/v1/info` | – | version, hostname, `servers` (as `…/info`) |
| GET | `…/info` | – | id, type, name, dirs, `pki_ready`, CA, OpenVPN version |
| GET | `…/setup` | – | `needed`, `has_pki`, defaults, container-fixed port/proto |
| GET / PUT | `…/firewall` | access rules `{enabled, default, rules[]}` | rules, generated iptables text, `applied`, warnings |
| POST | `…/firewall/addresses` | `{"names":[...]}` | gives clients fixed VPN addresses |
| POST | `…/setup` | wizard answers (`public_host`, `port`, `proto`, `subnet`, `dns`, `tls`, …) | creates PKI, keys, `server.conf`; 409 when it exists |
| GET | `…/certs` | – | all entries (history included; `current` marks the newest per name) |
| POST | `…/certs` | `{"name","kind"?,"passphrase"?,"validity_days"?}` | 201 cert |
| GET | `…/certs/{name}` | – | current cert |
| POST | `…/certs/{name}/revoke` | – | cert |
| POST | `…/certs/{name}/unrevoke` | – | cert |
| POST | `…/certs/{name}/renew` | `{"validity_days"?}` | new cert (same key; old one revoked as superseded) |
| DELETE | `…/certs/{name}` | – | 204; revokes and deletes key material, stays in CRL |
| GET | `…/certs/{name}/ovpn` | – | `.ovpn` profile (409 unless the cert is valid) |
| POST | `…/crl` | – | 204; regenerates `crl.pem` |
| GET | `…/sessions` | – | connected clients + server traffic counters |
| DELETE | `…/sessions/{client_id}` | – | 204; disconnects one session |
| DELETE | `…/certs/{name}/sessions` | – | `{"killed":n}`; disconnects all sessions of a name |

### WireGuard

| Method | Path | Body | Result |
|---|---|---|---|
| POST | `/v1/servers` | `{"type":"wireguard","id":"wg0","name","port","public_host","public_port","subnet","dns":[…],"mtu","allowed_ips":[…],"keepalive"}` | 201 server info; 409 for a used id, port or overlapping subnet |
| DELETE | `/v1/servers/{id}` | – | 204; takes the interface down and deletes its files (WireGuard only) |
| GET | `…/peers` | – | clients with live state (`connected`, `endpoint`, `last_handshake`, bytes) |
| POST | `…/peers` | `{"name","address"?,"lan"?:[cidr],"no_psk"?}` | 201 client (keys generated, next free address) |
| GET, PUT, DELETE | `…/peers/{name}` | PUT: `{"disabled"?,"lan"?}` | client / updated client / 204 |
| GET | `…/peers/{name}/conf` | – | the client's configuration file (409 while disabled) |
| GET | `…/sessions` | – | connected clients, as for OpenVPN (`DELETE …/sessions/{id}` is 409) |

Revoke and delete also disconnect the client right away (otherwise OpenVPN would only
notice at the next renegotiation, up to an hour later). Sessions need OpenVPN's management
interface: `management 127.0.0.1 2080` in `server.conf` (`TD_OPENVPN_MGMT`, optional
`TD_OPENVPN_MGMT_PASSWORD` for `pw-file` setups); 502 when it is unreachable.

Try it with a throw-away PKI:

```
export TD_AGENT_TOKEN=dev TD_PKI_DIR=/tmp/td/pki TD_CRL_COPIES=-
./bin/trapdoor init-pki
./bin/trapdoor agent &
curl -H "X-Trapdoor-Token: dev" -d '{"name":"alice"}' localhost:9090/v1/servers/openvpn/certs
```
