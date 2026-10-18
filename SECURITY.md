# Securing Aegis

Aegis holds OAuth tokens for music services and exposes user data (playlists,
library, search) to LLM agents. This document describes the recommended access
model when Aegis runs in a [Tailscale](https://tailscale.com) tailnet — the
same identity layer [Aperture](https://tailscale.com/docs/aperture) uses.

## Trust model

**By default, treat the tailnet as the access boundary.** Anything that can
reach Aegis was admitted to the tailnet by you (or another admin) via SSO,
device approval, and ACLs. Aegis does not need a second shared secret for
every agent on that tailnet.

Tailscale proves *who* is connecting:

- Each connection is bound to a node key; identity comes from the control plane.
- Applications can resolve the peer with `WhoIs` (LocalAPI) or Tailscale Serve
  identity headers — not from a header the client invented on a public socket.
- Aperture itself authenticates this way: login name + device tags, no
  distributed LLM API keys for tailnet clients.

Aegis extends that idea: **if the peer has a Tailscale identity, allow the
API** (optionally restricted to an allowlist). A shared API key remains available
for external bridges and debugging.

## Auth modes

| Mode | Behavior | When to use |
|------|----------|-------------|
| `auto` (default) | Prefer Tailscale identity; fall back to API key | Most deployments: tailnet-first, key for bridges |
| `tailnet` | Require Tailscale identity; ignore API key | Aegis only reachable inside the tailnet |
| `api-key` | Require `Authorization: Bearer <key>` or `X-Api-Key` | Legacy, tests, or non-Tailscale hosts |

Flags / env:

```bash
-auth-mode auto|tailnet|api-key     # or AEGIS_AUTH_MODE
-allowed-logins alice@example.com   # or AEGIS_ALLOWED_LOGINS
-ts-socket /var/run/tailscale/tailscaled.sock  # WhoIs LocalAPI (optional)
-api-key ...                        # optional unless auth-mode=api-key
```

Empty `-allowed-logins` means *any* authenticated tailnet identity is allowed
(the “everything I expose is for my tailnet” default). Set it on shared tailnets
so a guest laptop or tagged CI runner cannot read your Spotify library.

## How identity is resolved

Aegis tries, in order:

1. **Tailscale Serve headers** (`Tailscale-User-Login`, `Tailscale-Node-Tags`, …)  
   Trustworthy only when clients cannot reach Aegis except through
   `tailscale serve` (or an equivalent proxy that strips inbound identity
   headers and sets its own).

2. **WhoIs via tailscaled LocalAPI** (`/var/run/tailscale/tailscaled.sock`)  
   Works when Aegis shares a host with `tailscaled`. Queries the peer’s
   `RemoteAddr` and returns login name and tags from the control plane.

Tagged devices without a user login get a synthetic identity (tags joined with
commas), matching Aperture’s model — e.g. `tag:aperture,tag:ci`.

## Recommended tailnet policy

Tag the Aegis host and deny-by-default who can reach it:

```jsonc
// tailnet policy file (HuJSON)
{
  "tagOwners": {
    "tag:aegis": ["you@example.com"],
  },
  "grants": [
    {
      // Only you (or group:family, etc.) can connect to Aegis.
      "src": ["you@example.com"],
      "dst": ["tag:aegis"],
      "ip": ["tcp:443", "tcp:8080"],
    },
  ],
  "ssh": [] // not required; avoid granting SSH unless you need it
}
```

Then:

```bash
# On the Aegis host
sudo tailscale up --advertise-tags=tag:aegis --ssh=false

# Front the binary with Serve (recommended)
tailscale serve --bg --set-path / http://127.0.0.1:8080
```

Or bind Aegis only to the Tailscale IP / localhost so it is not reachable
from other LAN interfaces.

## Best practices

1. **Do not use Tailscale Funnel** for Aegis. Funnel publishes the service
   to the public internet; the API is designed for a private tailnet and holds
   music OAuth tokens.
2. **Prefer `auth-mode=tailnet`** once Serve or WhoIs is working; drop the
   shared key so it cannot leak into agent transcripts or shell history.
3. **Keep an API key** only if you need `ts-unplug` / CLI bridges or tools that
   are not on the tailnet — and treat that key like a password.
4. **Allowlist logins** on any tailnet that is not solo.
5. **Tag the service** (`tag:aegis`) so ACLs survive user device churn.
6. **Device approval** in the admin console prevents unapproved machines from
   joining even with a stolen auth key.
7. **Log identity.** Aegis logs `login` and `identity_source` on denied
   requests and attaches tailnet identity to library/proxy logs. Pre-request
   also warns when Aperture’s `login_name` does not match the Tailscale peer.
8. **Encrypt tokens at rest** (`-token-pass` / `AEGIS_TOKEN_PASS`) as today —
   tailnet auth does not replace disk encryption of `tokens.json` (under the
   [data directory](README.md#data-directory-issue-74), mode 0700/0600).
   PBKDF2 parameters are recorded in the file; rotate the passphrase with
   `-token-new-pass` (see [Token encryption](README.md#token-encryption-issue-101)).
9. **Keep OAuth client secrets out of `aegis.json`** with
   `"$ENV:NAME"` / `"${NAME}"` indirection (issue #102) so bootstrap config can
   be shared without leaking mintable credentials. Literal secrets still work
   when needed.
10. **OAuth login/callback stay public** on purpose (browser redirects from
    Spotify/Google/Amazon). Status and revoke remain behind auth middleware.
11. **Keep `/metrics` authenticated.** `/healthz` and `/readyz` are
    unauthenticated liveness/readiness probes with no inventory. `/metrics`
    is auth-scoped like admin because series include service ids and denial
    reasons (issue #105). Point Prometheus at it with an API key or from a
    tailnet identity — do not expose it through Funnel.
12. **Audit log is local state.** `<data-dir>/audit_log.db` records who
    accessed which data type and whether the call was allowed or denied
    (redacted — no tokens or bodies). Treat the file like `tokens.json`:
    mode 0700 directory, not world-readable, included in the same backup
    you use for other Aegis state. Default retention is 30 days
    (`-audit-retention-days`).
13. **User-defined type file access is admin-equivalent.**
    `GET /v1/admin/udt/browse` lists directories on the Aegis host and the
    source/extra endpoints accept absolute local paths. Whatever file is
    selected can then be read through `/v1/library/{udt}` by anyone who
    passes `AuthMiddleware` (tailnet identity or API key) — the same trust
    boundary as admin. Do not share an Aegis API key with untrusted agents.
    Prefer `auth-mode=tailnet` and `allowed-logins` on multi-user tailnets.
    Schema packs must not embed secrets in `http_json` headers (rejected at
    load); use named connections / the encrypted token store.

## What “authorized” means for data

After authentication, Aegis is still **single-user** with respect to stored
OAuth tokens: every authorized caller uses the same Spotify/YouTube/Amazon
tokens on disk. Tailnet identity controls *who can call the API*; it does not
yet isolate *whose* music library is returned.

If you need per-user libraries later, store tokens keyed by Tailscale login and
select the store from `PeerIdentityFrom(r)`. Until then, keep the allowlist
tight on shared tailnets.

### Data-type permissions

Authentication answers “who may call Aegis.” Per-service `permissions` in
`aegis.json` answer “what may an authorized agent do with each data type”
(for example deny `player` or limit `playlists` to `read`). Denials are enforced
on the content/ingest proxy and library endpoints with 403. Agent discovery is
via pre-request instructions and connector `SystemInstructions` (issue #119) —
not a capabilities listing API. Omitting `permissions` preserves unrestricted
access for authorized callers.

### Third-party app tokens (issue #131)

Non-agent clients use a **separate app token** (`aeg_app_…`) registered in the
admin API. App tokens:

- Are scope-limited to library **read** endpoints (not admin, proxy, OAuth, or mail).
- Use the same access levels as service permissions, including `freebusy` for events.
- Never grant more than service permissions allow (AND of both layers).
- Are stored as SHA-256 hashes in `apps.json` (0600); plaintext is shown once.

An app token is still only useful if the third party can **reach** Aegis. Keep
Aegis on the tailnet (or a path you control). Do not use Funnel to publish the
API for third-party apps. Prefer `freebusy` over full `events` for scheduling
clients.


## Deployment checklist

- [ ] Aegis host tagged (`tag:aegis`) and ACL/grant limited to intended users
- [ ] Not exposed with Funnel
- [ ] `tailscale serve` in front **or** Aegis bound to localhost/Tailscale IP
- [ ] `auth-mode=tailnet` (or `auto` while migrating)
- [ ] `allowed-logins` set if the tailnet is shared
- [ ] `AEGIS_TOKEN_PASS` set and `tokens.json` (under the data directory) not world-readable
- [ ] API key removed or stored outside agent-accessible paths
- [ ] `/metrics` scrape credentials stored like any other API key; `/healthz` stays unauthenticated
- [ ] `audit_log.db` retained/backed up with the data directory (default 30 days)

## References

- [Tailscale identity](https://tailscale.com/docs/concepts/tailscale-identity)
- [How Aperture works](https://tailscale.com/docs/aperture/how-aperture-works) (WhoIs-based auth)
- [Access controls / grants](https://tailscale.com/kb/1324/acls)
- [Tags](https://tailscale.com/kb/1068/tags)
- [Tailscale Serve](https://tailscale.com/kb/1312/serve) (identity headers)
- [tsnet](https://github.com/tailscale/tailscale/tree/main/tsnet) (embed Tailscale in Go; optional alternative to system `tailscaled`)
