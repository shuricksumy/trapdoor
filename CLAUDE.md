# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

trapdoor (formerly tunnelward) is a web UI for OpenVPN, WireGuard and AmneziaWG servers and the successor to openvpn-ui. It is one Go binary with two main parts:

- `agent`: privileged. It owns the PKI (native Go, easyrsa-3 compatible layout), `server.conf`, `ccd/`, OpenVPN control through the management interface, and the firewall rules.
- `web`: unprivileged. It handles admin users, the SQLite database (sessions and traffic history), the REST API, and the embedded Svelte UI.

Web talks to agent over HTTP with a shared token (`X-Trapdoor-Token`). In Docker that goes over a unix socket. The web part never touches OpenVPN files.

## Commands

```bash
make ui            # web/ (Svelte 5 + Vite) → internal/webui/dist, embedded by the next build
make build         # bin/trapdoor
make test          # go test ./...
make lint          # golangci-lint
cd web && npm run check   # svelte-check
sudo test/e2e/run.sh bin/trapdoor         # real OpenVPN server + clients, network namespace, iptables (needs openvpn, oathtool, iproute2)
test/docker/smoke.sh <image>                # the compose setup with a real VPN client
```

Run locally: `TD_AGENT_TOKEN=dev TD_OPENVPN_DIR=/tmp/td TD_CRL_COPIES=- TD_FIREWALL=off bin/trapdoor agent`, then in a second terminal `bin/trapdoor web` and `bin/trapdoor user add admin`.

## Layout

- `cmd/trapdoor`: subcommands `agent`, `web`, `user`, `init-pki`, `auth-verify` (OpenVPN 2FA hook), `health`, `version`.
- `internal/api`: request/response types shared by agent and web. It must not import other internal packages; other packages alias its types (e.g. `setup.Request = api.SetupRequest`).
- `internal/agent`: agent HTTP handlers. `Host` serves `/v1/info` and routes `/v1/servers/{id}/…` to each `Server` (one OpenVPN server, id `openvpn`, from the `TD_*` settings); the web API mirrors it under `/api/v1/servers/{id}/…` and the UI prefixes per-server paths in `lib/api.js`. History rows are keyed by server id. `Server.PKI()` is nil until setup creates the PKI. `restartMu` serializes restarts and setup; `netMu` guards static address changes; `fwMu` guards firewall applies.
- `internal/pki`: CA, issue/revoke/renew, CRL, `index.txt`. `compat_test.go` runs against real easyrsa when `TD_EASYRSA_DIR` is set (CI does this).
- `internal/serverconf`: Parse/Apply keep every unrelated line of `server.conf`. Check runs `openvpn --config X --version` plus value checks that OpenVPN itself only does at start. File writes keep backups and use a version for optimistic locking.
- `internal/setup`: wizard validation and rendering of `server.conf` / `client-template.txt`.
- `internal/ccd`, `internal/ccdnet`, `internal/clientauth`: per-client files. Network lines and 2FA lines (openvpn-ui format) share the same file and must not clobber each other.
- `internal/firewall`: access rules rendered as an iptables-restore chain (`TD-FWD` for OpenVPN, `TD-<id>` per WireGuard server; applied through `agent/fwtarget.go`), jumped to from INPUT and FORWARD for `tun+`. `TD_FIREWALL=direct|file|off`; in file mode `docker/openvpn-entrypoint.sh` applies the rules and reports `ok|error <sha256>`.
- `internal/wg`: WireGuard keys (crypto/ecdh X25519), server/client config rendering, `wg show dump` parsing. `internal/agent/wireguard.go`: WireGuard servers (`<id>.json` state in `TD_WIREGUARD_DIR`, `.conf` rendered from it, applied with `wg syncconf`; `TD_WIREGUARD=direct|file|off`, file mode left to the VPN container like the firewall). Sessions are derived from handshakes; byte counters are per interface lifetime, so a session counts from its start. AmneziaWG servers are the same `WGServer` with `Type: amneziawg`: `awg`/`awg-quick`, `TD_AMNEZIAWG_DIR`, obfuscation from `internal/wg/awg.go` (generation and validation follow the limits in the AmneziaWG docs). The image builds `amneziawg-go` and `amneziawg-tools` at pinned tags (Dockerfile ARGs).
- `internal/mgmt`: management interface client (`status 3`, kill, signal).
- `internal/ovpn`: builds `.ovpn` profiles (tls-crypt-v2 client keys in `keys-v2/`).
- `internal/web`, `internal/auth`, `internal/store`, `internal/history`: web side. Traffic is stored per client per hour; poll gaps are spread over the missing hours and marked estimated.
- `web/src`: `pages/*.svelte`, `lib/state.svelte.js` (route, session, settings, theme), `lib/api.js`.
- `docs/`: `guide.md` (how features work), `api.md`, `development.md`, `releases/`, `screenshots/`.

## Rules

- **Never put real server details in the repo** (hostnames, public IPs, paths, networks). Use `vpn.example.com`, `203.0.113.x`, `10.8.0.0/24` and similar. Screenshots use demo data only: admin `admin`, `TD_TZ=UTC`.
- Stay compatible with existing openvpn-ui directories: the PKI layout, `ccd` 2FA lines (`#2FA_KEY:` hex, `#STATIC_PASS:`) and the client profile format.
- Security posture: web has no privileges, agent has no network port in Docker, the management interface stays on 127.0.0.1, strict CSP (any inline script needs its hash in `internal/webui`).
- Every feature gets unit tests, plus a step in `test/e2e/run.sh` (real OpenVPN) or `test/docker/smoke.sh` when it touches OpenVPN, the firewall or Docker. Fakes must behave like the real thing: e.g. a SIGTERMed OpenVPN keeps answering on management for a few seconds.
- Go: stdlib first (net/http routing, log/slog). SQLite via `ncruces/go-sqlite3` (pure Go, CGO off). Go 1.24: check that a dependency's go.mod does not require a newer Go.
- UI: plain sentence-case copy written for users, light and dark themes, phone layout. Check with `npm run check` and look at screenshots (puppeteer with Chrome).

## Workflow

- Only `main` is long-lived. Work happens on a short-lived branch per step: push, open a PR, merge (squash) only when every check is green. The weekly Branch cleanup workflow deletes merged branches.
- Commits and PRs explain the why; keep README.md, docs/ and ROADMAP.md up to date in the same PR.
- CI: `ci.yml` (test, ui, compat, e2e, lint), `docker-publish.yml` (smoke on PRs; on `main` it publishes `ghcr.io/shuricksumy/trapdoor:latest` plus a short-sha tag for amd64 and arm64), `codeql.yml`, and cleanup workflows. Actions are pinned to commit SHAs.
- Releases: put the notes in `docs/releases/vX.Y.Z.md`, then Actions → Release → version `X.Y.Z` on `main`. It creates the tag, a GitHub release with Linux binaries, and the `X.Y.Z` / `X.Y` / `latest` images.
- The owner prefers one step at a time for anything he has to do on his own server.
- Next up (ROADMAP.md): first release v0.1.0, then v3 (servers as objects, WireGuard, AmneziaWG).
