# How trapdoor works

Details behind the pages of the web UI. For installing, see the [README](../README.md);
for the HTTP API, [api.md](api.md).

- [Setup](#setup)
- [Client profiles](#client-profiles)
- [Client two-factor login](#client-two-factor-login)
- [Server configuration](#server-configuration)
- [OpenVPN 2.7](#openvpn-27)
- [Expiring certificates](#expiring-certificates)
- [Client network settings](#client-network-settings)
- [Access rules](#access-rules)
- [Traffic history](#traffic-history)
- [WireGuard servers](#wireguard-servers)
- [AmneziaWG servers](#amneziawg-servers)
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
`<TD_OPENVPN_DIR>/client-template.txt` + inline `<ca>`, `<cert>`, `<key>` + the
control-channel key matching `server.conf` (`tls-crypt`, `tls-auth` with `key-direction 1`,
or a per-client `tls-crypt-v2` key). tls-crypt-v2 client keys live in `keys-v2/<name>.key`;
existing ones are reused, new ones are created with `openvpn --genkey` (`TD_OPENVPN_BIN`).

## Client two-factor login

Per client, in the UI (Clients → More → Two-factor login) or `PUT /api/v1/servers/openvpn/certs/{name}/auth`:
an authenticator code (TOTP, HMAC-SHA256, 6 digits, 30 s) and/or a static password. The
device logs in with username = its certificate name and password = static password followed
by the code. OpenVPN checks it with trapdoor:

```
# server.conf
script-security 2
auth-user-pass-verify "/usr/local/bin/trapdoor auth-verify" via-file
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

**Server** page (or `/api/v1/servers/openvpn/server/config`): a form for the common options (port, protocol,
VPN subnet, topology, DNS and search domain for clients, full tunnel, client-to-client,
duplicate-cn, max clients, server routes, routes pushed to everyone) and a raw editor for
the whole `server.conf`. Every other line and comment in the file is kept.

- Before saving, the config is checked: OpenVPN itself parses it, plus trapdoor's checks
  (values OpenVPN only checks at startup, referenced key/cert files and directories exist,
  missing `management` / `crl-verify` / `client-config-dir` are pointed out)
- A preview shows exactly which lines change; saving keeps a backup (last 10, in
  `trapdoor-backups/`) and is refused if the file changed in the meantime
- "Networks behind clients" offers the LANs set on clients that still lack a `route` line
- **Apply and restart** restarts OpenVPN through the management interface and waits for it;
  if it does not come back within 30 s, the config it ran with before is restored and it is
  started again
- Restart method `TD_OPENVPN_RESTART`: `SIGHUP` (default; OpenVPN re-reads its config) or
  `SIGTERM` when a supervisor starts it again — the Docker image's `openvpn` service does
  that inside the same container, so the agent keeps its shared network

## OpenVPN 2.7

The Docker image runs OpenVPN 2.7 (from Alpine). Configs written for 2.6 keep working,
with one change to look at before updating an existing server:

- **Topology:** a `server.conf` without a `topology` line used `net30` up to 2.6 and uses
  `subnet` from 2.7 on. Clients then get addresses from a different scheme, and fixed
  addresses in `ccd/` written for `net30` stop matching. The Server page warns about a
  missing `topology` line: before updating, add `topology net30` to keep the old
  behaviour (or move to `topology subnet` on purpose). Configs made by trapdoor's setup
  always have `topology subnet`. trapdoor follows the installed OpenVPN for the default.
- **Compression:** OpenVPN 2.7 no longer compresses what it sends; `comp-lzo` / `compress`
  only keep receiving compressed packets working, with a warning. Old clients still
  connect.
- `persist-key` is always on now (the option prints a notice); `ncp-disable` was already
  removed in 2.6.

Updating the image on your server: `docker compose pull && docker compose up -d`. The
Overview shows the running OpenVPN version.

## Expiring certificates

- Client certificates expiring within `TD_EXPIRY_WARN_DAYS` (default 30) are flagged in the UI
  and logged by the web process at start and once a day; the CA is flagged 90 days ahead
  (when it expires, every client stops working at once).
- Clients → select several (or "Select them" on the banner) → **Renew selected**, or
  `POST /api/v1/servers/openvpn/certs/renew {"names":[…],"validity_days":…}` with a result per name.
- Renewing keeps each key; a client's old certificate is revoked, so every renewed device
  needs its new profile.
- Renewing the **server** certificate keeps the old one valid (moved to `renewed/issued/`, as
  easyrsa does): OpenVPN uses it until it is restarted with the new one.

## Client network settings

Per client, in the UI (Clients → More → Network) or `PUT /api/v1/servers/openvpn/certs/{name}/network`:

| Setting | Written to `ccd/<name>` |
|---|---|
| static VPN address | `ifconfig-push 10.8.0.50 255.255.255.0` (net30: the /30 peer address) |
| networks the client can reach | `push "route 192.168.1.0 255.255.255.0"` |
| networks behind the client (site-to-site) | `iroute 192.168.50.0 255.255.255.0` |
| all traffic through the VPN | `push "redirect-gateway def1"` |

Addresses are checked against `server` / `topology` in `server.conf` and against the other
clients' static addresses. A LAN behind a client also needs `route <net> <mask>` in
`server.conf`: trapdoor warns with the exact line to add. `server.conf` needs
`client-config-dir ccd`. Changes apply when the client reconnects (the UI offers to reconnect
it). The 2FA lines and any other lines in the file are kept; openvpn-ui's files are read as they are.

## Access rules

The Access page decides which networks VPN clients can reach: rules from a client (or all
clients) to a network or address, optionally limited to TCP/UDP ports or ping, checked from
the top; the first match decides, then the default (allow or block). Replies to allowed
traffic always pass.

The agent renders them into the iptables chain `TD-FWD`, jumped to from `INPUT` and
`FORWARD` for traffic from `tun` devices, so they cover the server itself and everything
behind it. Rules match a client by its fixed VPN address; the page assigns free addresses
(from the top of the subnet) to clients that have none. With `client-to-client` OpenVPN
forwards traffic between clients itself, so the rules cannot see it.

Each WireGuard server has its own rules on the same page (pick the server first): chain
`TD-<id>` (e.g. `TD-wg0`), jumped to from `INPUT` and `FORWARD` for its interface. Every
WireGuard client has a fixed address, so rules always match; traffic between WireGuard
clients goes through the kernel and is filtered too. The rules are re-applied when clients
are added or removed.

| `TD_FIREWALL` | |
|---|---|
| `direct` (default) | the agent runs `iptables-restore` itself (host install as root) |
| `file` | the agent writes `trapdoor-firewall.rules` (WireGuard: `<id>.rules` in `TD_WIREGUARD_DIR`); the VPN container (the one with `NET_ADMIN`) applies it and reports back — the compose default |
| `off` | the agent never touches the firewall |

Nothing is changed until rules are saved for the first time. In direct mode the agent
restores them when it starts.

## Traffic history

The web process asks the agent for the connected clients every `TD_POLL_INTERVAL` (default
30 s) and stores sessions and traffic per client per hour in its SQLite database. If polls
were missed (web down, agent unreachable) the traffic of the gap is spread over its hours and
marked *estimated* instead of showing up as one spike.

The Traffic page shows any date range by day, month or year, for the whole server or one
client, with each client's share, session count and online time, and exports CSV. Days,
months and years follow `TD_TZ`. Upload and download are from the client's point of view.

API report parameters: `preset=today|this_month|last_month|last_30_days|this_year|last_year`
**or** `from=YYYY-MM-DD&to=YYYY-MM-DD` (inclusive; default this month), `group=day|month|year`,
`name=<user>` (omit for the whole server). Days/months/years follow `TD_TZ` (IANA name).
The report has totals, a gap-free series per period, and per-user totals with share,
session count and online time. Upload/download are from the user's point of view.

Traffic is stored per user per hour. If polls were missed (web down, agent unreachable) the
traffic of the gap is spread over its hours and flagged `estimated` instead of one spike.

## WireGuard servers

Next to OpenVPN, the agent can run WireGuard servers (so far through the
[agent API](api.md#wireguard)). In the web UI, use "Add WireGuard server" to create one; its Clients page adds devices, shows the config with a QR code and disables or deletes them. Each server is a network
interface with its own port and subnet, e.g. `wg0` on UDP 51820 with `10.9.0.0/24`:

- The agent keeps each server in `TD_WIREGUARD_DIR` (default `/etc/wireguard`):
  `<id>.json` with its keys and clients, and the `<id>.conf` it renders from it. Edits to
  the `.conf` are overwritten.
- Clients get a key pair, a preshared key (unless turned off) and the next free address.
  Their configuration file is for the WireGuard apps; it sends `0.0.0.0/0, ::/0` through
  the tunnel unless the server was created with other `allowed_ips`.
- Adding, disabling and removing clients is applied with `wg syncconf`: nobody else is
  disconnected. Disabling keeps the client but takes it off the interface; removing
  deletes its keys.
- WireGuard has no logins or sessions. A client counts as connected while its last
  handshake is less than 3 minutes old; traffic history works as for OpenVPN.

| `TD_WIREGUARD` | |
|---|---|
| `direct` (default) | the agent runs `wg-quick` / `wg` itself (host install as root, `wireguard-tools` installed) and turns on IP forwarding with FORWARD and NAT rules for the subnet |
| `file` | the agent writes the files; the VPN container (the one with `NET_ADMIN`) brings the interfaces up, applies changes with `wg syncconf`, adds NAT for the subnet and writes `wg show` to `<id>.dump` (keys hidden) — the compose default |
| `off` | no WireGuard servers |

In Docker, the compose file publishes one WireGuard port, `WG_PORT` (default 51820), the
same inside and outside: create the first WireGuard server with that port. Each more
server needs its own port added to the `openvpn` service. The host kernel needs the
WireGuard module (Linux 5.6 or newer has it).

On Ubuntu, AppArmor lets `wg-quick` read configurations only from `/etc/wireguard`, so keep
the default `TD_WIREGUARD_DIR` there.

## AmneziaWG servers

[AmneziaWG](https://docs.amnezia.org/documentation/amnezia-wg/) is WireGuard with traffic
that is hard to recognise and block: junk packets before the handshake, random bytes in
front of every packet, other message type values and, optionally, packets that imitate
another protocol. Clients use the AmneziaWG apps. In trapdoor an AmneziaWG server works
like a WireGuard server (clients, QR codes, live state, traffic, access rules); pick
AmneziaWG in *Add a server*.

- Each server gets random obfuscation values, inside the limits the AmneziaWG
  documentation gives (`Jc` 4–12, `Jmin` < `Jmax`, `S1`/`S2` 15–150 with S1 + 56 ≠ S2,
  four distinct `H1`–`H4`). Every client config carries the same values; the Overview
  shows them.
- **Strongest** (AmneziaWG 2.0, the default) adds `S3`/`S4` and `H1`–`H4` ranges; every
  device needs a current AmneziaWG app. **Most compatible** (1.5) leaves those out and
  also works with older apps and the kernel module.
- *Imitation packets* `I1`–`I5` are optional: packets the client sends before connecting
  (tags `<b 0xHEX>`, `<r N>`, `<rc N>`, `<rd N>`, `<t>`). With `S4`, the MTU is lowered
  by `S4` bytes unless one is set.
- Files live in `TD_AMNEZIAWG_DIR` (default `/etc/amnezia/amneziawg`), run with `awg` /
  `awg-quick`; `TD_WIREGUARD` (direct / file / off) applies to both.
- The Docker image ships `amneziawg-go` (userspace), so the host needs no kernel module.
  The compose file publishes `AWG_PORT` (default 51830), the same inside and outside:
  create the first AmneziaWG server with that port.

## Admin login and security

Create admins with the CLI (`trapdoor user add <name>`, also `passwd`, `del`, `list`); the
password comes from stdin or `TD_ADMIN_PASSWORD`.

- Passwords: PBKDF2-SHA256, 600 000 iterations, at least 10 characters
- Session cookie `td_session`: random 256-bit token, stored only as a SHA-256 hash; `HttpOnly`,
  `SameSite=Strict`, `Secure` (set `TD_COOKIE_SECURE=false` only for plain-HTTP testing)
- Logged out after `TD_SESSION_IDLE` (default 12h) of inactivity, and after 7 days at most;
  changing a password logs out all sessions of that user
- Failed logins throttled: 10 per IP and 30 per username per 15 minutes; unknown usernames take
  as long as wrong passwords
- Requests that change something are refused when they come from another site (CSRF)
- Behind a reverse proxy set `TD_TRUSTED_PROXIES` so the real client IP is used
- Every change is logged as `audit action=... target=... by=<user>`
- Strict Content-Security-Policy, no CDN: the UI and its fonts are served by trapdoor itself

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
