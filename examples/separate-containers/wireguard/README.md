# wireguard/

The WireGuard servers' files. Mounted at `/etc/wireguard` into **vpn-wireguard** (which brings
the interfaces up) and the **agent** (which writes them).

| Path | What |
| --- | --- |
| `<id>.json` | trapdoor's state of server `<id>` (e.g. `wg0`): its keys, clients, settings, access rules |
| `<id>.conf` | the WireGuard config rendered from it (do not edit: it is rewritten) |
| `<id>.rules`, `<id>.fwstatus` | its access rules and whether vpn-wireguard applied them |
| `<id>.dump` | live state written by vpn-wireguard every few seconds (keys hidden) |
| `<id>.stopped`, `<id>.noautostart` | present when the server is stopped / has autostart off |

Keep it private: `<id>.json` holds the private keys.
