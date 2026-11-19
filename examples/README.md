# Deployment examples

trapdoor is three programs in one image: **web** (the UI and API, no privileges), **agent**
(writes the VPN configuration, certificates and rules) and **vpn** (the only container with
network privileges: it runs the VPN servers, NAT and firewall rules). How you place them is
up to you. Three setups:

| Example | What runs where | Choose it when |
| --- | --- | --- |
| [All in one](all-in-one/) | one machine, one `vpn` container runs every OpenVPN, WireGuard and AmneziaWG server | the usual case: simple, one place to publish ports |
| [Separate containers](separate-containers/) | one machine, OpenVPN in one `vpn` container, WireGuard and AmneziaWG in another | the VPN types should not share a network stack, or need different networking (e.g. WireGuard on the host network) |
| [Remote machines](remote/) | the web UI on one machine, VPN servers on others | VPN servers in several places (a VPS, home, an office), managed from one UI |

The setups combine: a remote machine can itself use separate containers, and any machine
can be added to the web UI under **Hosts**.

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

## Separate containers

```
 browser ──▶ web ──unix socket──▶ agent ──▶ vpn (OpenVPN)          UDP 1194, TCP 1195
              │
              └──TLS, Docker network "trapdoor"──▶ agent ──▶ vpn (WireGuard, AmneziaWG)
                                                              UDP 51820, 51830
```

Each `vpn` container has its own network stack, NAT and firewall: OpenVPN clients and
WireGuard clients cannot reach each other at the network level, and each container can be
connected differently (e.g. WireGuard on the host's network, OpenVPN on a Docker bridge).
The second stack is added in the web UI as a host, so both appear on the Servers page.

## Remote machines

```
 machine A                                   machine B (VPS, office, …)
 browser ──▶ web ──▶ agent ──▶ vpn           agent ──▶ vpn
              │                               ▲
              └──── TLS (pinned certificate) ─┘  agent port only from machine A
                    + token, port 9443           (or over a private network)
```

Machine B runs only `vpn` and `agent`; its agent listens on TCP 9443 with TLS and a
self-signed certificate that the web UI pins by fingerprint. Its VPN clients connect to
machine B directly; only management goes through machine A.

## Things that hold for every setup

- Docker cannot publish ports at runtime: every VPN server's port must be in the compose
  file of the container that runs it (`docker compose up -d` after adding one).
- Each agent needs its own `TD_AGENT_TOKEN`; the web UI keeps the other hosts' tokens in its
  database.
- The OpenVPN management interface stays on `127.0.0.1` inside each `vpn` container, which is
  why an agent shares its `vpn` container's network.
- Backups: *Account → Download a backup* includes every host; each server's page has a
  backup of that server alone, which also moves it to another container or machine.
- Details: [docs/guide.md](../docs/guide.md) (several hosts, servers page, backup).
