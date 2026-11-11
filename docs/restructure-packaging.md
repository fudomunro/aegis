# Plan: Restructure packaging (admin / library / syncjobs)

> Design record for issue #358. **This document freezes the target layout**
> for the move campaign. Package placement is governed by "Package boundaries"
> in [`AGENTS.md`](../AGENTS.md), which now states the target; the per-package
> import table in `internal/core/churn_guardrails_test.go` is the ratchet and
> keeps matching the **tree**, not the target — every move PR updates the
> table in the same commit as the move. Do not re-open a settled home
> mid-campaign; append questions here instead. Precedent for a record like
> this: [`restructure-core-types-connectors.md`](restructure-core-types-connectors.md).

Status: **in progress** — Phase 0 (rules + this record), P1 (seam
vocabulary into `internal/shared`), and P2 (`internal/library` + engines)
are landed. P3–P5 pending.

Related issues: **#358** (this restructure), **#346** (hookhealth/inject
extraction — their final homes are settled here), **#325** (dissolution of
`internal/types`), **#280** (shared plumbing owner), **#227** (sync job queue
— the `syncjobs` name), **#312** (kind-owned routes, `Host` seam).

Related product rules: kind-centric surface; named-service connectors;
fail-open; Aegis owns OAuth; coverage floor 80%. This restructure is
structure only — no route, model, config-schema, or behavior changes.

---

## Decision note

**1. What is this a property of?**
Aegis-internal metadata — the module layout. Not a connected service, not a
library kind, not a data grant, not a reconciliation policy.

**2. What is the canonical name?**
**Hub** (`internal/core`), **surface package** (`internal/admin`,
`internal/library`, `internal/syncjobs`), **seam vocabulary**
(`internal/shared`), **engine** (self-contained store/runtime behind a seam).
Added to [`glossary.md`](glossary.md) in this change. The sync root package
is `internal/syncjobs`: the domain label stays "sync", but the package name
avoids shadowing stdlib `sync`, which the worker code is the heaviest
importer of.

**3. What is explicitly out of scope?**
Product behavior, HTTP routes, neutral models, config fields, the
`ServiceConnector`/`Host` seam shapes, and the name of the hub. This is not a
rename of `internal/core` and not a re-homing of `internal/kinds`'
merge pipeline.

---

## Problem

The hub root package (`internal/core`) has absorbed three audiences: admin
handlers, agent data-serving handlers, and sync runners sit side by side in
66 non-test files (~14.6k lines), wired through one `*Server`. Meanwhile
`internal/kinds` imports four `core/` sub-packages (`policy`, `scopes`,
`audit`, `search`) — dependency edges the seam design does not want.

A survey of every non-test hub file classified them: **16 admin**, **16
library**, **6 sync**, **24 genuine hub wiring/perimeter**, **4
misplacements**, and **8 mixed files** needing splits (the hardest:
`handler.go`, `syncrunner_mail.go`/`mail_archive_http.go`, `cache.go`).

## Target map (frozen)

Dependency direction, bottom to top: `internal/shared` → `internal/kinds` →
`internal/connectors` → surface packages → hub → `main`. Only `main`
packages import the hub; surfaces import domain packages, `shared`, and
`core/` engine sub-packages — never the hub.

### Surface packages

The three packages follow **audiences**, matching the listener split and
[`audience-map.md`](audience-map.md):

| Package | Absorbs from the hub root | Engines it owns |
|---------|---------------------------|-----------------|
| `internal/admin` — management surface | `admin.go`, `apps_admin.go`, `apps_grant.go`, `datascope_admin.go`, `flow.go`, `import_admin.go`, `mail_archive_http.go` (admin half), `onboarding.go`, `ownership.go` (handlers), `permissions_overview.go`, `quicken_admin.go`, `searchindex.go` (rename → `searchindex_admin.go`; it is admin status only), `signal_admin.go`, `syncjobs_admin.go`, `timeline_admin.go`, `udk_admin.go` | `core/apps`, `core/udk`, `core/scopes` (store half) |
| `internal/library` — agent data plane | `agentpaths.go`, `pathclass.go`, `librarykind.go`, `udk_handlers.go`, `canonical.go` (notices half), `grants.go` (enforcement + agent copy), `copy_ledger.go` (record half), `copy_ledger_http.go`, `entity_links_handlers.go`, `mirror.go`, `mirror_entity.go`, `photos_share.go`, `proxy.go` (handler half) + kind composition (see amendments) | `core/search` → `library/search`, `core/links` → `library/links` |
| `internal/syncjobs` — background consistency | `syncfreshness.go`, `syncfreshness_runner.go`, `syncrunner_canary.go`, `syncrunner_mail.go`, `syncrunner_mirror.go`, `maintenance.go` (collector half), `copy_ledger.go` (`recordTakeoutSnapshot`) | `core/syncjobs` (promoted), `core/copies` |

`internal/syncjobs` serves no HTTP routes; its admin read model lives in
`internal/admin` (`syncjobs_admin.go`) and calls the engine.

### Hub (stays)

`run.go` (wiring; CLI subcommands leave), `routes.go`, `listener.go`,
`identity.go`, `clientip.go`, `httphardening.go`, `ratelimit.go`, `csrf.go`,
`config.go`, `datadir.go`, `secrets.go`, `service_connections.go`,
`data_grants_overlay.go`, `oauth.go`, `connections.go`, `health.go`,
`brand.go`, `version.go`, `demo.go`, `host.go`, `mail_ingest.go`,
`hook_skip.go`, `hookhealth.go`, `handler.go` (split in place, see below),
and the credential/observer engines shared by more than one surface:
`core/policy`, `core/tokens`, `core/audit`, `core/metrics`, `core/mail`,
`core/ownership`, `core/sampledata`, `core/inject`, `core/hookhealth`.

### Shared (grows)

Seam vocabulary moves here so `internal/kinds` can drop every `core/`
import (this is the issue's second clause). Landed in P1, with two shape
refinements recorded at implementation time:

- **Grant vocabulary** — `GrantMap` (with its full pure method set:
  `Allows*`, `Level`, `AllowsKindMethod`, `PrefillDefault`, `Clone`,
  `AllowedKinds`/`DeniedKinds`), the access-level constants, and
  `NormalizeAccess` / `AllowsMethod` / `IsAccessLevel`. Stored grant
  *resolution* stays in `core/policy` (`DataGrant`/`DataGrants` and the
  `Levels()` materializer — methods live with their type).
- **Dataset-scope vocabulary** — the **request view** moves, not the store
  record: `shared.DataScope` (the dataset partition) plus `WithDataScope`,
  `DataScopeFrom`, `Dataset`, `ServiceVisible`. The stored record
  (`scopes.Scope` — credentials, `policy.DataGrants`) stays hub-side in
  `core/scopes` because a stdlib-only leaf cannot reference `core/policy`.
  `scopes.WithDataScope` attaches the record and the view together, so the
  nil-vs-empty-dataset distinction (owner principal vs default partition)
  is preserved exactly.
- **Search shapes** — `SearchDocument`, and — following shared's
  domain-prefix convention (`MailIngest*`, `Ownership*`) — `SearchQuery`
  and `SearchResult` (the freeze sketch said `Query`/`Result`).
  `internal/kinds` names a `SearchIndex` interface instead of
  `*search.Index`; the engine stays the concrete implementation, wired
  hub-side.
- **Generic bounded TTL cache** — the stdlib-only half of `cache.go`
  (P2, with the split).
- Audit decision constants needed no move: `internal/kinds` declares its
  own `DecisionAllow`/`DecisionDeny` on the `Host` seam and now uses those.

After P1: `internal/kinds` imports `internal/shared` only;
`internal/connectors` needs no change (it already imports only `shared` and
`kinds` — the `core/policy` row in the guardrails table was aspirational
and is dropped).

### Mixed-file splits

| File | Split |
|------|-------|
| `handler.go` | `AuthMiddleware` (`internal/core/handler.go:253`) and `AdminMiddleware` (`internal/core/handler.go:367`) stay in the hub as `auth.go`; `HandlePreRequest` (`internal/core/handler.go:721`) stays in the hub as `hook.go`. Both halves are perimeter — neither joins a surface package |
| `cache.go` | Generic TTL cache → `internal/shared` (**landed P2**); the invalidation helpers stay in the hub — every caller (admin, OAuth, onboarding, sync) is hub-side and they only work the shared cache |
| `copy_ledger.go` | `copyInfoFor` stays in the hub — it is the body of `kinds.Host.CopyInfo` (amendment below); `recordMirrorCopy` / `recordPhotoShareCopy` + `mirrorEntityKey` → `internal/library`; `recordTakeoutSnapshot` (sync bookkeeping) → `internal/syncjobs` |
| `mail_archive_http.go` | Admin status/sync/import handlers → `internal/admin`; `mailReadable*` grant gate and `mailArchiveHints` hook hints → `internal/library` |
| `maintenance.go` | Signal collector / bundle writer → `internal/syncjobs`; `HandleAdminMaintenanceSignals` → `internal/admin` |
| `metrics.go` | Access-event feeding stays hub wiring; `HandleMetrics` (admin-scoped) → `internal/admin` |
| `ownership.go` | Admin confirm/reopen handlers → `internal/admin`; evidence derivation → an engine beside `core/ownership` |
| `run.go` | CLI subcommands (`runSeed*`/`runImport*`, `internal/core/run.go:840`–) → `cmd/` |
| `canonical.go` | `validateCanonicalFor` (config validation) stays in the hub; request-time write-policy notices → `internal/library` |
| `syncrunner_mail.go` | `doMailIngest` stays **one** ingest walk in `internal/syncjobs`, serving both the queue runner and the admin sync route — no duplicated walk |

### Misplacements

| File | Destination |
|------|-------------|
| `musicbrainz.go` | `internal/kinds` — MBID lookup is merge-domain, stdlib HTTP + kinds models only |
| `maturity.go` | `internal/connectors` — static catalog beside `connectors/maturity.go` |
| `fakeupstream.go` | `cmd/` or a dev build tag |
| `httpclient.go` | fold into `core/host.go` or `internal/shared` |

## Settled calls

These are decided; changing one re-opens the freeze.

1. **`internal/syncjobs`, not `internal/sync`.** The package name would
   shadow stdlib `sync` — in the worker code that imports stdlib `sync` the
   most. The domain label stays "sync" in docs and issue titles.
2. **The pre-request hook stays in the hub.** `inject`, `hookhealth`,
   `hook_skip`, and `HandlePreRequest` are perimeter (Aperture ↔ LLM request
   surgery), not data serving. They landed in their current homes under
   #346; moving them again would be the churn this record exists to prevent.
   `internal/library` stays strictly the data plane.
3. **Dual-use engines stay in `core/`.** `policy`, `tokens`, `audit`,
   `metrics`, `mail`, `ownership`, `sampledata` serve more than one surface
   and keep their homes. Single-surface engines move under the surface that
   owns them (`search`/`links` → library, `apps`/`udk`/scopes store → admin,
   `copies`/sync queue → syncjobs). Alternative considered and rejected:
   promoting every engine to its own root-level package (~25 root packages).
4. **OAuth flows and `service_connections.json` stay in the hub** (the
   credential perimeter — "Aegis owns OAuth"). Admin owns only the UI that
   drives them.

## Implementation amendments (P2)

Recorded at implementation time; the homes above stand, these refine the
file-level rows:

1. **`kinds_host.go` splits, it does not move wholesale.** The
   `kinds.Host` implementation (plus `ownerGrants`, `requestGrants`,
   `copyInfoFor`, and the `mailArchive` adapter) stays in the hub —
   Package boundaries #4 assigns Host implementations to the hub, and a
   second seam under the adapter would be indirection for no gain. The
   *composition* half (kind registry wiring, engine accessor, route
   resolution, `userDefinedDeps`) moves to `internal/library/registry.go`.
2. **`copyInfoFor` stays hub-side** — it is the body of `Host.CopyInfo`.
3. **Cache invalidation stays hub-side** — every caller is admin/oauth/
   onboarding/sync, and the helpers only operate the shared cache.
4. **`proxy.go` splits**: the content-transport handler moves to
   `internal/library`; `GetValidToken` stays — it implements
   `connectors.Host` (the credential seam).
5. **`kinds.ServiceView` gains `Description`** — the content transport's
   block-deny copy needs it; one neutral string field on the existing view.
6. The `handler.go` split (into `auth.go` + `hook.go`) is **P5**, as its
   own row says — P2 needed no part of it; the hook simply calls the copy
   builders `internal/library` exports.

## Move phases

Each phase is one `refactor/…` branch and PR; tests move with the code; the
guardrails table updates in the same commit; the 80% floor never bends.

| Phase | Scope | Rules/tests touch |
|-------|-------|-------------------|
| **P0** | This record, `AGENTS.md` package boundaries, [`glossary.md`](glossary.md) | none (docs only) |
| **P1** | Seam vocabulary → `internal/shared`; `internal/kinds` drops `core/policy`, `core/scopes`, `core/audit`, `core/search` imports; `SearchIndex` interface lands in kinds — **landed** | `internal/kinds` row shrinks to `{shared}`; connectors row drops `core/policy` |
| **P2** | `internal/library` + `core/search`, `core/links`; splits of `cache.go`, `copy_ledger.go`, `canonical.go` — **landed** (the `handler.go` split is P5; see amendments) | `internal/library` row added |
| **P3** | `internal/admin` + `core/apps`, `core/udk`, scopes store; `searchindex.go` rename | add `internal/admin` row |
| **P4** | `internal/syncjobs` (promote `core/syncjobs`) + runners, freshness, maintenance, `core/copies` | add `internal/syncjobs` row; drop `core/syncjobs` |
| **P5** | Hub cleanup: `run.go` CLI → `cmd/`, `musicbrainz.go`, `maturity.go`, `fakeupstream.go`, `httpclient.go`, `handler.go` → `auth.go` + `hook.go` | table shrinkage |

## Out of scope

- Behavior, routes, neutral models, config fields, or admin copy changes.
- Widening `ServiceConnector`, `connectors.Host`, or `kinds.Host`.
- Renaming the hub (`internal/core` keeps its name) or dissolving
  `internal/kinds`.
- New external dependencies. Coverage floor, fail-open, and the secrets
  rules are invariants across every phase.

## Open questions

Recorded here rather than re-decided in a move PR:

- Whether `core/ownership`'s evidence derivation becomes its own engine
  package or stays beside the store (P2/P3 judgement).
- Whether `core/sampledata` should move beside `internal/connectors` when
  the sample connector is next touched. Not blocking.
