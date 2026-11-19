# amneziawg/

The AmneziaWG servers' files, the same layout as [`../wireguard/`](../wireguard/README.md)
(`<id>.json`, `<id>.conf`, …). Mounted at `/etc/amnezia/amneziawg` into **vpn-amneziawg**,
which runs them in userspace (`amneziawg-go`, no kernel module needed), and the **agent**.

`<id>.json` also holds the server's obfuscation settings (junk packets, S1–S4, H1–H4, …):
every client config carries the same values.
