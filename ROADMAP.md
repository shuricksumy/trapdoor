# Roadmap

## v1 — manage an existing OpenVPN server

- [x] Repo skeleton: single binary (`agent` / `web`), config via `TW_*` env, agent API with token auth, CI
- [ ] Native Go PKI with easyrsa-compatible storage (`index.txt`, `serial`, `crl.pem`, `issued/`, `private/`)
  - [ ] Read existing PKI (list certs, status, expiry)
  - [ ] Issue client cert (optional passphrase, encrypted PKCS#8)
  - [ ] Revoke / un-revoke, CRL generation
  - [ ] Renew — including expired certs
  - [ ] Remove cert completely
  - [ ] Compatibility tests against real easyrsa 3.2 in CI
- [ ] `.ovpn` generation from templates (tls-crypt / tls-auth / tls-crypt-v2), QR code
- [ ] Live sessions via OpenVPN management interface: list, traffic, kick
- [ ] Client 2FA: TOTP and/or static password, `tunnelward auth-verify` for OpenVPN
- [ ] Web: admin login, SQLite + migrations
- [ ] Frontend (SvelteKit, embedded in the binary): dashboard, clients, certificates
- [ ] Docker image + two-service `docker-compose` example
- [ ] Migration from openvpn-ui (`tunnelward migrate --from-openvpn-ui`, dry-run, backup)

## v2 — setup and configuration

- [ ] Setup wizard: CA, server cert, cipher, port/proto, DNS, tls-crypt
- [ ] Server config editor: forms for common options + validated raw editor
- [ ] Routing: per-client routes (`iroute` / `push route`), route groups, static client IPs, full/split tunnel
- [ ] Certificate expiry warnings and bulk renew
- [ ] Firewall rules managed by the agent

## v3 — extras

- [ ] OIDC login (e.g. Authelia)
- [ ] Multiple VPN servers (agents) in one UI
- [ ] Backup / restore (PKI + config + DB)
- [ ] Audit log
- [ ] Optional easyrsa backend for the PKI interface
