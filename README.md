# trapdoor

[![CI](https://github.com/shuricksumy/trapdoor/actions/workflows/ci.yml/badge.svg)](https://github.com/shuricksumy/trapdoor/actions/workflows/ci.yml)
[![Build](https://github.com/shuricksumy/trapdoor/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/shuricksumy/trapdoor/actions/workflows/docker-publish.yml)
[![CodeQL](https://github.com/shuricksumy/trapdoor/actions/workflows/codeql.yml/badge.svg)](https://github.com/shuricksumy/trapdoor/actions/workflows/codeql.yml)
[![Release](https://img.shields.io/github/v/release/shuricksumy/trapdoor)](https://github.com/shuricksumy/trapdoor/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

A web UI for your own OpenVPN, WireGuard and AmneziaWG servers. Set the server up in a few clicks, create a profile
for every phone and laptop, see who is connected and how much traffic each device used,
and decide which networks each device may reach.

trapdoor is the successor to [openvpn-ui](https://github.com/shuricksumy/openvpn-ui) and
works with an existing openvpn-ui directory as it is: same certificates, same profiles,
same two-factor logins.

| Overview | Phone (dark theme) |
| --- | --- |
| <img src="docs/screenshots/overview.png" width="560"> | <img src="docs/screenshots/mobile-dark.png" width="220"> |

| Clients | Traffic | Access rules |
| --- | --- | --- |
| <img src="docs/screenshots/clients.png" width="300"> | <img src="docs/screenshots/traffic-dark.png" width="300"> | <img src="docs/screenshots/access.png" width="300"> |

## Features

- **Setup** that creates the certificate authority, keys and `server.conf` with modern
  defaults (tls-crypt-v2, AES-GCM, TLS 1.2+) ([what it asks](docs/guide.md#setup))
- **Clients**: create, download the `.ovpn` profile, revoke, restore, renew (also in bulk),
  optional key passphrase; warnings before certificates expire
- **Two-factor login** per client with an authenticator app (QR code) and/or a static password
- **Connected now**: live sessions with traffic, disconnect a device
- **Traffic history** per client and for the whole server: any date range, by day, month or
  year, CSV export
- **Network per client**: fixed VPN address, routes, networks behind a client (site-to-site),
  all traffic through the VPN; named networks (e.g. "Office LAN") given to several clients at once
- **Access rules**: which client may reach which network or port; everything else allowed or
  blocked
- **Several servers** on one host: more OpenVPN servers (e.g. TCP 443 next to UDP 1194),
  WireGuard and AmneziaWG ([how](docs/guide.md#more-openvpn-servers))
- **Server settings** in a form or as raw `server.conf`, checked by OpenVPN before saving,
  with a preview of the changes, backups and restart with automatic rollback
- Light / dark / auto theme, works on phones
- Secure by design: the web UI has no privileges, admin passwords are hashed, login attempts
  are throttled, no external scripts or CDNs

## Quick start (Docker)

You need a Linux server with Docker and a UDP port reachable from the internet.

```bash
mkdir trapdoor && cd trapdoor
curl -fsSLO https://raw.githubusercontent.com/shuricksumy/trapdoor/main/docker-compose.yml
curl -fsSL -o .env https://raw.githubusercontent.com/shuricksumy/trapdoor/main/.env.example
sed -i "s/^TD_AGENT_TOKEN=.*/TD_AGENT_TOKEN=$(openssl rand -hex 32)/" .env
nano .env                                    # time zone (TD_TZ), VPN port; the rest can stay
docker compose up -d
docker compose exec -it web trapdoor user add admin    # choose your admin password
```

1. Open the web UI. It listens on `127.0.0.1:8080` of the server, so either put it behind
   your reverse proxy ([below](#reverse-proxy)) or, for a first look, open an SSH tunnel:
   `ssh -L 8080:127.0.0.1:8080 <server>` and browse to http://localhost:8080.
2. Log in. The setup opens: enter the address clients connect to (a hostname or your public
   IP), pick the options and press **Create the server**.
3. Forward the VPN port (`VPN_PORT`, UDP 1194 by default) from your router or cloud firewall
   to this server.
4. **Clients → New client**, then **Download profile** and import it in the OpenVPN app on the
   device (OpenVPN Connect, Tunnelblick, network-manager-openvpn, …).

## Moving from openvpn-ui

trapdoor reads openvpn-ui's directory as it is (`server.conf`, `client-template.txt`,
`easy-rsa/pki`, `ccd/`): certificates, profiles, routes and two-factor logins keep working.

1. Back up the directory.
2. Stop the openvpn-ui containers.
3. In `.env` set `OPENVPN_DIR` to that directory, then `docker compose up -d` and create an
   admin as above.
4. Check that `server.conf` has a `management` line (openvpn-ui's `management 0.0.0.0 2080`
   works; `127.0.0.1 2080` is better). The Server page points out anything missing.

Admin users are not taken over: create them with `trapdoor user add`.

## Reverse proxy

Put the web UI behind HTTPS. With **Traefik** use the ready-made override:

```bash
curl -fsSLO https://raw.githubusercontent.com/shuricksumy/trapdoor/main/docker-compose.traefik.yml
# in .env: COMPOSE_FILE=docker-compose.yml:docker-compose.traefik.yml, TD_HOSTNAME, TRAEFIK_NETWORK
docker compose up -d
```

It removes the host port, joins your Traefik network and adds the router labels; adjust the
entrypoint, certificate resolver and middlewares (an Authelia line is included, commented
out) in the file. With **Caddy, nginx** or anything else, proxy to `WEB_BIND` and set
`TD_TRUSTED_PROXIES` to the proxy's address so login throttling sees real client IPs.

## Updating

```bash
docker compose pull && docker compose up -d
```

The `agent` container uses the network of the `openvpn` container. After restarting
`openvpn` on its own, restart `agent` too (`docker compose restart agent`), otherwise the
agent cannot reach OpenVPN; `docker compose up -d` handles this by itself.

Images on GitHub Container Registry, `ghcr.io/shuricksumy/trapdoor` (amd64 and arm64):

| Tag | Built from |
| --- | --- |
| `latest` | the `main` branch |
| `1.2.3`, `1.2` | releases (set `TD_IMAGE=ghcr.io/shuricksumy/trapdoor:1.2` in `.env` for a stable setup) |

## Backups

Everything that matters is in two places:

- **`OPENVPN_DIR`** (default `./openvpn`): the certificate authority and all client keys,
  `server.conf`, per-client settings. Losing it means re-issuing every profile, so back it up
  and keep it private.
- **The `web-data` volume**: admin users and traffic history.
  `docker compose stop web && docker compose cp web:/data/trapdoor.db ./trapdoor.db && docker compose start web`

## Settings

Set in `.env` next to `docker-compose.yml`:

| Variable | Default | Meaning |
| --- | --- | --- |
| `TD_AGENT_TOKEN` | — | Shared secret between the web UI and the agent; required, long and random |
| `TD_TZ` | `UTC` | Time zone for days, months and years in traffic reports, e.g. `Europe/Berlin` |
| `OPENVPN_DIR` | `./openvpn` | OpenVPN directory on the host |
| `VPN_PORT` / `VPN_PROTO` | `1194` / `udp` | Port and protocol published for VPN clients |
| `VPN_NAT_SUBNET` | `auto` | NAT for VPN clients: `auto` follows `server.conf`, or a subnet, or empty for none |
| `WEB_BIND` | `127.0.0.1:8080` | Where the web UI listens on the host |
| `TD_COOKIE_SECURE` | `true` | `false` only for testing over plain HTTP |
| `TD_TRUSTED_PROXIES` | — | Reverse proxy addresses (CIDRs) allowed to pass the client IP |
| `TD_WEB_API_TOKEN` | — | Optional token for scripts: `Authorization: Bearer …` ([API](docs/api.md)) |
| `TD_EXPIRY_WARN_DAYS` | `30` | Warn this many days before a client certificate expires |
| `TD_SESSION_IDLE` | `12h` | Admin logout after this much inactivity |
| `TD_TOTP_ISSUER` | `trapdoor` | Name shown in authenticator apps |
| `TD_IMAGE` | `…/trapdoor:latest` | Image to run |

## Without Docker

Each [release](https://github.com/shuricksumy/trapdoor/releases) has Linux binaries
(amd64, arm64, armv7) with the web UI built in. Run `trapdoor agent` as root next to
OpenVPN and `trapdoor web` as an unprivileged user, with the same `TD_AGENT_TOKEN`; see
[docs/guide.md](docs/guide.md#admin-login-and-security) for how the parts fit together and
[`internal/config/config.go`](internal/config/config.go) for every setting.

## Documentation

- [How trapdoor works](docs/guide.md): setup, profiles, two-factor login, server
  settings, client networks, access rules, traffic history, security
- [HTTP API](docs/api.md)
- [Development](docs/development.md): building, tests, GitHub Actions, releases
- [Roadmap](ROADMAP.md)

## License

[MIT](LICENSE)
