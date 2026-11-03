# Changelog

History and design decisions for Aegis. User-facing docs do not cite GitHub
issue numbers; this file is where that context lives.

## Unreleased

### Added

- **Sync-maintenance dashboard, published at every release (issue #239).** An
  entirely build-time generated static site — one page per service
  connection with history and success / stability / confidence marks —
  regenerates on every merge to main and ships with the marketing site
  (`site/maintenance/`, linked from the site nav). The release build runs
  `cmd/aegis-dashboard`, which scrapes the repo's issue record through the
  ticket surface's `gh` transport: the loop's sync tickets in full lifecycle
  plus reported issues behind an explicit allowlist (a `bug` label or closure
  by a fix-convention PR) naming a service — open problems count against the
  fix rate, completed ones read as historical fixes. Marks measure
  maintenance responsiveness — fix rate, quietness since the last recorded
  failure, record depth — not live sync outcomes. `site/maintenance/services.json`
  is the committed manifest naming the pages and the drift-injection harness,
  whose page is annotated as deliberately unstable (its heavy, unstable
  activity is the point). The instance view stays on
  `POST /v1/admin/maintenance/dashboard`, building the same pages from that
  instance's signal bundles (which gain a `by_service` rollup) plus its own
  issue scrape, fail-open.

- **Drift-injection harness for the sync-maintenance loop (issue #86).** A
  dev-only fake provider (`-fake-upstream`, mounted at
  `/demo/fake-upstream`) serves a Spotify saved-tracks shape with switchable
  failure modes — `healthy`, `empty`, `http-500`, `http-401`, `garbage`,
  `drift-items` (the `items` array renamed to `entries` — the issue #240
  migration), and `drift-unmapped` (renamed to a name no adapter knows —
  the silent-degradation shape the loop must catch). A new `canary` sync job type fetches one saved-tracks
  page through the configured endpoint and fails on errors or on that
  incoherent shape (total > 0, zero items; a genuinely empty library is
  health), so injected drift becomes sync-job failures, signal bundles, and
  scanner tickets without waiting for real provider drift. The harness seeds
  placeholder tokens for services pointed at the fake — only under the dev
  flag, never on a real instance. `TestMaintenanceLoop_FakeDriftToTicket`
  drives the whole chain in CI. Walkthrough: "Driving the loop without real
  drift" in `docs/sync-maintenance.md`.

- **Maintenance scanner: sync-failure tickets as GitHub issues (issue #86,
  phase 2).** `POST /v1/admin/maintenance/scan` reads the captured signal
  bundles and clusters failures by normalized signature (service, job type,
  error class, and an error shape with ids, URLs, and status codes
  collapsed). Recurrent signatures — two or more distinct bundles — are
  ticketed as GitHub issues (label `sync-failure`, one issue per signature,
  deduped by a body marker); freshness findings (stale/missing copies)
  ticket straight from the latest bundle, since the rollup is state rather
  than an event stream. One-off flake stays a metric. The issue body carries
  the phase-3 input contract in a scanner-owned evidence block
  (`repro_state`, version-stamped observations, fixture/test slots) that
  rescans rewrite idempotently without touching triage prose or labels;
  flipping the `needs-fixture` label to `ready` is how triage promotes a
  ticket into the fix loop. Ticketing is enabled with
  `-maintenance-repo owner/name` (via the `gh` CLI — no service token) and
  triggered by a local scheduled job for now; the scanner reads only bundle
  paths so it can move to cloud compute later. There is deliberately no
  local defect store.

- **Sync maintenance phase 1: signal bundles and repro fixtures (issue #86).**
  `GET /v1/admin/maintenance/signals` reports a self-contained observation
  bundle — build stamp, sync-queue rollups, up to 20 recent failed jobs with
  scrubbed error text, and the copy freshness rollup — and `POST` captures it
  as a0600 `signals-<UTC>.json` under `<data-dir>/maintenance/` for the
  scheduled scanner. `/metrics` gains `aegis_build_info{version,commit}` so a
  failure can be attributed to the build that produced it. Connectors gain
  repro fixtures (`internal/connectors/repro.go` + `testdata/repro/`):
  scrubbed request/response captures that replay through `httptest` on the
  configured-endpoint seam, with a `ReproRecorder` for live capture, a
  scrubber that drops credential headers and redacts secrets (always applied
  on save), and link rewriting so paginated captures replay offline. A
  scrubbed Spotify saved-tracks capture (null `track` items + pagination)
  ships as the first regression fixture. The long-term approach — loop
  phases, ticket schema, fixture rules, review tiers, guardrails — is
  documented in `docs/sync-maintenance.md`.

- **Admin visibility for the library search index (issue #178 follow-up).**
  `GET /v1/admin/search/status` reports enabled/path plus each kind's
  searchable flag and indexed document count. The admin Library tab marks
  searchable kinds with `q=` count chips and shows the current kind's index
  depth in the source-overview title, the Home map gained a "Search index"
  store node next to the token vault and data cache, and the Runtime
  endpoints card documents `?q=` and the new status route.

- **Library search (`q=`) with a pluggable per-kind index spec (issue #178).**
  Each `LibraryKind` (and user-defined schema pack via a `search` object)
  declares which neutral fields are searchable; a shared SQLite FTS5 index
  (`search-index.db`, dataset-partitioned like the mail archive) answers
  `GET /v1/library/{kind}?q=…`. The index is refreshed write-behind on every
  library fetch and cold-starts from one live fetch on the first query. Hits
  pass the same data-grant, app-scope, and dataset gates as the list surface,
  and level-sensitive kinds (documents, places, transactions, events)
  re-apply their access-level redaction to index-hydrated payloads. The mail
  archive's `mailFTSMatch` was folded into the shared `ftsMatchExpr`, which
  prefix-matches the final token (`rain` finds “Raining Blood”). Elasticsearch
  was evaluated and rejected: a JVM service is the wrong weight for a
  single-user tailnet, and the type-layer spec is the seam that keeps the
  engine swappable.

### Fixed

- **Spotify paging reads came up silently empty when the provider renamed
  the `items` array (issue #240).** Every paging read — mirror source and
  library connector alike — keyed on `items` alone, so a contract change
  yielded an empty library with no error while `total` still claimed
  contents. Found by the drift-injection loop (the canary's total>0 /
  zero-items guard ticketed it as `sync-failure` #240); fixed by routing
  every read through one `connectors.SpotifyPagingItems` helper that knows
  the `items` → `entries` migration, so the next rename has exactly one
  place to land. Repro fixture + red tests ride along.
- `entity_links.json` was the one state store still written with plain
  `os.WriteFile`, so a crash could truncate it and silently drop the
  operator's confirmed identities. It now goes through `atomicWriteFile`.
- `EffectivePermissions` / `EffectiveAccess` handed live `typePolicies` and
  `GrantLimits` map references to an unlocked caller after releasing `RLock`.
  The only in-place mutator is `SeedDataGrant`, which today has no production
  caller, so the escape was latent rather than reachable — but the read lock
  is now held across the whole resolution so it cannot become a race. A
  regression test drives concurrent `SeedDataGrant` + reads under `-race` and
  fails on the old code.
- `shouldSkipMatching` read `hookSkip` without the lock `Reload` (SIGHUP)
  writes it under, so a reload racing live hook traffic was a data race.

### Changed

- `POST /v1/mail/sync` and the admin mail-sync routes now run the same ingest
  implementation as the sync queue (`doMailIngest`) instead of a parallel
  inline copy, so the two surfaces cannot drift. The request and response
  contract is unchanged.
- `import_snapshot` is no longer a reserved-but-unimplemented job type: it is
  the snapshot-import type (Google Takeout), matching the job table on issue
  #227. `ingest` is live cursor paging (Gmail). Existing `ingest` jobs naming
  `service: google-takeout` still run through the same code path.
- Sync jobs renew their lease while a step is running (every third of the
  lease window) and the step winds down if ownership is lost. A long mirror
  step — hundreds of name-based track lookups — could previously outlast the
  fixed lease and be reclaimed mid-write.

### Added

- Mirror and repair jobs on the sync queue (issue #227, phase 3): `mirror`
  turns the one-shot `POST /v1/library/mirror` copy into queued work that
  finishes the whole list — each step reads one source page and **appends to a
  single target playlist**, so a 500-track playlist is one playlist on the
  target rather than one per batch. The target adapters gained
  `placeTracks(playlistID, name, tracks)` (create-or-append) and the source
  adapters gained cursor-paged reads, so a chained step costs one upstream
  page instead of re-walking the list. `repair` is the executor surface for
  "this backup drifted, fix it": it re-runs a mirror and **skips what the
  target already holds**, matched by name and artist — the same identity
  mirror uses across services — so a repair converges instead of duplicating.
- Per-service concurrency caps (`-sync-service-concurrency`, default one job
  per service) so one provider's quota cannot starve the rest of the queue.
- Queue pause/resume: `POST /v1/admin/sync/pause` and
  `POST /v1/admin/sync/resume` hold work globally or per service. Queued jobs
  for the scope move to `paused`, new enqueues land there too, and the claim
  filter skips paused scopes. The Data sync panel shows the paused state and
  a Pause/Resume control.
- Copy freshness checks and sync schedules (issue #227, phase 2): a
  `freshness_check` runner that walks the copy ledger and stamps
  `status` / `last_checked_at` by comparing each copy's live version
  descriptor (`snapshot_id` / `item_count` for mirrored playlists and liked
  tracks on Spotify and YouTube Music; the recorded export path for Google
  Takeout snapshots) — observational only, never writing to a source service.
  Only version tokens present on both sides are compared, so an omitted
  `snapshot_id` or a renamed playlist cannot produce a false `stale`. Services
  with no checker (Bluesky shares, per-item CalDAV/CardDAV/Google etags) are
  left untouched and reported as **unchecked** rather than guessed at.
- Recurring sync schedules: templates with their own `next_run_at` that feed
  the queue on a cadence and re-arm themselves. First start seeds a daily
  copy-freshness pass for `messages`, `playlists`, and `tracks`; the seed
  never clobbers an operator's pause or edit. Pause/resume via
  `POST /v1/admin/sync/schedules/{id}/enable|disable`.
- `GET /v1/admin/sync` now returns a per-kind `freshness` rollup
  (`in_sync` / `stale` / `missing` / `unknown` / `unchecked` / `total` /
  `last_checked_at`) and the `schedules` list; the Advanced → Data sync panel
  shows both.
- Queue-based data synchronization (issue #227, phase 1): a durable SQLite
  sync job queue co-located with the copy ledger, an in-process worker pool
  with lease-based claiming and backoff, and a mail-archive `ingest` runner so
  Gmail page batches chain through the queue instead of a caller loop. Admin
  surface `GET/POST /v1/admin/sync` plus `POST /v1/admin/sync/{id}/cancel`,
  an Advanced → **Data sync** panel (queue state, per-job progress, "Sync mail
  now"), and Prometheus sync series (`aegis_sync_jobs_total`,
  `aegis_sync_retries_total`, `aegis_sync_job_duration_seconds`,
  `aegis_sync_queue_depth`, `aegis_sync_last_success_timestamp_seconds`).
  New flags: `-sync-jobs` (defaults to the copy-ledger path so job completion
  and copy stamps share one transaction) and `-sync-workers`. Fail-open: an
  empty `-sync-jobs` disables the queue and every request-driven sync surface
  keeps working. Store choice (SQLite over PostgreSQL) is recorded on the
  issue.

### Fixed

- Copy freshness walks page on the ledger's numeric rowid (`CopyCheckRow`)
  instead of the `cpy_<hex>` display id, which is not sortable and silently
  broke chained batches.
- Pre-request hook no longer intercepts software-development messages that
  mention service names as identifiers (issue #205). Built-in development
  detection (code fences, source file paths, Aegis repo markers) plus optional
  `hook.skip_patterns` regexes skip matching and allow the request unchanged.

### Security

- CI secret scan (gitleaks) over the full tree and history, with an allowlist
  for operator snippets and test fakes. `public_hygiene_test.go` fails the
  Test job if private host/path/email patterns reappear in the worktree.

### Documentation

- Refresh the marketing site (`site/`) for current product language (data
  grants, access levels, service limits) and add the F.A.Q. from issue #177
  (#196).
- Split the monolithic README into an entry point plus reference docs
  (`docs/api.md`, `docs/configuration.md`, `docs/connect/*`, `docs/observability.md`,
  `docs/development.md`).
- CI checks local markdown links/fragments and keeps README headings and admin
  UI visible copy free of issue numbers and glossary-avoid terms.

## Earlier milestones (issue references)

These landed before the docs split; see the linked issues for design notes.

| Area | Issues | Notes |
|------|--------|-------|
| Named-service connectors + type-centric library | #119, #15 | `/v1/services` is content/ingest only; library kinds live in `internal/types` |
| Entity resolution / merge | #4 | Prefer ISRC/UPC/MBID over name |
| Canonical sources / mirror | #16 | `canonical_for` marks the non-Aegis home; mirror is explicit copy |
| Data grants + service limits | #148, #160 | Type-owned policy; service maps only tighten |
| User-defined types (schema packs) | #15 | Packs load into the same aggregate pipeline |
| Token encryption of static credentials | #101, #184 | AES-256-GCM + PBKDF2; rekey via `-token-new-pass` |
| Pagination contract | #185 | Shared `next_cursor` / `total` / `truncated` |
| Admin auth (admin vs agent) | #181 | Agent credentials never admin |
| CSRF / origin protection | #182 | Writes check Origin |
| Places / location redaction | #142 | Default `none`; city / precise ladder |
| Documents / Paperless | #143 | Metadata vs read ladder |
| Mail archive + Signal / Takeout import | #168 | Local stores; no cloud mail storage |
| Copy ledger | #76 | Durable known-copies map |
| Hook body shapes (OpenAI / Anthropic / Responses) | #103 | Fail-open on unknown shapes |
| Library source overview | #162 | Admin map of sources → types |
| Photos ↔ places join | — | Timeline + OwnTracks feeds |
| CalDAV / CardDAV | #146 | Nextcloud, iCloud, Fastmail |
| App data grants (third-party tokens) | #131 | Scoped `aeg_app_…` tokens |
| Terminology pass (glossary) | #174 | Prefer *data grant*, *canonical home*, *content transport* |

## Maintainer docs

Process notes (worktrees, PR shape, coverage floor) live in [`AGENTS.md`](AGENTS.md),
not here.
