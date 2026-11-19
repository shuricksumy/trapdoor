# Separate containers

One compose file, one web UI, one agent, and a `vpn` container per VPN type. Each type has
its own folder, mounted only into its own container:

```
separate-containers/
├── docker-compose.yml        web, agent, vpn-openvpn, vpn-wireguard, vpn-amneziawg
├── docker-compose.host.yml   variant: vpn-wireguard on the host's network
├── .env.example
├── openvpn/      ──▶ vpn-openvpn     (and the agent)   PKI, server.conf, ccd, servers/<id>/
├── wireguard/    ──▶ vpn-wireguard   (and the agent)   <id>.json, <id>.conf
└── amneziawg/    ──▶ vpn-amneziawg   (and the agent)   <id>.json, <id>.conf
```

```
 browser ──HTTPS──▶ web
                     │ unix socket
                     ▼
                   agent ── shares the network of ──▶ vpn-openvpn     OpenVPN   UDP 1194, TCP 1195
                     │                                (tun0, ovpn-tcp, …)
                     ├── ./wireguard ──── files ─────▶ vpn-wireguard   WireGuard UDP 51820
                     │                                (wg0, …)
                     └── ./amneziawg ──── files ─────▶ vpn-amneziawg   AmneziaWG UDP 51830
                                                      (awg0, …, userspace)
```

## How it works

- The **agent** writes all three folders. It shares `vpn-openvpn`'s network only because
  OpenVPN's management interface stays on `127.0.0.1`; WireGuard and AmneziaWG need no
  network path at all: the agent writes `<id>.conf`, the access rules and stop markers, and
  reads back the `<id>.dump` the vpn container writes every few seconds.
- Each **vpn container** runs what it finds in the folders it has: `vpn-openvpn` only sees
  `/etc/openvpn`, `vpn-wireguard` only `/etc/wireguard`, `vpn-amneziawg` only
  `/etc/amnezia/amneziawg`.
- In the web UI it is all **This machine**: *Add a server → WireGuard* lands in
  `vpn-wireguard` because that is where `./wireguard` is mounted; the type decides the
  container.

## Why

- **Isolation**: each container has its own network stack, routing table, NAT and firewall.
  Clients of one VPN type cannot reach another type's networks, even without access rules:
  the other container has no route there.
- **Different networking per type**: e.g. WireGuard on the host's network (below) while
  OpenVPN and AmneziaWG stay on Docker bridges.
- **Restarts**: restarting one container drops only that type's clients.

The cost: a few more containers; still one web UI, one agent, one token.

## Setup

```bash
cp .env.example .env          # set TD_AGENT_TOKEN (openssl rand -hex 32)
docker compose up -d
docker compose exec -it web trapdoor user add admin
```

Open the UI, run the OpenVPN setup, then *Add a server* for WireGuard and AmneziaWG with the
ports this file publishes (`WG_PORT`, `AWG_PORT`). More servers of a type need more ports on
that type's container.

## Variant: WireGuard on the host's network

```bash
sudo sysctl -w net.ipv4.ip_forward=1        # Docker cannot set it in the host namespace
docker compose -f docker-compose.yml -f docker-compose.host.yml up -d
```

- `wg0`, `wg1`, … and their iptables rules are on the host itself: no Docker port
  publishing or Docker NAT in the path, and a new server's port is open at once (no compose
  change; open it in the host's firewall).
- NAT leaves through the host's default route; set `VPN_NAT_IFACE` (e.g. `eth0`) when that
  is wrong. Without a default route the container logs it once and does no NAT.
- Stopping `vpn-wireguard` takes its interfaces, NAT and rules off the host again.
- The agent needs no port: it still talks to `vpn-wireguard` through `./wireguard`.

The same override can be written for `vpn-amneziawg` (not tested here). OpenVPN is
different: the agent shares `vpn-openvpn`'s network, so moving that one to the host network
moves the agent and the management interface there too.

## Keeping a VPN's clients inside

Docker publishes ports only from networks with a route out, so a `vpn` container on an
`internal: true` network cannot take clients from the internet. To keep a VPN's clients
inside, use its **Access** page: *Block everything else* plus rules for the networks they
may use.

## Tested

Checked with Docker from these files: OpenVPN, WireGuard and AmneziaWG servers created
through the web UI, each running in its own container; real clients connected to each; no
client reached another type's network; stop / start in the UI took the interface down / up
in the right container; and the host-network variant with a client, its interface removed
from the host when the container stopped.
