# guest/

Node **guest**: the files of the VPN servers it runs (same layout as [main/](../main/)).
`agent-guest` joins the web UI by itself with `TD_JOIN_SECRET` and listens with TLS on the
compose network (`https://vpn-guest:9443`, not published); `vpn-guest` runs the servers.

| Folder | Mounted at |
| --- | --- |
| `openvpn/` | `/etc/openvpn` (also the agent's TLS certificate, `trapdoor-agent.crt`) |
| `wireguard/` | `/etc/wireguard` |
| `amneziawg/` | `/etc/amnezia/amneziawg` |
