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
| PUT | `…/certs/{name}/notes` | display name: `{"notes"}` (empty removes it) |
| DELETE | `…/certs/{name}/sessions`, `…/sessions/{client_id}` | disconnect |
| GET | `…/network` | VPN subnet, topology, server routes, static addresses in use |
| GET, PUT | `…/certs/{name}/network` | `{"static_ip","full_tunnel","routes":[cidr],"lan":[cidr]}`: the client's own settings; response adds `warnings` and `named` (`[{"name","as":"route"\|"lan"}]`, the named networks it is part of) |
| GET | `…/network/clients` | every client's network: `[{"name", …client network}]` (own routes, `named`, `warnings`) |
| GET | `…/named-networks` | `{"networks":[{"name","description","networks":[cidr],"via","clients":[name]}],"warnings"}` |
| PUT, DELETE | `…/named-networks/{name}` | save (body: a network; `name` in the body renames it) or remove a named network; rewrites the ccd files of its clients; answers like GET (409 for a used name, 404 for an unknown one) |
| GET, PUT | `…/certs/{name}/auth` | 2FA status / change: `{"totp":"enable\|reset\|disable","static_password":"…","remove_static":true}`; a new key's secret and `otpauth://` URI are returned once |
| GET, PUT | `…/server/config` | server.conf as `raw` + `settings`; save with `{"base_version", "raw"\|"settings"}` (422 with `problems`, 409 if changed) |
| POST | `…/server/config/check` | dry run: resulting `raw` + `problems` |
| POST | `…/server/restart` | `{"rollback":true}` → `{"ok","seconds","rolled_back","error"}` |
| GET | `…/server/backups`, `…/server/backups/{name}` | earlier versions |
| GET, POST | `…/setup` | setup status / run the setup (see the agent API) |
| GET, PUT | `…/firewall` | access rules |
| POST | `…/firewall/addresses` | `{"names":[…]}` → fixed VPN addresses for these clients |

| POST | `/api/v1/servers` | add an OpenVPN, WireGuard or AmneziaWG server (body as the agent's `POST /v1/servers`, plus `"host"`: the machine, empty for this one) |
| DELETE | `/api/v1/servers/{server}` | remove a WireGuard / AmneziaWG server and its interface, or an added OpenVPN server |
| POST | `/api/v1/servers/{server}/backup` | `{"passphrase"}` → one server's backup (its files and traffic history) |
| POST | `/api/v1/servers/restore` | form upload: `file`, `passphrase`, `host` (empty: this machine), `replace` (`1`) → 201 the restored server; 409 when its id or port is taken |
| POST | `/api/v1/backup` | `{"passphrase"}` → a backup file (`.tdbackup` encrypted, or `.tar.gz` without a passphrase); see the guide |
| GET, PUT | `/api/v1/email` | email settings: `{"enabled","host","port","security":"starttls\|tls\|none","username","password","from","insecure_skip_verify","admins":[…],"notify":{"backup_failed","node_down","cert_expiry"}}`; GET never returns the password (`password_set`) and adds `last` (the last email's outcome); PUT with an empty password keeps it, `clear_password` removes it |
| POST | `/api/v1/email/test` | `{"to"}` (optional, default: the admins) → a test email; 502 with the SMTP error |
| GET | `/api/v1/servers/{server}/emails` | the clients' addresses: `{"<client>": "<email>"}` |
| PUT | `/api/v1/servers/{server}/emails/{name}` | `{"email"}` (empty: forget it) |
| POST | `/api/v1/servers/{server}/emails/{name}/send` | `{"email","note","qr_gif"}` (all optional; email: another address, stored; qr_gif: base64 GIF attached) → emails the client's `.ovpn` / `.conf` with instructions |
| GET | `/api/v1/backups` | scheduled backups: `{"schedule","last","next","dir","encrypted","files":[{"name","size","time"}],"running"}`; 501 with `TD_BACKUP_DIR=-` |
| PUT | `/api/v1/backups/schedule` | `{"every":"off\|daily\|weekly","weekday":0-6,"hour":0-23,"keep":1-365,"servers":true}` |
| POST | `/api/v1/backups/run` | back up now (answer: the run's `{"time","ok","error","files"}`); 409 while one runs |
| GET, DELETE | `/api/v1/backups/files/{name}` | download / delete a file of the backup folder (only `trapdoor-all-…` / `trapdoor-server-…` names) |
| GET | `/api/v1/servers` | every server (with `container`, `host`) with `clients`, `connected`, `today_in`, `today_out` (bytes since midnight in `TD_TZ`), `stopped`, `autostart` |
| POST | `/api/v1/servers/{server}/start`, `…/stop` | start / stop a server (answer: its info, `note` when there is something to do, e.g. start OpenVPN with systemd) |
| PUT | `/api/v1/servers/{server}/autostart` | `{"autostart": true\|false}` |
| GET | `/api/v1/nodes` | the other nodes: `[{"name","machine","url","version","hostname","containers","error"}]` (asks each one) |
| POST | `/api/v1/nodes` | add a node by hand: `{"name":"vps","url":"https://203.0.113.5:9443","token","fingerprint":"sha256:…"}`: connects once, then saves (same name: replaced); 400 when the agent does not answer, the token or the certificate is wrong |
| DELETE | `/api/v1/nodes/{name}` | forget a node (its servers keep running there) |
| POST | `/api/v1/nodes/tokens` | `{"name":"vps","hours":24}` (both optional; name empty: any) → 201 `{"token":"tdj_…","name","expires_at"}`, a one-time join token (shown once; at most 30 days) |
| POST | `/api/v1/nodes/join` | **no login**: a node joins, `{"token","name","url","fingerprint","agent_token"}`; allowed with an unused join token (for that name), `TD_JOIN_SECRET`, or the fingerprint the node already has; the web UI connects back before it answers 201 (like `POST /api/v1/nodes`); 401 for an invalid, used or expired token |

`/api/v1/hosts` (GET, POST, DELETE) is the same as `/api/v1/nodes`, kept for scripts.

Servers on another node have the id `<node>.<id>` (e.g. `vps.wg0`) in every
`/api/v1/servers/{server}/…` path and in `agent.servers` of `/api/v1/status`, with `"host"`
set; `agent.hosts` lists the other nodes and whether they answer, `agent.machine` and
`agent.node` name the own node (`TD_MACHINE`, `TD_NODE_NAME`).
| GET, POST | `…/peers` | WireGuard clients: list / add (body as the agent's) |
| POST | `…/peers/bulk` | add several clients |
| GET, PUT | `…/settings` | WireGuard server settings and client defaults |
| GET, PUT, DELETE | `…/peers/{name}` | get / `{"disabled"?,"lan"?,"address"?,"psk"?,"settings"?:{notes,dns,allowed_ips,mtu,keepalive,expires_at}}` / delete |
| GET, PUT | `…/scripts` | presets and own scripts: `{"dns_redirect","block_dot","isolate","up","down"}`; GET adds `editable` (`TD_SCRIPTS=on`), `supported`, `iface`, `files`, `state` (`ok`, `error` + `message`, `pending`, `inactive`); PUT 403 when `up` / `down` change on a node without `TD_SCRIPTS=on` |
| GET, PUT | `…/cert-defaults` | OpenVPN: `{"validity_days"}` for new and renewed client certificates when a request has none (0 = 825 days) |
| GET | `…/peers/{name}/conf` | download the client's WireGuard configuration |

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
| POST | `/v1/servers/{id}/start`, `/v1/servers/{id}/stop` | – | server info with `stopped`; restart of a stopped OpenVPN server: 409 |
| PUT | `/v1/servers/{id}/autostart` | `{"autostart"}` | server info |
| GET | `/v1/servers/{id}/backup` | – | tar of one server: `server.json`, then `state.json` (WireGuard) or `files/…` (OpenVPN; `pki/`, `ccd/` when elsewhere) |
| POST | `/v1/servers/restore[?replace=1]` | that tar | 201 server info; 409 for a used id or port |
| GET | `/v1/backup` | – | tar of the agent's directories (`openvpn/`, `wireguard/`, `amneziawg/`, and `pki/`, `ccd/`, `openvpn-servers/` when they are elsewhere) |
| GET | `…/info` | – | id, type, name, dirs, `pki_ready`, CA, OpenVPN version |
| GET | `…/setup` | – | `needed`, `has_pki`, defaults, container-fixed port/proto |
| GET / PUT | `…/firewall` | access rules `{enabled, default, rules[]}` | rules, generated iptables text, `applied`, warnings, `client_to_client` (OpenVPN passes traffic between clients itself) |
| POST | `…/firewall/addresses` | `{"names":[...]}` | gives clients fixed VPN addresses |
| POST | `…/setup` | wizard answers (`public_host`, `port`, `proto`, `subnet`, `dns`, `tls`, …) | creates PKI, keys, `server.conf`; 409 when it exists |
| GET | `…/certs` | – | all entries (history included; `current` marks the newest per name) |
| POST | `…/certs` | `{"name","kind"?,"passphrase"?,"validity_days"?,"notes"?}` | 201 cert (`notes`: display name) |
| GET | `…/certs/{name}` | – | current cert |
| POST | `…/certs/{name}/revoke` | – | cert |
| POST | `…/certs/{name}/unrevoke` | – | cert |
| POST | `…/certs/{name}/renew` | `{"validity_days"?}` | new cert (same key; old one revoked as superseded) |
| DELETE | `…/certs/{name}` | – | 204; revokes and deletes key material, stays in CRL |
| GET | `…/certs/{name}/ovpn` | – | `.ovpn` profile (409 unless the cert is valid) |
| PUT | `…/certs/{name}/notes` | `{"notes"}` | cert; sets the display name (`#TD_NOTE:` in the ccd file, empty removes it) |
| POST | `…/crl` | – | 204; regenerates `crl.pem` |
| GET | `…/sessions` | – | connected clients + server traffic counters |
| DELETE | `…/sessions/{client_id}` | – | 204; disconnects one session |
| DELETE | `…/certs/{name}/sessions` | – | `{"killed":n}`; disconnects all sessions of a name |

### WireGuard

| Method | Path | Body | Result |
|---|---|---|---|
| POST | `/v1/servers` | `{"type":"wireguard"\|"amneziawg","id":"wg0","name","port","public_host","public_port","subnet","dns":[…],"mtu","allowed_ips":[…],"keepalive"}`; AmneziaWG also `"awg_level":"2.0"\|"1.5"\|"3.1"` and optional `"obfuscation":{jc,jmin,jmax,s1…s4,h1…h4,i1…i5}` plus at 3.1 `header_protection_key,random_trailers,content_padding,disable_cookies,rekey_after_time,reject_after_time,rekey_timeout,keepalive_timeout` (only `i1`–`i5`: the rest is generated) | 201 server info (with `obfuscation`); 409 for a used id, port or overlapping subnet |
| POST | `/v1/servers` | `{"type":"openvpn","id":"ovpn-tcp","name","port":443,"proto":"udp"\|"tcp"}`: an OpenVPN server in `TD_OPENVPN_SERVERS_DIR/<id>/`, then set up with `…/setup` | 201 server info; 409 for a used id or port |
| POST | `/v1/servers/{id}/peers/bulk` | `{"prefix","count","psk",` + client settings`}` | 201 the new clients (`<prefix>-N`) |
| GET, PUT | `/v1/servers/{id}/settings` | `{"name","public_host","public_port","dns":[…],"allowed_ips":[…],"mtu","keepalive","psk"}` | a WireGuard server's settings and client defaults |
| DELETE | `/v1/servers/{id}` | – | 204; WireGuard: takes the interface down and deletes its files; added OpenVPN server: stops it and moves its directory to `.removed/` (the first server: 409) |
| GET | `…/peers` | – | clients with live state (`connected`, `endpoint`, `last_handshake`, bytes) |
| POST | `…/peers` | `{"name","address"?,"lan"?:[cidr],"public_key"?,"psk"?,"notes"?,"dns"?:[…],"allowed_ips"?:[…],"mtu"?,"keepalive"?,"expires_at"?}` (`expires_at`: RFC 3339; after it the client is off the interface and `"expired": true`) | 201 client (keys generated unless `public_key`, next free address; empty settings = the server's defaults) |
| GET, PUT, DELETE | `…/peers/{name}` | PUT: `{"disabled"?,"lan"?,"address"?,"psk"?,"settings"?}` (settings replace the client's own, empty = server defaults) | client / updated client / 204 |
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
