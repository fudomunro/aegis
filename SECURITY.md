# Securing Aegis

Aegis holds OAuth tokens for music services and exposes user data (playlists,
library, search) to LLM agents. This document describes the recommended access
model for both deployment shapes: on a [Tailscale](https://tailscale.com)
tailnet (the default) and externally reachable ("external access", see
[`docs/external-access.md`](docs/external-access.md)). Product vocabulary
(data grant, access levels, LLM agent) is in
[`docs/glossary.md`](docs/glossary.md). The structured asset/threat/residual-risk
model for auditors is in [`docs/threat-model.md`](docs/threat-model.md).

## Trust model

Aegis serves **two listeners with different perimeters**:

| Listener | Perimeter | Exposure |
|----------|-----------|----------|
| **API listener** (`/v1/library/*`, pre-request, content transport, mail agent routes, app-token endpoints) | **The credential** when external access is enabled; the tailnet when it is not | May be public behind TLS |
| **Admin listener** (`/v1/admin/*`, `/metrics`, OAuth flows, app consent, mirror/link writes, mail sync) | **The tailnet** — Tailscale identity or the admin API key | In-tailnet / loopback only. Never public |

### Tailnet perimeter (admin listener, tailnet-only API)

**By default, treat the tailnet as the access boundary.** Anything that can
reach a tailnet-only listener was admitted to the tailnet by you (or another
admin) via SSO, device approval, and ACLs. Aegis does not need a second
shared secret for every agent on that tailnet.

Tailscale proves *who* is connecting:

- Each connection is bound to a node key; identity comes from the control plane.
- Applications can resolve the peer with `WhoIs` (LocalAPI) or Tailscale Serve
  identity headers — not from a header the client invented on a public socket.
- Aperture itself authenticates this way: login name + device tags, no
  distributed LLM API keys for tailnet clients.

Aegis extends that idea: **if the peer has a Tailscale identity, allow the
API** (optionally restricted to an allowlist). A shared agent API key remains
available for external bridges and debugging; admin surfaces use a separate
credential (issue #181) so agents cannot rewrite the policy that constrains
them.

### Credential perimeter (external access)

When the API listener is reachable outside the tailnet, reachability proves
nothing — the socket is hostile. **The credential is the perimeter:**

- Every request must present a token: an **invite token** (data-scope key),
  an app token, or the agent API key. Nothing is ambient.
- **Ambient Tailscale identity is never trusted on a publicly reachable
  listener.** Serve identity headers are just headers; anyone who can open a
  TCP connection can forge `Tailscale-User-Login` and the default admin rule
  would hand them the store. Identity resolution (Serve headers, WhoIs) runs
  only on listeners that are not externally reachable.
- **External principals are deny-by-default.** An invite token sees exactly
  the kinds its data scope grants (see
  [`docs/data-scopes.md`](docs/data-scopes.md) and "What authorized means"
  below) — sharing is opt-in per kind.
- **Admin stays in-tailnet.** The admin listener is never published; policy
  is changed from inside the tailnet only (issue #149 decision).
- TLS termination, rate limiting, and abuse damping are the deployment's
  duty once the listener is public (see `docs/external-access.md`
  "Transport and hardening duties").

An external principal may be a person, a third-party backend, or another
Aegis instance's operator — the credential model is identical. There is no
federation protocol; "other Aegis" is just a caller holding an invite token.

## Auth modes

Auth modes govern the **tailnet perimeter**. External access adds token
credentials that work in any mode (see `docs/external-access.md`).

| Mode | Behavior | When to use |
|------|----------|-------------|
| `auto` (default) | Prefer Tailscale identity; fall back to API key | Most deployments: tailnet-first, key for bridges |
| `tailnet` | Require Tailscale identity; ignore API key | Aegis only reachable inside the tailnet |
| `api-key` | Require `Authorization: Bearer <key>` or `X-Api-Key` | Legacy, tests, or non-Tailscale hosts |

Token credentials accepted alongside these modes: the **invite token**
(data-scope key; external principal, deny-by-default) and the **app token**
(`aeg_app_…`, scoped library reads) on both listeners; the shared **agent
API key** (owner-tier) on the **admin listener only** when external access
is enabled — the published bind never accepts it. In-tailnet agents
(Aperture, tagged hosts) keep ambient identity on the admin listener
unchanged; they need no key when external access turns on.

Flags / env:

```bash
-auth-mode auto|tailnet|api-key     # or AEGIS_AUTH_MODE
-allowed-logins alice@example.com   # or AEGIS_ALLOWED_LOGINS
-admin-logins alice@example.com     # or AEGIS_ADMIN_LOGINS (admin surfaces)
-ts-socket /var/run/tailscale/tailscaled.sock  # WhoIs LocalAPI (optional)
-api-key ...                        # agent key; optional unless auth-mode=api-key
-admin-api-key ...                  # admin key; optional when admin logins cover operators
```

Empty `-allowed-logins` means *any* authenticated tailnet identity is allowed
(the “everything I expose is for my tailnet” default). Set it on shared tailnets
so a guest laptop or tagged CI runner cannot read your Spotify library.

## Admin vs agent credentials (issue #181)

Authentication answers *who may call Aegis*. A second tier answers *who may
change policy*. The two tiers live on **separate listeners**: agent/external
traffic on the API listener, policy on the admin listener (in-tailnet).

| Principal | Credentials | May call |
|-----------|-------------|----------|
| **Agent** | Agent API key (`-api-key`), tagged devices, third-party app tokens, allowed tailnet logins, invite tokens | Library reads, pre-request hook, content transport (`/v1/services/*`), mail search/status, `GET /v1/auth/status` |
| **External** | Invite token (data-scope key) | Same agent surface, narrowed to the scope's **granted** kinds and dataset; never admin |
| **Admin** | Admin API key (`-admin-api-key`) or a tailnet **user** login that passes admin rules, on the admin listener | Everything agents can, plus `/v1/admin/*`, data-grant writes, app minting (`POST /v1/apps/authorize`), mirror / photo-share / entity-link writes, mail sync, OAuth revoke, `/metrics` |

**Default on a solo tailnet:** admin = allowed user logins (your browser
session through Tailscale Serve); agents = tagged nodes and the API key. Tagged
identities (`tag:aperture`, `tag:ci`, …) are **not** admin unless listed in
`-admin-logins`.

**The shared `-api-key` is never admin.** Data grants only mean something if
the party they constrain cannot rewrite them. A prompt-injected agent that
holds the agent key gets **403** on every admin write. If you need key-based
admin (curl, scripts), set `-admin-api-key` to a *different* secret.

`-admin-logins`, when set, is the sole tailnet allowlist for admin surfaces.
When empty, any identity that passes `-allowed-logins` and is a **user login**
(not a tagged device) is admin.

App tokens (`aeg_app_…`) are never admin, even when scopes look broad.

## CSRF / origin protection on writes (issue #182)

Tailnet identity is **ambient**: Tailscale Serve attaches identity headers on
every request from your device, which behaves like a cookie. A hostile page in
the same browser could otherwise auto-submit a write.

On every non-GET/HEAD route behind `AuthMiddleware` / `AdminMiddleware`:

1. **Same-origin required for ambient identity.** The request must carry
   `Origin` matching `-base-url`, or `Sec-Fetch-Site: same-origin`. Missing or
   cross-site `Origin` → **403**.
2. **API key and app token writes skip the Origin check.** Browsers do not
   attach those credentials automatically, so they are not CSRF-ambient.
   Non-browser agents and curl should use API keys for writes.
3. **JSON write surfaces require `Content-Type: application/json`.** Cross-site
   `text/plain` and form POSTs are CORS “simple requests” (no preflight) and
   are rejected. Content transport (`/v1/services/*`) keeps free-form media
   types. Bodyless POSTs (cache clear, rotate) may omit Content-Type.
4. **Consent form CSRF token.** `GET /v1/apps/authorize` renders a single-use
   token; form-encoded `POST /v1/apps/authorize` must echo it. JSON grant
   POSTs rely on Origin + Content-Type.

The admin SPA and consent page send same-origin `fetch` with
`Content-Type: application/json`, so they are unaffected.

## How identity is resolved

**Identity resolution runs only on listeners that are not externally
reachable.** On a public socket these headers are forgeable by any client, so
the API listener under external access resolves no ambient identity at all —
explicit tokens only. This is a property of listener exposure, not of the
auth mode name.

On the admin listener (and a tailnet-only API listener), Aegis tries, in
order:

1. **Tailscale Serve headers** (`Tailscale-User-Login`, `Tailscale-Node-Tags`, …)  
   Trusted only when the request arrives **from this host** (loopback or an
   address on one of the machine's own interfaces) — the hop `tailscale
   serve` or a same-host reverse proxy makes. A peer that reaches the socket
   directly (LAN or tailnet) can write those headers itself, so from a
   non-local `RemoteAddr` they are ignored and resolution falls through to
   WhoIs. `RemoteAddr` comes from the accepted TCP connection and cannot be
   forged. An equivalent remote proxy is not a supported trust path — use
   WhoIs or API keys instead.

2. **WhoIs via tailscaled LocalAPI** (`/var/run/tailscale/tailscaled.sock`)  
   Works when Aegis shares a host with `tailscaled`. Queries the peer’s
   `RemoteAddr` and returns login name and tags from the control plane.

Tagged devices without a user login get a synthetic identity (tags joined with
commas), matching Aperture’s model — e.g. `tag:aperture,tag:ci`.

**Bind address is validated at startup.** With Serve identity trust
(`auto`/`tailnet` auth mode), Aegis refuses to start on a wildcard
(`:8080`, `0.0.0.0`) or LAN bind — those make the port reachable by peers who
could try forged headers and expand the attack surface for no reason. `-addr`
defaults to `127.0.0.1:8080`. Sound binds: loopback (front with `tailscale
serve`), the Tailscale IP, or `-auth-mode api-key` (no ambient identity to
forge). The request-level local-peer gate above holds regardless.

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

1. **Publish the API listener only as external access describes**
   (`docs/external-access.md`). If you expose it, expose **only** the API
   listener, behind TLS, with invite tokens — never the admin listener.
   Tailscale Funnel is acceptable **only** for that split setup: Funnel
   requests reach the backend without Serve identity headers and outside
   WhoIs resolution, so the forged-identity path is closed — provided the
   API listener is on a **different backend port** from the one Serve uses
   for the admin listener. A single-listener binary must stay inside the
   tailnet unless the edge enforces the split itself (the named demo
   exception: `deploy/demo/` returns 404 for admin, metrics, and the admin
   SPA at nginx).
2. **Prefer `auth-mode=tailnet`** once Serve or WhoIs is working; drop the
   shared key so it cannot leak into agent transcripts or shell history.
3. **Keep an agent API key** only if you need `ts-unplug` / CLI bridges or
   tools that are not on the tailnet — and treat that key like a password.
   Never reuse it as `-admin-api-key`.
4. **Allowlist logins** on any tailnet that is not solo. Use `-admin-logins`
   to keep admin on your human login when tagged agents share the tailnet.
5. **Tag the service** (`tag:aegis`) so ACLs survive user device churn. Tag
   agent hosts separately (`tag:aperture`) so they stay agent-only.
6. **Device approval** in the admin console prevents unapproved machines from
   joining even with a stolen auth key.
7. **Log identity.** Aegis logs `login` and `identity_source` on denied
   requests and attaches tailnet identity to library/proxy logs. Pre-request
   also warns when Aperture’s `login_name` does not match the Tailscale peer.
8. **All secrets at rest are encrypted under `-token-pass`** (issue #184).
   OAuth tokens **and** static service-connection credentials (Immich/YNAB
   API keys, Tailscale Admin API key) live in `tokens.json` under the same
   AES-256-GCM / PBKDF2 master key. `service_connections.json` holds only
   non-secret overlay fields. Tailnet auth does not replace disk encryption.
   PBKDF2 parameters are recorded in the file; rotate the passphrase with
   `-token-new-pass` (see [Token encryption](docs/configuration.md)).
   Wrong `-token-pass` refuses to start and never overwrites the store
   (issue #100).
9. **Keep OAuth client secrets out of `aegis.json`** with
   `"$ENV:NAME"` / `"${NAME}"` indirection (issue #102) so bootstrap config can
   be shared without leaking mintable credentials. Literal secrets still work
   when needed.
10. **OAuth login/callback stay public** on purpose (browser redirects from
    Spotify/Google/Amazon). Status and revoke remain behind auth middleware.
11. **Keep `/metrics` authenticated.** `/healthz` and `/readyz` are
    unauthenticated liveness/readiness probes with no inventory. `/metrics`
    is admin-scoped (issue #181) because series include service ids and denial
    reasons (issue #105). Point Prometheus at it with an admin API key or from
    a tailnet admin identity — it lives on the admin listener and is never
    published.
12. **Audit log is local state.** `<data-dir>/audit_log.db` records who
    accessed which kind and whether the call was allowed or denied
    (redacted — no tokens or bodies). Treat the file like `tokens.json`:
    mode 0700 directory, not world-readable, included in the same backup
    you use for other Aegis state. Default retention is 30 days
    (`-audit-retention-days`).
13. **User-defined type file access is admin-equivalent.**
    `GET /v1/admin/udt/browse` lists directories on the Aegis host and the
    source/extra endpoints accept absolute local paths. Those endpoints
    require an **admin** principal (issue #181). Whatever file is selected can
    then be read through `/v1/library/{udt}` by authorized agents — so the
    *selection* is the dangerous step and is no longer on the agent credential.
    Schema packs must not embed secrets in `http_json` headers (rejected at
    load); use named connections / the encrypted token store.

## What “authorized” means for data

After authentication, Aegis is still **single-user** with respect to stored
OAuth tokens: every authorized caller uses the same Spotify/YouTube/Amazon
tokens on disk. Credentials control *who can call the API and what they may
see*; they do not yet isolate *whose* music library is returned. External
principals therefore read the **owner's** connected services, narrowed by
their grants ([`docs/data-scopes.md`](docs/data-scopes.md)).

If you need per-user libraries later, store tokens keyed by the caller's
login/invite and select the store from the request principal. Until then,
keep the allowlist tight on shared tailnets and grant external principals
only what they need.

### Data grants (permissions per kind)

Authentication answers “who may call Aegis.” A **data grant** answers “what may
an authorized LLM agent or app do with each kind” — for example deny
`player` or limit `playlists` to `read`. This is the app-capability layer
alongside Tailscale **grants** (who can reach Aegis); see
[`docs/glossary.md`](docs/glossary.md).

Agent data grants live in `data_grants` (aegis.json or `data_grants.json`);
service `grant_limits` only tighten. Denials are enforced on the
content/ingest proxy and library endpoints with 403. Agent discovery is via
pre-request instructions and connector `SystemInstructions` (issue #119) — not
a capabilities listing API. Omitting a data grant (or `permissions`) preserves
unrestricted access for authorized callers unless a kind defaults closed
(places default to `none`).

**External principals (invite tokens) are deny-by-default** — the owner-tier
“omit = unrestricted” rule does not apply to them. Their data scope's
`grants` map is the complete allow list: an unlisted kind resolves to
`none` (a reserved `default` entry can set the level for unlisted kinds;
opt-in kinds still require an explicit name). Sharing is opt-in per kind.
Scope grants also never exceed what agent data grants allow (AND of
both layers). This is the deliberate split from owner-tier semantics — see
`docs/external-access.md` “Deny-by-default”.

### Third-party app tokens (issue #131)

Non-agent clients use a **separate app token** (`aeg_app_…`) registered in the
admin API. App tokens:

- Are scope-limited to library **read** endpoints (not admin, proxy, OAuth, or mail).
- Carry **app data grants** (`scopes`) using the same access levels, including `freebusy` for events.
- Never grant more than agent data grants / service permissions allow (AND of both layers).
- Are stored as SHA-256 hashes in `apps.json` (0600); plaintext is shown once.

An app token is an authorization layer; reachability comes from the
deployment. With external access, the third party calls the published API
listener like any invite-token holder. Without it, the third party must
reach Aegis over the tailnet, VPN, or a tunnel you operate. Prefer `freebusy`
over full `events` for scheduling clients.


## Deployment checklist

Tailnet-only deployment:

- [ ] Aegis host tagged (`tag:aegis`) and ACL/grant limited to intended users
- [ ] Single listener not published (Serve in front **or** bound to localhost/Tailscale IP)
- [ ] `auth-mode=tailnet` (or `auto` while migrating)
- [ ] `allowed-logins` set if the tailnet is shared
- [ ] `admin-logins` set if tagged agent hosts share the tailnet with you
- [ ] `admin-api-key` set (and different from `api-key`) if you admin via curl

External access adds (see `docs/external-access.md`):

- [ ] **Split listeners**: admin listener on tailnet/loopback only; API listener is the only published surface
- [ ] TLS terminated at an edge you control (reverse proxy, tunnel, or Funnel on the API listener only)
- [ ] Invite tokens minted per external principal; scope grants listed explicitly (deny-by-default — no relying on "omit = everything")
- [ ] Ambient identity disabled on the API listener (startup refuses a public bind with Serve-header trust)
- [ ] Rate limiting / brute-force damping in front of token auth
- [ ] Admin surfaces verified unreachable from outside (404 on the API listener)

Every deployment:

- [ ] `AEGIS_TOKEN_PASS` set and `tokens.json` (under the data directory) not world-readable
- [ ] Agent API key removed or stored outside agent-accessible paths; never reused as admin key
- [ ] `/metrics` scrape credentials stored like any other admin API key; `/healthz` stays unauthenticated
- [ ] `audit_log.db` retained/backed up with the data directory (default 30 days)

## References

- [Tailscale identity](https://tailscale.com/docs/concepts/tailscale-identity)
- [How Aperture works](https://tailscale.com/docs/aperture/how-aperture-works) (WhoIs-based auth)
- [Access controls / grants](https://tailscale.com/kb/1324/acls)
- [Tags](https://tailscale.com/kb/1068/tags)
- [Tailscale Serve](https://tailscale.com/kb/1312/serve) (identity headers)
- [tsnet](https://github.com/tailscale/tailscale/tree/main/tsnet) (embed Tailscale in Go; optional alternative to system `tailscaled`)
- [Concept glossary](docs/glossary.md) (service/connector/connection, data grant, LLM agent vs coding agent)
- [Threat model](docs/threat-model.md) (assets, actors, threats, residual risks)
