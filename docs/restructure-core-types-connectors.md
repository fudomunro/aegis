# Plan: Restructure into core / data types / connectors

Status: **proposal** — start of work for the codebase split called for in #15,
with #148 as the first product feature that lands on the type layer.

Related issues: **#15** (user-defined types; designated split point),
**#148** (permissions live with data types).  
Related product rules: type-centric surface; named-service connectors;
fail-open; Aegis owns OAuth; coverage floor 80%.

---

## 1. Evaluation

### Issue #15 — user-defined types (“bring your own types”)

**What it asks for.** Let a user define a source and a schema for personal
data, then have Aegis expose that data to agents under the same type-centric
surface as built-in kinds (`/v1/library/{kind}`, neutral list, permissions,
pre-request injection). Work spreadsheets / DBs were named as the likely
utility; canonical ownership is still open.

**What the issue already decided.** The scope note treats #15 as the **split
point** for layout: break flat `package main` into core / data-type handling /
connectors, and stop advertising “single package `main`” in AGENTS.md.

**Why the split is forced by #15.** User-defined kinds cannot be compiled-in
Go structs and switch statements. They need:

| Need | Today | Required |
|------|-------|----------|
| Type description | `LibraryKind` + hard-coded model | Schema-as-data (fields, identity, merge, redaction) |
| Registry | `var libraryKinds = []LibraryKind{…}` in Go | Runtime registration + built-ins |
| Handlers | `HandleLibraryWidgets` per kind | Generic aggregate over a schema |
| Permissions | map keys hard-coded in `DataTypeForPath` | Type-owned policy + path patterns |
| Provider | `WidgetProvider` Go interface | Pluggable fetch (HTTP mapping, SQL, local file) |

Without a real type layer, user-defined types become a parallel code path —
exactly what AGENTS.md forbids.

**Open product questions (do not decide in the restructure PRs).**
- What sources are in scope for v1 (HTTP+JSON path mapping? CSV/spreadsheet?
  SQLite? CalDAV later is still a *named connector*, not a UDT)?
- Who is the author (admin UI vs config file)?
- Does a UDT ever own canonical copies, or is it always a mirror/view?
- Are UDT fields agent-visible with the same `sources[]` contract?

These belong in a design spike **after** the type layer exists.

### Issue #148 — permissions with data types, not service providers

**What it asks for.** Policy ownership moves from
`services[].permissions` (service-scoped, already keyed by data-type name) to
**type-level** policy with optional provider / attribute filters. Service-level
filtering becomes the minority case.

**What stays.** Enforcement on proxy, library, capabilities output, and
pre-request injection. Access levels (`none` / `read` / `write` / redaction
levels) stay. Fail-open and clamp-writes stay.

**Why it belongs on the type layer.** A type (or UDT schema) is what knows
which fields are sensitive (places city/precise, freebusy, summary). A provider
only knows how to fetch. Redaction rules already live in `permissions.go` as
type-keyed maps (`freeBusyOnlyTypes`, `cityOnlyTypes`, `optInDefaultNone`) —
that is type policy living in the wrong file, owned by the service.

**Today’s shape (problem).**

```text
Service.Permissions: map[dataType]accessLevel   // per service, in config
permissions.json overlay: serviceID → Permissions
libraryReadableFor / collectProviders: "does any service allow tracks?"
```

**Target shape.**

```text
TypePolicy (owned by type / LibraryKind / type schema)
  default access for agents
  optional provider overrides   // minority: block Spotify tracks only
  optional attribute filters    // field-level, when the type supports it
permissions.json overlay: typeID → TypePolicy
Service.Permissions: optional legacy override during migration only
```

Resolution order at enforcement time:

```text
type default → provider override (if any) → attribute / redaction filters
```

**Depends on:** type layer owning policy *shape* (can be introduced as types
before packages move). **Does not depend on** full UDT support.

### How they fit together

```text
#15 needs a type layer that can describe kinds as data
#148 needs that same type layer to own policy
       ↓
Restructure = create the layer and the package boundaries
       ↓
#148 lands first on the new policy types (high value, smaller than UDT)
#15 lands next as schema-driven kinds + generic connectors
```

Do **not** implement user-defined types or change permission *semantics* in
the restructure PRs. Restructure is boundaries only.

---

## 2. Current state (what we are cutting)

- **145** Go files, **all `package main`**, **71** non-test sources, **~27k**
  lines of tests.
- `*Server` is the god object: auth, config, tokens, OAuth, cache, connectors
  map, metrics, entity links, copy ledger, mail, quicken, timeline, apps.
- Connectors hold `*Server` and call `getValidToken`, `upstreamBaseFor`,
  `config`, `cache` (~50 call sites).
- Type knowledge is split across `models.go`, `librarykind.go`, `match.go`,
  `permissions.go` (`DataTypeForPath`), `canonical.go`, `copy_ledger_stamp.go`.
- Extension checklist is already excellent (`docs/adding-data-types.md`) — it
  describes *logical* layers that do not yet exist as packages.

### Coupling that blocks a naive move

```text
connectors ──► *Server ──► types (models, LibraryKind)
     ▲            │
     └────────────┘  hasProvider / collectProviders / handleLibraryAggregate
```

`LibraryKind.HasProviders` / `Readable` take `*Server`.  
`libraryKindSpec` methods are bound to `*Server`.  
`DataTypeForPath` is a hard-coded switch in `permissions.go`.

---

## 3. Target architecture

Three packages under `internal/` (private service; no accidental imports).
Keep `package main` at repo root so `go build -o aegis .` and CI stay stable.

```text
github.com/fudomunro/aegis/
  main.go, admin UI embed, flags, route wiring     (package main)
  internal/types/       pure data-type layer
  internal/core/        runtime: auth, config, tokens, proxy, cache, admin,
                        permission engine, library aggregate pipeline
  internal/connectors/  named-service adapters
```

### Layer responsibilities

| Layer | Owns | Must not own |
|-------|------|--------------|
| **types** | Neutral models, identity/merge, `LibraryKind` *data*, type schema (including future UDT), `TypePolicy` / access levels, redaction *rules*, `DataTypeCapability` / `Operation` shapes, error sentinels (`ErrUnauthorized`) | HTTP, tokens, config file I/O, `*Server`, provider HTTP |
| **core** | `Server` / host, auth, config + overlays, OAuth + tokens, proxy + cache + httpclient, permission *enforcement*, `handleLibraryAggregate`, admin/flow/metrics, state stores (copy ledger, entity links, mail, …) | Provider-specific JSON, per-kind merge logic, service brand knowledge |
| **connectors** | `ServiceConnector` impls, `Capabilities()`, `SystemInstructions`, `HandleProxy`, `ListUser*` providers, setup flows (Immich, YNAB), sample calendar | Permission policy, neutral merge, admin HTML, token storage |

### Dependency rule

```text
connectors ──depends on──► types
     │
     └──depends on──► core.Host (narrow interface)
                           │
core ──depends on──► types │
     └─────────────────────┘  (core implements core.Host for connectors)

main ──wires──► core + connectors
```

**No cycles.** `types` imports only stdlib (+ maybe `golang.org/x/…` if a
future schema needs it). `connectors` never import `core` types beyond a small
`Host` interface (or the interface is defined in `types` / `connectors` and
implemented by core — pick one home and stick to it; recommend `core.Host`
exported and documented).

### `core.Host` (connector seam — the first real code change)

Today connectors use roughly:

```text
getValidToken(serviceID, logger) string
upstreamBaseFor(serviceID, fallback) string
config.GetService(id) *Service
cache  (read-through)
```

Introduce:

```go
// Host is the dependency surface connectors may use.
type Host interface {
    GetValidToken(serviceID string, logger *slog.Logger) string
    UpstreamBaseFor(serviceID, fallback string) string
    Service(id string) *types.Service   // or a narrow snapshot
    Cache() types.Cache                 // optional
}
```

Connectors store `Host` (or a still-narrower per-connector view) instead of
`*Server`. This unblocks package moves and makes mock hosts trivial.

### Type policy (#148 shape, land types early)

```go
// internal/types/policy.go (illustrative)
type Access string // none, freebusy, summary, city, precise, read, write, read-write

type TypePolicy struct {
    Default   Access            `json:"default,omitempty"`
    Providers map[string]Access `json:"providers,omitempty"` // minority
    // Attributes later (#148 / field filters)
}

type Policy map[string]TypePolicy // typeID → policy
```

Built-in kinds ship a default policy (e.g. places default `none`).
Service-level `Permissions` remains as an **optional override** until
migration completes (compatibility window — document in AGENTS.md pitfalls).

---

## 4. Phased plan

Principles: one concern per PR; tests move with code; coverage floor never
drops; fail-open and connector product rules unchanged; no UDT feature work
until the layer exists; README/AGENTS.md updated when layout or policy ownership
changes.

Worktrees: `/Users/alec/Work/aegis-<slug>`, branches `refactor/…` then
`feat/…`. PR bodies start with `## Summary` and name `#15` / `#148`.

### Phase 0 — Align docs and issue (no code)

- [ ] Comment on #15 / #148 linking this plan (or attach as docs PR).
- [ ] AGENTS.md: note that “single package `main`” is **ending**; layout change
      is in progress under #15. Do not remove the rule until Phase 2–3 land.
- [ ] `docs/adding-data-types.md`: add a short “package map (target)” section
      pointing at `internal/types` / `core` / `connectors` as the destination
      of the existing logical layers.

**Done when:** plan is agreed; no behavior change.

### Phase 1 — Seams inside `package main` (`refactor/issue-15-host-seam`)

Goal: connectors stop depending on `*Server` concrete type; policy types
exist. Still one package — pure mechanical + interface work.

1. Introduce `Host` interface + adapters on `*Server` (exported names or
   small wrappers so `getValidToken` → `GetValidToken` etc.).
2. Change every connector constructor to take `Host` (keep `NewX(srv *Server)`
   as a thin wrapper if tests churn too much — prefer not to).
3. Introduce `types` policy/model *file group* still in `package main`
   (`policy.go`, or split `model_*.go`) so the later move is a `git mv` +
   package rename.
4. Split `LibraryKind` into **data** (ID, Path, PermType, messages, keywords)
   and **runtime predicates** (`HasProviders`, `Readable`) owned by core
   registration — or keep predicates as `func(Host) bool`.
5. Replace `DataTypeForPath` giant switch with **kind-declared path patterns**
   (still evaluated in permissions/proxy): each kind/connector registers
   prefixes. Behavior must match current cases 1:1 (table-driven tests).

**Tests:** existing suite green; add focused tests for Host wrapper and path
pattern table parity.

**Done when:** `go vet`, race tests, 80% floor, staticcheck clean; no public
API change; connectors compile against `Host` only.

### Phase 2 — Extract `internal/types` (`refactor/issue-15-types-pkg`) — **done**

Move (and rename package):

| From | To |
|------|----|
| `models.go` | `internal/types/models.go` |
| `match.go`, identity/merge helpers | `internal/types/merge_*.go` |
| `librarykind.go` data + `Instructions`/`Mentions`/`CacheKey` | `internal/types/kind.go` |
| access levels + `TypePolicy` + redaction *rules* | `internal/types/policy.go` |
| `DataTypeCapability`, `Operation`, … | `internal/types/capabilities.go` |
| `ErrUnauthorized` and shared list status shapes | `internal/types/…` |

Leave in core: `handleLibraryAggregate`, `collectProviders`, `hasProvider`,
enforcement, cache keys generation if it needs Server.

**Cycles to break while moving:**
- `stamp*Canonical` / `stamp*Copies` stay in core (they need stores); kinds
  only declare that they participate.
- `entity_links.go` store can stay core; pure link-key functions go types.
- `musicbrainz.go` HTTP client stays core; identity preference helpers go types.

**Tests:** move merge/model/kind unit tests with the code. Keep integration
tests (aggregate, auth) in main/core tests.

**Done when:** `types` package has no import of `core` or `connectors`;
build + full suite green.

### Phase 3 — Extract `internal/connectors` (`refactor/issue-15-connectors-pkg`) — **done**

Move named providers and their setup:

`spotify.go`, `youtube.go`, `amazonmusic.go`, `google-*.go`, `immich.go`,
`immich_setup.go`, `ynab.go`, `ynab_setup.go`, `quicken.go`, `owntracks.go`,
`timeline.go`, `sample_calendar.go`, `mirror_spotify.go`, `mirror_youtube.go`
(mirror may straddle core write path — decide: mirror orchestration in core,
provider-specific remotes in connectors).

Registration in `main.go` stays the explicit list (or a `connectors.RegisterAll(host, srv)`).

**Done when:** connectors package does not import `main`; core does not import
connector types except via `ServiceConnector` interface (already in the seam).

### Phase 4 — Core residual + layout docs (`refactor/issue-15-core-pkg`) — **done**

- Move runtime into `internal/core` (Server, handler hook, proxy, oauth,
  tokens, admin API, overlays, metrics, state stores, sampledata, mail).
- Root `main.go` becomes flags + wiring + `//go:embed` for `web/`.
- Update **AGENTS.md** (layout, extension points, pitfalls, architecture map),
  **README.md** project structure, **docs/adding-data-types.md** checklist paths.
- Keep `docs/adding-data-types.md` as the migration guide; rewrite file paths
  to packages, not root `*.go`.

**Done when:** AGENTS.md no longer claims single-package root layout;
`go build -o aegis .` works; CI unchanged commands work.

### Phase 5 — #148 type-level permissions (`feat/issue-148-type-permissions`) — implemented

1. Persist type policy as primary (`permissions.json` schema version bump):
   `typeID → TypePolicy`.
2. Enforcement order: type default → provider override → redaction/attributes.
3. `collectProviders` / `libraryReadableFor` read type policy first; provider
   override can only **further restrict** (clamp, same as admin today).
4. Admin API + UI: edit by data type (primary) and optional per-service
   override (secondary).
5. Pre-request injection uses type policy (visibility of kinds).
6. AGENTS.md updates required by the issue comment:
   - “Permissions are data” → type-level policy (+ optional provider filter).
   - Architecture map points at type schema / policy home.
   - Pitfalls: “service permissions dual-path” if compatibility window remains.
7. Migration: read legacy `services[].permissions` as provider overrides until
   a version flag / data migration rewrites the overlay.

**Done when:** README + example config show type policy; tests cover default,
provider override, redaction levels, places opt-in, clamp-writes; no fail-closed
regressions.

### Phase 6 — #15 user-defined types (`feat/issue-15-user-defined-types`)

Only after Phases 2–5. Suggested spike then feature:

1. **Spike:** type schema JSON (fields, list envelope, identity rules, merge,
   path patterns for a simple HTTP+JSON source). Written as a design note
   under `docs/` or the issue — not production code.
2. **Runtime kinds:** register schema → `LibraryKind` + generic
   `handleLibraryAggregate` encode/merge (name-normalize merge first; durable
   IDs later).
3. **Generic connector:** `HTTPSourceConnector` mapping config → neutral items
   + `sources[]`; tokens stay in Aegis if OAuth; otherwise header/secret refs
   via existing secrets path.
4. **Permissions:** UDTs get `TypePolicy` like built-ins; default `none` until
   enabled (reuse places opt-in pattern).
5. **Admin:** define/edit/list UDTs; no media storage; fail-open fetch.
6. **Sample data:** deterministic fixtures for one UDT end-to-end.
7. Checklist in `docs/adding-data-types.md` gains a “user-defined kind”
   section with the schema file example.

**Explicitly out of scope for v1 UDT:** auto-detected scrapers, photo
ownership proofs (#20), multi-user (#149), replacing named connectors.

---

## 5. What to do first (immediate next actions)

1. Agree package names (`internal/types`, `internal/core`, `internal/connectors`)
   and `core.Host` home.
2. Land **Phase 0 + Phase 1** on a `refactor/issue-15-host-seam` worktree —
   this is the only step that unblocks everything else and is reviewable alone.
3. File the schema spike as a child of #15 once Phase 1 is in `main`.
4. Start #148 implementation only after `TypePolicy` exists (Phase 1 can
   already add the types without flipping persistence).

### Suggested first PR title

`refactor: Host seam + path patterns + TypePolicy types (area 1 of #15)`

### Out of scope for the restructure

- Behavior changes to merge, cache, OAuth, or fail-open
- New providers (Apple Music #144, M365 #145, CalDAV #146, Documents #143)
- Lowering coverage, `t.Skip`, or expanding fuzz corpus “for later”
- Rewriting admin HTML

---

## 6. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Import cycles Server ↔ kinds ↔ connectors | Host seam first; types pure; core owns runtime predicates |
| 27k-line test churn | Phase 1 stays in-package; move tests only with the source they cover |
| Path classification regressions | Table-driven parity tests for every `DataTypeForPath` case before the switch |
| Permission dual-path confusion (#148) | Document compatibility window; clamp overrides; AGENTS.md pitfalls row |
| Coverage floor noise from moved files | Keep statement coverage identical; run full CI shape each PR |
| Scope creep into UDT feature | Phases 5–6 gated; Phase 1–4 accept no product change |
| `mirror_*` / setup flows straddling layers | Setup HTTP in core; provider-specific bodies in connectors; write-path tests stay with core |

---

## 7. Definition of done (restructure complete)

- [ ] `internal/types`, `internal/core`, `internal/connectors` exist with the
      dependency rule above (enforced by `go list` / review, optionally a
      test that connectors do not import core internals).
- [ ] Connectors depend on `Host` + `types`, not `*Server`.
- [ ] `LibraryKind` data and type policy live in `types`; enforcement in `core`.
- [ ] AGENTS.md + README + adding-data-types.md describe the new layout.
- [ ] `go vet ./...`, `go test ./... -count=1 -race`, coverage ≥ 80%,
      staticcheck clean, `go build -o aegis .`.
- [ ] No secrets in the diff; fail-open and OAuth ownership unchanged.

After that, #148 and #15 are product PRs on a stable layer, not layout PRs.
