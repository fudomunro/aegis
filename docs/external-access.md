# External access (public interface contract)

Design note for the trust-model change that lets Aegis be reached from outside
the tailnet, and with it the answer to multi-user modes (issue #149). This is
the **contract**; the listener split and grant enforcement land in the
implementation that follows. Trust-model prose lives in
[`SECURITY.md`](../SECURITY.md); the asset/threat view is in
[`threat-model.md`](threat-model.md); principal isolation is in
[`data-scopes.md`](data-scopes.md).

## The assumption that changes

Today the **tailnet is the perimeter**: anything that can reach Aegis was
admitted by Tailscale (SSO, device approval, ACLs), so Aegis needs no second
gate. External access reverses that for one listener:

> On the public surface, **the credential is the perimeter.** Reachability
> proves nothing; every request must present a token, and the token decides
> what is visible.

Everything downstream of that sentence changes: identity resolution, admin
exposure, grant defaults, TLS/rate-limit duties, and the threat model.

## Decisions

1. **Split listeners.** The **API listener** carries agent and external
   surfaces and may be publicly exposed. The **admin listener** carries
   policy and setup surfaces and **stays in-tailnet** — its trust is the
   tailnet itself (identity via Serve/WhoIs, or an explicit admin key), not a
   new credential system.
2. **Invite-only tokens** authenticate external principals. No self-service
   signup, passwords, or OIDC in this phase. An invite is minted by the
   admin and issued out-of-band.
3. **Deny-by-default for external principals.** An external principal sees
   only kinds its data scope **explicitly grants**. Sharing is opt-in
   per kind. (Owner-tier credentials keep today's semantics; see the
   split below.)
4. **No federation features yet.** An external principal may be a person,
   an app, or *another Aegis instance's operator* — but it authenticates as
   an ordinary invite token. Discovery, per-instance identity, and trust
   between instances are future work; "other Aegis" needs nothing special
   today.
5. **Ambient identity is never trusted on the public surface.** Serve
   identity headers are forgeable by anyone who can open a TCP connection to
   a public socket; see "The landmine" below.
6. **The agent API key never crosses the public bind.** Under external
   access the published API listener accepts **invite and app tokens only**;
   the agent key stays a tailnet-listener credential. A transcript leak of
   the agent key must not become internet-wide full read.

## Model

```
                    ┌─ credential perimeter (may be public) ─┐
Public internet ──► │  API listener (route allowlist)        │
  (invite tokens,   │  pre-request · library · content       │
   app tokens)      │  transport · mail agent routes ·       │
                    │  auth/status · app token redeem        │
                    └───────────────────┬────────────────────┘
                                        │ DataScope { dataset,
                                        │   grants (deny-by-default),
                                        │   read_only }
                                        ▼
                               provider APIs + local state

                    ┌─ tailnet perimeter (never public) ─────┐
Tailnet ──────────► │  Admin listener                        │
  (Serve / WhoIs)   │  admin tier: /v1/admin/* · /metrics ·  │
  or admin key      │    OAuth · consent · sync              │
                    │  agent tier: same API routes, for      │
                    │    tailnet agents (ambient identity    │
                    │    or agent key) — no migration for    │
                    │    Aperture / tagged hosts             │
                    └────────────────────────────────────────┘
```

| Listener | Serves | Auth | Exposure |
|----------|--------|------|----------|
| **API** | **Allowlist only**: `POST /v1/pre-request`, `/v1/library/*`, `/v1/services/*`, `GET /v1/auth/status`, `POST /v1/apps/token`, `POST /v1/apps/revoke`, `/healthz`, `/readyz` | **Invite token, app token only** under external access. No ambient identity, no agent key | May be public behind TLS |
| **Admin** | Admin tier: `/v1/admin` and `/v1/admin/*` (API + SPA shell), the admin SPA routes (`/`, `/home`, `/library`, `/services`, `/advanced`), `/metrics`, `/v1/auth/*` OAuth login/callback, `GET`/`POST /v1/apps/authorize`, mail ingest (`/v1/admin/mail/*`). **Plus the agent-tier routes above**, for tailnet callers | Tailnet identity (existing admin rules) or admin API key for admin tier; tailnet identity or agent key for agent tier | Tailnet or loopback only |

**The API listener route set is an allowlist, not a blocklist.** A route that
is not listed answers **404** there and is served only by the admin listener.
New routes default to the admin side until explicitly added to the allowlist;
a forgotten route therefore cannot leak onto the public surface. `/healthz`
on both listeners is deliberate liveness. Demo routes are **off** the
allowlist by default: `/demo/fake-upstream/*` (dev harness) never publishes,
and `/demo/third-party` is exposed only by the demo deployment's edge
(`deploy/demo/`), which is the named single-listener exception.

Serving the agent tier on the admin listener is what keeps in-tailnet agents
(Aperture's `/v1/pre-request` call, tagged hosts) working unchanged — and it
is the more secure arrangement: tailnet agents need **no shared key at all**
(keep `auth-mode=tailnet` and drop `-api-key`, per `SECURITY.md` best
practice 2), instead of every external-access deployment being pushed to
distribute the owner-tier agent key to its agents.

### External principal

An **external principal** is a non-owner caller holding an invite token. The
token is a data-scope API key ([`data-scopes.md`](data-scopes.md)): one
principal → one `DataScope { dataset, grants, read_only }`. The principal may
be a family member, a friend, a third-party backend, or another Aegis
instance's operator — the credential model is identical.

Owner-tier credentials are unchanged and are *not* external principals:

| Credential | Tier | Grant semantics | Accepted on |
|------------|------|-----------------|-------------|
| Tailnet identity (admin rules) | Admin | Owner policy; admin surfaces | Admin listener |
| Admin API key | Admin | Owner policy; admin surfaces | Admin listener |
| Agent API key (`-api-key`) | Agent (owner) | Agent **data grants**; omit = unrestricted (places opt-in `none`) | Admin listener only under external access (never the published bind) |
| Invite token (scope key) | Agent (external) | **Scope grants, deny-by-default** | Both listeners |
| App token (`aeg_app_…`) | App | App scopes AND agent data grants (unchanged) | Both listeners |

### Deny-by-default (external principals)

For an invite token, the scope's `grants` map is the **complete allow list**:

1. A kind not listed resolves to **`none`** for that principal —
   unlike agent data grants, where omit means unrestricted.
2. A reserved `default` entry sets the level for **unlisted** kinds only, so
   "share everything read-only" stays expressible
   (`"grants": {"default": {"default": "read"}}`) without listing every
   kind.
3. Opt-in types (places) still require an explicit name — `default` never
   enables them.
4. `read_only: true` still caps methods regardless of grant levels.
5. The dataset partition rule is unchanged: household sharing is the owner's
   dataset (empty `dataset`) with narrower grants; isolated personas keep
   named datasets.

**Resolution is fail-closed, and every ambiguity resolves to `none`:**

| Entry | Resolves to |
|-------|-------------|
| Type absent, no `default` key | `none` |
| Type absent, `default` key present | the `default` key's level |
| `{"music": {}}` (listed, empty) | `none` — a listed kind must state its own level |
| `{"music": {"providers": {"spotify": "read"}}}` | `none` — provider overrides can only **tighten** a level, never open a closed kind. To grant, set the kind's `default` field and use provider entries to narrow |
| `{"default": {"default": "read"}, "music": {...}}` with `music.default` empty | `none` for `music` — the `default` key covers **unlisted** kinds only; listing a kind is an explicit act |

This resolution runs in a **dedicated scope-grants resolver**, not in
`policy.DataGrants.Access`. `DataGrants.Access` keeps owner-tier semantics
("unset default preserves pre-permission behavior"); the two must not be
merged, or a scope fix silently changes owner behavior and vice versa. The
scope resolver is its own function with its own tests (every row of the
table above is a case).

This is a deliberate **split** from owner-tier semantics, not a silent
behavior change: the shared agent key and tailnet agents keep the current
"omit = unrestricted" contract (tightened only by data grants / service
limits). Migration note: demo and sample packs that rely on "empty grants =
full read of my dataset" must add an explicit `default` entry when
enforcement lands.

Enforcement points are the existing seams — library aggregation, content
transport, mail routes, pre-request injection, and cache keys per scope —
plus the new rule that a scope grant can never exceed what the agent data
grants already allow (AND of both layers, same as app tokens).

## The landmine (must fix before any public bind)

`buildIdentityResolver` wires `ServeHeaderResolver` in `auto`/`tailnet` modes.
`Tailscale-User-Login` is just a request header: on any socket a client can
reach, a client can claim any login, and the default admin rule (any allowed
non-tagged user login) turns one forged header into **full admin**.

The LAN exposure this created — the default `-addr :8080` binding all
interfaces — is closed by `fix/serve-header-loopback-guard`, which lands
before any external-access code:

1. **Request-level gate:** Serve headers are trusted only when `RemoteAddr`
   is a local peer (loopback or an address on this host's own interfaces —
   the hop `tailscale serve` or a same-host proxy makes). From a remote peer
   they are ignored and resolution falls through to WhoIs. `RemoteAddr` is
   the accepted connection's address and cannot be forged.
2. **Loopback default:** `-addr` defaults to `127.0.0.1:8080`.
3. **Startup refusal:** with Serve identity trust (`auto`/`tailnet`), a
   wildcard or LAN bind is refused — sound binds are loopback, a Tailscale
   IP, or `-auth-mode api-key`.

Remaining contract for the listener split itself:

1. When external access is enabled, the API listener resolves **no ambient
   identity** — no Serve headers, no WhoIs. Explicit credentials only.
2. The admin listener keeps Serve/WhoIs resolution; it is reachable from the
   tailnet only, which is what makes those headers trustworthy.
3. The trust of identity headers stays a property of *listener exposure*,
   never of the auth mode name.
4. **Startup refusal, not a warning** for the external-access case: Aegis
   refuses to start when the API listener's bind would expose it without the
   external-access profile (tokens-only, allowlisted routes).

## Transport and hardening duties

External access moves TLS, abuse, and availability from Tailscale's job to
the deployment's:

- **TLS termination** at an edge the operator controls (reverse proxy,
  tunnel). Aegis continues to speak HTTP on the listener. Deployment notes
  for the edge are in [TLS edge deployment](#tls-edge-deployment) below.
- **Rate limiting** on authentication and token-bearing routes (brute-force
  damping; per-IP and per-token failure caps) ships in the binary
  (`rate_limits` config). Per-IP caps need a **trusted-proxy
  configuration** (`trusted_proxies`): behind an edge every request arrives
  from the proxy's address, so the real client IP is only trustworthy from
  an explicitly configured `X-Forwarded-For` / `X-Real-IP` hop (the demo
  nginx already sets `X-Real-IP`). Without that list, forwarded headers are
  ignored and caps key on the proxy address.
- **Body caps** already exist per route family and stay.
- **Audit log** records principal and decision; external principals audit
  as `scope:<id>` with the resolved client IP (`client_ip`).
- **Invite tokens are stored hashed at rest** (`api_key_hash`, like
  `apps.json`): mint writes only the SHA-256 hash, plaintext is displayed
  once, and legacy plaintext `api_key` entries are migrated on load.

### TLS edge deployment

The supported public shape is: an edge the operator controls terminates
TLS and forwards plain HTTP to Aegis bound to loopback (or a private
interface) — the admin listener stays tailnet-side and is never published.

1. **Terminate TLS at the edge** (nginx, Caddy, a tunnel). Point it at
   Aegis's API listener (`-addr`, or the split `-addr` + `-admin-addr`
   layout where only `-addr` is published); never publish the admin
   listener. **Startup refuses** `trusted_proxies` together with Serve
   identity trust (auth modes `auto`/`tailnet`): a generic edge forwards
   client-supplied `Tailscale-User-Login` straight through, so that
   combination would be admin takeover. Behind an edge use
   `-auth-mode api-key` (or the split's `-external-access`, tokens only on
   the published listener).
2. **Forward the client identity hop.** Configure `trusted_proxies` with
   the edge's address so Aegis believes its forwarded headers, and set
   `client_ip_header` to whichever header your edge *overwrites*
   (`x-forwarded-for` by default; `x-real-ip` for the demo nginx shape).
   Aegis reads exactly one of the two — never a fallback chain, so a
   client-supplied value in the other header cannot forge the client IP.

```json
{
  "trusted_proxies": ["127.0.0.1/32"],
  "client_ip_header": "x-real-ip",
  "rate_limits": {
    "auth_per_minute": 60,
    "auth_failures_per_ip": 10,
    "auth_failures_per_token": 10,
    "window_seconds": 300
  }
}
```

3. **At the edge**, set the chosen client-IP header on proxied requests,
   **strip Tailscale identity headers** (defense in depth — the startup
   refusal above is the real gate), publish only the API route set, keep
   body limits, and add a coarse request rate limit of your own — Aegis's
   caps damp credential guessing; availability is still the edge's job.

nginx sketch (the demo host uses this shape):

```nginx
server {
    listen 443 ssl;
    server_name aegis.example.com;
    # ...certificates...

    # Only the API surface is published. The listener split's allowlist
    # (pre-request hook, library reads, content transport, mail agent
    # routes, auth/status, app token redeem, health) is the authority —
    # keep the edge in front of the same set so admin routes never appear.
    location /v1/pre-request   { proxy_pass http://127.0.0.1:8080; include aegis-proxy.conf; }
    location /v1/library/      { proxy_pass http://127.0.0.1:8080; include aegis-proxy.conf; }
    location /v1/services/     { proxy_pass http://127.0.0.1:8080; include aegis-proxy.conf; }
    location /v1/auth/status   { proxy_pass http://127.0.0.1:8080; include aegis-proxy.conf; }
    location /v1/apps/token    { proxy_pass http://127.0.0.1:8080; include aegis-proxy.conf; }
    location /v1/apps/revoke   { proxy_pass http://127.0.0.1:8080; include aegis-proxy.conf; }
    location /healthz          { proxy_pass http://127.0.0.1:8080; include aegis-proxy.conf; }
}

# aegis-proxy.conf
# proxy_set_header X-Real-IP $remote_addr;
# proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
# proxy_set_header Tailscale-User-Login "";
# proxy_set_header Tailscale-User-Name "";
# proxy_set_header Tailscale-User-Profile-Pic "";
# proxy_set_header Tailscale-Identity "";
# proxy_set_header Tailscale-User-Login "";
```

Without `trusted_proxies`, Aegis ignores forwarded headers entirely and
**skips per-IP rate limits for loopback peers** (a same-host edge would put
every caller in one bucket — one attacker could lock out the whole
deployment, including valid credentials); per-token failure caps still
apply. Never widen `trusted_proxies` past the actual edge addresses: any
client that can reach Aegis *from* a listed address may forge its client
IP.

## What does not change

- **Aegis owns OAuth.** External callers see neutral library models and
  content bytes they were granted — never tokens. This matters *more* once
  callers are outside the tailnet.
- **Fail-open** on provider fetch/parse errors. Authentication and
  authorization still fail closed (401/403).
- **Kind-centric surface.** External principals call the same
  `/v1/library/{kind}` as agents; grants narrow visibility, they do not
  invent a second API.
- **App tokens.** Same shape; external access is what finally makes
  third-party reachability practical.
- **Pre-request hook.** Stays on the API allowlist for external callers
  (an external principal's agent gets instructions like any other). In-
  tailnet Aperture keeps calling it with ambient identity or the agent key
  on the admin listener — no migration for the hook path.

## Issue #149 mapping

| Mode | Path |
|------|------|
| Family/household access to your data | Invite token per person → data scope on the owner dataset with per-person grants. Works from the tailnet **or** anywhere else; tailnet login → scope mapping stays a possible later convenience |
| Other Aegis instances sharing with you | Each side issues the other an invite token and calls the other's API listener. No federation protocol in this phase; discovery and per-instance identity are future work |

**Non-goals (this contract):** per-user provider connections (every
principal still reads the owner's connected services), self-service signup,
OIDC/passwords, cross-instance discovery, hosted multi-tenant SaaS.

## Implementation sketch

Status: items 1–2 landed (listener split + credential gating, this change;
the LAN forge path closed earlier in `fix/serve-header-loopback-guard`).
Items 3–5 remain follow-on work.

1. **Listener split** — DONE. `AdminMiddleware` routes serve on `-admin-addr`
   (tailnet/loopback bind), which also serves the agent tier for tailnet
   callers (ambient identity / agent key); API routes stay on `-addr` behind
   a **route allowlist** — unlisted routes 404 there and default to the admin
   side. Per-listener base URLs (`-admin-base-url`) for OAuth redirects and
   CSRF origin checks.
2. **Identity and credential gating** — DONE. Ambient identity resolution
   attaches per listener: admin always, API never under external access
   (`-external-access`, which requires `-admin-addr`). The agent key is
   refused on the published bind; startup refuses external access without
   the split and refuses non-loopback/non-tailnet admin binds. (The LAN
   forge path closed in `fix/serve-header-loopback-guard`: local-peer
   Serve-header gate, loopback `-addr` default, startup refusal for wildcard
   and LAN binds under Serve trust.)
3. **Scope grant enforcement** — a dedicated fail-closed scope-grants
   resolver (never `DataGrants.Access`), the reserved `default` entry, the
   empty/override cases in the resolution table above, migration for packs
   assuming empty = full read, and the isolation-contract tests extended
   with a third principal that holds *no* grants plus a case per resolution
   row. (Landed: `policy.ScopeGrantsAccess` / `ScopeTighten`, enforced at the
   library, content-transport, mail, pre-request, and cache-key seams.)
4. **Invite lifecycle** — admin mint / rotate / disable for scope keys
   (reusing `api_key_hint`). **Hash at rest**: mint writes only the SHA-256
   hash, plaintext is shown once (like app tokens), and existing plaintext
   `api_key` entries are migrated to hashes on load. (Landed:
   `POST /v1/admin/data-scopes` mint, `.../{id}/rotate`, `PATCH .../{id}`
   disable; `data_scopes.json` version 2 stores `api_key_hash`.)
5. **Hardening** — rate limiting on auth and token routes (with
   trusted-proxy config for client IPs), external-principal audit fields,
   deployment docs for the TLS edge. (Landed: `rate_limits` + `trusted_proxies`
   config, per-IP/per-token failure caps on auth surfaces, `scope:<id>` audit
   agents with `client_ip`, and the TLS edge notes above.)

No change to fail-open, named-service connectors, "Aegis owns OAuth", the
coverage floor, or the secrets-out-of-git rule.
