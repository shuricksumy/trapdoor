# Development

Requires Go 1.24+ and, for the UI, Node 22.

```
make ui      # web/ → internal/webui/dist (embedded by the next build); runs svelte-check and npm test
make build   # → bin/trapdoor (without `make ui` the UI shows a "not built" notice)
make test
make lint    # needs golangci-lint
```

UI development with hot reload: run `trapdoor web` on :8080, then `cd web && npm run dev`
(Vite proxies `/api` to it).

Run locally (two terminals), with a throw-away directory:

```
export TD_AGENT_TOKEN=dev TD_OPENVPN_DIR=/tmp/td TD_CRL_COPIES=- TD_FIREWALL=off
./bin/trapdoor agent
./bin/trapdoor web       # second terminal; then: trapdoor user add admin
```

## Layout

| Path | |
|---|---|
| `cmd/trapdoor` | the single binary: `agent`, `web`, `user`, `init-pki`, `auth-verify`, `health`, `version` |
| `internal/agent` | privileged HTTP API |
| `internal/web`, `internal/auth`, `internal/store`, `internal/history` | web process, admin login, SQLite, traffic history |
| `internal/pki` | certificate authority in Go on an easyrsa 3 compatible directory |
| `internal/serverconf`, `internal/setup`, `internal/ccdnet`, `internal/clientauth`, `internal/firewall` | server.conf editing, setup, client network, 2FA, access rules |
| `internal/mgmt`, `internal/ovpn` | OpenVPN management interface, `.ovpn` profiles |
| `web/` | Svelte 5 UI |
| `docker/`, `Dockerfile`, `docker-compose*.yml` | image and compose files |

## Tests

```
make test                # unit tests
sudo test/e2e/run.sh     # real OpenVPN server and clients on this machine (needs openvpn, iproute2)
test/docker/smoke.sh <image>   # the compose setup with a real VPN client
```

## GitHub Actions

| Workflow | When | What |
|---|---|---|
| CI | push, PR | Go tests, UI type-check, PKI compatibility with real easyrsa + openssl, end-to-end test with real OpenVPN, lint |
| Build and publish Docker image | push to main, PR | smoke test of the compose setup; on main pushes `ghcr.io/shuricksumy/trapdoor:latest` + a short-sha tag (amd64, arm64) |
| Release | manual | enter a version: tests, tag `vX.Y.Z`, GitHub release with Linux binaries (amd64, arm64, armv7) and checksums, images `X.Y.Z`, `X.Y`, `latest` |
| CodeQL | push, PR, weekly | code scanning (Go, JavaScript) |
| GHCR package cleanup | weekly | deletes old short-sha image tags; keeps `latest` and releases |
| Branch cleanup | weekly | deletes merged branches |
| Dependabot auto-merge | Dependabot PRs | merges patch updates once checks pass |

### Making a release

Actions → Release → Run workflow on `main` → version, e.g. `1.0.0`. Release notes come from
`docs/releases/v1.0.0.md` if it exists, otherwise GitHub writes them from the merged PRs.

## Keeping the VPN software current

- **OpenVPN, wireguard-tools, iptables and the Alpine base packages** come from Alpine
  and have no pin. The *Rebuild for new OpenVPN / Alpine packages* workflow runs daily: it
  asks the published `:latest` image whether Alpine has newer packages and, if so, starts
  *Build and publish Docker image* without cache. The smoke test (real OpenVPN, WireGuard
  and AmneziaWG clients) runs before the new `:latest` is pushed. The run summary shows
  the versions in the image and the latest OpenVPN release upstream.
- **AmneziaWG** (`amneziawg-go`, `amneziawg-tools`) is pinned in the Dockerfile
  (`AMNEZIAWG_*_VERSION`, marked `# renovate:`). Renovate opens a PR for each new release
  and merges it when CI is green; the push to `main` publishes `:latest`.
- **A new Alpine release** (e.g. 3.24 → 3.25, usually with a newer OpenVPN) is the
  `FROM alpine:` tag. Renovate opens a PR labelled `alpine` and merges it when every
  check is green, the smoke test included; the push to `main` publishes `:latest`. When
  the smoke test fails (e.g. a new OpenVPN changes a default), the PR stays open for a
  fix, as for OpenVPN 2.7 in #32.
- Renovate merges these itself after it sees all checks green (`platformAutomerge`
  off): `main` has no required checks that GitHub's auto-merge could wait for.
- Released images (`X.Y.Z`, `X.Y`) are not rebuilt; a new release picks everything up.
