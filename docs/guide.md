# How trapdoor works

Details behind the pages of the web UI. For installing, see the [README](../README.md);
for the HTTP API, [api.md](api.md).

- [Setup](#setup)
- [Client profiles](#client-profiles)
- [Client two-factor login](#client-two-factor-login)
- [Server configuration](#server-configuration)
- [OpenVPN 2.7](#openvpn-27)
- [Expiring certificates](#expiring-certificates)
- [Client network settings](#client-network-settings)
- [Access rules](#access-rules)
- [Traffic history](#traffic-history)
- [WireGuard servers](#wireguard-servers)
- [AmneziaWG servers](#amneziawg-servers)
- [Admin login and security](#admin-login-and-security)

## Setup

<img src="screenshots/setup.png" width="640">

On a server without `server.conf` the web UI opens the setup after login. It asks for the
public address, protocol and port, the VPN subnet, full or split tunnel, DNS and two-factor
login, then creates the certificate authority (EC P-256 by default; P-384 and RSA are in
"Certificates and encryption"), the `server` certificate, the control channel key
(tls-crypt-v2 by default), `server.conf` and `client-template.txt`, and waits for OpenVPN to
start. Nothing is overwritten: an existing PKI is reused, and the setup refuses when
`server.conf` exists.

In Docker OpenVPN listens on 1194 inside the container; `VPN_PORT` / `VPN_PROTO` in `.env`
set what is published and what client profiles use. `VPN_NAT_SUBNET=auto` (default)
masquerades the `server` subnet of `server.conf` and follows it when it changes.

The generated `server.conf` drops OpenVPN's privileges (`user nobody`), checks the CRL at
every connect, allows TLS 1.2+ with AES-GCM / ChaCha20 data ciphers, and keeps the
management interface on 127.0.0.1.

## Client profiles

`.ovpn` files are built like openvpn-ui did, so existing servers produce the same files:
`<TD_OPENVPN_DIR>/client-template.txt` + inline `<ca>`, `<cert>`, `<key>` + the
control-channel key matching `server.conf` (`tls-crypt`, `tls-auth` with `key-direction 1`,
or a per-client `tls-crypt-v2` key). tls-crypt-v2 client keys live in `keys-v2/<name>.key`;
existing ones are reused, new ones are created with `openvpn --genkey` (`TD_OPENVPN_BIN`).

## Client two-factor login

Per client, in the UI (Clients → More → Two-factor login) or `PUT /api/v1/servers/openvpn/certs/{name}/auth`:
an authenticator code (TOTP, HMAC-SHA256, 6 digits, 30 s) and/or a static password. The
device logs in with username = its certificate name and password = static password followed
by the code. OpenVPN checks it with trapdoor:

```
# server.conf
script-security 2
auth-user-pass-verify "/usr/local/bin/trapdoor auth-verify" via-file
# client-template.txt
auth-user-pass
```

The settings live in `ccd/<name>` exactly as openvpn-ui wrote them (`#2FA_KEY:` hex secret,
`#2FA_USER:`, `#STATIC_PASS:`), so existing enrolments keep working without `oathtool` or
`auth_client.sh`. Differences from openvpn-ui's script:

- the username must match the certificate: one client's password and code cannot be used with
  another client's certificate (the old script allowed that)
- new static passwords are stored hashed (`#STATIC_PASS_HASH:`); old plain ones still work
- codes from the previous and next 30-second step are accepted (clock drift)
- other lines in `ccd/<name>` (routes, `ifconfig-push`) are never touched; deleting the client
  deletes its `ccd` file

Use an authenticator app that supports SHA-256 codes (some apps only do SHA-1).

## Server configuration

**Server** page (or `/api/v1/servers/openvpn/server/config`): a form for the common options (port, protocol,
VPN subnet, topology, DNS and search domain for clients, full tunnel, client-to-client,
duplicate-cn, max clients, server routes, routes pushed to everyone) and a raw editor for
the whole `server.conf`. Every other line and comment in the file is kept.

- Before saving, the config is checked: OpenVPN itself parses it, plus trapdoor's checks
  (values OpenVPN only checks at startup, referenced key/cert files and directories exist,
  missing `management` / `crl-verify` / `client-config-dir` are pointed out)
- A preview shows exactly which lines change; saving keeps a backup (last 10, in
  `trapdoor-backups/`) and is refused if the file changed in the meantime
- "Networks behind clients" offers the LANs set on clients that still lack a `route` line
- **Apply and restart** restarts OpenVPN through the management interface and waits for it;
  if it does not come back within 30 s, the config it ran with before is restored and it is
  started again
- Restart method `TD_OPENVPN_RESTART`: `SIGHUP` (default; OpenVPN re-reads its config) or
  `SIGTERM` when a supervisor starts it again — the Docker image's `vpn` service does
  that inside the same container, so the agent keeps its shared network

## OpenVPN 2.7

The Docker image runs OpenVPN 2.7 (from Alpine). Configs written for 2.6 keep working,
with one change to look at before updating an existing server:

- **Topology:** a `server.conf` without a `topology` line used `net30` up to 2.6 and uses
  `subnet` from 2.7 on. Clients then get addresses from a different scheme, and fixed
  addresses in `ccd/` written for `net30` stop matching. The Server page warns about a
  missing `topology` line: before updating, add `topology net30` to keep the old
  behaviour (or move to `topology subnet` on purpose). Configs made by trapdoor's setup
  always have `topology subnet`. trapdoor follows the installed OpenVPN for the default.
- **Compression:** OpenVPN 2.7 no longer compresses what it sends; `comp-lzo` / `compress`
  only keep receiving compressed packets working, with a warning. Old clients still
  connect.
- `persist-key` is always on now (the option prints a notice); `ncp-disable` was already
  removed in 2.6.

Updating the image on your server: `docker compose pull && docker compose up -d`. The
Overview shows the running OpenVPN version.

## Expiring certificates

- Client certificates expiring within `TD_EXPIRY_WARN_DAYS` (default 30) are flagged in the UI
  and logged by the web process at start and once a day; the CA is flagged 90 days ahead
  (when it expires, every client stops working at once).
- Clients → select several (or "Select them" on the banner) → **Renew selected**, or
  `POST /api/v1/servers/openvpn/certs/renew {"names":[…],"validity_days":…}` with a result per name.
- Renewing keeps each key; a client's old certificate is revoked, so every renewed device
  needs its new profile.
- Renewing the **server** certificate keeps the old one valid (moved to `renewed/issued/`, as
  easyrsa does): OpenVPN uses it until it is restarted with the new one.

## Client network settings

Per client, in the UI (Clients → More → Network) or `PUT /api/v1/servers/openvpn/certs/{name}/network`:

| Setting | Written to `ccd/<name>` |
|---|---|
| static VPN address | `ifconfig-push 10.8.0.50 255.255.255.0` (net30: the /30 peer address) |
| networks the client can reach | `push "route 192.168.1.0 255.255.255.0"` |
| networks behind the client (site-to-site) | `iroute 192.168.50.0 255.255.255.0` |
| all traffic through the VPN | `push "redirect-gateway def1"` |

Addresses are checked against `server` / `topology` in `server.conf` and against the other
clients' static addresses. A LAN behind a client also needs `route <net> <mask>` in
`server.conf`: trapdoor warns with the exact line to add. `server.conf` needs
`client-config-dir ccd`. Changes apply when the client reconnects (the UI offers to reconnect
it). The 2FA lines and any other lines in the file are kept; openvpn-ui's files are read as they are.

### Named networks

A named network is a set of networks with a name, like openvpn-ui's routes: e.g.
"Office LAN" = `192.168.50.0/24`, behind the client `office-gw`, reachable for `laptop`
and `phone`. Manage them under Clients → Named networks, or with
`PUT /api/v1/servers/{server}/named-networks/{name}`.

- They are kept in `trapdoor-networks.json` next to `server.conf`. Their routes are
  written into the ccd files like the client's own: `push "route …"` for the clients that
  reach them, `iroute …` for the client they are behind. Nothing changes for OpenVPN.
- Changing or removing a network updates every client that has it; each client's own
  routes stay. A change that would make any client's file invalid (e.g. a network inside the
  VPN subnet behind a client) is refused as a whole.
- A client's Network dialog shows its named networks as chips next to its own routes:
  remove one, or pick *Add a named network…* (reach it) / *…behind this client*; that
  changes the named network right away, for this client.
- The **Network** page (menu) shows it all at once: the server's network and routes, the
  named networks, and for every client its VPN address, whether all its traffic goes
  through the VPN, what it reaches (own routes, named networks, routes for everyone) and
  what is behind it, with the warnings in one place. For WireGuard / AmneziaWG it lists
  every client's address, networks behind it, allowed IPs and DNS.
- Behind a client, the server needs a `route` line for the networks, as for a LAN set on
  the client: trapdoor warns with the line to add.

## Access rules

The Access page decides which networks VPN clients can reach: rules from a client (or all
clients) to a network or address, optionally limited to TCP/UDP ports or ping, checked from
the top; the first match decides, then the default (allow or block). Replies to allowed
traffic always pass.

The agent renders them into the iptables chain `TD-FWD`, jumped to from `INPUT` and
`FORWARD` for traffic from `tun` devices, so they cover the server itself and everything
behind it. Rules match a client by its fixed VPN address; the page assigns free addresses
(from the top of the subnet) to clients that have none. With `client-to-client` OpenVPN
forwards traffic between clients itself, so the rules cannot see it.

Each WireGuard server has its own rules on the same page (pick the server first): chain
`TD-<id>` (e.g. `TD-wg0`), jumped to from `INPUT` and `FORWARD` for its interface. Every
WireGuard client has a fixed address, so rules always match; traffic between WireGuard
clients goes through the kernel and is filtered too. The rules are re-applied when clients
are added or removed.

| `TD_FIREWALL` | |
|---|---|
| `direct` (default) | the agent runs `iptables-restore` itself (host install as root) |
| `file` | the agent writes `trapdoor-firewall.rules` (WireGuard: `<id>.rules` in `TD_WIREGUARD_DIR`); the VPN container (the one with `NET_ADMIN`) applies it and reports back — the compose default |
| `off` | the agent never touches the firewall |

Nothing is changed until rules are saved for the first time. In direct mode the agent
restores them when it starts.

## Traffic history

The web process asks the agent for the connected clients every `TD_POLL_INTERVAL` (default
30 s) and stores sessions and traffic per client per hour in its SQLite database. If polls
were missed (web down, agent unreachable) the traffic of the gap is spread over its hours and
marked *estimated* instead of showing up as one spike.

The Traffic page shows any date range by day, month or year, for the whole server or one
client, with each client's share, session count and online time, and exports CSV. Days,
months and years follow `TD_TZ`. Upload and download are from the client's point of view.

API report parameters: `preset=today|this_month|last_month|last_30_days|this_year|last_year`
**or** `from=YYYY-MM-DD&to=YYYY-MM-DD` (inclusive; default this month), `group=day|month|year`,
`name=<user>` (omit for the whole server). Days/months/years follow `TD_TZ` (IANA name).
The report has totals, a gap-free series per period, and per-user totals with share,
session count and online time. Upload/download are from the user's point of view.

Traffic is stored per user per hour. If polls were missed (web down, agent unreachable) the
traffic of the gap is spread over its hours and flagged `estimated` instead of one spike.

## Servers page: start, stop, autostart

*Servers* (link above every page) lists every server on this machine and the other hosts,
grouped by machine: type, port, subnet, state, clients, connected now and today's traffic.
From there a server is opened, started or stopped, backed up or removed.

**Where a server runs** is shown everywhere: the Servers page has the container under each
server's type, every server page has a chip next to the server switcher ("WireGuard ·
vps · container vpn"), *Add a server* says before you press the button what will be created
where ("Adds WireGuard wg1 on vps, in container vpn, UDP port 51821"), and the stop and remove
dialogs name the server, its machine and its container. The container names come from the
agent's `TD_CONTAINER_OPENVPN`, `TD_CONTAINER_WIREGUARD` and `TD_CONTAINER_AMNEZIAWG` (the
compose files set them); a type a machine does not run (e.g. `TD_WIREGUARD=off`) cannot be
picked for it.

- **Stop**: the server stops until it is started again; its clients cannot connect, its
  settings and clients stay. A stopped server's pages show a banner with *Start*.
- **Autostart** off: the server stays stopped when the VPN container (or the machine)
  starts again, until it is started in trapdoor. On (default): it runs after a restart even
  if it was stopped.
- The state is two marker files next to the server: `trapdoor.stopped` /
  `trapdoor.noautostart` in an OpenVPN server's directory, `<id>.stopped` /
  `<id>.noautostart` next to a WireGuard server's `<id>.conf`. In Docker the `vpn` container
  follows them (stops OpenVPN, takes the interface down; at its start it marks servers with
  autostart off as stopped). Without Docker the agent stops OpenVPN through the management
  interface and runs `wg-quick` itself; starting OpenVPN again is then up to systemd.
- While stopped, changes (new clients, rules) are saved and apply when it starts; a restart
  of a stopped OpenVPN server is refused. The traffic history has a gap, not estimates.

## More OpenVPN servers

The first OpenVPN server is the one in `TD_OPENVPN_DIR` (an existing openvpn-ui
directory, or the one the setup creates). More can be added next to it, for example a
second one on TCP 443 for networks that block everything else: "Add a server" → OpenVPN,
with an id (also its network device, e.g. `ovpn-tcp`), a port and UDP or TCP. The setup
wizard then runs for it, with that port and protocol fixed.

- Each added server is independent: its own directory `TD_OPENVPN_SERVERS_DIR/<id>/`
  (default `<openvpn dir>/servers/<id>/`) with `server.conf`, its own certificate
  authority in `pki/`, `ccd/`, clients, two-factor logins, access rules and traffic
  history. A client of one server cannot connect to another.
- trapdoor keeps the id, port, protocol and management port in `trapdoor.json` there.
  Management ports follow the first one: 2081, 2082, … on 127.0.0.1.
- Its tun device is named after the id (`dev ovpn-tcp` + `dev-type tun`), so its access
  rules have their own chain `TD-<id>`. Ids starting with `tun` are not allowed: the first
  server's rules cover `tun+`.
- The setup suggests a subnet no other server uses and refuses one that overlaps.
- Removing a server stops OpenVPN and moves its directory to `servers/.removed/<id>-<time>`:
  the certificates are kept until you delete that folder. The first server cannot be
  removed.

In Docker, the `vpn` service runs every server: it starts OpenVPN for each
`servers/<id>/server.conf` as soon as the setup writes it, restarts it like the first
one, adds NAT for its subnet (`VPN_NAT_SUBNET=auto` or empty for none; a fixed CIDR
applies to the first server only), applies its access rules and stops it when the server is
removed. The compose file publishes one more port for it, `VPN2_PORT` / `VPN2_PROTO`
(default TCP 1195), the same inside and outside: add the server with that port. For TCP
443, set `VPN2_PORT=443` if nothing else on the host uses it.

Without Docker, start each added server yourself, for example with a systemd unit running
`openvpn --cd /etc/openvpn/servers/<id> --config server.conf`. With
`TD_OPENVPN_RESTART=SIGHUP` (the default outside Docker) trapdoor restarts it through its
management interface.

## WireGuard servers

Next to OpenVPN, the agent can run WireGuard servers (so far through the
[agent API](api.md#wireguard)). In the web UI, use "Add a server" to create one; its Clients page adds devices, shows the config with a QR code and disables or deletes them. Each server is a network
interface with its own port and subnet, e.g. `wg0` on UDP 51820 with `10.9.0.0/24`:

- The agent keeps each server in `TD_WIREGUARD_DIR` (default `/etc/wireguard`):
  `<id>.json` with its keys and clients, and the `<id>.conf` it renders from it. Edits to
  the `.conf` are overwritten.
- Clients get a key pair, a preshared key (unless turned off) and the next free address.
  Their configuration file is for the WireGuard apps; it sends `0.0.0.0/0, ::/0` through
  the tunnel unless the server was created with other `allowed_ips`.
- Adding, disabling and removing clients is applied with `wg syncconf`: nobody else is
  disconnected. Disabling keeps the client but takes it off the interface; removing
  deletes its keys.
- **Adding clients**: one, or several at once (`<prefix>-1`, `-2`, … on the next free
  addresses). Per client: notes, the VPN address (a free one is suggested), keys made here
  or the device's own public key (then trapdoor never has its private key and the config
  has a placeholder for it), a preshared key on or off, networks behind it, and its own
  DNS, allowed IPs, MTU and keepalive. All of it can be changed later (Clients → Edit).
- **Settings and client defaults** (Overview): name, the public address and port clients
  connect to, and the defaults for every client's config: DNS, allowed IPs, MTU,
  keepalive, preshared key for new clients. A client's own settings win. Devices need the
  new config (QR code) after a change.
- WireGuard has no logins or sessions. A client counts as connected while its last
  handshake is less than 3 minutes old; traffic history works as for OpenVPN.

| `TD_WIREGUARD` | |
|---|---|
| `direct` (default) | the agent runs `wg-quick` / `wg` itself (host install as root, `wireguard-tools` installed) and turns on IP forwarding with FORWARD and NAT rules for the subnet |
| `file` | the agent writes the files; the VPN container (the one with `NET_ADMIN`) brings the interfaces up, applies changes with `wg syncconf`, adds NAT for the subnet and writes `wg show` to `<id>.dump` (keys hidden) — the compose default |
| `off` | no WireGuard servers |

In Docker, the compose file publishes one WireGuard port, `WG_PORT` (default 51820), the
same inside and outside: create the first WireGuard server with that port. Each more
server needs its own port added to the `vpn` service. The host kernel needs the
WireGuard module (Linux 5.6 or newer has it).

On Ubuntu, AppArmor lets `wg-quick` read configurations only from `/etc/wireguard`, so keep
the default `TD_WIREGUARD_DIR` there.

## AmneziaWG servers

[AmneziaWG](https://docs.amnezia.org/documentation/amnezia-wg/) is WireGuard with traffic
that is hard to recognise and block: junk packets before the handshake, random bytes in
front of every packet, other message type values and, optionally, packets that imitate
another protocol. Clients use the AmneziaWG apps. In trapdoor an AmneziaWG server works
like a WireGuard server (clients, QR codes, live state, traffic, access rules); pick
AmneziaWG in *Add a server*.

- Each server gets random obfuscation values, inside the limits the AmneziaWG
  documentation gives (`Jc` 4–12, `Jmin` < `Jmax`, `S1`/`S2` 15–150 with S1 + 56 ≠ S2,
  four distinct `H1`–`H4`). Every client config carries the same values; the Overview
  shows them.
- **Strongest** (AmneziaWG 2.0, the default) adds `S3`/`S4` and `H1`–`H4` ranges; every
  device needs a current AmneziaWG app. **Most compatible** (1.5) leaves those out and
  also works with older apps and the kernel module.
- **Newest** (AmneziaWG 3.1) is 2.0 plus:
  - header protection: a random `HeaderProtectionKey` encrypts the header fields
    (needs `S1`–`S4` of at least 12, so `S4` is 12–32);
  - `RandomTrailers = on`: random bytes after every packet;
  - `DisableCookies = on` on the server: no cookie replies, which active probing can
    recognise (it also drops WireGuard's protection against handshake floods);
  - random client timings around WireGuard's own: `RekeyAfterTime` 100–140 s,
    `RejectAfterTime` 180–200 s, `RekeyTimeout` 4–6 s, `KeepaliveTimeout` 8–12 s.

  Header key, trailers and the optional `ContentPaddingAddition` (API only, up to 256
  bytes, taken off the MTU) must match on both sides and are in every client config.
  Every device needs an AmneziaWG app that supports 3.1.
- *Imitation packets* `I1`–`I5` are optional: packets the client sends before connecting
  (tags `<b 0xHEX>`, `<r N>`, `<rc N>`, `<rd N>`, `<t>`). With `S4`, the MTU is lowered
  by `S4` bytes unless one is set.
- Files live in `TD_AMNEZIAWG_DIR` (default `/etc/amnezia/amneziawg`), run with `awg` /
  `awg-quick`; `TD_WIREGUARD` (direct / file / off) applies to both.
- The Docker image ships `amneziawg-go` (userspace), so the host needs no kernel module.
  The compose file publishes `AWG_PORT` (default 51830), the same inside and outside:
  create the first AmneziaWG server with that port.

## Nodes

A **node** is an agent with its `vpn` container: it runs VPN servers of any type. A
**machine** can have several nodes (e.g. one per network, each with its own network stack,
NAT and firewall), and one web UI manages the nodes on every machine. The UI shows it as
*machine › node › VPN server*: the **Nodes** page lists the machines with their nodes and
each node's servers, *Add a server* asks for the node first, and every page shows where the
selected server runs (e.g. *vps › node vps · container vpn*).

The node next to the web UI is the one in `docker-compose.yml` (unix socket). Its name and
machine come from `TD_NODE_NAME` and `TD_MACHINE` (default *main* on *This machine*).
Other nodes run an agent with TLS and join the web UI by themselves:

**A node on another machine** (`docker-compose.remote.yml`: the `vpn` container and the
agent, no web UI):

1. In the web UI: **Nodes → Add a node → With a join token**: a name (e.g. `vps`), how long
   the token is valid, the machine and the node's address. It shows the lines for the
   node's `.env`: `TD_JOIN_URL` (this web UI), `TD_JOIN_TOKEN` (one-time, `tdj_…`),
   `TD_NODE_NAME`, `TD_NODE_URL` (how the web UI reaches the node's agent, e.g.
   `https://203.0.113.5:9443`) and `TD_MACHINE`.
2. On the other machine: `.env` with its own `TD_AGENT_TOKEN` and those lines, then
   `docker compose -f docker-compose.remote.yml up -d`.

At start the agent sends the token, its address, its certificate fingerprint and its agent
token to `TD_JOIN_URL/api/v1/nodes/join`. The web UI connects back with that certificate
pinned and adds the node (it retries every 10 seconds until the web UI answers). The token is
used up then; later starts of the same node (same certificate) join without one.

**Nodes in the web UI's own compose file** join with `TD_JOIN_SECRET` (set on the web
service, at least 24 characters) as their `TD_JOIN_TOKEN`: see
[examples/nodes](../examples/nodes/).

**By hand** (an agent without join settings): `docker compose -f docker-compose.remote.yml
exec agent trapdoor agent-cert` prints the fingerprint; then *Nodes → Add a node → By hand*
with a name, `https://<address>:9443` (`AGENT_PORT`), its `TD_AGENT_TOKEN` and the
fingerprint. trapdoor connects once before saving.

A node's servers work like local ones: clients, sessions, traffic history, access rules,
setup.

- A server on another node has the id `<node>.<id>` in the web API and in the traffic
  history (e.g. `vps.wg0`); the own node's servers keep their ids.
- The node's agent listens on TCP with TLS 1.3 (`TD_AGENT_TLS=auto`) and a self-signed
  certificate it creates on first start (`trapdoor-agent.crt` / `.key` in its OpenVPN
  directory). The web UI trusts exactly that certificate (its SHA-256 fingerprint, like an SSH
  host key) and sends the token with every request. Still, allow the agent port only from the
  machine with the web UI.
- A join token is shown once; only its hash is kept. A join with a different certificate
  for an existing node name needs a new token (or the secret).
- `TD_JOIN_URL` should be `https://` when the node is on another machine: the join request
  carries the node's agent token.
- The node list (with tokens) is kept in the web database. Removing a node only forgets it:
  its servers keep running there, and their history stays. A node with join settings joins
  again at its next start; remove them from its `.env`.
- A node that does not answer is shown on the Nodes and Servers pages with the error; the
  other servers keep working.
- An agent that should run no OpenVPN server (e.g. one only for WireGuard) gets
  `TD_OPENVPN=off`: then it has no "openvpn" server at all. More layouts (several nodes in
  one compose file, the host's network, private networks between machines):
  [examples](../examples/).

## Backup and restore

There are two kinds of backup: one server, and everything (every node). Both can also run
on a schedule.

### One server

Every server's page (Server for OpenVPN, Overview for WireGuard and AmneziaWG) has
*Backup*: a file with that server only, its keys and certificates, clients, settings,
access rules and traffic history (`POST /api/v1/servers/{server}/backup` with
`{"passphrase": "…"}`; the passphrase works as for the full backup below).

*Add a server → From a backup* brings it back (`POST /api/v1/servers/restore`, a form upload
with `file`, `passphrase`, `host` and `replace`), on this machine or another host, with the
same id: so a server can also be moved to another machine.

- The id, the port and (WireGuard) the subnet must be free there; *replace* swaps a server
  with the same id (an added OpenVPN server's old files go to `.removed/`).
- An added OpenVPN server whose management port is taken on the new machine gets the next
  free one (written into its `server.conf`).
- The first OpenVPN server comes back into `TD_OPENVPN_DIR` where it is not set up yet,
  or over the current one with *replace* (then OpenVPN restarts). Its backup leaves out the
  other servers and the agent's TLS certificate.
- Traffic history is added under the server's new id (`<host>.<id>` on another host);
  rows that exist already are kept.
- Clients keep working with their old profiles and configs; for a server on another machine
  they need its address, so check the public address in its settings.

### Everything

Account → *Download a backup* (or `POST /api/v1/backup` with `{"passphrase": "…"}`) makes one
file:

| In the file | From |
|---|---|
| `agent/openvpn/…` | this machine's OpenVPN directory: PKI, `server.conf`, `ccd/`, keys, added servers, access rules, named networks |
| `agent/wireguard/…`, `agent/amneziawg/…` | WireGuard and AmneziaWG servers (`<id>.json` with keys and clients, `<id>.conf`) |
| `web/trapdoor.db` | admins, traffic history, other hosts (a consistent copy, made while running) |
| `hosts/<name>/…` | every other host's directories, like `agent/` (an unreachable host is left out with a note) |

With a passphrase the file (`.tdbackup`) is encrypted: AES-256-GCM in 64 KiB chunks, the key
from PBKDF2-SHA256 with 600 000 iterations. Changed, reordered or cut files are refused.
Without one it is a plain `.tar.gz`; keep that one private, it holds the CA keys and the
hosts' tokens. Runtime state (status files, `wg show` dumps) is left out.

Restore with trapdoor stopped, on the machine and with the same settings (`TD_OPENVPN_DIR`
and the others) the files go to:

```bash
trapdoor restore agent backup.tdbackup                 # VPN servers into TD_OPENVPN_DIR, TD_WIREGUARD_DIR, …
trapdoor restore agent --host vps backup.tdbackup      # another host's part, on that host
trapdoor restore web backup.tdbackup                   # the web database into TD_DB_PATH
```

In Docker the agent part goes through the `vpn` service (it has the directories mounted)
and the web part through `web`; the backup file must be readable for the web container's
user (`chmod 644`):

```bash
docker compose run --rm --no-deps -v ./backup.tdbackup:/b:ro --entrypoint trapdoor vpn restore agent /b
docker compose run --rm --no-deps -v ./backup.tdbackup:/b:ro web restore web /b
```

The passphrase is asked for, or taken from `TD_BACKUP_PASSPHRASE`. Restore refuses to
overwrite a machine that is set up already (`server.conf` or a CA) or an existing database;
`--force` replaces the files that are in the backup (other files stay) and keeps the old
database as `trapdoor.db.before-restore-<time>`. A restored agent keeps its TLS certificate,
so the hosts' fingerprints stay valid.

### Scheduled

Account → *Scheduled backups*: every day or every week at a set hour (in `TD_TZ`), into
`TD_BACKUP_DIR` on the web UI's machine (default `backups/` next to the database: `/data/backups`
in Docker, in the `web-data` volume).

- Each run writes `trapdoor-all-<time>` (everything, as *Download a backup*, every node
  included) and, with *Also one file per server*, `trapdoor-server-<id>-<time>` for every
  server (restore with *Add a server → From a backup*).
- *Keep* is how many files of each kind stay; older ones are deleted. Other files in the
  folder are never touched.
- `TD_BACKUP_PASSPHRASE` on the web service encrypts them (`.tdbackup`); without it they are
  plain `.tar.gz` and the page warns. The passphrase is not stored in the database, so a copy
  of the folder alone cannot be opened. `trapdoor restore` reads the same variable.
- The page shows the last run (and its error), the next one, *Back up now*, and the files to
  download or delete; the Servers page shows a line, or the error when the last one failed.
- A run that was due while the web UI was down runs when it is back; a failed one is not
  retried before the next slot. Files are written to a temporary name first, so a failed run
  leaves no half file.
- Keep a copy somewhere else: mount the folder from a NAS, or copy it off the machine (e.g.
  `rsync` in cron). In Docker, a host folder instead of the volume:
  `- ./backups:/data/backups` under `web`'s volumes, owned by the web user
  (`sudo chown 10001 ./backups`).
- `TD_BACKUP_DIR=-` turns scheduled backups off.

## Admin login and security

Create admins with the CLI (`trapdoor user add <name>`, also `passwd`, `del`, `list`); the
password comes from stdin or `TD_ADMIN_PASSWORD`.

- Passwords: PBKDF2-SHA256, 600 000 iterations, at least 10 characters
- Session cookie `td_session`: random 256-bit token, stored only as a SHA-256 hash; `HttpOnly`,
  `SameSite=Strict`, `Secure` (set `TD_COOKIE_SECURE=false` only for plain-HTTP testing)
- Logged out after `TD_SESSION_IDLE` (default 12h) of inactivity, and after 7 days at most;
  changing a password logs out all sessions of that user
- Failed logins throttled: 10 per IP and 30 per username per 15 minutes; unknown usernames take
  as long as wrong passwords
- Requests that change something are refused when they come from another site (CSRF)
- Behind a reverse proxy set `TD_TRUSTED_PROXIES` so the real client IP is used
- Every change is logged as `audit action=... target=... by=<user>`
- Strict Content-Security-Policy, no CDN: the UI and its fonts are served by trapdoor itself

The pieces are separated so that the part facing the browser has no privileges:

```
[ Browser ] → [ web ] ──unix socket / HTTP──▶ [ agent ] → OpenVPN, PKI, firewall
              users, history, UI              privileged, runs next to OpenVPN
```

| Docker service | Runs as | Privileges | Purpose |
|---|---|---|---|
| `openvpn` | root | `NET_ADMIN`, `/dev/net/tun` | the OpenVPN server, NAT, access rules |
| `agent` | root | none; shares `openvpn`'s network | PKI, profiles, config, sessions via the management interface on 127.0.0.1 |
| `web` | uid 10001 | none, read-only filesystem | UI + API, database in a volume |

Web and agent talk over a unix socket on a shared volume, so the agent has no network port,
and OpenVPN's management interface is never published.
