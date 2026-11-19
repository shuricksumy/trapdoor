# main/

Node **main**: the files of the VPN servers it runs. Its agent (`agent-main`) talks to the
web UI over a unix socket; `vpn-main` runs the servers.

| Folder | Mounted at | What |
| --- | --- | --- |
| `openvpn/` | `/etc/openvpn` | OpenVPN: PKI (`easy-rsa/pki/`), `server.conf`, `ccd/`, more servers in `servers/<id>/` |
| `wireguard/` | `/etc/wireguard` | WireGuard: `<id>.json` (state), `<id>.conf` (rendered) |
| `amneziawg/` | `/etc/amnezia/amneziawg` | AmneziaWG, the same layout |

Created on the first start. Keep them private: they hold the CA's and servers' private keys.
