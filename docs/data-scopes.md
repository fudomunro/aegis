# Data scopes (multi-user seam)

Design note for the principal → data partition contract. This is the first
slice of multi-user modes and the isolation model the public demo (#217)
relies on. Household sharing, cross-instance sharing, and admin UX for
people remain open in the multi-user investigation.

## Problem

Aegis today assumes one implicit user: the tailnet owner. Every connector
serves “the user’s” data, one shared agent API key reads everything, and
data grants only limit *which types* a caller may use.

Two upcoming needs break that assumption:

1. **Public demo** — several synthetic personas (one API key each) must not
   read each other’s mail, contacts, or photos on one instance.
2. **Household / shared Aegis** — people who are not the owner need a
   principal with its own grants (and eventually its own data).

Both need a first-class binding: *authenticated caller → which data*.

## Model

```
Principal (API key, later a tailnet login)
    → DataScope { id, dataset, grants, read_only }
        → dataset partition of local stores + service visibility
        → type-level data grants (existing DataGrants)
```

A request resolves to **at most one** `DataScope`. The scope is attached to
the request context and is the only thing library, mail, and content paths
use to decide *whose* data is in play.

### DataScope

| Field | Meaning |
|-------|---------|
| `id` | Stable scope id (`frodo`, `default`, …) |
| `name` | Display label |
| `dataset` | Partition id. Empty = owner/default partition (no filter) |
| `api_key` | Agent credential for this scope (file-only; never logged). Under external access this is the **invite token** |
| `read_only` | Reject mutating agent methods (demo default) |
| `grants` | Type-level `DataGrants` — the **complete allow list** for invite tokens (deny-by-default; unlisted = `none`) |
| `disabled` | Soft delete |

### Principal resolution (M1)

1. Request carries a scope API key (`Authorization: Bearer` or `X-Api-Key`)
   → that scope. Under external access this is the **invite token** — the
   only credential an external principal holds.
2. Legacy `-api-key` / `-admin-api-key`, or tailnet identity → **default**
   scope (`dataset` empty, no extra grants). Existing behavior unchanged.
3. Optional later: tailnet login → scope mapping (in-tailnet family members
   without a token). Not required by the external-access contract.

## Dataset partition

Partitioning is **by named dataset**, not by principal id. Two principals can
share a dataset (household: same mail, different grants). Two datasets on one
instance never mix (demo personas).

| Store | Rule |
|-------|------|
| Services / connectors | `Service.Dataset` empty → visible to every scope. Set → visible only when `scope.dataset` equals it. |
| Mail archive rows | `dataset` column. Scope with empty dataset sees all rows. Scope with `dataset=X` sees only rows tagged `X`. |
| Library cache | Cache keys include the scope dataset so one persona cannot read another’s cached page. |
| Sibling mail hydration | Same dataset filter as search, so merge cannot pull a foreign partition. |

Invisible services are **skipped**, not reported as denied — a caller should
not learn that another persona’s connectors exist.

### Why services carry `dataset`

Connectors are already the unit of data. Binding partition at the service
keeps providers dumb (they serve “their” user) and puts isolation in one
place: `collectProviders` + mail queries + cache keys. Real multi-user later
can move to per-user connector state without changing the principal model.

## Grants

`DataScope.grants` uses the existing type-level `types.DataGrants` and is
validated on load. **Not yet enforced** in code — enforcement is part of the
external-access implementation. The semantics are decided
([`external-access.md`](external-access.md)):

**Deny-by-default for invite tokens.** For an external principal the scope's
`grants` map is the **complete allow list**: an unlisted data type resolves
to `none`. A reserved `default` entry sets the level for unlisted types (so
"share everything read-only" stays expressible); opt-in types (places) still
require an explicit name. `read_only: true` still caps methods regardless of
levels.

Until enforcement lands, do not assume scope grants restrict reads — mint
scopes for principals you already trust.

**Owner-tier split.** The shared agent key and tailnet agents keep today's
"omit = unrestricted" agent-data-grant semantics (places opt-in `none`).
Scope grants for invite tokens deliberately diverge: sharing is opt-in per
data type. Scope grants also never exceed what agent data grants allow (AND
of both layers, same as app tokens).

Demo scopes that expect full read of *their* dataset must set
`"grants": {"default": {"default": "read"}}` (or list types) once
enforcement lands; `read_only: true` remains the demo's write wall.

## Isolation contract (testable)

For scopes A and B with datasets `a` and `b`:

1. GET `/v1/library/*` with A’s key never returns rows sourced only from `b`.
2. `/v1/mail/search` and `/v1/library/messages` with A’s key never return `b` rows.
3. A cache fill under A is never served to B.
4. Services bound to `b` are invisible to A (no 403 oracle) — including
   `/v1/services/{id}/...`, `/v1/library/documents/{service}/{id}/text`, and
   pre-request matching/blocking.
5. Default scope (empty dataset) keeps today’s “see everything” behavior.
6. `read_only` scopes cannot invoke mutating agent routes.
7. Data-scope keys never authenticate admin routes.

## Non-goals (this note)

- Household admin UI, invitations, or tailnet login mapping
- Per-user OAuth connections (Aegis still owns tokens globally)
- Cross-instance sharing
- Renaming existing data-grant product language

## File layout

| File | Contents |
|------|----------|
| `<data-dir>/data_scopes.json` | Scope records (0600). Holds `api_key` material — never commit. |

Unit tests and docs must use allowlisted placeholder keys (`YOUR_*`,
`aeg_test_*` / `aeg_demo_*`, or `*-key-1234`), never realistic secrets —
gitleaks runs on history (`.gitleaks.toml`).

`data_scopes.json` is in the same class as `apps.json` / `tokens.json`:
local secret-bearing state, not a git file.

## Relationship to the public demo (#217)

| Demo need | Covered here |
|-----------|--------------|
| One API key per persona | `DataScope.api_key` |
| Personas cannot read each other | dataset partition + cache keys |
| Synthetic-only, read-only | `read_only` + demo-owned datasets |
| Theme (LotR, …) | Dataset *content*, not in this note |
| Agent kit without Aperture | Out of scope (static brief) |
| DigitalOcean host | Out of scope |

## Relationship to multi-user modes

This note defines the **principal and isolation seam**. Multi-user modes
(issue #149) resolve through external access
([`external-access.md`](external-access.md)):

- **Household / family access** = one invite token (scope key) per person on
  the owner dataset, with per-person grants. Works from the tailnet or
  anywhere else. Covered here + deny-by-default grants.
- **Other Aegis instances sharing with you** = each side issues the other an
  invite token and calls the other's API listener. No federation protocol;
  the credential model is identical.

Still open (not part of the external-access contract):

- Optional tailnet login → scope mapping
- Per-user provider connections / canonical homes (every principal reads the
  owner's connected services)
- Cross-instance discovery and per-instance identity

## Implementation sketch (M1)

1. `DataScope` store + request context (`internal/core/datascope.go`)
2. `Service.Dataset` + visibility filter in `collectProviders`
3. Mail `dataset` column + query filters (including sibling hydration)
4. Scope-aware library cache keys
5. Sample library/mail generators namespaced per dataset
6. Two-key isolation tests against the sample set

No change to fail-open, named-service, or “Aegis owns OAuth” rules.
