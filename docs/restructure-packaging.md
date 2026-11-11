# Plan: Restructure packaging (admin / library / syncjobs)

> Design record for issue #358. **This document freezes the target layout**
> for the move campaign. Package placement is governed by "Package boundaries"
> in [`AGENTS.md`](../AGENTS.md), which now states the target; the per-package
> import table in `internal/core/churn_guardrails_test.go` is the ratchet and
> keeps matching the **tree**, not the target — every move PR updates the
> table in the same commit as the move. Do not re-open a settled home
> mid-campaign; append questions here instead. Precedent for a record like
> this: [`restructure-core-types-connectors.md`](restructure-core-types-connectors.md).

Status: **in progress** — Phase 0 (rules + this record) on branch
`docs/358-package-boundaries`; code moves P1–P5 not yet started.

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
| `internal/library` — agent data plane | `agentpaths.go`, `cache.go` (invalidation half), `canonical.go` (write-policy notices), `copy_ledger.go` (`copyInfoFor`), `copy_ledger_http.go`, `entity_links_handlers.go`, `grants.go`, `kinds_host.go`, `librarykind.go`, `mirror.go`, `mirror_entity.go`, `pathclass.go`, `photos_share.go`, `proxy.go`, `udk_handlers.go` | `core/search`, `core/links` |
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
import (this is the issue's second clause):

- **Grant vocabulary** — `GrantMap`, the access-level constants, and the
  trivial pure lookups (`AllowsMethod`, `Allows`, `Level`). Grant
  *resolution* over stored configuration stays in `core/policy`.
- **Dataset-scope vocabulary** — `Scope`, `From`, `Dataset`, `ServiceVisible`.
  The `data_scopes.json` store and invite-token hashing stay in
  `core/scopes`.
- **Search shapes** — `SearchDocument`, `Query`. `internal/kinds` names a
  `SearchIndex` interface instead of `*search.Index`; the engine stays the
  concrete implementation, wired hub-side.
- **Generic bounded TTL cache** — the stdlib-only half of `cache.go`.
- Audit decision constants need no move: `internal/kinds` already declares
  its own `DecisionAllow`/`DecisionDeny` on the `Host` seam; it should use
  those and drop the `core/audit` import.

After P1: `internal/kinds` imports `internal/shared` only;
`internal/connectors` needs no change (it already imports only `shared` and
`kinds` — the `core/policy` row in the guardrails table is aspirational and
can be dropped).

### Mixed-file splits

| File | Split |
|------|-------|
| `handler.go` | `AuthMiddleware` (`internal/core/handler.go:253`) and `AdminMiddleware` (`internal/core/handler.go:367`) stay in the hub as `auth.go`; `HandlePreRequest` (`internal/core/handler.go:721`) stays in the hub as `hook.go`. Both halves are perimeter — neither joins a surface package |
| `cache.go` | Generic TTL cache → `internal/shared`; library invalidation (`invalidateLibrary`) → `internal/library` |
| `copy_ledger.go` | `copyInfoFor` (library response stamps) → `internal/library`; `recordTakeoutSnapshot` (sync bookkeeping) → `internal/syncjobs` |
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

## Move phases

Each phase is one `refactor/…` branch and PR; tests move with the code; the
guardrails table updates in the same commit; the 80% floor never bends.

| Phase | Scope | Rules/tests touch |
|-------|-------|-------------------|
| **P0** | This record, `AGENTS.md` package boundaries, [`glossary.md`](glossary.md) | none (docs only) |
| **P1** | Seam vocabulary → `internal/shared`; `internal/kinds` drops `core/policy`, `core/scopes`, `core/audit`, `core/search` imports; `SearchIndex` interface lands in kinds | `internal/kinds` row shrinks to `{shared}`; connectors row drops `core/policy` |
| **P2** | `internal/library` + `core/search`, `core/links`; splits of `cache.go`, `copy_ledger.go`, `canonical.go`, `handler.go` | add `internal/library` row |
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
