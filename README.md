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

## Configuration

All settings come from environment variables (prefix `TW_`); see
[`internal/config/config.go`](internal/config/config.go) for the full list and defaults.

## License

MIT
