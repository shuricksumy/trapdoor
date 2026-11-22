# Sign in with Authelia (OIDC)

trapdoor can sign people in with Authelia, or any other OpenID Connect provider, next to the
admin password. Everyone in an allowed group gets **an account of their own** at their first
sign-in, so the log says who did what; their password, reset and two factors stay Authelia's
business. Members of `TD_OIDC_GROUPS` sign in as **admins**, members of
`TD_OIDC_VIEWER_GROUPS` **read-only** (they see everything, change nothing and download no keys).
The admin password stays as the way in when Authelia is down, and can be limited to your own
networks.

Examples use `vpn-admin.example.com` for trapdoor and `auth.example.com` for Authelia.

## 1. Register trapdoor in Authelia

Make a client secret. Authelia keeps only its hash; trapdoor needs the plain secret:

```bash
docker exec authelia authelia crypto hash generate pbkdf2 --variant sha512 \
  --random --random.length 64 --random.charset alphanumeric
```

It prints the *Random Password* (for trapdoor) and the *Digest* (for Authelia). Add the client
under `identity_providers.oidc.clients`, like your other apps; policy and two factors are
Authelia's as usual:

```yaml
      - client_id: 'trapdoor'
        client_name: 'trapdoor'
        client_secret: '$pbkdf2-sha512$310000$…'     # the Digest
        public: false
        authorization_policy: 'two_factor'
        redirect_uris:
          - 'https://vpn-admin.example.com/api/v1/auth/oidc/callback'
        scopes: ['openid', 'profile', 'email', 'groups']
        grant_types: ['authorization_code']
        response_types: ['code']
        response_modes: ['query']
        require_pkce: true
        pkce_challenge_method: 'S256'
        token_endpoint_auth_method: 'client_secret_basic'
        userinfo_signed_response_alg: 'none'
        consent_mode: 'implicit'                      # or 'explicit' to confirm each time
```

- `response_modes` must be `query`. With `form_post` Authelia answers with a cross-site POST, and
  the browser does not bring trapdoor's one-time cookie along, so the sign-in fails.
- The redirect URI must match exactly: `https://` + trapdoor's address +
  `/api/v1/auth/oidc/callback`, nothing after it.
- Put your admin account into the group trapdoor allows (`admin` by default) in Authelia's users.

Restart Authelia (or let it reload the file) and check its log for errors.

## 2. Set up trapdoor

For the **web** service (`.env` next to `docker-compose.yml`; the compose files pass these on):

```bash
TD_PUBLIC_URL=https://vpn-admin.example.com      # trapdoor's address; the callback is built from it
TD_OIDC_ISSUER=https://auth.example.com          # Authelia's address
TD_OIDC_CLIENT_ID=trapdoor
TD_OIDC_CLIENT_SECRET=…                          # the Random Password from step 1
TD_OIDC_GROUPS=admin                             # who may sign in, comma-separated
```

Then `docker compose up -d web`. The log says `sign-in with OIDC issuer=… groups=[admin]`, and
the login page shows **Sign in with Authelia** with *Use the admin password* under it.

| Setting | |
|---|---|
| `TD_OIDC_ISSUER` | the provider's address (empty: sign-in with it is off) |
| `TD_OIDC_CLIENT_ID`, `TD_OIDC_CLIENT_SECRET` | as registered; or `TD_OIDC_CLIENT_SECRET_FILE` |
| `TD_OIDC_GROUPS` | groups that sign in as admins (default `admin`) |
| `TD_OIDC_VIEWER_GROUPS` | groups that sign in read-only (default: none) |
| `TD_OIDC_NAME` | the button: *Sign in with …* (default `Authelia`) |
| `TD_OIDC_USER` | sign everyone in as this local account instead of their own (how it worked before 0.9) |
| `TD_OIDC_SCOPES` | default `openid profile email groups` |
| `TD_OIDC_AUTH_METHOD` | `client_secret_basic` (default) or `client_secret_post` |
| `TD_OIDC_CA_FILE` | a PEM certificate to trust for the provider besides the system's (below) |
| `TD_PASSWORD_LOGIN_FROM` | networks the admin password works from (below) |

## 3. Authelia behind Cloudflare on the same machine

The browser reaches Authelia through Cloudflare, and so does trapdoor's own call to it (the
token exchange) unless you route it otherwise. Cloudflare's bot protection often answers a
server's request, especially from a cloud provider's address, with a challenge page that a
server cannot solve. trapdoor's log then says:

```
sign-in failed err="token endpoint: 403 Forbidden (server=cloudflare, cf-mitigated=challenge, cf-ray=…)"
```

Let trapdoor reach Authelia through your reverse proxy directly, and trust the proxy's
certificate (with Cloudflare in front, usually a *Cloudflare Origin* certificate, which only
Cloudflare trusts):

```yaml
  web:
    extra_hosts:
      - "auth.example.com:172.20.0.10"                    # the reverse proxy's internal address
    environment:
      TD_OIDC_CA_FILE: /etc/trapdoor/proxy.crt
    volumes:
      - ./proxy/cert.crt:/etc/trapdoor/proxy.crt:ro       # the certificate only, never its key
```

The browser keeps going through Cloudflare; only trapdoor's own calls stay inside the machine.

If CrowdSec (or fail2ban) watches Authelia, keep the server's own address out of its bans:
failed attempts while you set this up look like password guessing.

## 4. The admin password only from your networks

With Authelia as the way in from outside, the password does not need to work from everywhere:

```bash
TD_PASSWORD_LOGIN_FROM=10.8.0.0/24,192.168.1.0/24
```

From other addresses the password is refused and the login page shows only *Sign in with
Authelia*. The address is the client's as trapdoor sees it after `TD_TRUSTED_PROXIES`; behind
Cloudflare that is a Cloudflare address, so the password is refused from the internet. A VPN
client gets the password only when it reaches trapdoor directly (e.g. its DNS points trapdoor's
name at the proxy's internal address). Sign-in with Authelia, the API token and the
`trapdoor user` commands are not affected.

## When it does not work

The login page shows a short reason; trapdoor's log has the details (`sign-in failed` /
`sign-in refused`).

| What you see | Why | What to do |
|---|---|---|
| *The sign-in provider cannot be reached* | trapdoor cannot load `<issuer>/.well-known/openid-configuration` | check `TD_OIDC_ISSUER`, DNS and the certificate (`TD_OIDC_CA_FILE`) from the web container |
| Authelia: *redirect URI mismatch* | the client's `redirect_uris` differ from `<TD_PUBLIC_URL>/api/v1/auth/oidc/callback` | fix the URI (no quotes or slash at the end) |
| *The sign-in could not be checked*, log `server=cloudflare, cf-mitigated=challenge` | Cloudflare challenges trapdoor's server request | section 3 |
| log `invalid_client` | wrong client id or secret | the plain secret in trapdoor, the hash in Authelia |
| *… is not in a group allowed to sign in here* | the user is not in `TD_OIDC_GROUPS` | add the group in Authelia, or the group to `TD_OIDC_GROUPS` |
| *The sign-in expired or came from another browser* | more than 10 minutes, or the callback without trapdoor's cookie | try again; check `response_modes: ['query']` |
| log *answers with a signed JWT* | userinfo is signed | `userinfo_signed_response_alg: 'none'` |
| *The account … is disabled here* | an admin turned the account off | turn it on again (Admins page, or `trapdoor user`) |

## How it is checked

The authorization code flow with PKCE (S256), a one-time state bound to the browser (a
`SameSite=Lax` cookie for the callback path only) and a nonce. The ID token's signature is checked
against the provider's published keys (RS256 / ES256 and their 384 / 512 variants), and so are its
issuer, audience and lifetime. Then userinfo is read; Authelia puts the groups there unless the
client's claims policy adds them to the ID token. The account is found again by the
provider's subject (never by name: a local account with the same name is not taken over, the
new one gets a suffix), and its role follows the person's groups at every sign-in. The log
line `login username=… role=… via=oidc oidc_user=… oidc_email=…` says who signed in.
