# How tunnelward works

Details behind the pages of the web UI. For installing, see the [README](../README.md);
for the HTTP API, [api.md](api.md).

- [Setup](#setup)
- [Client profiles](#client-profiles)
- [Client two-factor login](#client-two-factor-login)
- [Server configuration](#server-configuration)
- [Expiring certificates](#expiring-certificates)
- [Client network settings](#client-network-settings)
- [Access rules](#access-rules)
- [Traffic history](#traffic-history)
- [Admin login and security](#admin-login-and-security)

## Setup

<img src="screenshots/setup.png" width="640">

On a server without `server.conf` the web UI opens the setup after login. It asks for the
public address, protocol and port, the VPN subnet, full or split tunnel, DNS and two-factor
login, then creates the certificate authority (EC P-256 by default; P-384 and RSA are in
"Certificates and encryption"), the `server` certificate, the control channel key
(tls-crypt-v2 by default), `server.conf` and `client-template.txt`, and waits for OpenVPN to
start. Nothing is overwritten: an existing PKI is reused, and the setup refuses when
`server.conf` exists.

In Docker OpenVPN listens on 1194 inside the container; `VPN_PORT` / `VPN_PROTO` in `.env`
set what is published and what client profiles use. `VPN_NAT_SUBNET=auto` (default)
masquerades the `server` subnet of `server.conf` and follows it when it changes.

The generated `server.conf` drops OpenVPN's privileges (`user nobody`), checks the CRL at
every connect, allows TLS 1.2+ with AES-GCM / ChaCha20 data ciphers, and keeps the
management interface on 127.0.0.1.

## Client profiles

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

## Access rules

The Access page decides which networks VPN clients can reach: rules from a client (or all
clients) to a network or address, optionally limited to TCP/UDP ports or ping, checked from
the top; the first match decides, then the default (allow or block). Replies to allowed
traffic always pass.

The agent renders them into the iptables chain `TW-FWD`, jumped to from `INPUT` and
`FORWARD` for traffic from `tun` devices, so they cover the server itself and everything
behind it. Rules match a client by its fixed VPN address; the page assigns free addresses
(from the top of the subnet) to clients that have none. With `client-to-client` OpenVPN
forwards traffic between clients itself, so the rules cannot see it.

| `TW_FIREWALL` | |
|---|---|
| `direct` (default) | the agent runs `iptables-restore` itself (host install as root) |
| `file` | the agent writes `tunnelward-firewall.rules`; the OpenVPN container (the one with `NET_ADMIN`) applies it and reports back — the compose default |
| `off` | the agent never touches the firewall |

Nothing is changed until rules are saved for the first time. In direct mode the agent
restores them when it starts.

## Traffic history

The web process asks the agent for the connected clients every `TW_POLL_INTERVAL` (default
30 s) and stores sessions and traffic per client per hour in its SQLite database. If polls
were missed (web down, agent unreachable) the traffic of the gap is spread over its hours and
marked *estimated* instead of showing up as one spike.

The Traffic page shows any date range by day, month or year, for the whole server or one
client, with each client's share, session count and online time, and exports CSV. Days,
months and years follow `TW_TZ`. Upload and download are from the client's point of view.

API report parameters: `preset=today|this_month|last_month|last_30_days|this_year|last_year`
**or** `from=YYYY-MM-DD&to=YYYY-MM-DD` (inclusive; default this month), `group=day|month|year`,
`name=<user>` (omit for the whole server). Days/months/years follow `TW_TZ` (IANA name).
The report has totals, a gap-free series per period, and per-user totals with share,
session count and online time. Upload/download are from the user's point of view.

Traffic is stored per user per hour. If polls were missed (web down, agent unreachable) the
traffic of the gap is spread over its hours and flagged `estimated` instead of one spike.

## Admin login and security

Create admins with the CLI (`tunnelward user add <name>`, also `passwd`, `del`, `list`); the
password comes from stdin or `TW_ADMIN_PASSWORD`.

- Passwords: PBKDF2-SHA256, 600 000 iterations, at least 10 characters
- Session cookie `tw_session`: random 256-bit token, stored only as a SHA-256 hash; `HttpOnly`,
  `SameSite=Strict`, `Secure` (set `TW_COOKIE_SECURE=false` only for plain-HTTP testing)
- Logged out after `TW_SESSION_IDLE` (default 12h) of inactivity, and after 7 days at most;
  changing a password logs out all sessions of that user
- Failed logins throttled: 10 per IP and 30 per username per 15 minutes; unknown usernames take
  as long as wrong passwords
- Requests that change something are refused when they come from another site (CSRF)
- Behind a reverse proxy set `TW_TRUSTED_PROXIES` so the real client IP is used
- Every change is logged as `audit action=... target=... by=<user>`
- Strict Content-Security-Policy, no CDN: the UI and its fonts are served by tunnelward itself

The pieces are separated so that the part facing the browser has no privileges:

```
[ Browser ] → [ web ] ──unix socket / HTTP──▶ [ agent ] → OpenVPN, PKI, firewall
              users, history, UI              privileged, runs next to OpenVPN
```

| Docker service | Runs as | Privileges | Purpose |
|---|---|---|---|
| `openvpn` | root | `NET_ADMIN`, `/dev/net/tun` | the OpenVPN server, NAT, access rules |
| `agent` | root | none; shares `openvpn`'s network | PKI, profiles, config, sessions via the management interface on 127.0.0.1 |
| `web` | uid 10001 | none, read-only filesystem | UI + API, database in a volume |

Web and agent talk over a unix socket on a shared volume, so the agent has no network port,
and OpenVPN's management interface is never published.
