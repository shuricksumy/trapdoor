# tunnelward

Web management for an OpenVPN server — certificates, clients, 2FA, routing and live sessions.

> **Status:** early development. Not usable yet. See [ROADMAP.md](ROADMAP.md).

tunnelward is the successor to [openvpn-ui](https://github.com/shuricksumy/openvpn-ui),
rebuilt as a client–server application with a native Go PKI.

## Architecture

```
[ Browser ] → [ tunnelward web ] ──HTTP API──▶ [ tunnelward agent ] → openvpn / PKI / firewall
               users, DB, UI                     privileged, runs next to OpenVPN
```

- **agent** — the only privileged part. Owns the PKI (easyrsa-compatible layout on disk,
  implemented in Go — no easyrsa or shell scripts at runtime), OpenVPN config, process
  control, management interface and client auth.
- **web** — unprivileged. Admin users, database, REST API for the frontend, serves the UI.

Both run from the same binary:

```
tunnelward agent   # privileged side
tunnelward web     # web UI + API
tunnelward version
```

## Web UI

The UI (Svelte 5 + Vite, in `web/`) is embedded in the binary and served by `tunnelward web`:
overview with today's connection timeline, clients (certificates), live sessions, and traffic
statistics with day / month / year views, presets, per-client shares and CSV export.
Light, dark or automatic theme; works on phones. Strict Content-Security-Policy, no CDN:
fonts (IBM Plex) are bundled.

## Docker

One image (`ghcr.io/shuricksumy/tunnelward`, amd64 + arm64) runs three services
from [docker-compose.yml](docker-compose.yml):

| Service | Runs as | Privileges | Purpose |
|---|---|---|---|
| `openvpn` | root | `NET_ADMIN`, `/dev/net/tun` | the OpenVPN server (+ optional NAT) |
| `agent` | root | none; shares `openvpn`'s network | PKI, profiles, sessions via management on `127.0.0.1` |
| `web` | uid 10001 | none, read-only filesystem | UI + API, database in a volume |

Web and agent talk over a unix socket on a shared volume, so the agent has no network port,
and OpenVPN's management interface is never published.

### Existing OpenVPN directory (e.g. from openvpn-ui)

```
cp .env.example .env                 # set TW_AGENT_TOKEN, TW_TZ, OPENVPN_DIR
docker compose up -d
docker compose exec -it web tunnelward user add admin
```

Expected layout in `OPENVPN_DIR` (the openvpn-ui defaults): `server.conf`,
`client-template.txt`, `easy-rsa/pki/`, `crl.pem` (kept up to date by the agent).
`server.conf` needs `management 127.0.0.1 2080` (openvpn-ui's `0.0.0.0 2080` also works
inside the shared network namespace, but nothing else can reach it there).

### New install

```
cp .env.example .env                 # set TW_AGENT_TOKEN, TW_TZ, VPN_PORT, VPN_PROTO
docker compose up -d                 # openvpn waits until server.conf exists
docker compose exec -it web tunnelward user add admin
```

Log in: the setup opens by itself. It asks for the public address, protocol and port,
the VPN subnet, full/split tunnel, DNS and two-factor login, then creates the certificate
authority (EC P-256 by default), the `server` certificate, the tls-crypt-v2 key,
`server.conf` and `client-template.txt`, and waits for OpenVPN to start. Nothing is
overwritten: an existing PKI is reused, and the setup refuses when `server.conf` exists.

In Docker OpenVPN listens on 1194 inside the container; `VPN_PORT` / `VPN_PROTO` set what
is published and what client profiles use. `VPN_NAT_SUBNET=auto` (default) masquerades the
`server` subnet of `server.conf` and follows it when it changes.

### HTTPS

The UI listens on `127.0.0.1:8080`. Put a reverse proxy with TLS in front (Traefik, Caddy,
nginx) and set `TW_TRUSTED_PROXIES` to its address. `TW_COOKIE_SECURE=false` is only for a
first local test over plain HTTP.

## Development

Requires Go 1.24+ and, for the UI, Node 22.

```
make ui      # web/ → internal/webui/dist (embedded by the next build)
make build   # → bin/tunnelward (without `make ui` the UI shows a "not built" notice)
make test
make lint    # needs golangci-lint
```

UI development with hot reload: run `tunnelward web` on :8080, then `cd web && npm run dev`
(Vite proxies `/api` to it).

Run locally (two terminals):

```
TW_AGENT_TOKEN=dev ./bin/tunnelward agent
TW_AGENT_TOKEN=dev ./bin/tunnelward web
curl localhost:8080/api/v1/status
```

## Agent API

All `/v1/*` requests need the header `X-Tunnelward-Token: <TW_AGENT_TOKEN>`.
Errors are JSON `{"error": "..."}` with 400 / 401 / 404 / 409 / 503.

| Method | Path | Body | Result |
|---|---|---|---|
| GET | `/healthz` | – | `{"status":"ok"}` (no token) |
| GET | `/v1/info` | – | version, dirs, `pki_ready` |
| GET | `/v1/setup` | – | `needed`, `has_pki`, defaults, container-fixed port/proto |
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
interface: `management 127.0.0.1 2080` in `server.conf` (`TW_OPENVPN_MGMT`, optional
`TW_OPENVPN_MGMT_PASSWORD` for `pw-file` setups); 502 when it is unreachable.

Try it with a throw-away PKI:

```
export TW_AGENT_TOKEN=dev TW_PKI_DIR=/tmp/tw/pki TW_CRL_COPIES=-
./bin/tunnelward init-pki
./bin/tunnelward agent &
curl -H "X-Tunnelward-Token: dev" -d '{"name":"alice"}' localhost:9090/v1/certs
```

### Client profiles

`.ovpn` files are built like openvpn-ui did, so existing servers produce the same files:
`<TW_OPENVPN_DIR>/client-template.txt` + inline `<ca>`, `<cert>`, `<key>` + the
control-channel key matching `server.conf` (`tls-crypt`, `tls-auth` with `key-direction 1`,
or a per-client `tls-crypt-v2` key). tls-crypt-v2 client keys live in `keys-v2/<name>.key`;
existing ones are reused, new ones are created with `openvpn --genkey` (`TW_OPENVPN_BIN`).

## Client two-factor login

Per client, in the UI (Clients → More → Two-factor login) or `PUT /api/v1/certs/{name}/auth`:
an authenticator code (TOTP, HMAC-SHA256, 6 digits, 30 s) and/or a static password. The
device logs in with username = its certificate name and password = static password followed
by the code. OpenVPN checks it with tunnelward:

```
# server.conf
script-security 2
auth-user-pass-verify "/usr/local/bin/tunnelward auth-verify" via-file
# client-template.txt
auth-user-pass
```

The settings live in `ccd/<name>` exactly as openvpn-ui wrote them (`#2FA_KEY:` hex secret,
`#2FA_USER:`, `#STATIC_PASS:`), so existing enrolments keep working without `oathtool` or
`auth_client.sh`. Differences from openvpn-ui's script:

- the username must match the certificate: one client's password and code cannot be used with
  another client's certificate (the old script allowed that)
- new static passwords are stored hashed (`#STATIC_PASS_HASH:`); old plain ones still work
- codes from the previous and next 30-second step are accepted (clock drift)
- other lines in `ccd/<name>` (routes, `ifconfig-push`) are never touched; deleting the client
  deletes its `ccd` file

Use an authenticator app that supports SHA-256 codes (some apps only do SHA-1).

## Server configuration

**Server** page (or `/api/v1/server/config`): a form for the common options (port, protocol,
VPN subnet, topology, DNS and search domain for clients, full tunnel, client-to-client,
duplicate-cn, max clients, server routes, routes pushed to everyone) and a raw editor for
the whole `server.conf`. Every other line and comment in the file is kept.

- Before saving, the config is checked: OpenVPN itself parses it, plus tunnelward's checks
  (values OpenVPN only checks at startup, referenced key/cert files and directories exist,
  missing `management` / `crl-verify` / `client-config-dir` are pointed out)
- A preview shows exactly which lines change; saving keeps a backup (last 10, in
  `tunnelward-backups/`) and is refused if the file changed in the meantime
- "Networks behind clients" offers the LANs set on clients that still lack a `route` line
- **Apply and restart** restarts OpenVPN through the management interface and waits for it;
  if it does not come back within 30 s, the config it ran with before is restored and it is
  started again
- Restart method `TW_OPENVPN_RESTART`: `SIGHUP` (default; OpenVPN re-reads its config) or
  `SIGTERM` when a supervisor starts it again — the Docker image's `openvpn` service does
  that inside the same container, so the agent keeps its shared network

## Expiring certificates

- Client certificates expiring within `TW_EXPIRY_WARN_DAYS` (default 30) are flagged in the UI
  and logged by the web process at start and once a day; the CA is flagged 90 days ahead
  (when it expires, every client stops working at once).
- Clients → select several (or "Select them" on the banner) → **Renew selected**, or
  `POST /api/v1/certs/renew {"names":[…],"validity_days":…}` with a result per name.
- Renewing keeps each key; a client's old certificate is revoked, so every renewed device
  needs its new profile.
- Renewing the **server** certificate keeps the old one valid (moved to `renewed/issued/`, as
  easyrsa does): OpenVPN uses it until it is restarted with the new one.

## Client network settings

Per client, in the UI (Clients → More → Network) or `PUT /api/v1/certs/{name}/network`:

| Setting | Written to `ccd/<name>` |
|---|---|
| static VPN address | `ifconfig-push 10.8.0.50 255.255.255.0` (net30: the /30 peer address) |
| networks the client can reach | `push "route 192.168.1.0 255.255.255.0"` |
| networks behind the client (site-to-site) | `iroute 192.168.50.0 255.255.255.0` |
| all traffic through the VPN | `push "redirect-gateway def1"` |

Addresses are checked against `server` / `topology` in `server.conf` and against the other
clients' static addresses. A LAN behind a client also needs `route <net> <mask>` in
`server.conf`: tunnelward warns with the exact line to add. `server.conf` needs
`client-config-dir ccd`. Changes apply when the client reconnects (the UI offers to reconnect
it). The 2FA lines and any other lines in the file are kept; openvpn-ui's files are read as they are.

## Web API

The web process records session history (polling the agent every `TW_POLL_INTERVAL`,
default 30s) into SQLite (`TW_DB_PATH`) and serves reports.

### Login

Create the first admin, then log in from the browser (or curl):

```
TW_DB_PATH=./data/tunnelward.db tunnelward user add admin   # password from stdin or TW_ADMIN_PASSWORD
tunnelward user list | passwd <name> | del <name>
```

- Passwords: PBKDF2-SHA256, 600 000 iterations, at least 10 characters
- Session cookie `tw_session`: random 256-bit token, stored only as a SHA-256 hash; `HttpOnly`,
  `SameSite=Strict`, `Secure` (set `TW_COOKIE_SECURE=false` only for plain-HTTP testing)
- Logged out after `TW_SESSION_IDLE` (default 12h) of inactivity, and after 7 days at most;
  changing a password logs out all sessions of that user
- Failed logins throttled: 10 per IP and 30 per username per 15 minutes (429 + `Retry-After`);
  unknown usernames take as long as wrong passwords
- CSRF: state-changing requests from another site are refused (Fetch Metadata / Origin)
- Behind a reverse proxy set `TW_TRUSTED_PROXIES` (CIDRs) so the real client IP is used
- Scripts can use `Authorization: Bearer <TW_WEB_API_TOKEN>` instead (optional; off when unset)

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

Report parameters: `preset=today|this_month|last_month|last_30_days|this_year|last_year`
**or** `from=YYYY-MM-DD&to=YYYY-MM-DD` (inclusive; default this month), `group=day|month|year`,
`name=<user>` (omit for the whole server). Days/months/years follow `TW_TZ` (IANA name).
The report has totals, a gap-free series per period, and per-user totals with share,
session count and online time. Upload/download are from the user's point of view.

Traffic is stored per user per hour. If polls were missed (web down, agent unreachable) the
traffic of the gap is spread over its hours and flagged `estimated` instead of one spike.

## Tests

```
make test                # unit tests
sudo test/e2e/run.sh     # real OpenVPN server + client on localhost (needs openvpn)
```

CI also runs PKI compatibility tests against real easyrsa + openssl.

## Configuration

All settings come from environment variables (prefix `TW_`); see
[`internal/config/config.go`](internal/config/config.go) for the full list and defaults.

## License

MIT
