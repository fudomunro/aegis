# AGENTS.md — Aegis

Instructions for coding agents working in this repository. Read this before
touching code, config, or docs. `README.md` holds the full product/API surface;
`SECURITY.md` holds the tailnet trust model. Canonical English for product
concepts is in [`docs/glossary.md`](docs/glossary.md) (issue #174). This file is
about how work actually happens here and which product rules must not drift.

Note on the word *agent*: product docs mean the **LLM agent** that calls
`/v1/library/*`; this file and `AGENTS.md` mean the **coding agent** or
maintainer editing the repo. Prefer those labels over a bare “agent”.

## What this project is

A private Go service that sits next to [Tailscale Aperture](https://tailscale.com/docs/aperture).
It does two jobs:

1. **Pre-request hook** (`POST /v1/pre-request`) — detect named services in an
   LLM request and inject instructions that point the agent at Aegis’s
   library APIs (and, only where needed, content/ingest transport).
2. **Unified library + content transport** — agents call
   `/v1/library/{kind}` for user data. `/v1/services/{id}/...` is content/ingest
   only (photo bytes, full message bodies, playlist items, local import; issue
   #119) — not a general provider-API mirror. Aegis attaches OAuth tokens,
   caches, enforces data grants, and returns neutral models.

Module: `github.com/fudomunro/aegis`. Root `main.go` is thin (`core.Run()`).
Package layout is governed by the principles in **Package boundaries** below —
there is no closed list of packages. `docs/restructure-core-types-connectors.md`
is a historical record of the #15 split, not the layout contract.  
Runtime: Go **1.27.1** (`go.mod`); external deps: `golang.org/x/crypto`, `modernc.org/sqlite` (local mail archive FTS)

## Non-negotiable product rules

These come from explicit user decisions across sessions. Do not “improve”
them away.

- **Kind-centric surface; named-service connectors.** The product API is
  organized around kinds (`/v1/library/{kind}`, neutral models,
  `LibraryKind`) — including user-defined kinds (#15) — not provider-shaped
  endpoints. Named services (Spotify, YouTube Music, Amazon Music,
  Google Photos/Contacts, Immich, …) remain the first-class **connectors** and
  match targets; do not prioritize generic scrapers or ad-hoc type detection
  over them. When provider-shaped APIs and type-level semantics conflict,
  prefer the type-level design. `/v1/services` is content/ingest transport
  only (issue #119) — do not reintroduce provider list mirrors, playback,
  catalog search, or generic OAuth pass-through as agent API.
- **Fail-open.** Fetch failures, missing endpoints, bad payloads, and
  non-actionable match results must **pass through unchanged**. The hook
  returns `allow` on parse/log errors. Do not invent fail-closed behavior.
- **Aegis owns OAuth.** Agents never see tokens and must never be prompted
  to authorize services directly. Tokens live encrypted on disk; proxy
  auto-refreshes; admin/API responses expose presence/scopes/expiry only.
- **Connections, not per-surface OAuth.** Surfaces reference a top-level
  `connections` entry (`connection` + `oauth_scopes`). Do not reintroduce
  per-service inline OAuth dual paths.
- **Aegis does not store media.** Canonical content stays on the source
  service. `canonical_for` marks the non-Aegis home per library kind.
- **Coverage floor stays.** CI fails below **80%** statement coverage. Raise
  the floor when the suite grows; do **not** lower it to land untested write
  paths.
- **Secrets stay out of git.** Never commit `aegis.json`, `tokens.json`,
  `entity_links.json`, `data_grants.json`, `apps.json`, `data_scopes.json`,
  `service_connections.json`, `user_kinds.json`, `ownership.json`,
  `.aegis-token-pass`, or live
  binaries. Example config only via `aegis.example.json`. Prefer the OS data
  directory (issue #74) over the git worktree for runtime state.
- **External access is invite-only and split-listener.** The API listener may
  be published (credential perimeter: **invite tokens and app tokens only** —
  the owner-tier agent key never crosses the public bind); the **admin
  listener stays in-tailnet** and is never published (it also serves agent
  routes for tailnet callers, so Aperture and tagged hosts keep ambient
  identity with no migration). Ambient Tailscale identity is never trusted
  on an externally reachable listener. External principals are
  **deny-by-default** (scope grants are the complete allow list; resolution
  is fail-closed in a dedicated resolver). The API route set is an
  **allowlist** — unlisted routes 404 and default to the admin side. Hash
  at rest for invite tokens is a contract requirement. No self-service
  signup, passwords, OIDC, or instance federation in this phase. See
  `docs/external-access.md` and `SECURITY.md`.
- **Core never names a built-in kind.** Built-in kinds declare their routes,
  filters, redaction, and stamping beside their data; the hub does not switch
  on kind IDs. Per-kind code that legitimately remains in the hub (writes,
  ownership derivation, sync runners) sits behind a per-file allowlist that
  may only shrink. Enforced by `kind_literal_hygiene_test.go`.
- **Kind routes declare their exposure.** Every kind-owned route carries its
  visibility (agent API, third-party app token) in its own declaration, and
  the API-listener / app-token allowlists derive from those declarations.
  Prefix-based allows must not grow (#312).
- **No issue numbers in user-facing copy.** GitHub issue refs stay in code
  comments and maintainer docs only — never README headings, admin UI visible
  text, or LLM-agent strings. Enforced by `internal/core/docs_hygiene_test.go`
  and `agent_string_hygiene_test.go`. History belongs in `CHANGELOG.md`.

## Architecture map

```
Agent → Aperture → Aegis pre-request hook → LLM
Agent → Aegis library / content transport / admin → provider APIs
```

| Area | Where | Notes |
|------|-------|-------|
| Packaging restructure (in progress) | [`docs/restructure-packaging.md`](docs/restructure-packaging.md) | Target layout: `internal/admin` / `internal/library` / `internal/syncjobs` surface packages + shared seam-vocabulary growth (issue #358). "Package boundaries" states the target; the churn-guardrails table ratchets the tree as code moves |
| Entry, flags, routes, connector registration | `internal/core` hub (`main.go` is thin) | Wrap nearly every route in `AuthMiddleware` |
| Data directory + state path layout | `internal/core` hub | Defaults under OS app data dir; explicit path flags override (issue #74) |
| Config + matching | `internal/core` hub (`Config`) | Flat `services[]` + top-level `connections`; loads bootstrap + default overlay path |
| Admin permissions overlay | `internal/core` hub | `data_grants.json` v3: `data_grants` (primary DataGrant); admin saves never rewrite aegis.json |
| Data scopes (principal → dataset) | `internal/admin/scopes/`, `internal/shared` (request view), `docs/data-scopes.md` | `data_scopes.json` (0600): API key → dataset partition + optional tightened grants. Isolation seam for multi-user / demo. Scope grants are deny-by-default for invite tokens. The admin surface owns the store; the dataset-partition view crosses into the kinds pipeline as `shared.DataScope` |
| External access (listeners, invite tokens) | `docs/external-access.md`, `SECURITY.md` | Split listeners: API (publishable, credential perimeter) vs admin (in-tailnet). Contract for issue #149 multi-user |
| Pre-request injection | `internal/core/inject` (engine: wire-shape detection + JSON surgery), `internal/core` hub (`handler.go` hook) | `allow` / `block` / `modify`; structured `Operation` contract |
| Hook-health engine | `internal/core/hookhealth/` (engine), `internal/core` hub (`hookhealth.go` wrapper, admin handlers) | Ring of redacted hook observations + Aperture config checks (`/v1/admin/hook-health`); `hook_skip.go` stays in the hub |
| ServiceConnector interface | `internal/connectors/host.go` | `Capabilities` + `SystemInstructions` + `HandleProxy`; the hub wires the registry |
| Generic/shared content transport | `internal/library` (`proxy.go` handler), `internal/connectors` (upstream transport), `internal/core` hub (`GetValidToken` — the `connectors.Host` credential seam), `internal/shared` (HTTP client) | Prefer shared helpers over copy-paste `do*Request`; `IsAgentContentPath` gates agent-facing `/v1/services` paths. Tokens never leave the hub |
| Shared plumbing + seam vocabulary (HTTP client, page cap, JSON writers, URL/JSON/math/file/path utils, kind-rule table, body-shape tokens; capability/onboarding/mail-ingest/ownership seam shapes) | `internal/shared` | Stdlib-only imports. One owner: plumbing and the shapes two internal packages exchange to agree on a contract — shapes and trivial pure constructors/accessors/predicates over them (the `kindrules.go` table), no state, no I/O, no configuration resolution, no `Host`, no service registries, no provider paths (issue #280, amended #325). Do not re-grow twins in `core` or `connectors` |
| Tokens / OAuth / CSRF | `internal/core/tokens/`, `internal/shared` (state store), `internal/core` hub (flows) | AES-256-GCM + PBKDF2; refresh singleflight |
| Sync job queue / worker / schedules | `internal/core/syncjobs/` (engine), `internal/core` hub (`syncjobs_admin.go`, `syncrunner_*.go` runners) | Durable SQLite-backed jobs (issue #227). The engine takes a `SyncMetrics` seam; hub wiring and runners stay in core |
| Access + Prometheus metrics engines | `internal/core/metrics/` (engines), `internal/core` hub (`metrics.go` handlers/derivation) | `AccessMetrics` ring + `PromMetrics` text export. The hub feeds them from request handlers; `PromMetrics` satisfies `syncjobs.SyncMetrics` |
| Sample/demo datasets + sample:// connector | `internal/core/sampledata/`, `docs/development.md`, `docs/demo-datasets.md` | Deterministic fixtures (generic + LotR/Lincoln packs), mbox/JSON writers, `SampleLibraryConnector`. Demo lockdown validation stays in the hub (`demo.go`) |
| UDK registry + admin overlay | `internal/core/udk/` (registry/loader + `user_kinds.json` overlay), `internal/admin` (`udk_admin.go`), `internal/library` (serving glue) | User-defined kinds (issue #15). Schema packs under `user-kinds/` load into the same pipeline. The registry stays a `core/` engine — the data plane's kind composition consumes it |
| Apps registry + consent grants | `internal/admin/apps/` (store + grant codes), `internal/admin` (`apps_grant.go`, `apps_admin.go`) | Third-party app tokens (issue #131). The admin surface owns registration, consent, and revocation; the hub's auth reads the minted identities |
| Neutral models + kind data | `internal/kinds` | Models, merge, and kind registry metadata; authorization vocabulary is `internal/shared` and stored grant resolution is `internal/core/policy` — never here |
| Library kinds + aggregate pipeline | one file per kind in `internal/kinds` (see Extension points) | Kinds own their routes, pipeline calls, and stamping over the `Host` seam; `internal/library` owns the fixed cross-kind routes, kind composition, and agent-facing copy; admin writes stay admin-side |
| Agent data plane | `internal/library` (+ `library/links`, `library/search`) | Fixed `/v1/library/*` write/maintenance routes, `/v1/services` content transport, kind registry composition, agent-facing instruction copy. The hub constructs `library.Handlers`, mounts its route map behind auth, and implements its `kinds.Host` seam |
| Management surface | `internal/admin` (+ `admin/apps`, `admin/scopes`) | `/v1/admin/*`, the admin UI shell, setup flows, admin-owned writes (data grants, apps, data scopes, UDK overlay, connections, ownership), and the admin read models. The hub constructs `admin.Handlers`, mounts its routes behind the admin perimeter, and implements its `admin.Host` seam; the surfaces never import each other |
| Merge / entity IDs | `internal/kinds` (merge), `internal/core/links/` (entity-link store) | ISRC/UPC/MBID preferred over name |
| Connectors | `internal/connectors` (one file per named service; local import stores share the surface) | Register in the hub's ordered `srv.connectors` list; stores use a local-transport constructor (design: `docs/local-import-stores.md`) |
| Location history (places) | `internal/connectors` (timeline, owntracks), the places kind | Visits/trips, city/read/precise redaction (issue #142); default `none` |
| Admin UI | `internal/admin/web/admin.html` via `//go:embed` | Keep SPA markup out of Go handlers; setup panels render connector-declared specs (issue #265). Brand/demo assets stay in `internal/core/web/` |
| Onboarding runner | `internal/core` hub (host), `internal/shared` (shapes), impls beside each connector | Generic `/v1/admin/setup/{id}` host over `connectors.Onboarder` (design: `docs/connector-onboarding.md`) |
| Docs walkthrough | `experiments/index.html` | Experimental walkthrough under `experiments/` (not CI-gated); stamp with current commit when regenerated |
| Maintenance tooling (dashboard, scanner, tickets) | `cmd/aegis-dashboard` | Separate tool (issues #335, #344): builds the static sync-maintenance dashboard from the repo's issue record, the services manifest, captured signal bundles (`build -bundles`), and the connector metadata catalog; `scan` runs the maintenance scanner that opens sync-failure tickets. Instance-level captures stay on the admin surface (`/v1/admin/maintenance/signals`); the application code contains no dashboard, scanner, or ticket surface |

### Extension points agents must use

**New library kinds** are a `BuiltinLibraryKinds` entry in
`internal/kinds/librarykinds.go`, an `internal/kinds/<kind>.go` with
declared routes / filters / redaction / stamping, and one line in the
ordered `kinds.Builtin()` list — no `internal/core` edits. Do not clone an aggregate pipeline. Checklist:
`docs/adding-kinds.md`.

**User-defined kinds** (`docs/user-defined-kinds.md`, issue #15) are schema
packs under `user-kinds/` loaded into the same pipeline. A pack can never
self-enable, shadow a built-in `id`/`perm_type`/`path`, or store secrets in
`http_json` headers. Local file selection is the `user_kinds.json` overlay.

**New or changed service behavior** goes through `ServiceConnector`
(interface: `internal/connectors/host.go` — read the source; copies in docs
drift and are not kept here).

- `KindCapability.Operations` is `[]Operation` (method, path, parameters,
  response spec) — not `[]string`.
- Empty `[]Operation{}` means “type recognized, not implemented”.
- New library kinds plug into the `kinds.BuiltinLibraryKinds` registry (see
  Extension points above for the full target shape).
- Connectors must read **configured** `GetService(id).Endpoint` — never hardcode
  upstream base URLs (breaks mock tests and custom endpoints).
- Prefer `connectors.RunProxy` / `connectors.DoUpstreamRequest` / `shared.HTTPClient`
  over new `http.DefaultClient` call sites.
- **Connectors depend on `Host`** (`host.go`) — not `*Server`, and never the
  core hub. Model packages `internal/kinds` and `internal/shared` are
  allowed (neutral models, access-level vocabulary). That seam
  is how named services leave package main under #15. Extend `Host` only for
  cross-connector runtime needs; provider-specific stores are constructor
  args (see `NewQuickenConnector`).
- Proxy path → kind classification is the ordered table in
  `internal/core/pathclass.go`. Do not reintroduce a `switch` or a second
  classifier. Agent-facing `/v1/services` paths must pass
  `types.IsAgentContentPath` (issue #119).

**Advanced setup flows** go through the optional `Onboarder` interface
(`internal/connectors/host.go` — read the source; design:
`docs/connector-onboarding.md`). Immich and YNAB are the worked examples.

- The spec is the contract: fields, copyable guides (prose/command blocks,
  declared steps), ladder and checklist labels, and the verbs the flow runs.
  Every provider-visible string lives there or in the status the connector
  computes.
- **A connector never ships HTML or JS** and never writes Aegis tokens or
  config — save/apply verbs return `types.StateUpdate` values core alone
  persists (`SetServiceConnection`, `SetTailscaleAdmin`).
- Core hosts the flow generically (`internal/core/onboarding.go`,
  `/v1/admin/setup/{id}`): admin routes and rendering, secret writes, the
  permissions perimeter. The panel and the stepped walkthrough render from
  the spec; `TestAdminHTML_RestatesNoOnboardingCopy` rejects any spec string
  in `web/admin.html`.
- Status is derived per request (ladder, checks, hints, probes) and the
  runner recomputes it after applying updates — a save verb cannot see the
  post-save world and must not try.

### Mail ingest (local archive sources)

Feed the local mail archive by implementing `MailIngest` beside the
connector (`internal/connectors/mail_ingest.go` — read the source; design:
`docs/mail-ingest.md`). The Gmail connector's live paging and the Google
Takeout export walk are the worked examples.

- The source writes through core's `MailSink` (store, cursor round-trip,
  ingest-private meta) and never sees the archive type or its SQL. Core
  owns the archive store and the library/admin mail surfaces; the ingest
  owns the walk and the provider semantics.
- Source grouping and canonical-ness are **declared, never branched on**:
  `MailSource{ServiceID, Parent, Canonical}` beside the ingest, display
  names in the `serviceNames` registry, freshness through
  `types.SnapshotProber`. Core renders and registers generically
  (`mail_provider_hygiene_test.go` rejects provider literals in core).
- Provider quota/pacing surfaces only as `types.MailIngestResult.RetryAfter`;
  core never classifies provider errors.

### Local state layout (issue #74)

All mutable local state defaults under one data directory (override with
`-data-dir` / `AEGIS_DATA_DIR`):

| OS | Default |
|----|---------|
| macOS | `~/Library/Application Support/Aegis/` |
| Linux | `$XDG_DATA_HOME/aegis` or `~/.local/share/aegis` |

```
<data-dir>/aegis.json, data_grants.json, apps.json, data_scopes.json,
entity_links.json, ownership.json, tokens.json,
user_kinds.json, user-kinds/, service_connections.json, copy_ledger.db, mail-archive.db,
search-index.db, quicken.db, timeline.db, signal.db, audit_log.db, maintenance/, logs/aegis.log
```

Path flags stay explicit overrides. Empty `-mail-archive` disables the mail archive;
empty `-copy-ledger` disables the copy ledger; empty `-log-file` disables file
logging. Startup logs the resolved directory and `/v1/admin/summary` exposes
`runtime.data_dir`. Do not reintroduce cwd defaults for these files.

## Package boundaries

Principles, not a closed package list. `import_boundary_test.go` and
`churn_guardrails_test.go` enforce these; this section explains them.

> **Layout transition (issue #358).** This section states the **target**
> dependency direction; the tree is mid-move toward it (P1–P3 landed:
> shared seam vocabulary, `internal/library`, and `internal/admin`). The
> per-package import
> table in `churn_guardrails_test.go` is the ratchet and keeps matching the
> current tree — each move PR updates the table in the same commit as the
> move. The frozen file → package map, move phases, and settled placement
> calls live in [`docs/restructure-packaging.md`](docs/restructure-packaging.md).
> Do not re-open a settled home mid-campaign; record new questions in that
> design record instead.

1. **Dependency direction.** Layers, bottom to top: `internal/shared` →
   `internal/kinds` → `internal/connectors` → the three surface packages
   (`internal/admin`, `internal/library`, `internal/syncjobs`) → the hub
   (`internal/core`) → `main`. Nothing under `internal/` imports the hub; only
   `main` packages do (the repo root and `cmd/*`). `internal/shared` is a
   leaf. `internal/kinds` imports `internal/shared` only; `internal/connectors`
   imports `internal/shared` and `internal/kinds`. Surface packages may import
   domain packages, `internal/shared`, and `core/` engine sub-packages
   (`core/policy`, `core/tokens`, …) — never the hub.
2. **Surface packages follow audiences.** `internal/admin` is the management
   surface (`/v1/admin/*`, admin UI, setup flows, admin-owned writes).
   `internal/library` is the agent data plane (`/v1/library/*`, content
   transport, read-time enforcement). `internal/syncjobs` is background
   consistency (queue, runners, freshness, maintenance) and serves no HTTP
   routes. The hub registers routes and wraps handlers with the auth
   perimeter; each surface exposes its handler set and read models for the
   hub to mount. The pre-request hook stays in the hub (perimeter).
3. **When a new package is allowed.** Any new internal package that (a)
   exposes one documented seam interface that the hub implements (like
   `connectors.Host`) or is a self-contained store/engine, (b) obeys the
   dependency direction, and (c) is guarded by a test. No issue or doc
   pre-approval is needed. Placement defaults still apply: managed data
   models and kind metadata → `internal/kinds`; Aegis-internal
   stores/engines/policy → a `core/` sub-package (or under the owning
   surface package), or a seam package when the engine is a domain with its
   own interface (`internal/kinds` under #312); stdlib-only plumbing and seam
   vocabulary → `internal/shared`. Do not park new machinery in a catch-all
   leaf — `internal/types` was audited back out three times (policy, search
   docs, kind rules) and then dissolved (#325).
4. **What the hub owns.** The mux, auth/admin middleware, body limits, the
   listener and app-token perimeters, the credential perimeter (OAuth flows,
   token wiring), config and overlays, state wiring, Host implementations, and
   the pre-request hook (`inject`, `hookhealth`, `hook_skip`). Handlers that
   need only a seam may live with their domain (`ServiceConnector.HandleProxy`
   already does this).
5. **Explicit wiring.** No `init()` registration of connectors or kinds.
   Ordered literal lists (`srv.connectors`, `kinds.Builtin()`).
6. **`internal/shared` owns stdlib-only plumbing and cross-package seam
   vocabulary** (issue #280, amended #325) — the shapes two internal packages
   exchange to agree on a contract (`capabilities.go`, `onboarding.go`,
   `mail_ingest.go`, `ownership.go`, kind rules, grant maps, dataset scopes,
   search documents) and the trivial pure constructors, accessors, and
   predicates over those shapes. No state, no I/O, no configuration
   resolution, no `Host`, no service registries, no provider paths. Anything
   that resolves over stored grants, matches, merges, or otherwise decides on
   domain state lives with its domain owner. The `kindrules.go` table is the
   worked example: domain owners register rules, and `core/policy` resolves
   them.
7. **Enforce with tests, not prose.** `import_boundary_test.go` asserts the
   dependency direction (hub imports only from `main` packages;
   `internal/shared` imports nothing from the module).
   `churn_guardrails_test.go` holds the finer per-package import table, and
   the hygiene tests (`mail_provider_hygiene_test.go`,
   `agent_string_hygiene_test.go`, `docs_hygiene_test.go`) keep provider
   literals and issue refs out of the wrong layers.

## How work happens here

Observed pattern across sessions (and the reason this file exists):

1. **Issue-driven, phased.** Features land as numbered phases under a GitHub
   issue (`#4` entity resolution, `#16` canonical sources, `#53` cleanup).
   PR titles/bodies reference the issue or “Area N of #X”.
   Before the first PR, the issue answers three questions (template:
   `.github/ISSUE_TEMPLATE/design-decision.md`): **what is this a property
   of** (service / library kind / data grant / reconciliation policy /
   Aegis-internal metadata), **what is the canonical name** (add it to
   `docs/glossary.md` in the same PR), and **what is explicitly out of
   scope**. Settling ownership and vocabulary up front is cheaper than the
   rework when a concept is renamed or re-homed later.
2. **One concern per branch/PR.** Branch prefixes:
   - `feat/…` product capability
   - `fix/…` behavior bug
   - `refactor/…` structure without product change
   - `chore/…` debt / removals
   - `test/…` coverage-only
   - `docs/…` documentation
3. **Git worktree per branch.** Main worktree stays on `main`; feature work
   runs in sibling dirs like `../aegis-<slug>`.
   Do not mutate `main` in place for feature work. Before the first
   mutating command, assert the worktree and branch
   (`git rev-parse --show-toplevel && git branch --show-current`) and pass
   the worktree as `workdir` on every call — edits that land on `main` by
   mistake have needed reverting repeatedly.
4. **PR before merge.** Merge commits are the normal landing path
   (`Merge pull request #N from …`). Body starts with `## Summary` and lists
   concrete file/behavior bullets.
5. **Tests are part of the feature, not a follow-up.** Coverage-gap PRs are
   welcome, but new write paths without tests will fail the floor.
6. **README is the entry point; `docs/` is the product contract.** Config
   fields, endpoints, scopes, and canonical/mirror semantics live in
   `docs/api.md` / `docs/configuration.md` / `docs/connect/` and must stay
   accurate when behavior changes.
7. **User prefers Go** for this service (aligns with Tailscale stack). Do not
   introduce another runtime for Aegis itself.
8. **`experiments/learn.md` still applies to `experiments/index.html`:** walkthrough page should
   mark the **current code version** (commit) when regenerated.

## Commands

Use a login shell if `go` is not on PATH; on this machine Go is typically
`/opt/homebrew/bin/go` (1.27.1).

```bash
# Full local CI-shaped test run (do this before claiming done)
go vet ./...
go test ./... -count=1 -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -n 1
go tool cover -func=coverage.out | sort -t$'\t' -k3 -n | head -n 20

# Lint (blocking in CI; pin matches .github/workflows/ci.yml)
go install honnef.co/go/tools/cmd/staticcheck@2026.2.1
"$(go env GOPATH)/bin/staticcheck" ./...

# Fast iteration
go test -count=1 ./...
go test -v -count=1 -run TestName ./...

# Fuzz hostile-input parsers (issue #107). Seeds always run as unit tests;
# -fuzz mutates them. Targets live in ./internal/core, ./internal/core/inject
# and ./internal/connectors (the repo root package has no test files). Use a
# longer -fuzztime locally when hunting bugs.
go test -run='^$' -fuzz='^FuzzInjectRequestInstructions$' -fuzztime=30s ./internal/core/inject
go test -run='^$' -fuzz='^FuzzMergeTracksSync$' -fuzztime=30s ./internal/core
go test -run='^$' -fuzz='^FuzzParseTakeoutMessage$' -fuzztime=30s ./internal/connectors
go test -run='^$' -fuzz='^FuzzWalkMboxFrom$' -fuzztime=30s ./internal/connectors
# Other targets (CI fuzzes every one in scope; keep the lists complete):
# FuzzDetectBodyShape, FuzzIsMboxFromLine, FuzzTakeoutMessageID,
# FuzzNormalizeIdentityKey, FuzzCleanYouTubeTitle,
# FuzzTrackIdentityKey, FuzzMergePlaylistsSync, FuzzPlaylistLinkKey, FuzzMergeAlbumsSync

# Build
go build -o aegis .

# Dev server (values are local-only examples)
./aegis \
  -addr 127.0.0.1:8080 \
  -base-url http://localhost:8080 \
  -api-key dev-key \
  -token-pass dev-pass \
  -log-level debug \
  -log-format text
```

CI (`.github/workflows/ci.yml`) is scope-aware: a first job classifies the
diff, and each heavy job runs only when the change can reach it — the Test
job skips structurally inert diffs, Lint/Build run on Go changes, and each
fuzz group runs when the code its targets exercise changed. Whenever it
runs, the Test job is the full suite: `go vet`, race tests + coverage, an
**80% floor**, plus **staticcheck** (findings are blocking), a **30s fuzz
pass** over the hostile-input parsers, and `go build -o aegis .`. Unknown
scope runs everything.

## Code conventions

- **`internal/shared` owns stdlib-only plumbing and cross-package seam
  vocabulary** (issue #280, amended #325) — the shapes two internal packages
  exchange to agree on a contract (`capabilities.go`, `onboarding.go`,
  `mail_ingest.go`, `ownership.go`, kind rules, grant maps, dataset scopes,
  search documents). Shapes and trivial pure constructors, accessors, and
  predicates over them (the `kindrules.go` table is the worked example); no
  state, no I/O, no configuration resolution — nothing that matches, merges,
  or resolves policy over stored configuration. Anything that decides on
  domain state lives with its domain owner.
  The generic **kind-rule table** (`kindrules.go`: level tokens +
  `DefaultNone` flags, `RegisterKindRule` / `KindAllowsLevel` / …) is shared
  plumbing on purpose: domain owners register rules (built-in kinds at init,
  user-defined packs at load) and `core/policy` resolves them, so policy never
  imports `internal/kinds` for kind queries. Placement and
  dependency direction live under **Package boundaries** — there is no closed
  package list.
- **Stdlib-first.** `net/http`, `log/slog`, `encoding/json`. Avoid new deps
  unless they clearly pay for themselves.
- **Structured logs.** `log/slog` with useful fields (`service`, `login`,
  `identity_source`, `path`, `error`). Default JSON in prod.
- **Neutral models at the library boundary.** Provider quirks stay inside
  connectors; `/v1/library/*` speaks `Playlist` / `Track` / `Contact` / `Photo`.
- **Grants are data, not comments.** **Data grants** (what the user allows
  an LLM agent or app to use) live in `internal/core/policy` as stored
  `DataGrant` entries; the access-level vocabulary and request rule map are
  `internal/shared` (`GrantMap` — kind → access level). Each recognized key
  materializes one map and authorization is one lookup over it — uniformly
  deny-by-default (`GrantMap.Level`). Enforced on the content/ingest proxy,
  library (reads and writes), and pre-request injection. Prefer **data
  grant** in product English (`docs/glossary.md`). Aegis authorization
  resolution is `internal/core/policy` (vocabulary in `internal/shared`) —
  never `internal/kinds`, which holds models and kind metadata only.
- **Cache policy.** Cache **GET + HTTP 200** only. Do not cache error bodies.
- **Outbound HTTP.** Use `shared.HTTPClient` and
  `http.NewRequestWithContext`. No lock-held I/O on the token store hot path;
  token persist happens outside the map mutex; refresh uses singleflight.
- **HTML in embed files.** Admin/setup markup belongs in `web/` +
  `//go:embed`, not multi-kilobyte Go string literals.
- **Secrets in UI/API.** Omit client secrets, access tokens, refresh tokens.
  Presence flags and expiry are fine.
- **Comments.** Only where the *why* is non-obvious (fail-open edge, OAuth
  scope nuance, match heuristic). Do not narrate the code.

## Testing conventions

- Tests live beside sources: `*_test.go`, same package as the code (`core`, `types`, `connectors`).
- Handler tests **must wrap auth** when the route is protected:
  `srv.AuthMiddleware(srv.HandleX)(w, req)`.
  Calling the handler directly skips auth and lies about status codes.
- Test servers must initialize the fields production code touches:
  `tokens`, `oauthStates`, `connectors`, `cache`, `metrics`, `links`.
- Keep token-store tests off disk: empty token path must not persist files
  (`persist()` skips when path is empty).
- Upstream APIs are mocked with `httptest.NewServer`; point service
  `endpoint` at the mock URL — never the real provider.
- JSON-derived numbers can be `float64`; directly constructed fixtures can be
  `int`. Assert both when the value is unmarshaled vs built in Go.
- Auth expectations: unauthenticated tailnet/api-key paths generally **401**.
- Write-path tests (mirror adapters, merge review, picker errors, canonical
  stamps) are first-class — they were the recent coverage floor work.
- Do not add `t.Skip` for convenience. Remove obsolete tests or fix them.
- **Public hygiene:** `public_hygiene_test.go` rejects private host/path/email
  patterns (use `your-tailnet.ts.net` / `example.com` placeholders). CI also
  runs gitleaks (`.gitleaks.toml` allowlists only non-secret placeholders).

## Pitfalls that keep recurring

| Pitfall | Do this instead |
|---------|-----------------|
| Hardcoded `https://api.spotify.com/v1` (etc.) in connectors | Read `config.GetService(id).Endpoint` |
| Fail-closed on fetch/parse errors | Fail-open / allow, log, continue |
| Agents prompting users for OAuth | Central connections + setup/admin flows |
| Caching 401/404/502 bodies | Cache 200 GET only |
| Token encrypt/persist under exclusive map lock | Mutate under lock; persist outside; ordered `persistMu` |
| Per-service OAuth config resurrected | Top-level `connections` only |
| Resurrecting per-service grant limits or provider tighten overrides | One grant map per key is the only policy; keep authorization a single lookup |
| Large admin HTML pasted back into `admin.go` | Keep `internal/admin/web/admin.html` embed |
| Kind route without declared exposure | Declare exposure on the route (`AppReadable`); API-listener and app-token allowlists derive from declarations — prefix allows must not grow (#312) |
| Coverage floor lowered to green a PR | Add tests, or split untested work out |
| Name-only merges for music entities | Prefer ISRC/UPC/MBID; duration-guard name merges |
| Unbounded cache / OAuth state / page loops | Caps, eviction, `shared.LibraryPageCap`, singleflight |
| Re-growing `writeJSON` / page-cap / URL-trim twins in `core` or `connectors` | One owner: `internal/shared` (issue #280) |
| Sequential multi-provider or multi-kind work when already parallelizable | Follow the aggregate pipeline's fan-out; don’t serialize merge review without cause |
| Local variables named after `internal/` package names (`mail`, `scopes`, `copies`, `links`, `tokens`, `search`, `audit`, `ownership`, `kinds`, `syncjobs`, `metrics`, `sampledata`, `udk`, `apps`) | Rename the local (`mailArch`, `scopeStore`, `copyLedger`, `kindList`, `jobStore`, `accessMetrics`, `sampleSet`, `udkReg`, `appStore`, …). A local with the package name silently shadows `pkg.Symbol` and compiles until a real package ref is needed |
| Concept named before its glossary entry | `docs/glossary.md` entry in the introducing PR; retire synonyms in the same change |
| Parking kind data or seam shapes in a catch-all leaf "for now" | Kind data → `internal/kinds`, seam shapes → `internal/shared` — `internal/types` was audited back out three times, then dissolved (#325) |
| Moving code again within weeks of its first move | Freeze the target layout for the duration of a move campaign; record layout questions in [`docs/restructure-packaging.md`](docs/restructure-packaging.md) instead of another relocation |
| Mutating a worktree without checking which one | Preflight `git rev-parse --show-toplevel` and pass `workdir` on every call |

## Where to look first

| Task | Start here |
|------|------------|
| New library kind | `docs/adding-kinds.md` (checklist), `kinds.BuiltinLibraryKinds`, `internal/kinds/<kind>.go`, one line in `kinds.Builtin()` |
| Hook behavior / system instructions | the `internal/core` hub (pre-request hook), connector `SystemInstructions` |
| Proxy + cache + token injection | `internal/core` hub (proxy/cache/token injection), `internal/connectors` (upstream transport), `internal/shared` (HTTP client) |
| Config schema / service matching | `internal/core` hub (`Config`), `aegis.example.json`, README “Configuration” |
| Admin data-grant writes | `internal/admin` (handlers), `internal/core/policy` (resolution + overlay persistence), `docs/configuration.md` “Data grants” |
| OAuth / connections / refresh | `internal/core` hub (flows), `internal/core/tokens/` (store) |
| Unified library + merge | `internal/kinds` (models/merge/kind data + aggregate pipeline) |
| Mail ingest / local mail archive | `docs/mail-ingest.md`, `internal/connectors` (ingests), `internal/core` hub (`MailSink` wiring) |
| Data grants / agent visibility | `internal/core/policy` (`DataGrant` resolution), `internal/shared` (`GrantMap` vocabulary) + hub enforcement | One grant map per key, deny-by-default; product English: **data grant** |
| Data ownership / completeness feedback | `docs/data-ownership.md`, `internal/core/ownership` (store), `internal/shared` (shapes), hub (derivation) | Per-kind user confirmation + derived evidence; distinct from connection maturity |
| Admin map / flow | `internal/admin`, `internal/admin/web/admin.html` |
| Auth model / ACLs | `SECURITY.md`, `internal/core` hub (identity) |
| Product/API docs | `README.md` (entry), `docs/api.md`, `docs/configuration.md`, `docs/connect/` |
| Sync maintenance / self-repair loop | `docs/sync-maintenance.md` (#86); capture surface in `docs/observability.md`, scanner and ticket surface in `cmd/aegis-dashboard` |
| Sync job queue / worker / schedules | `internal/core/syncjobs/` (engine), `internal/core` hub (`syncjobs_admin.go`, `syncrunner_*.go`); design under issue #227 |
| Access stats / Prometheus export | `internal/core/metrics/` (engines), `internal/core` hub (`metrics.go` handlers); `docs/observability.md` |
| Past hard-won facts | Project/global memory + session checkpoints under `~/.local/share/mimocode/memory/` |
| Concept vocabulary / overlapping names | `docs/glossary.md` (issue #174); inventory in `docs/core-concepts.md` |
| Shared plumbing helpers (HTTP client, JSON writers, URL/JSON/math utils, kind-rule lookup) | `internal/shared` (issue #280). `DoUpstreamRequest` stays in `internal/connectors` |

## Open direction (do not assume closed)

Still open or investigational in issues — research before implementing as if
decided:

- Live Immich canonical photos testing (#39)
- Obtaining data from other sources / photo storage (#34, #21)
- Photo ownership / provenance proofs (#20)
- User-defined kinds full product surface (admin authoring, hot-reload packs;
  PoC + file selection land under #15)
- First-run onboarding for tailnet + OAuth (#8)
- Per-user provider connections (every principal reads the owner's connected
  services; external access does not change this) and instance federation
  beyond invite tokens (#149 — the external-access contract is decided in
  `docs/external-access.md`; these two remain open)

## Definition of done

- `go vet ./...` clean
- `go test ./... -count=1 -race` passing
- Coverage profile meets or beats the 80% floor
- README/SECURITY/example config updated if user-visible behavior or config
  fields changed
- No secrets or live tokens in the diff
- PR summary explains *why* and names the issue/phase when applicable
