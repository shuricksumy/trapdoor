# tunnelward

Web management for an OpenVPN server — certificates, clients, 2FA, routing and live sessions.

> **Status:** early development. Not usable yet. See [ROADMAP.md](ROADMAP.md).

tunnelward is the successor to [openvpn-ui](https://github.com/shuricksumy/openvpn-ui),
rebuilt as a client–server application with a native Go PKI.

## Architecture

```
[ Browser ] → [ tunnelward web ] ──HTTP API──▶ [ tunnelward agent ] → openvpn / PKI / firewall
               users, DB, UI                     privileged, runs next to OpenVPN
```

- **agent** — the only privileged part. Owns the PKI (easyrsa-compatible layout on disk,
  implemented in Go — no easyrsa or shell scripts at runtime), OpenVPN config, process
  control, management interface and client auth.
- **web** — unprivileged. Admin users, database, REST API for the frontend, serves the UI.

Both run from the same binary:

```
tunnelward agent   # privileged side
tunnelward web     # web UI + API
tunnelward version
```

## Development

Requires Go 1.24+.

```
make build   # → bin/tunnelward
make test
make lint    # needs golangci-lint
```

Run locally (two terminals):

```
TW_AGENT_TOKEN=dev ./bin/tunnelward agent
TW_AGENT_TOKEN=dev ./bin/tunnelward web
curl localhost:8080/api/v1/status
```

## Agent API

All `/v1/*` requests need the header `X-Tunnelward-Token: <TW_AGENT_TOKEN>`.
Errors are JSON `{"error": "..."}` with 400 / 401 / 404 / 409 / 503.

| Method | Path | Body | Result |
|---|---|---|---|
| GET | `/healthz` | – | `{"status":"ok"}` (no token) |
| GET | `/v1/info` | – | version, dirs, `pki_ready` |
| GET | `/v1/certs` | – | all entries (history included; `current` marks the newest per name) |
| POST | `/v1/certs` | `{"name","kind"?,"passphrase"?,"validity_days"?}` | 201 cert |
| GET | `/v1/certs/{name}` | – | current cert |
| POST | `/v1/certs/{name}/revoke` | – | cert |
| POST | `/v1/certs/{name}/unrevoke` | – | cert |
| POST | `/v1/certs/{name}/renew` | `{"validity_days"?}` | new cert (same key; old one revoked as superseded) |
| DELETE | `/v1/certs/{name}` | – | 204; revokes and deletes key material, stays in CRL |
| POST | `/v1/crl` | – | 204; regenerates `crl.pem` |

Try it with a throw-away PKI:

```
export TW_AGENT_TOKEN=dev TW_PKI_DIR=/tmp/tw/pki TW_CRL_COPIES=-
./bin/tunnelward init-pki
./bin/tunnelward agent &
curl -H "X-Tunnelward-Token: dev" -d '{"name":"alice"}' localhost:9090/v1/certs
```

## Configuration

All settings come from environment variables (prefix `TW_`); see
[`internal/config/config.go`](internal/config/config.go) for the full list and defaults.

## License

MIT
