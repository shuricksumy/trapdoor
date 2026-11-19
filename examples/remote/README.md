# Remote machines

The web UI on one machine, VPN servers on others: a VPS abroad, a box at home, an office.
Each remote machine runs only `vpn` and `agent`
([`docker-compose.remote.yml`](../../docker-compose.remote.yml)); the main machine runs the
usual [`docker-compose.yml`](../../docker-compose.yml) (or any example here).

```
 machine A (main)                                  machine B (remote)
 ┌──────────────────────────────────┐              ┌───────────────────────────────────┐
 │ web ──▶ agent ──▶ vpn            │              │ agent (TLS :9443) ──▶ vpn         │
 │  │      (its own VPN servers)    │              │  ▲                    ├ OpenVPN   │
 │  │                               │  TLS 1.3,    │  │                    ├ WireGuard │
 │  └──────────────────────────────────── pinned ──────┘                    └ AmneziaWG │
 │                                  │  certificate │                                   │
 └──────────────────────────────────┘  + token     └───────────────────────────────────┘
                                                      ▲ VPN clients connect to machine B
```

Only management goes from A to B. VPN clients of machine B's servers connect to machine B
directly; if machine A is down, they keep working (only the UI and the traffic history pause).

## Setup

On **machine B**:

```bash
mkdir trapdoor && cd trapdoor
curl -fsSLO https://raw.githubusercontent.com/shuricksumy/trapdoor/main/docker-compose.remote.yml
curl -fsSL -o .env https://raw.githubusercontent.com/shuricksumy/trapdoor/main/.env.example
sed -i "s/^TD_AGENT_TOKEN=.*/TD_AGENT_TOKEN=$(openssl rand -hex 32)/" .env   # its own token
docker compose -f docker-compose.remote.yml up -d
docker compose -f docker-compose.remote.yml exec agent trapdoor agent-cert  # sha256:…
```

In the web UI on **machine A**: **Hosts → Add a host** with a name (e.g. `vps`), the address
`https://<machine B>:9443`, machine B's `TD_AGENT_TOKEN` and the fingerprint. trapdoor
connects once before it saves the host. Then **Add a server**, machine `vps`.

## How the connection is protected

- The agent serves TLS 1.3 with a self-signed certificate it makes on its first start
  (`trapdoor-agent.crt` / `.key` in its OpenVPN directory). The web UI trusts exactly that
  certificate, by its SHA-256 fingerprint, like an SSH host key; a different certificate
  (another machine, a man in the middle) is refused.
- Every request carries machine B's token. The web UI keeps it in its database (include it
  in your backups' protection: use a backup passphrase).
- The certificate stays the same across restarts, updates and restores of machine B, so the
  host does not need to be added again.

## Who can reach the agent port

The agent can change every VPN server on machine B, so its port should be reachable only
from machine A. Pick one:

**Public address, firewalled to machine A**

```bash
# on machine B
sudo ufw allow from 203.0.113.10 to any port 9443 proto tcp   # machine A's address
sudo ufw deny 9443/tcp
```

(With Docker, published ports bypass `ufw`'s default chains on many systems: check with an
outside `nc -vz <machine B> 9443`, or use the next option.)

**A private network between the machines (recommended)**

Put both machines on Tailscale, a WireGuard tunnel or a VPC, and publish the agent port only
on that address:

```bash
# machine B's .env
AGENT_BIND=100.101.102.103     # machine B's Tailscale / WireGuard / VPC address
AGENT_PORT=9443
```

Add the host as `https://100.101.102.103:9443`. The port is then not on the public address at
all.

## Ports on machine B

| Variable | Default | For |
| --- | --- | --- |
| `AGENT_BIND` / `AGENT_PORT` | `0.0.0.0` / `9443` | the agent API for the web UI |
| `VPN_PORT` / `VPN_PROTO` | `1194` / `udp` | its first OpenVPN server |
| `VPN2_PORT` / `VPN2_PROTO` | `1195` / `tcp` | a second OpenVPN server |
| `WG_PORT`, `AWG_PORT` | `51820`, `51830` | its first WireGuard and AmneziaWG servers |

A remote machine without OpenVPN: set `TD_OPENVPN: "off"` for its agent; it then lists no
"openvpn" server. A remote machine can also use a container per VPN type: start from
[separate containers](../separate-containers/) and give its agent `TD_AGENT_LISTEN: ":9443"`
and `TD_AGENT_TLS: auto` instead of the unix socket (and no web service).

## Day to day

- **Servers page**: machine B's servers are grouped under its name, with start / stop and
  autostart like local ones; their ids are `vps.<id>` in the API and the traffic history.
- **Unreachable**: the Hosts page and the Servers page show the error; everything else keeps
  working, and the history continues when it answers again.
- **Backups**: *Account → Download a backup* includes machine B (`hosts/vps/…`); restore
  that part on machine B with `trapdoor restore agent --host vps <file>`. A single server's
  backup can also be restored on another machine (*Add a server → From a backup*), which
  moves it.
- **Removing the host** only forgets it in the web UI: its VPN servers keep running there.
- **Updating**: `docker compose -f docker-compose.remote.yml pull && … up -d` on machine B;
  the web UI and the agents talk the same API, so update both sides around the same time.

## Tested

The remote setup is part of the Docker smoke test in CI (a second compose project from
`docker-compose.remote.yml`, added with its fingerprint, a wrong fingerprint refused, a
WireGuard server and a client created through the main web UI) and was checked by hand with
Docker, including `AGENT_BIND` limiting the published agent port to one address.
