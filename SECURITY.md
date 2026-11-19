# Security

trapdoor manages VPN servers, their certificate authorities and client keys, so security
reports are very welcome.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private reporting instead:
[Report a vulnerability](https://github.com/shuricksumy/trapdoor/security/advisories/new).

Include what you found, how to reproduce it and which version (`trapdoor version`, or the
image tag) you used. You will get an answer within a few days; a fix is released as soon as
it is ready, and you are credited unless you prefer not to be.

## Supported versions

The latest release. Fixes are not backported to older ones.

## How trapdoor is built to be safe

- The web UI runs without privileges (read-only filesystem, no capabilities); only the
  agent and the vpn container touch VPN files and the network.
- In Docker the agent has no network port; between machines it uses TLS 1.3 with a
  certificate pinned by fingerprint, plus a token.
- Strict Content-Security-Policy, login throttling, sessions that expire, and a token for
  scripts.
- Backups can be encrypted (AES-GCM, PBKDF2), restore refuses unsafe archives.
- Own start / stop scripts (run as root in the vpn container) can be edited in the web UI
  only when the node's agent allows it (`TD_SCRIPTS=on`).
