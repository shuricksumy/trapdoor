# HTTP API

Everything the web UI does goes through the web API, so it can be scripted too.

## Web API

Log in like the browser does (`POST /api/v1/auth/login` sets the session cookie), or set
`TD_WEB_API_TOKEN` and send `Authorization: Bearer <token>`. Errors are JSON
`{"error": "..."}`.

| Method | Path | Result |
|---|---|---|
| POST | `/api/v1/auth/login` | `{"username","password"}` → sets the session cookie |
| POST | `/api/v1/auth/logout` | 204 |
| GET | `/api/v1/auth/me` | current user |
| POST | `/api/v1/auth/password` | `{"current_password","new_password"}` → 204, logs out everywhere |
| GET | `/api/v1/status` | web version + agent reachability |
| GET | `/api/v1/sessions` | live sessions (from the agent) |
| GET | `/api/v1/stats` | traffic report (below) |
| GET | `/api/v1/stats.csv` | same, as CSV: period, name, upload, download, estimated |
| GET | `/api/v1/history/sessions` | recorded sessions, newest first (`limit`, default 100) |
| GET, POST | `/api/v1/certs` | list / issue certificates (same bodies as the agent API) |
| GET, PUT | `/api/v1/server/config` | server.conf as `raw` + `settings`; save with `{"base_version", "raw"\|"settings"}` (422 with `problems`, 409 if changed) |
| POST | `/api/v1/server/config/check` | dry run: resulting `raw` + `problems` |
| POST | `/api/v1/server/restart` | `{"rollback":true}` → `{"ok","seconds","rolled_back","error"}` |
| GET | `/api/v1/server/backups`, `/api/v1/server/backups/{name}` | earlier versions |
| POST | `/api/v1/certs/renew` | bulk renew: `{"names":[…],"validity_days":…}` → result per name |
| GET, DELETE | `/api/v1/certs/{name}` | get / delete |
| POST | `/api/v1/certs/{name}/revoke`, `/unrevoke`, `/renew` | |
| GET | `/api/v1/certs/{name}/ovpn` | download the `.ovpn` profile |
| DELETE | `/api/v1/certs/{name}/sessions`, `/api/v1/sessions/{client_id}` | disconnect |
| GET | `/api/v1/network` | VPN subnet, topology, server routes, static addresses in use |
| GET, PUT | `/api/v1/certs/{name}/network` | `{"static_ip","full_tunnel","routes":[cidr],"lan":[cidr]}`, response adds `warnings` |
| GET, PUT | `/api/v1/certs/{name}/auth` | 2FA status / change: `{"totp":"enable\|reset\|disable","static_password":"…","remove_static":true}`; a new key's secret and `otpauth://` URI are returned once |

Certificate and session calls are passed to the agent; every change is logged as
`audit action=... target=... by=<user>`.
| GET | `/api/v1/setup`, POST `/api/v1/setup` | setup status / run the setup (see the agent API) |
| GET, PUT | `/api/v1/firewall` | access rules |
| POST | `/api/v1/firewall/addresses` | `{"names":[…]}` → fixed VPN addresses for these clients |

Report parameters (`/api/v1/stats`, `/stats.csv`): see [the guide](guide.md#traffic-history).

Example:

```
curl -H "Authorization: Bearer $TD_WEB_API_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"alice-phone"}' https://vpn-admin.example.com/api/v1/certs
curl -H "Authorization: Bearer $TD_WEB_API_TOKEN" -o alice-phone.ovpn \
  https://vpn-admin.example.com/api/v1/certs/alice-phone/ovpn
```

## Agent API (web → agent)

All `/v1/*` requests need the header `X-Trapdoor-Token: <TD_AGENT_TOKEN>`.
Errors are JSON `{"error": "..."}` with 400 / 401 / 404 / 409 / 503.

| Method | Path | Body | Result |
|---|---|---|---|
| GET | `/healthz` | – | `{"status":"ok"}` (no token) |
| GET | `/v1/info` | – | version, dirs, `pki_ready` |
| GET | `/v1/setup` | – | `needed`, `has_pki`, defaults, container-fixed port/proto |
| GET / PUT | `/v1/firewall` | access rules `{enabled, default, rules[]}` | rules, generated iptables text, `applied`, warnings |
| POST | `/v1/firewall/addresses` | `{"names":[...]}` | gives clients fixed VPN addresses |
| POST | `/v1/setup` | wizard answers (`public_host`, `port`, `proto`, `subnet`, `dns`, `tls`, …) | creates PKI, keys, `server.conf`; 409 when it exists |
| GET | `/v1/certs` | – | all entries (history included; `current` marks the newest per name) |
| POST | `/v1/certs` | `{"name","kind"?,"passphrase"?,"validity_days"?}` | 201 cert |
| GET | `/v1/certs/{name}` | – | current cert |
| POST | `/v1/certs/{name}/revoke` | – | cert |
| POST | `/v1/certs/{name}/unrevoke` | – | cert |
| POST | `/v1/certs/{name}/renew` | `{"validity_days"?}` | new cert (same key; old one revoked as superseded) |
| DELETE | `/v1/certs/{name}` | – | 204; revokes and deletes key material, stays in CRL |
| GET | `/v1/certs/{name}/ovpn` | – | `.ovpn` profile (409 unless the cert is valid) |
| POST | `/v1/crl` | – | 204; regenerates `crl.pem` |
| GET | `/v1/sessions` | – | connected clients + server traffic counters |
| DELETE | `/v1/sessions/{client_id}` | – | 204; disconnects one session |
| DELETE | `/v1/certs/{name}/sessions` | – | `{"killed":n}`; disconnects all sessions of a name |

Revoke and delete also disconnect the client right away (otherwise OpenVPN would only
notice at the next renegotiation, up to an hour later). Sessions need OpenVPN's management
interface: `management 127.0.0.1 2080` in `server.conf` (`TD_OPENVPN_MGMT`, optional
`TD_OPENVPN_MGMT_PASSWORD` for `pw-file` setups); 502 when it is unreachable.

Try it with a throw-away PKI:

```
export TD_AGENT_TOKEN=dev TD_PKI_DIR=/tmp/tw/pki TD_CRL_COPIES=-
./bin/trapdoor init-pki
./bin/trapdoor agent &
curl -H "X-Trapdoor-Token: dev" -d '{"name":"alice"}' localhost:9090/v1/certs
```
