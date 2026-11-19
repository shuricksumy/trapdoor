# Contributing

Thanks for helping. Bug reports, ideas and pull requests are all welcome.

## Reporting a bug or an idea

Open an [issue](https://github.com/shuricksumy/trapdoor/issues/new/choose). For a bug: what
you did, what you expected, what happened, the version (`trapdoor version` or the image tag)
and the relevant log lines (`docker compose logs web agent vpn`). Remove real addresses,
hostnames and keys first. Security problems: see [SECURITY.md](SECURITY.md).

## Working on the code

```bash
make ui            # the Svelte UI (web/) into the Go binary
make build         # bin/trapdoor
make test          # go test ./...
make lint          # golangci-lint
cd web && npm run check
```

[docs/development.md](docs/development.md) explains the layout, how to run it locally and the
end-to-end tests (`test/e2e/run.sh` with a real OpenVPN, `test/docker/smoke.sh` with Docker).

## Pull requests

- One topic per pull request, on a branch; describe *why* in the commit message.
- Every feature gets unit tests, and a step in the e2e or smoke test when it touches
  OpenVPN, WireGuard, the firewall or Docker.
- Keep README.md, docs/ and ROADMAP.md up to date in the same pull request.
- UI text: plain sentence case, written for users; check light and dark themes and a
  phone-sized screen.
- **Never commit real server details**: use `vpn.example.com`, `203.0.113.x`,
  `10.8.0.0/24` and similar, and demo data in screenshots.
- Go: standard library first; check that a new dependency does not need a newer Go than
  go.mod.

All checks (tests, lint, e2e, Docker smoke test, CodeQL) must pass before a merge.
