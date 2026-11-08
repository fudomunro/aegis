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

Module: `github.com/fudomunro/aegis`  
Layout: **`internal/types`** (data-type layer) + **`internal/core`**
(runtime hub: auth, config, proxy, admin) with concern sub-packages
(`core/tokens`, `core/audit`, `core/links`, `core/copies`, `core/mail`,
`core/ownership`, `core/scopes`, `core/search`, `core/policy`) + **`internal/connectors`**
(named-service adapters on `Host`) + **`internal/shared`** (stdlib-only
transport/JSON/math/file/path plumbing + generic kind-rule table). Root
`main.go` is thin
(`core.Run()`). Sub-packages follow the Tailscale `ipn/` pattern: durable
stores and engines get their own package, `*Server` wiring and HTTP/admin
handlers stay in the hub. See `docs/restructure-core-types-connectors.md`
for the earlier core/types/connectors split.  
Runtime: Go **1.27.1** (`go.mod`); external deps: `golang.org/x/crypto`, `modernc.org/sqlite` (local mail archive FTS)

## Non-negotiable product rules

These come from explicit user decisions across sessions. Do not “improve”
them away.

- **Type-centric surface; named-service connectors.** The product API is
  organized around data types (`/v1/library/{kind}`, neutral models,
  `LibraryKind`) — including user-defined types (#15) — not provider-shaped
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
  `service_connections.json`, `user_types.json`, `ownership.json`,
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
| Entry, flags, routes, connector registration | `internal/core/run.go` (`main.go` is thin) | Wrap nearly every route in `AuthMiddleware` |
| Data directory + state path layout | `internal/core/datadir.go` | Defaults under OS app data dir; explicit path flags override (issue #74) |
| Config + matching | `internal/core/config.go` | Flat `services[]` + top-level `connections`; loads bootstrap + default overlay path |
| Admin permissions overlay | `internal/core/permissions_overlay.go` | `data_grants.json` v3: `data_grants` (primary DataGrant) + `service_limits` (tighten-only overrides); admin saves never rewrite aegis.json |
| Data scopes (principal → dataset) | `internal/core/scopes/`, `docs/data-scopes.md` | `data_scopes.json` (0600): API key → dataset partition + optional tightened grants. Isolation seam for multi-user / demo. Scope grants are deny-by-default for invite tokens |
| External access (listeners, invite tokens) | `docs/external-access.md`, `SECURITY.md` | Split listeners: API (publishable, credential perimeter) vs admin (in-tailnet). Contract for issue #149 multi-user |
| Pre-request injection | `internal/core/handler.go` | `allow` / `block` / `modify`; structured `Operation` contract |
| ServiceConnector interface | `internal/core/handler.go` | `Capabilities` + `SystemInstructions` + `HandleProxy` |
| Generic/shared content transport | `internal/connectors/proxy.go`, `internal/core/proxy.go`, `internal/core/httpclient.go` | Prefer shared helpers over copy-paste `do*Request`; `IsAgentContentPath` gates agent-facing `/v1/services` paths |
| Shared plumbing (HTTP client, page cap, JSON writers, URL/JSON/math/file/path utils, kind-rule table) | `internal/shared` | Stdlib-only; no domain models, `Host`, service IDs, or provider knowledge (issue #280). One owner — do not re-grow twins in `core` or `connectors` |
| Tokens / OAuth / CSRF | `internal/core/tokens/`, `internal/shared/statestore.go`, `internal/core/oauth.go`, `internal/core/connections.go` | AES-256-GCM + PBKDF2; refresh singleflight |
| Neutral models + library | `internal/types` (models/merge/kind), `internal/core/library.go`, `internal/core/librarykind.go` | Type-layer data in `internal/types`; aggregate pipeline lives in `internal/core` |
| Merge / entity IDs | `internal/types/merge.go`, `internal/core/links/`, `internal/core/entity_links_handlers.go`, `internal/core/musicbrainz.go` | ISRC/UPC/MBID preferred over name |
| Connectors | `internal/connectors/{spotify,youtube,amazonmusic,google-*,immich,bluesky,ynab,owntracks}.go`; local import stores `signal.go`, `quicken.go`, `timeline.go` + shared `localstore.go` | Register in `internal/core/run.go` `srv.connectors`; stores share the `ServiceConnector` surface with a local-transport constructor (design: `docs/local-import-stores.md`) |
| Location history (places) | `internal/core/places.go`, `internal/connectors/{timeline,owntracks}.go` | Visits/trips, city/read/precise redaction (issue #142); default `none` |
| Admin UI | `internal/core/web/admin.html` via `//go:embed` | Keep SPA markup out of Go handlers; setup panels render connector-declared specs (issue #265) |
| Onboarding runner | `internal/core/onboarding.go`; shapes `internal/types/onboarding.go`; impls beside each connector | Generic `/v1/admin/setup/{id}` host over `connectors.Onboarder` (design: `docs/connector-onboarding.md`) |
| Docs walkthrough | `experiments/index.html` | Experimental walkthrough under `experiments/` (not CI-gated); stamp with current commit when regenerated |
| Maintenance dashboard generator | `cmd/aegis-dashboard` | Static sync-maintenance dashboard build for `pages.yml`; instance-level captures stay on the admin surface |

### Extension points agents must use

**New library data types** follow `docs/adding-data-types.md`: neutral model +
provider interface + `LibraryKind` registry entry + thin
`handleLibraryAggregate` handler. Do not clone an aggregate pipeline.

**User-defined types** (`docs/user-defined-types.md`, issue #15) are schema
packs under `user-types/` loaded into the same pipeline. A pack can never
self-enable, shadow a built-in `id`/`perm_type`/`path`, or store secrets in
`http_json` headers. Local file selection is the `user_types.json` overlay.

**New or changed service behavior** goes through `ServiceConnector`:

```go
type ServiceConnector interface {
    Capabilities() ServiceCapabilities
    SystemInstructions(baseURL string) string
    HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string)
}
```

- `DataTypeCapability.Operations` is `[]Operation` (method, path, parameters,
  response spec) — not `[]string`.
- Empty `[]Operation{}` means “type recognized, not implemented”.
- New library kinds plug into the `BuiltinLibraryKinds` registry in `internal/types/librarykinds.go`.
- Connectors must read **configured** `GetService(id).Endpoint` — never hardcode
  upstream base URLs (breaks mock tests and custom endpoints).
- Prefer `connectors.RunProxy` / `connectors.DoUpstreamRequest` / `shared.HTTPClient`
  over new `http.DefaultClient` call sites.
- **Connectors depend on `Host`** (`host.go`) — not `*Server`, and never the
  core hub. Model packages `internal/types` and `internal/core/policy` are
  allowed (neutral models, access-level vocabulary, `GrantLimits`). That seam
  is how named services leave package main under #15. Extend `Host` only for
  cross-connector runtime needs; provider-specific stores are constructor
  args (see `NewQuickenConnector`).
- Proxy path → data-type classification is the ordered table in
  `internal/types/pathclass.go`. Do not reintroduce a `switch` or a second
  classifier. Agent-facing `/v1/services` paths must pass
  `types.IsAgentContentPath` (issue #119).

**Advanced setup flows** go through the optional `Onboarder` interface
(`internal/connectors/host.go`; design: `docs/connector-onboarding.md`).
Immich and YNAB are the worked examples.

```go
type Onboarder interface {
    Onboarding() types.OnboardingSpec
    OnboardingStatus(ctx context.Context) types.OnboardingStatus
    OnboardingRun(ctx context.Context, action string, in types.OnboardingInput) (types.OnboardingResult, error)
}
```

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
connector (`internal/connectors/mail_ingest.go`; design:
`docs/mail-ingest.md`). The Gmail connector's live paging and the Google
Takeout export walk are the worked examples.

```go
type MailIngest interface {
    IngestMail(ctx context.Context, sink MailSink, opts types.MailIngestOptions) (types.MailIngestResult, error)
}
```

- The source writes through core's `MailSink` (store, cursor round-trip,
  ingest-private meta) and never sees the archive type or its SQL. Core
  owns the archive store and `/v1/mail`; the ingest owns the walk and the
  provider semantics.
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
user_types.json, user-types/, service_connections.json, copy_ledger.db, mail-archive.db,
search-index.db, quicken.db, timeline.db, signal.db, audit_log.db, maintenance/, logs/aegis.log
```

Path flags stay explicit overrides. Empty `-mail-archive` disables `/v1/mail`;
empty `-copy-ledger` disables the copy ledger; empty `-log-file` disables file
logging. Startup logs the resolved directory and `/v1/admin/summary` exposes
`runtime.data_dir`. Do not reintroduce cwd defaults for these files.

## How work happens here

Observed pattern across sessions (and the reason this file exists):

1. **Issue-driven, phased.** Features land as numbered phases under a GitHub
   issue (`#4` entity resolution, `#16` canonical sources, `#53` cleanup).
   PR titles/bodies reference the issue or “Area N of #X”.
2. **One concern per branch/PR.** Branch prefixes:
   - `feat/…` product capability
   - `fix/…` behavior bug
   - `refactor/…` structure without product change
   - `chore/…` debt / removals
   - `test/…` coverage-only
   - `docs/…` documentation
3. **Git worktree per branch.** Main worktree stays on `main`; feature work
   runs in sibling dirs like `../aegis-<slug>`.
   Do not mutate `main` in place for feature work.
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
# -fuzz mutates them. Targets live in ./internal/core and ./internal/connectors
# (the repo root package has no test files). Use a longer -fuzztime locally
# when hunting bugs.
go test -run='^$' -fuzz='^FuzzInjectRequestInstructions$' -fuzztime=30s ./internal/core
go test -run='^$' -fuzz='^FuzzMergeTracksSync$' -fuzztime=30s ./internal/core
go test -run='^$' -fuzz='^FuzzParseTakeoutMessage$' -fuzztime=30s ./internal/connectors
go test -run='^$' -fuzz='^FuzzWalkMboxFrom$' -fuzztime=30s ./internal/connectors
# Other targets (CI fuzzes every one; list them all in ci.yml):
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

CI (`.github/workflows/ci.yml`) runs `go vet`, race tests + coverage, an
**80% floor**, **staticcheck** (findings are blocking), a **30s fuzz pass**
over the hostile-input parsers, and `go build -o aegis .`.

## Code conventions

- **Packages follow the #15 split plus #301 sub-packages.** `internal/types`,
  `internal/core` (hub + concern sub-packages), `internal/connectors`, and
  `internal/shared` have landed. Sub-packages under `core/` are for
  self-contained stores/engines (`tokens`, `audit`, `links`, `copies`,
  `mail`, `ownership`, `scopes`, `search`, `policy`) — `*Server` wiring and
  HTTP/admin handlers stay in the hub. `internal/shared` stays stdlib-only
  plumbing.
  Do not invent other internal packages outside
  `docs/restructure-core-types-connectors.md` and the #301 layout.
  `internal/shared` is stdlib-only transport/JSON/math plumbing — no domain
  models, no `Host`, no service IDs, no provider knowledge, no policy.
  The generic **kind-rule table** (`kindrules.go`: level tokens +
  `DefaultNone` flags, `RegisterKindRule` / `KindAllowsLevel` / …) is shared
  plumbing on purpose: domain owners register rules (built-in kinds at init,
  user-defined packs at load) and `core/policy` resolves them, so policy and
  the type layer never import each other for kind queries.
- **Stdlib-first.** `net/http`, `log/slog`, `encoding/json`. Avoid new deps
  unless they clearly pay for themselves.
- **Structured logs.** `log/slog` with useful fields (`service`, `login`,
  `identity_source`, `path`, `error`). Default JSON in prod.
- **Neutral models at the library boundary.** Provider quirks stay inside
  connectors; `/v1/library/*` speaks `Playlist` / `Track` / `Contact` / `Photo`.
- **Permissions are data, not comments.** **Data grants** (what the user allows
  an LLM agent or app to use) are owned by `DataGrant` in
  `internal/core/policy` (keyed by data type) plus optional provider/attribute
  filters and must stay
  enforced on the content/ingest proxy, library, capabilities output, and
  pre-request injection. Service `permissions` maps are migration overrides
  that can only tighten the type default (issue #148). Prefer **data grant** in
  product English (`docs/glossary.md`). Aegis authorization policy (grants,
  permission maps, access levels) is `internal/core/policy` — never
  `internal/types`, which is the data-type layer only.
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
| `http.DefaultClient` / `http.PostForm` without timeout | `shared.HTTPClient` + context |
| Token encrypt/persist under exclusive map lock | Mutate under lock; persist outside; ordered `persistMu` |
| Per-service OAuth config resurrected | Top-level `connections` only |
| Service permissions dual-path treated as equal owners | Data grant is primary; service maps only tighten until the compat window ends |
| Large admin HTML pasted back into `admin.go` | Keep `internal/core/web/admin.html` embed |
| Provider copy in `admin.html` / `docs/` | Declare it in the connector's `Onboarder` spec; hygiene tests reject restatements (issue #265) |
| Coverage floor lowered to green a PR | Add tests, or split untested work out |
| Name-only merges for music entities | Prefer ISRC/UPC/MBID; duration-guard name merges |
| Unbounded cache / OAuth state / page loops | Caps, eviction, `shared.LibraryPageCap`, singleflight |
| Re-growing `writeJSON` / page-cap / URL-trim twins in `core` or `connectors` | One owner: `internal/shared` (issue #280) |
| Service-ID switches or provider literals growing back into `core` mail paths | Declare grouping/canonical/freshness beside the ingest (`MailSource`, `types.SnapshotProber`); `mail_provider_hygiene_test.go` rejects provider literals (issue #286) |
| Sequential multi-provider or multi-kind work when already parallelizable | Follow `internal/core/librarykind.go` fan-out; don’t serialize merge review without cause |
| Local variables named after `core/` sub-packages (`mail`, `scopes`, `copies`, `links`, `tokens`, `search`, `audit`, `ownership`) | Rename the local (`mailArch`, `scopeStore`, `copyLedger`, …). A local with the package name silently shadows `pkg.Symbol` and compiles until a real package ref is needed |

## Where to look first

| Task | Start here |
|------|------------|
| New library data type / kind | `docs/adding-data-types.md` (checklist), then `internal/types/librarykinds.go` |
| Hook behavior / system instructions | `internal/core/handler.go`, connector `SystemInstructions` |
| Proxy + cache + token injection | `internal/core/proxy.go`, `internal/connectors/proxy.go`, `internal/core/httpclient.go` |
| Config schema / service matching | `internal/core/config.go`, `aegis.example.json`, README “Configuration” |
| Admin data-grant writes | `internal/core/permissions_overlay.go`, `internal/core/config.go` `SetDataGrants` / `SetServicePermissions`, `docs/configuration.md` “Data grants” |
| OAuth / connections / refresh | `internal/core/oauth.go`, `internal/core/connections.go`, `internal/core/tokens.go` |
| Unified library + merge | `internal/core/librarykind.go`, `internal/types/{merge,models}.go` |
| Mail ingest / local mail archive | `docs/mail-ingest.md`, `internal/connectors/mail_ingest.go`, `internal/core/mail_ingest.go` |
| Data grants / agent visibility | `internal/core/policy` (`DataGrant` / `EffectiveAccess`), `internal/core/permissions_overlay.go`, `internal/core/permissions.go` (enforcement) | DataGrant map + provider tighten-only overrides; product English: **data grant** |
| Data ownership / completeness feedback | `docs/data-ownership.md`, `internal/core/ownership` (`OwnershipEntry` store), `internal/types/ownership.go` (status + declaration shapes), `internal/core/ownership.go` (derivation) | Per-kind user confirmation + derived evidence; distinct from connection maturity |
| Admin map / flow | `internal/core/flow.go`, `internal/core/admin.go`, `internal/core/web/admin.html` |
| Auth model / ACLs | `SECURITY.md`, `internal/core/identity.go` |
| Product/API docs | `README.md` (entry), `docs/api.md`, `docs/configuration.md`, `docs/connect/` |
| Sync maintenance / self-repair loop | `docs/sync-maintenance.md` (#86); signals surface in `docs/observability.md` |
| Past hard-won facts | Project/global memory + session checkpoints under `~/.local/share/mimocode/memory/` |
| Concept vocabulary / overlapping names | `docs/glossary.md` (issue #174); inventory in `docs/core-concepts.md` |
| Shared plumbing helpers (HTTP client, JSON writers, URL/JSON/math utils, kind-rule lookup) | `internal/shared` (issue #280). `DoUpstreamRequest` stays in `internal/connectors/http.go` |

## Open direction (do not assume closed)

Still open or investigational in issues — research before implementing as if
decided:

- Live Immich canonical photos testing (#39)
- Obtaining data from other sources / photo storage (#34, #21)
- Photo ownership / provenance proofs (#20)
- User-defined types full product surface (admin authoring, hot-reload packs;
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
