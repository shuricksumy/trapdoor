# Nodes

One web UI and several **nodes** in one compose file. A node is an agent with its `vpn`
container: it runs VPN servers of any type (OpenVPN, WireGuard, AmneziaWG), with its own
folder, network stack, NAT, firewall and ports. Several nodes can be on one machine.

```
nodes/
├── docker-compose.yml        web, and per node an agent + a vpn container
├── docker-compose.host.yml   variant: node main on the host's network
├── .env.example
├── main/     ──▶ vpn-main    + agent-main     openvpn/ wireguard/ amneziawg/
├── office/   ──▶ vpn-office  + agent-office   openvpn/ wireguard/ amneziawg/
└── guest/    ──▶ vpn-guest   + agent-guest    openvpn/ wireguard/ amneziawg/
```

```
 Machine docker-host
 ┌──────────────────────────────────────────────────────────────────────────┐
 │  browser ─▶ web ─── unix socket ──────▶ node main     vpn-main    UDP 1194, TCP 1195, 51820, 51830
 │              ▲  └── TLS, pinned ──────▶ node office   vpn-office  UDP 1294, 51920, 51930
 │              │  └── TLS, pinned ──────▶ node guest    vpn-guest   UDP 1394, 52020, 52030
 │              └──── join (TD_JOIN_SECRET) ── office, guest at start
 └──────────────────────────────────────────────────────────────────────────┘
```

In the web UI: **Nodes** shows machine › node › VPN servers; **Add a server** asks for the
node first, then the type; every page shows where the selected server runs, e.g.
*docker-host › node office · container vpn-office*.

## How nodes join

- `main` is next to the web UI (unix socket, like the all-in-one setup).
- `office` and `guest` start their agent with TLS (`TD_AGENT_TLS=auto`, a self-signed
  certificate) on the compose network only; port 9443 is not published. At start the agent
  calls `TD_JOIN_URL` (`http://web:8080`) with `TD_JOIN_SECRET`, its address
  (`TD_NODE_URL`), certificate fingerprint and agent token. The web UI connects back with
  that certificate pinned and lists the node. Until the web UI answers, the agent retries
  every 10 seconds.
- On later starts the node joins again with the same certificate (nothing changes). A
  node removed in the UI joins again at its next start as long as it has the secret: remove
  it from the compose file too.
- Nodes on other machines join with a one-time token from *Nodes → Add a node* instead:
  see [../remote](../remote/).

## Why nodes

- **Isolation**: each node has its own network stack, routing table, NAT and firewall.
  Clients of `guest` cannot reach `office`'s networks, whatever the access rules say: there
  is no route.
- **Any type per node**: each node can run OpenVPN, WireGuard and AmneziaWG servers, as
  many as it has ports for.
- **Different networking per node**: e.g. `main` on the host's network (below), the others
  on Docker bridges.
- **Restarts**: restarting a node drops only its clients.

## Setup

```bash
cp .env.example .env    # tokens: openssl rand -hex 32 (one per agent), TD_JOIN_SECRET: openssl rand -hex 24
docker compose up -d
docker compose exec -it web trapdoor user add admin
```

Open the UI: *Nodes* lists main, office and guest. Run the OpenVPN setup of a node by
picking its OpenVPN server, or *Add a server* on a node with the ports this file publishes
for it (e.g. WireGuard on office: 51920). Docker cannot publish ports later: a new server
needs its port in the node's `ports:` and `docker compose up -d`.

One more node: copy the `office` blocks and folder, change the name, ports and agent token.

## Variant: node main on the host's network

```bash
sudo sysctl -w net.ipv4.ip_forward=1        # Docker cannot set it in the host namespace
docker compose -f docker-compose.yml -f docker-compose.host.yml up -d
```

- main's interfaces and iptables rules are on the host itself: no Docker port publishing or
  Docker NAT in the path; a new server's port is open at once (open it in the host's
  firewall).
- NAT leaves through the host's default route; set `VPN_NAT_IFACE` (e.g. `eth0`) when that
  is wrong. Stopping `vpn-main` takes its interfaces, NAT and rules off the host again.
- OpenVPN's management interface is then on the host's `127.0.0.1:2080`.

## Keeping a VPN's clients inside

Docker publishes ports only from networks with a route out, so a `vpn` container on an
`internal: true` network cannot take clients from the internet. To keep a server's clients
inside, use its **Access** page: *Block everything else* plus rules for the networks they
may use.
