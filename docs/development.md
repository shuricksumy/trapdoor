# Development

Requires Go 1.24+ and, for the UI, Node 22.

```
make ui      # web/ → internal/webui/dist (embedded by the next build)
make build   # → bin/tunnelward (without `make ui` the UI shows a "not built" notice)
make test
make lint    # needs golangci-lint
```

UI development with hot reload: run `tunnelward web` on :8080, then `cd web && npm run dev`
(Vite proxies `/api` to it).

Run locally (two terminals), with a throw-away directory:

```
export TW_AGENT_TOKEN=dev TW_OPENVPN_DIR=/tmp/tw TW_CRL_COPIES=- TW_FIREWALL=off
./bin/tunnelward agent
./bin/tunnelward web       # second terminal; then: tunnelward user add admin
```

## Layout

| Path | |
|---|---|
| `cmd/tunnelward` | the single binary: `agent`, `web`, `user`, `init-pki`, `auth-verify`, `health`, `version` |
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
| Build and publish Docker image | push to main, PR | smoke test of the compose setup; on main pushes `ghcr.io/shuricksumy/tunnelward:latest` + a short-sha tag (amd64, arm64) |
| Release | manual | enter a version: tests, tag `vX.Y.Z`, GitHub release with Linux binaries (amd64, arm64, armv7) and checksums, images `X.Y.Z`, `X.Y`, `latest` |
| CodeQL | push, PR, weekly | code scanning (Go, JavaScript) |
| GHCR package cleanup | weekly | deletes old short-sha image tags; keeps `latest` and releases |
| Branch cleanup | weekly | deletes merged branches |
| Dependabot auto-merge | Dependabot PRs | merges patch updates once checks pass |

### Making a release

Actions → Release → Run workflow on `main` → version, e.g. `1.0.0`. Release notes come from
`docs/releases/v1.0.0.md` if it exists, otherwise GitHub writes them from the merged PRs.
