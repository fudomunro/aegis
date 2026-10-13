# AGENTS.md — Aegis

Instructions for coding agents working in this repository. Read this before
touching code, config, or docs. `README.md` holds the full product/API surface;
`SECURITY.md` holds the tailnet trust model. This file is about how work
actually happens here and which product rules must not drift.

## What this project is

A private Go service that sits next to [Tailscale Aperture](https://tailscale.com/docs/aperture).
It does two jobs:

1. **Pre-request hook** (`POST /v1/pre-request`) — detect named services in an
   LLM request and inject instructions that point the agent at Aegis’s
   proxy/library APIs.
2. **Service proxy + unified library** — agents call
   `/v1/services/{id}/...` and `/v1/library/{kind}` instead of provider APIs.
   Aegis attaches OAuth tokens, caches, enforces permissions, and returns
   neutral models.

Module: `github.com/fudomunro/aegis`  
Layout: **single package `main`** (flat `*.go` files at the repo root)  
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
  prefer the type-level design.
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
  `entity_links.json`, `permissions.json`, `service_connections.json`,
  `.aegis-token-pass`, or live binaries. Example config only via
  `aegis.example.json`. Prefer the OS data directory (issue #74) over the git
  worktree for runtime state.
- **No Tailscale Funnel.** Aegis holds music OAuth tokens; it is a tailnet
  service. See `SECURITY.md`.

## Architecture map

```
Agent → Aperture → Aegis pre-request hook → LLM
Agent → Aegis proxy / library / admin → provider APIs
```

| Area | Where | Notes |
|------|-------|-------|
| Entry, flags, routes, connector registration | `main.go` | Wrap nearly every route in `AuthMiddleware` |
| Data directory + state path layout | `datadir.go` | Defaults under OS app data dir; explicit path flags override (issue #74) |
| Config + matching | `config.go` | Flat `services[]` + top-level `connections`; loads bootstrap + default overlay path |
| Admin permissions overlay | `permissions_overlay.go` | `permissions.json` wins over aegis.json bootstrap; admin saves never rewrite aegis.json |
| Pre-request injection | `handler.go` | `allow` / `block` / `modify`; structured `Operation` contract |
| ServiceConnector interface | `handler.go` | `Capabilities` + `SystemInstructions` + `HandleProxy` |
| Generic/shared proxy path | `connector_proxy.go`, `proxy.go`, `httpclient.go` | Prefer shared helpers over copy-paste `do*Request` |
| Tokens / OAuth / CSRF | `tokens.go`, `oauth.go`, `statestore.go`, `connections.go` | AES-256-GCM + PBKDF2; refresh singleflight |
| Neutral models + library | `models.go`, `library.go`, `librarykind.go` | LibraryKind registry + parallel provider fan-out |
| Merge / entity IDs | `match.go`, `entity_links*.go`, `musicbrainz.go` | ISRC/UPC/MBID preferred over name |
| Connectors | `spotify.go`, `youtube.go`, `amazonmusic.go`, `google-*.go`, `immich.go` | Register in `main.go` `srv.connectors` |
| Location history (places) | `places.go`, `timeline.go`, `owntracks.go` | Visits/trips, city/read/precise redaction (issue #142); default `none` |
| Admin UI | `web/admin.html` via `//go:embed` | Keep SPA markup out of Go handlers |
| Docs walkthrough | `docs/index.html` | Regenerated artifact; stamp with current commit |

### Extension points agents must use

**New library data types** follow `docs/adding-data-types.md`: neutral model +
provider interface + `LibraryKind` registry entry + thin
`handleLibraryAggregate` handler. Do not clone an aggregate pipeline.

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
- New library kinds plug into `LibraryKind` registry in `librarykind.go`.
- Connectors must read **configured** `GetService(id).Endpoint` — never hardcode
  upstream base URLs (breaks mock tests and custom endpoints).
- Prefer `runConnectorProxy` / `doUpstreamRequest` / `aegisHTTPClient`
  over new `http.DefaultClient` call sites.

### Local state layout (issue #74)

All mutable local state defaults under one data directory (override with
`-data-dir` / `AEGIS_DATA_DIR`):

| OS | Default |
|----|---------|
| macOS | `~/Library/Application Support/Aegis/` |
| Linux | `$XDG_DATA_HOME/aegis` or `~/.local/share/aegis` |

```
<data-dir>/aegis.json, permissions.json, apps.json, entity_links.json, tokens.json,
copy_ledger.db, mail-archive.db, logs/aegis.log, cache/
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
   runs in sibling dirs like `/Users/alec/Work/aegis-<slug>`.
   Do not mutate `main` in place for feature work.
4. **PR before merge.** Merge commits are the normal landing path
   (`Merge pull request #N from …`). Body starts with `## Summary` and lists
   concrete file/behavior bullets.
5. **Tests are part of the feature, not a follow-up.** Coverage-gap PRs are
   welcome, but new write paths without tests will fail the floor.
6. **README is the product contract.** Config fields, endpoints, scopes, and
   canonical/mirror semantics must stay accurate when behavior changes.
7. **User prefers Go** for this service (aligns with Tailscale stack). Do not
   introduce another runtime for Aegis itself.
8. **`learn.md` still applies to docs/index.html:** walkthrough page should
   mark the **current code version** (commit) when regenerated.

## Commands

Use a login shell if `go` is not on PATH; on this machine Go is typically
`/opt/homebrew/bin/go` (1.27.1).

```bash
# Full local CI-shaped test run (do this before claiming done)
go vet ./...
go test ./... -count=1 -race -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -n 1
go tool cover -func=coverage.out | sort -t$'\t' -k3 -n | head -n 20

# Lint (blocking in CI; pin matches .github/workflows/ci.yml)
go install honnef.co/go/tools/cmd/staticcheck@2026.2.1
"$(go env GOPATH)/bin/staticcheck" ./...

# Fast iteration
go test -count=1 ./...
go test -v -count=1 -run TestName ./...

# Fuzz hostile-input parsers (issue #107). Seeds always run as unit tests;
# -fuzz mutates them. Use a longer -fuzztime locally when hunting bugs.
go test -run='^$' -fuzz='^FuzzInjectRequestInstructions$' -fuzztime=30s .
go test -run='^$' -fuzz='^FuzzRecordFromTakeoutMessage$' -fuzztime=30s .
go test -run='^$' -fuzz='^FuzzWalkMboxFrom$' -fuzztime=30s .
go test -run='^$' -fuzz='^FuzzMergeTracksSync$' -fuzztime=30s .
# Other targets: FuzzDetectBodyShape, FuzzInjectSystemMessage, FuzzIsMboxFromLine,
# FuzzTakeoutMessageID, FuzzNormalizeIdentityKey, FuzzCleanYouTubeTitle,
# FuzzTrackIdentityKey, FuzzMergePlaylistsSync, FuzzPlaylistLinkKey, FuzzMergeAlbumsSync

# Build
go build -o aegis .

# Dev server (values are local-only examples)
./aegis \
  -addr :8080 \
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

- **Single package.** No internal packages unless there is a strong, reviewed
  reason; keep the flat layout.
- **Stdlib-first.** `net/http`, `log/slog`, `encoding/json`. Avoid new deps
  unless they clearly pay for themselves.
- **Structured logs.** `log/slog` with useful fields (`service`, `login`,
  `identity_source`, `path`, `error`). Default JSON in prod.
- **Neutral models at the library boundary.** Provider quirks stay inside
  connectors; `/v1/library/*` speaks `Playlist` / `Track` / `Contact` / `Photo`.
- **Permissions are data, not comments.** Service `permissions` +
  path classification in `permissions.go` must stay enforced on proxy,
  library, capabilities output, and pre-request injection.
- **Cache policy.** Cache **GET + HTTP 200** only. Do not cache error bodies.
- **Outbound HTTP.** Use `aegisHTTPClient` and
  `http.NewRequestWithContext`. No lock-held I/O on the token store hot path;
  token persist happens outside the map mutex; refresh uses singleflight.
- **HTML in embed files.** Admin/setup markup belongs in `web/` +
  `//go:embed`, not multi-kilobyte Go string literals.
- **Secrets in UI/API.** Omit client secrets, access tokens, refresh tokens.
  Presence flags and expiry are fine.
- **Comments.** Only where the *why* is non-obvious (fail-open edge, OAuth
  scope nuance, match heuristic). Do not narrate the code.

## Testing conventions

- Tests live beside sources: `*_test.go`, package `main`.
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

## Pitfalls that keep recurring

| Pitfall | Do this instead |
|---------|-----------------|
| Hardcoded `https://api.spotify.com/v1` (etc.) in connectors | Read `config.GetService(id).Endpoint` |
| Fail-closed on fetch/parse errors | Fail-open / allow, log, continue |
| Agents prompting users for OAuth | Central connections + setup/admin flows |
| Caching 401/404/502 bodies | Cache 200 GET only |
| `http.DefaultClient` / `http.PostForm` without timeout | `aegisHTTPClient` + context |
| Token encrypt/persist under exclusive map lock | Mutate under lock; persist outside; ordered `persistMu` |
| Per-service OAuth config resurrected | Top-level `connections` only |
| Large admin HTML pasted back into `admin.go` | Keep `web/admin.html` embed |
| Coverage floor lowered to green a PR | Add tests, or split untested work out |
| Name-only merges for music entities | Prefer ISRC/UPC/MBID; duration-guard name merges |
| Unbounded cache / OAuth state / page loops | Caps, eviction, `libraryPageCap`, singleflight |
| Sequential multi-provider or multi-kind work when already parallelizable | Follow `librarykind.go` fan-out; don’t serialize merge review without cause |

## Where to look first

| Task | Start here |
|------|------------|
| New library data type / kind | `docs/adding-data-types.md` (checklist), then `librarykind.go` |
| Hook behavior / system instructions | `handler.go`, connector `SystemInstructions` |
| Proxy + cache + token injection | `proxy.go`, `connector_proxy.go`, `httpclient.go` |
| Config schema / service matching | `config.go`, `aegis.example.json`, README “Configuration” |
| Admin permission writes | `permissions_overlay.go`, `config.go` `SetServicePermissions`, README “Data permissions” |
| OAuth / connections / refresh | `oauth.go`, `connections.go`, `tokens.go` |
| Unified library + merge | `librarykind.go`, `match.go`, `models.go` |
| Permissions / agent visibility | `permissions.go` |
| Admin map / flow | `flow.go`, `admin.go`, `web/admin.html` |
| Auth model / ACLs | `SECURITY.md`, `identity.go` |
| Product/API docs | `README.md` |
| Past hard-won facts | Project/global memory + session checkpoints under `~/.local/share/mimocode/memory/` |

## Open direction (do not assume closed)

Still open or investigational in issues — research before implementing as if
decided:

- Live Immich canonical photos testing (#39)
- Obtaining data from other sources / photo storage (#34, #21)
- Photo ownership / provenance proofs (#20)
- User-defined types (#15)
- First-run onboarding for tailnet + OAuth (#8)

## Definition of done

- `go vet ./...` clean
- `go test ./... -count=1 -race` passing
- Coverage profile meets or beats the 80% floor
- README/SECURITY/example config updated if user-visible behavior or config
  fields changed
- No secrets or live tokens in the diff
- PR summary explains *why* and names the issue/phase when applicable
