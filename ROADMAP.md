# Roadmap

## Next — rename to trapdoor (before the first release)

The project becomes **trapdoor** (`github.com/shuricksumy/trapdoor`), since it will manage
more than OpenVPN. Done in one go before v0.1.0, so nobody has to migrate settings:

- [x] Repo renamed on GitHub (old URLs redirect)
- [x] Go module path, binary (`trapdoor agent` / `web` / `user` …), Docker image `ghcr.io/shuricksumy/trapdoor`
- [x] Settings prefix `TW_` → `TD_`, cookie, firewall chain and file names, socket path, UI and docs
- [ ] First release v0.1.0 under the new name

## v1 — manage an existing OpenVPN server

- [x] Repo skeleton: single binary (`agent` / `web`), config via `TD_*` env, agent API with token auth, CI
- [x] Native Go PKI with easyrsa-compatible storage (`index.txt`, `serial`, `crl.pem`, `issued/`, `private/`)
  - [x] Read existing PKI (list certs, status, expiry)
  - [x] Issue client cert (optional passphrase, encrypted PKCS#8)
  - [x] Revoke / un-revoke, CRL generation
  - [x] Renew — including expired certs
  - [x] Remove cert completely
  - [x] Compatibility tests against real easyrsa 3.2 in CI
- [x] `.ovpn` generation from templates (tls-crypt / tls-auth / tls-crypt-v2)
- [x] End-to-end test with a real OpenVPN server and client in CI
- [ ] ~~QR code for `.ovpn`~~ dropped: a profile with keys (~5-8 kB) is larger than a QR code can hold
- [x] Live sessions via OpenVPN management interface: list, traffic, kick; revoke/delete disconnects
- [x] Session history & traffic statistics API (web): sessions + hourly traffic per user in SQLite,
      any date range with day / month / year grouping per user and for the whole server,
      presets, CSV export, gaps marked as estimated
- [x] Statistics page in the UI: date range with presets, day/month/year chart, per-client table with share, CSV export
- [x] Client 2FA: TOTP and/or static password, `trapdoor auth-verify` for OpenVPN, compatible with openvpn-ui's ccd format, QR enrolment in the UI
- [x] Web: SQLite + migrations
- [x] Web: admin login (users CLI, secure session cookie, throttling, CSRF protection)
- [x] Web: certificate and session management endpoints (proxy to the agent, audit log lines)
- [x] Web UI (Svelte 5, embedded): login, overview with today's timeline, clients, live sessions, account; light/dark/auto theme; phone layout
- [x] Docker image (amd64/arm64, GHCR) + compose: openvpn / agent / web, smoke-tested in CI with a real VPN client
- [ ] Migration from openvpn-ui (`trapdoor migrate --from-openvpn-ui`, dry-run, backup)

## v2 — setup and configuration

- [x] Setup wizard: CA, server cert, key type, port/proto, subnet, DNS, tls-crypt(-v2)/tls-auth, 2FA; NAT follows the subnet in Docker
- [x] Server config editor: form for common options + raw editor, OpenVPN-checked before saving, diff preview, backups, restart with automatic rollback
- [x] Routing: per-client routes (`push route`), LAN behind a client (`iroute`), static client IPs, full/split tunnel
- [x] Named networks reusable across clients (openvpn-ui "routes"): written into each client's ccd file
- [x] Certificate expiry warnings (clients, server, CA; UI + daily log) and bulk renew
- [x] Access rules (firewall) per client or for all: networks, ports, allow/block, default policy; iptables chain applied by the agent or the OpenVPN container

## v3 — several servers, WireGuard and AmneziaWG

Like [WGDashboard](https://github.com/WGDashboard/WGDashboard), but for OpenVPN, WireGuard and
AmneziaWG side by side: several VPN servers of any type on one host, managed in one UI.

- [x] **Servers** as their own objects: each has a type (OpenVPN / WireGuard / AmneziaWG), name,
      port, subnet; clients, live sessions, traffic history and access rules belong to a server;
      server switcher in the UI; existing setups become the first OpenVPN server
  - [x] API per server (`/v1/servers/{id}/…`, `/api/v1/servers/{id}/…`), history keyed by
        server, existing setup is server `openvpn`, switcher shown when there are several
- [x] Agent backends behind one interface (create / remove client, sessions, traffic, apply config)
  - [x] Host with OpenVPN and WireGuard servers; WireGuard servers created / removed at runtime
- [x] **WireGuard** server: setup, `wg0.conf` written by the agent and applied live with
      `wg syncconf` (no disconnects), peers with key pairs and addresses, AllowedIPs for routing
      and site-to-site, optional preshared key, `.conf` download + QR code, live status and traffic
      from `wg show dump` (connected = recent handshake), remove = revoke (no certificates)
  - [x] Agent: create / remove servers, clients (keys, PSK, next free address, LANs, disable),
        `.conf` download, live state and sessions for history, `direct` / `file` modes,
        e2e with a real client
  - [x] Web API and UI (clients page, QR code, create-server form)
- [x] **AmneziaWG** server: the WireGuard backend with `awg` tools and the obfuscation settings
      (junk packets, S1–S4, H1–H4 single values or ranges, optional I1–I5; levels 1.5 and 2.0,
      2.0 by default), random values within the documented limits, matching client configs
      for the AmneziaWG apps; `amneziawg-go` built into the image (no kernel module needed)
  - [x] AmneziaWG 3.1 level: header protection, random trailers, no cookie replies, client
        timing ranges, optional content padding; smoke-tested with a real 3.1 client
- [x] Several OpenVPN servers (e.g. UDP 1194 + TCP 443), each with its own directory, PKI,
      management port and tun device; the container supervisor runs all of them
- [x] Access rules for WireGuard / AmneziaWG interfaces; two-factor login
      stays OpenVPN-only (WireGuard has no login step)
  - [x] WireGuard and AmneziaWG: chain `TD-<id>` per server, same Access page, direct and file (Docker) mode
- [x] Docker: WireGuard / AmneziaWG in the `vpn` container (called `openvpn` before), ports per server
  - [x] Container renamed `vpn`; the old entrypoint name still works
  - [x] WireGuard in the same container (`TD_WIREGUARD=file`), `WG_PORT`, smoke-tested with a real client
  - [x] AmneziaWG in the same container (userspace `amneziawg-go`), `AWG_PORT`, smoke-tested
- [x] One UI for several hosts: `docker-compose.remote.yml` (vpn + agent with TLS), hosts added in
      the UI with the agent's pinned certificate fingerprint; servers `<host>.<id>`

## v4 — extras

- [ ] OIDC login (e.g. Authelia)
- [x] Backup / restore (PKI + config + DB, every host): one file from the UI, optionally
      encrypted; `trapdoor restore agent|web`
  - [x] Per server: backup on the server's page, restore (or move to another host) with
        *Add a server → From a backup*, traffic history included
- [ ] Audit log
- [ ] Optional easyrsa backend for the PKI interface
