# openvpn/

The OpenVPN servers' files. Mounted at `/etc/openvpn` into **vpn-openvpn** (which runs
them) and the **agent** (which writes them). Empty until the setup; then:

| Path | What |
| --- | --- |
| `server.conf`, `client-template.txt` | the first OpenVPN server |
| `easy-rsa/pki/` | its certificate authority, server and client certificates and keys |
| `ccd/<client>` | per-client settings: fixed address, routes, two-factor login |
| `tls-crypt-v2.key`, `crl.pem` | control channel key, list of revoked certificates |
| `trapdoor-firewall.*`, `trapdoor-networks.json` | access rules, named networks |
| `servers/<id>/` | every other OpenVPN server, each with its own `server.conf`, `pki/`, `ccd/` |
| `trapdoor.stopped`, `trapdoor.noautostart` | present when the server is stopped / has autostart off |

Keep it private: it holds the CA's private key. Back it up with *Account → Download a backup*.
