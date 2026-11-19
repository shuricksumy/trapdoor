# office/

Node **office**: the files of the VPN servers it runs (same layout as [main/](../main/)).
`agent-office` joins the web UI by itself with `TD_JOIN_SECRET` and listens with TLS on the
compose network (`https://vpn-office:9443`, not published); `vpn-office` runs the servers.

| Folder | Mounted at |
| --- | --- |
| `openvpn/` | `/etc/openvpn` (also the agent's TLS certificate, `trapdoor-agent.crt`) |
| `wireguard/` | `/etc/wireguard` |
| `amneziawg/` | `/etc/amnezia/amneziawg` |
