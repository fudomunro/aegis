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
   only data types its data scope **explicitly grants**. Sharing is opt-in
   per data type. (Owner-tier credentials keep today's semantics; see the
   split below.)
4. **No federation features yet.** An external principal may be a person,
   an app, or *another Aegis instance's operator* — but it authenticates as
   an ordinary invite token. Discovery, per-instance identity, and trust
   between instances are future work; "other Aegis" needs nothing special
   today.
5. **Ambient identity is never trusted on the public surface.** Serve
   identity headers are forgeable by anyone who can open a TCP connection to
   a public socket; see "The landmine" below.

## Model

```
                    ┌─ credential perimeter (may be public) ─┐
Public internet ──► │  API listener                          │
  (invite tokens)   │  pre-request · library · content       │
                    │  transport · mail agent routes ·       │
                    │  auth/status · app token redeem        │
                    └───────────────────┬────────────────────┘
                                        │ DataScope { dataset,
                                        │   grants (deny-by-default),
                                        │   read_only }
                                        ▼
                               provider APIs + local state

                    ┌─ tailnet perimeter (never public) ─────┐
Tailnet ──────────► │  Admin listener                        │
  (Serve / WhoIs)   │  /v1/admin/* · /metrics · OAuth flows  │
  or admin key      │  app consent · mail sync · mirror/…    │
                    └────────────────────────────────────────┘
```

| Listener | Serves | Auth | Exposure |
|----------|--------|------|----------|
| **API** | `POST /v1/pre-request`, `/v1/library/*`, `/v1/services/*`, `/v1/mail/*` (agent routes), `GET /v1/auth/status`, `POST /v1/apps/token`, `POST /v1/apps/revoke`, `/healthz`, `/readyz` | Invite token (data-scope key), app token, agent API key. **No ambient identity** when external access is on | May be public behind TLS |
| **Admin** | `/v1/admin/*`, `/metrics`, `/v1/auth/*` OAuth login/callback, `GET`/`POST /v1/apps/authorize`, `POST /v1/mail/sync`, mirror / photo-share / entity-link writes | Tailnet identity (existing admin rules) or admin API key | Tailnet or loopback only |

When a route is registered on both lists (`/healthz`), that is deliberate
liveness. Everything admin-tier answers **404 on the API listener** when
external access is enabled — the public surface must not advertise the
policy API.

### External principal

An **external principal** is a non-owner caller holding an invite token. The
token is a data-scope API key ([`data-scopes.md`](data-scopes.md)): one
principal → one `DataScope { dataset, grants, read_only }`. The principal may
be a family member, a friend, a third-party backend, or another Aegis
instance's operator — the credential model is identical.

Owner-tier credentials are unchanged and are *not* external principals:

| Credential | Tier | Grant semantics |
|------------|------|-----------------|
| Tailnet identity (admin rules) | Admin | Owner policy; admin surfaces |
| Admin API key | Admin | Owner policy; admin surfaces |
| Agent API key (`-api-key`) | Agent (owner) | Agent **data grants**; omit = unrestricted (places opt-in `none`) |
| Invite token (scope key) | Agent (external) | **Scope grants, deny-by-default** |
| App token (`aeg_app_…`) | App | App scopes AND agent data grants (unchanged) |

### Deny-by-default (external principals)

For an invite token, the scope's `grants` map is the **complete allow list**:

1. A data type not listed resolves to **`none`** for that principal —
   unlike agent data grants, where omit means unrestricted.
2. A reserved `default` entry sets the level for unlisted types, so
   "share everything read-only" stays expressible
   (`"grants": {"default": {"default": "read"}}`) without listing every
   kind.
3. Opt-in types (places) still require an explicit name — `default` never
   enables them.
4. `read_only: true` still caps methods regardless of grant levels.
5. The dataset partition rule is unchanged: household sharing is the owner's
   dataset (empty `dataset`) with narrower grants; isolated personas keep
   named datasets.

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

`buildIdentityResolver` currently wires `ServeHeaderResolver` unconditionally
in `auto`/`tailnet` modes. `Tailscale-User-Login` is just a request header: on
a public socket any client can claim any login, and the default admin rule
(any allowed non-tagged user login) turns one forged header into **full
admin**. Today this is safe only because nothing but `tailscale serve` can
reach the binary.

Contract:

1. When external access is enabled, the API listener resolves **no ambient
   identity** — no Serve headers, no WhoIs. Explicit credentials only. An
   in-tailnet caller uses the same keys as anyone else (agent key or its
   invite token).
2. The admin listener keeps Serve/WhoIs resolution; it is reachable from the
   tailnet only, which is what makes those headers trustworthy.
3. The trust of identity headers stays a property of *listener exposure*,
   never of the auth mode name.

## Transport and hardening duties

External access moves TLS, abuse, and availability from Tailscale's job to
the deployment's:

- **TLS termination** at an edge the operator controls (reverse proxy,
  tunnel). Aegis continues to speak HTTP on the listener.
- **Rate limiting** on authentication and token-bearing routes (brute-force
  damping; per-IP and per-token failure caps). Required before the public
  bind is a supported deployment; none exists in the binary today.
- **Body caps** already exist per route family and stay.
- **Audit log** already records principal and decision; external principals
  appear there like any other caller.
- **Secrets at rest** are unchanged — invite tokens are stored hashed
  (`data_scopes.json` is 0600 and secret-bearing, like `apps.json`).

## What does not change

- **Aegis owns OAuth.** External callers see neutral library models and
  content bytes they were granted — never tokens. This matters *more* once
  callers are outside the tailnet.
- **Fail-open** on provider fetch/parse errors. Authentication and
  authorization still fail closed (401/403).
- **Type-centric surface.** External principals call the same
  `/v1/library/{kind}` as agents; grants narrow visibility, they do not
  invent a second API.
- **App tokens.** Same shape; external access is what finally makes
  third-party reachability practical.
- **Pre-request hook.** Stays on the API listener (Aperture and bridges are
  just callers with the agent key or an invite token).

## Issue #149 mapping

| Mode | Path |
|------|------|
| Family/household access to your data | Invite token per person → data scope on the owner dataset with per-person grants. Works from the tailnet **or** anywhere else; tailnet login → scope mapping stays a possible later convenience |
| Other Aegis instances sharing with you | Each side issues the other an invite token and calls the other's API listener. No federation protocol in this phase; discovery and per-instance identity are future work |

**Non-goals (this contract):** per-user provider connections (every
principal still reads the owner's connected services), self-service signup,
OIDC/passwords, cross-instance discovery, hosted multi-tenant SaaS.

## Implementation sketch (follow-on work)

1. **Listener split** — `AdminMiddleware` routes move to `-admin-addr`
   (tailnet/loopback bind); API routes stay on `-addr` (or `-api-addr`).
   Admin paths return 404 on the API listener when external access is on.
   Per-listener base URLs for OAuth redirect and CSRF origin checks.
2. **Identity gating** — ambient identity resolution attaches per listener:
   admin always, API only when external access is off. Startup refuses a
   public API bind with Serve-header trust enabled.
3. **Scope grant enforcement** — deny-by-default resolution layered over
   `types.DataGrants`, the reserved `default` entry, migration for packs
   assuming empty = full read, and the isolation-contract tests extended
   with a third principal that holds *no* grants.
4. **Invite lifecycle** — admin mint / rotate / disable for scope keys
   (reusing `api_key_hint`), keeping plaintext display-once.
5. **Hardening** — rate limiting on auth and token routes, external-principal
   audit fields, deployment docs for the TLS edge.

No change to fail-open, named-service connectors, "Aegis owns OAuth", the
coverage floor, or the secrets-out-of-git rule.
