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
| GET | `/v1/certs/{name}/ovpn` | – | `.ovpn` profile (409 unless the cert is valid) |
| POST | `/v1/crl` | – | 204; regenerates `crl.pem` |
| GET | `/v1/sessions` | – | connected clients + server traffic counters |
| DELETE | `/v1/sessions/{client_id}` | – | 204; disconnects one session |
| DELETE | `/v1/certs/{name}/sessions` | – | `{"killed":n}`; disconnects all sessions of a name |

Revoke and delete also disconnect the client right away (otherwise OpenVPN would only
notice at the next renegotiation, up to an hour later). Sessions need OpenVPN's management
interface: `management 127.0.0.1 2080` in `server.conf` (`TW_OPENVPN_MGMT`, optional
`TW_OPENVPN_MGMT_PASSWORD` for `pw-file` setups); 502 when it is unreachable.

Try it with a throw-away PKI:

```
export TW_AGENT_TOKEN=dev TW_PKI_DIR=/tmp/tw/pki TW_CRL_COPIES=-
./bin/tunnelward init-pki
./bin/tunnelward agent &
curl -H "X-Tunnelward-Token: dev" -d '{"name":"alice"}' localhost:9090/v1/certs
```

### Client profiles

`.ovpn` files are built like openvpn-ui did, so existing servers produce the same files:
`<TW_OPENVPN_DIR>/client-template.txt` + inline `<ca>`, `<cert>`, `<key>` + the
control-channel key matching `server.conf` (`tls-crypt`, `tls-auth` with `key-direction 1`,
or a per-client `tls-crypt-v2` key). tls-crypt-v2 client keys live in `keys-v2/<name>.key`;
existing ones are reused, new ones are created with `openvpn --genkey` (`TW_OPENVPN_BIN`).

## Web API

The web process records session history (polling the agent every `TW_POLL_INTERVAL`,
default 30s) into SQLite (`TW_DB_PATH`) and serves reports. Until admin login exists,
every `/api/v1/*` call needs `Authorization: Bearer <TW_WEB_API_TOKEN>`; without that
variable the API stays disabled (401).

| Method | Path | Result |
|---|---|---|
| GET | `/api/v1/status` | web version + agent reachability |
| GET | `/api/v1/sessions` | live sessions (from the agent) |
| GET | `/api/v1/stats` | traffic report (below) |
| GET | `/api/v1/stats.csv` | same, as CSV: period, name, upload, download, estimated |
| GET | `/api/v1/history/sessions` | recorded sessions, newest first (`limit`, default 100) |

Report parameters: `preset=today|this_month|last_month|last_30_days|this_year|last_year`
**or** `from=YYYY-MM-DD&to=YYYY-MM-DD` (inclusive; default this month), `group=day|month|year`,
`name=<user>` (omit for the whole server). Days/months/years follow `TW_TZ` (IANA name).
The report has totals, a gap-free series per period, and per-user totals with share,
session count and online time. Upload/download are from the user's point of view.

Traffic is stored per user per hour. If polls were missed (web down, agent unreachable) the
traffic of the gap is spread over its hours and flagged `estimated` instead of one spike.

## Tests

```
make test                # unit tests
sudo test/e2e/run.sh     # real OpenVPN server + client on localhost (needs openvpn)
```

CI also runs PKI compatibility tests against real easyrsa + openssl.

## Configuration

All settings come from environment variables (prefix `TW_`); see
[`internal/config/config.go`](internal/config/config.go) for the full list and defaults.

## License

MIT
