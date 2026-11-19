# Deployment examples

trapdoor is three programs in one image: **web** (the UI and API, no privileges), **agent**
(writes the VPN configuration, certificates and rules) and **vpn** (the only container with
network privileges: it runs the VPN servers, NAT and firewall rules). An agent with its
`vpn` container is a **node**; it can run any VPN type. A machine can have several nodes, and
one web UI manages the nodes on every machine. The UI shows it as *machine › node › VPN
server*. Three setups:

| Example | What runs where | Choose it when |
| --- | --- | --- |
| [All in one](all-in-one/) | one machine, one node: one `vpn` container runs every OpenVPN, WireGuard and AmneziaWG server | the usual case: simple, one place to publish ports |
| [Nodes](nodes/) | one machine, one compose file: the web UI and several nodes (`main`, `office`, `guest`), each an agent + `vpn` container with its own folder | groups of VPN servers that must not share a network stack, or need different networking (e.g. one node on the host network) |
| [Remote machines](remote/) | the web UI on one machine, nodes on others, joining with a one-time token | VPN servers in several places (a VPS, home, an office), managed from one UI |

The setups combine: a remote machine can run several nodes too, and every node shows up
under **Nodes**.

## All in one

```
 browser ──HTTPS──▶ web ──unix socket──▶ agent ──files──▶ vpn
                                                         ├─ OpenVPN    UDP 1194
                                                         ├─ OpenVPN    TCP 1195
                                                         ├─ WireGuard  UDP 51820
                                                         └─ AmneziaWG  UDP 51830
```

One `vpn` container, one set of published ports, one firewall. Servers are separated by
trapdoor's access rules (iptables chains per server), not by network stacks.

## Nodes

```
 machine docker-host
 browser ──▶ web ─┬─ unix socket ─────▶ node main    (agent-main   + vpn-main)    ← ./main
                  ├─ TLS, pinned ─────▶ node office  (agent-office + vpn-office)  ← ./office
                  └─ TLS, pinned ─────▶ node guest   (agent-guest  + vpn-guest)   ← ./guest
                  ▲
                  └── office and guest join by themselves (TD_JOIN_SECRET)
```

One compose file, one web UI, several nodes. Each node has its own network stack, NAT,
firewall, ports and folder, and runs any VPN type: clients of one node cannot reach another
node's networks. *Add a server* asks for the node.

## Remote machines

```
 machine A                                   machine B (VPS, office, …)
 browser ──▶ web ──▶ agent ──▶ vpn           agent ──▶ vpn
              │                               ▲
              └──── TLS (pinned certificate) ─┘  agent port only from machine A
                    + token, port 9443           (or over a private network)
```

Machine B runs a node: only `vpn` and `agent`. Its agent listens on TCP 9443 with TLS and a
self-signed certificate that the web UI pins by fingerprint; with a join token from *Nodes →
Add a node* it adds itself to the web UI at start. Its VPN clients connect to
machine B directly; only management goes through machine A.

## Things that hold for every setup

- Docker cannot publish ports at runtime: every VPN server's port must be in the compose
  file of the container that runs it (`docker compose up -d` after adding one).
- Each agent needs its own `TD_AGENT_TOKEN`; the web UI keeps the other nodes' tokens in its
  database.
- The OpenVPN management interface stays on `127.0.0.1` inside each `vpn` container, which is
  why an agent shares its `vpn` container's network.
- Backups: *Account → Download a backup* includes every node; each server's page has a
  backup of that server alone, which also moves it to another container or machine.
- Details: [docs/guide.md](../docs/guide.md) (nodes, servers page, backup).
