# All in one

One machine, three containers: `web`, `agent` and one `vpn` container that runs every VPN
server. This is the repository's [`docker-compose.yml`](../../docker-compose.yml); this page
explains it.

```
                      ┌──────────────────────────── machine ────────────────────────────┐
 browser ──HTTPS────▶ │ web (127.0.0.1:8080)                                            │
 (reverse proxy)      │  │ unix socket /run/trapdoor/agent.sock                         │
                      │  ▼                                                              │
                      │ agent ── writes ──▶ ./openvpn  ./wireguard  ./amneziawg         │
                      │  │ shares the network of ▼                                      │
                      │ vpn  (NET_ADMIN)                                                │
 VPN clients ───────▶ │  ├─ OpenVPN    tun0       UDP 1194   VPN_PORT / VPN_PROTO       │
                      │  ├─ OpenVPN    ovpn-tcp   TCP 1195   VPN2_PORT / VPN2_PROTO     │
                      │  ├─ WireGuard  wg0        UDP 51820  WG_PORT                    │
                      │  ├─ AmneziaWG  awg0       UDP 51830  AWG_PORT (userspace)       │
                      │  └─ iptables: NAT per subnet, chain TD-<id> per server          │
                      └─────────────────────────────────────────────────────────────────┘
```

## How it works

- **web** has no capabilities and a read-only filesystem. It keeps admins, sessions,
  traffic history and other hosts in `/data/trapdoor.db` (volume `web-data`) and talks to
  the agent over a unix socket in the shared volume `run`.
- **agent** owns the files: the PKI, `server.conf`, `ccd/`, WireGuard `<id>.json` /
  `<id>.conf`, access rules, stop markers. It runs no VPN processes itself
  (`TD_FIREWALL=file`, `TD_WIREGUARD=file`). It shares the `vpn` container's network, so
  OpenVPN's management interface can stay on `127.0.0.1`.
- **vpn** is the only container with `NET_ADMIN`. Its entrypoint looks at the files every
  1–2 seconds: it starts every OpenVPN `server.conf` under a supervisor, brings WireGuard and
  AmneziaWG interfaces up, applies client changes with `wg syncconf` (nobody is
  disconnected), adds NAT for each subnet and applies the access rules.

Starting a server in the UI: web → agent (removes the `stopped` marker) → the `vpn`
container starts it within seconds.

## Setup

```bash
mkdir trapdoor && cd trapdoor
curl -fsSLO https://raw.githubusercontent.com/shuricksumy/trapdoor/main/docker-compose.yml
curl -fsSL -o .env https://raw.githubusercontent.com/shuricksumy/trapdoor/main/.env.example
sed -i "s/^TD_AGENT_TOKEN=.*/TD_AGENT_TOKEN=$(openssl rand -hex 32)/" .env
docker compose up -d
docker compose exec -it web trapdoor user add admin
```

Open the UI (SSH tunnel to `127.0.0.1:8080`, or a reverse proxy), run the OpenVPN setup,
then **Add a server** for WireGuard, AmneziaWG or a second OpenVPN server.

## Ports

| Variable | Default | For |
| --- | --- | --- |
| `VPN_PORT` / `VPN_PROTO` | `1194` / `udp` | the first OpenVPN server (listens on 1194 inside) |
| `VPN2_PORT` / `VPN2_PROTO` | `1195` / `tcp` | a second OpenVPN server (same port inside and outside) |
| `WG_PORT` | `51820` | the first WireGuard server |
| `AWG_PORT` | `51830` | the first AmneziaWG server |
| `WEB_BIND` | `127.0.0.1:8080` | the web UI |

More servers of a kind need more ports: add a line to the `vpn` service's `ports:` (the
same number inside and outside) and run `docker compose up -d`.

## Good to know

- Restarting the `vpn` container restarts every VPN server on the machine; servers with
  autostart off stay stopped. After restarting `vpn` alone, restart `agent` too (it shares
  its network); `docker compose up -d` does both.
- All servers share one network stack. Clients of different servers are kept apart by the
  access rules (each server has its own chain), not by separate kernels' routing tables.
  When that is not enough, see [Separate containers](../separate-containers/).
- The host needs the WireGuard kernel module (Linux 5.6+) for WireGuard; AmneziaWG runs in
  userspace (`amneziawg-go`) and needs nothing.
