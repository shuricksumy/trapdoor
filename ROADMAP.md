# Roadmap

## v1 — manage an existing OpenVPN server

- [x] Repo skeleton: single binary (`agent` / `web`), config via `TW_*` env, agent API with token auth, CI
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
- [x] Client 2FA: TOTP and/or static password, `tunnelward auth-verify` for OpenVPN, compatible with openvpn-ui's ccd format, QR enrolment in the UI
- [x] Web: SQLite + migrations
- [x] Web: admin login (users CLI, secure session cookie, throttling, CSRF protection)
- [x] Web: certificate and session management endpoints (proxy to the agent, audit log lines)
- [x] Web UI (Svelte 5, embedded): login, overview with today's timeline, clients, live sessions, account; light/dark/auto theme; phone layout
- [x] Docker image (amd64/arm64, GHCR) + compose: openvpn / agent / web, smoke-tested in CI with a real VPN client
- [ ] Migration from openvpn-ui (`tunnelward migrate --from-openvpn-ui`, dry-run, backup)

## v2 — setup and configuration

- [x] Setup wizard: CA, server cert, key type, port/proto, subnet, DNS, tls-crypt(-v2)/tls-auth, 2FA; NAT follows the subnet in Docker
- [x] Server config editor: form for common options + raw editor, OpenVPN-checked before saving, diff preview, backups, restart with automatic rollback
- [x] Routing: per-client routes (`push route`), LAN behind a client (`iroute`), static client IPs, full/split tunnel
- [ ] Named networks reusable across clients (openvpn-ui "routes")
- [x] Certificate expiry warnings (clients, server, CA; UI + daily log) and bulk renew
- [x] Access rules (firewall) per client or for all: networks, ports, allow/block, default policy; iptables chain applied by the agent or the OpenVPN container

## v3 — extras

- [ ] OIDC login (e.g. Authelia)
- [ ] Multiple VPN servers (agents) in one UI
- [ ] Backup / restore (PKI + config + DB)
- [ ] Audit log
- [ ] Optional easyrsa backend for the PKI interface
