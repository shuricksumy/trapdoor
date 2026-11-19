# Separate containers

One machine, two stacks: OpenVPN in one `vpn` container, WireGuard and AmneziaWG in
another. One web UI manages both: the second stack is added under **Hosts**, so all servers
show on the Servers page as usual (the second stack's as `wireguard.<id>`).

```
 ┌──────────────────────────────────── machine ─────────────────────────────────────┐
 │  openvpn/ stack                            wireguard/ stack                       │
 │                                                                                   │
 │  web ──unix socket──▶ agent                agent  (TD_OPENVPN=off, TLS :9443)     │
 │   │                    │ shares network     │ shares network                      │
 │   │                    ▼                    ▼                                     │
 │   │                   vpn                  vpn                                    │
 │   │                    ├─ OpenVPN UDP 1194  ├─ WireGuard UDP 51820                │
 │   │                    └─ OpenVPN TCP 1195  └─ AmneziaWG UDP 51830                │
 │   │                                         ▲                                     │
 │   └──── TLS (pinned) on the Docker network "trapdoor" ───┘                        │
 └───────────────────────────────────────────────────────────────────────────────────┘
```

## Why

- **Isolation**: each `vpn` container has its own network stack, routing table, NAT and
  firewall. OpenVPN clients cannot reach the WireGuard networks (and the other way round)
  even without access rules: the OpenVPN container has no route to them at all.
- **Different networking per VPN type**: e.g. WireGuard on the host's network (below)
  while OpenVPN stays on a Docker bridge.
- **Restarts**: restarting one `vpn` container does not drop the other's clients.

The cost: a second agent with its own token, and its port in one more compose file.

## Setup

```bash
# 1. The web UI and OpenVPN (creates the Docker network "trapdoor")
cd openvpn
cp .env.example .env          # set TD_AGENT_TOKEN (openssl rand -hex 32)
docker compose up -d
docker compose exec -it web trapdoor user add admin

# 2. WireGuard and AmneziaWG, with their own agent
cd ../wireguard
cp .env.example .env          # its own TD_AGENT_TOKEN
docker compose up -d
docker compose exec agent trapdoor agent-cert      # prints sha256:…
```

3. In the web UI: **Hosts → Add a host**: name `wireguard`, address
   `https://trapdoor-wireguard:9443`, the second stack's `TD_AGENT_TOKEN` and the fingerprint.
4. **Add a server → WireGuard / AmneziaWG**, machine `wireguard`, with the port the
   `wireguard` stack publishes (`WG_PORT`, `AWG_PORT`).

The OpenVPN stack has `TD_WIREGUARD=off` (WireGuard cannot be created there by mistake);
the WireGuard stack has `TD_OPENVPN=off` (no empty OpenVPN server in the list). Its agent
port is not published: only containers on the network `trapdoor` (the web UI) reach it.

## Variant: WireGuard on the host's network

```bash
sudo sysctl -w net.ipv4.ip_forward=1                    # Docker cannot set it here
cd wireguard && docker compose -f docker-compose.yml -f docker-compose.host.yml up -d
cd ../openvpn && docker compose -f docker-compose.yml -f docker-compose.host-access.yml up -d
```

Add the host as `https://host.docker.internal:9443` (`AGENT_PORT`).

- No Docker port publishing or Docker NAT in the path: `wg0` / `awg0` and their iptables
  rules are on the host, and a new server's port is open at once (no compose change).
- Choose an `AGENT_PORT` that is free on the host, and allow it only from the web UI
  (e.g. `ufw deny 9443/tcp` from outside: the web UI comes from the Docker bridge).
- NAT goes out through the host's default route; set `VPN_NAT_IFACE` (e.g. `eth0`) when
  there is none or it is the wrong one. Without it the container logs one line and does
  no NAT (clients then reach the VPN network only).
- Docker Desktop (Mac / Windows): `host.docker.internal` is the computer, not Docker's
  Linux VM where host-network containers run; use `https://172.17.0.1:9443` there.

## Clients that should reach only some networks

Docker publishes ports only from networks with a route out, so a `vpn` container on an
`internal: true` network cannot take clients from the internet. To keep a VPN's clients
inside, use its **Access** page instead: *Block everything else* plus rules for the networks
they may use (e.g. the Docker network of your services). That works in either stack.

## Tested

This setup was checked with Docker: both stacks from these files, the second added as a
host, OpenVPN, WireGuard and AmneziaWG servers created through the web UI, real clients
connected to each, the OpenVPN client unable to reach the WireGuard network, and the
host-network variant with a client.
