# Changelog

History and design decisions for Aegis. User-facing docs do not cite GitHub
issue numbers; this file is where that context lives.

## Unreleased

### Fixed

- **Sync job timestamps compare in fixed-width format.** The job store
  compares `next_run_at` / `lease_until` / `finished_at` lexicographically
  as TEXT in SQL, but wrote them as RFC3339Nano, which trims trailing
  zeros — so a job due at `…40.580s` was stored as `…40.58Z` and sorted
  *after* the later claim time `…40.582Z`, making fresh work look
  not-yet-due. `formatSyncTime` now emits a fixed nine-digit fraction
  (string order == clock order) and the raw format sites in enqueue/claim/
  reclaim go through it. `parseSyncTime` stays lenient, so rows written by
  older builds still read correctly. This was the intermittent
  `TestSyncWorker_RunsJobToSuccess` flake.

### Changed

- **“Data type” is retired in favor of “library kind”.** The phrase read as
  general development language and collided with the field-shape sense of
  `type` (`"type": "string"` in `capabilities.go` and `udt.go`) — the same
  word for two axes. The registry axis is already shipped as `kind` in
  `/v1/library/{kind}` and `LibraryKind`, so prose now follows the API:
  docs, admin copy, consent copy, the marketing site, and agent strings say
  **library kind** (short: **kind**). Field shape keeps **type** /
  **field type**. **Data grant** is deliberately *not* renamed to “kind
  grant” — it names what is granted (the user's data) while **library kind**
  names the slice it is keyed by, so “a data grant on kind `tracks` at
  access level `read`” keeps three distinct nouns and avoids loading one
  word with two senses. Config and response keys (`perm_type`,
  `data_types`, `available_data_types`, `writable_data_types`,
  `data_grants`) are unchanged and now documented as naming a kind.
  `docs/adding-data-types.md` becomes `docs/adding-kinds.md`; the product
  rule “Type-centric surface” becomes “Kind-centric surface”. The
  glossary-avoid lists (`docsForbidden`, `agentStringForbidden`) now reject
  “data type” in README headings, admin visible copy, and agent strings
  (#305).
- **Search index format leaves `internal/types`.** `SearchDocument` and
  `BuildSearchDocument` (the item→document flattening) move from
  `internal/types` to `internal/core/search`: the index record is
  Aegis-internal metadata, not a managed kind. `SearchSpec` stays in
  `internal/types` — kinds and schema packs declare it (like `AllowedLevels`)
  and the engine is the machinery behind that declaration. The engine no
  longer references the agent page cap: the library search handler clamps
  `limit` to `MaxPageLimit` at the API boundary (effective limits
  unchanged).
- **OwnershipEntry moves to its store package.** The user-confirmation
  record (`Confirmed` / `ConfirmedAt` / `Note`) leaves `internal/types` for
  `internal/core/ownership`, which stores it — matching `scopes.Scope`. The
  ownership connector-declaration and status shapes stay in `internal/types`
  (connectors declare them beside maturity). No behavior change.
- **Kind-rule lookups become shared plumbing.** The generic queries over
  kind rules — which optional level tokens a kind honors, whether it is
  opt-in (`DefaultNone`), and which kinds allow a level — move from
  `internal/types` to a rule table in `internal/shared`
  (`RegisterKindRule` / `MarkKindDefaultNone` / `KindAllowsLevel` /
  `KindDefaultsNone` / `KindsAllowingLevel`). Built-in kinds register at
  package init; user-defined packs register at load. This removes the
  `core/policy` → `internal/types` import (policy resolves rules through
  shared only) and unifies the two halves of opt-in state (runtime marks
  and built-in `DefaultNone` flags) into one table. No behavior change.
- **Access policy moves to `internal/core/policy` (package cleanup).** Data
  grants, permission maps, the access-level vocabulary, grant resolution
  (`EffectiveAccess` / scope-grant resolvers), and the permissions-editor
  view models left `internal/types`: they are Aegis-internal authorization
  policy, not data types. `internal/types` now holds only the data-type
  layer (neutral models, merge, kind registry). `internal/core/scopes`
  depends on `internal/core/policy` instead of the type layer, and
  connectors may import `internal/core/policy` alongside `internal/types`
  (their `GrantLimits` vocabulary). No behavior change — including UDT
  `default_access` canonicalization, which moved to pack load.
- **Library cache: aggregates not pages, coalesced fetches, targeted
  invalidation (issue #273).** The in-memory cache used to store one encoded
  response per page, so paging through N pages re-ran the full provider
  fan-out N times and concurrent identical requests (the common agent
  parallel-tool-call pattern) each fanned out on their own. The pipeline now
  caches one merged aggregate per kind+view (`library:{id}|agg` — app and
  data-scope suffixes kept) and slices agent pages from it; parallel misses
  for the same key share one fetch via a flight group (a leader disconnect
  no longer poisons the shared result). Blanket `Clear()` at the ~20
  admin/save/sync sites is replaced with targeted invalidation — by kind
  (`invalidateLibraryKind`), by service (`invalidateService`), or all
  response entries (`invalidateCaches`) — so scheduled sync jobs no longer
  wipe unrelated caches on every completion. MusicBrainz MBID lookups moved
  to their own cache and survive library invalidation (they keep the 24h/2h
  TTLs and the lookup budget), and the search-index write-behind runs off the
  response path. Degraded aggregates are still never cached, GET+200 only,
  and cache errors never fail a response. The admin cache view lists both
  caches; the admin flush-all endpoint clears both.

- **Connector `HandleProxy` implementations consolidate onto two shared
  pipelines (issue #275).** `connectors.RunProxy` (buffered JSON: token →
  cache → upstream → credential/rate-limit handling → transform →
  cache-on-200) was only reachable from the three music connectors, while
  OwnTracks and YNAB re-implemented the same pipeline line for line and four
  Google connectors each carried an identical streaming tail. `RunProxy` now
  takes the pieces those copies had diverged on — a credential resolver for
  API-key services, provider-specific credential-failure copy, an opt-in
  "403 means bad key" mapping (OAuth providers answer 403 for insufficient
  scope, which must stay visible), and a transform that may decline a payload
  so upstream error pages pass through un-re-marshaled. OwnTracks and YNAB
  are on it now, which also gives OwnTracks upstream latency metrics,
  shared 429 handling, and `ServiceConfig.CacheTTL` support — the three
  places its copy had drifted. `connectors.StreamProxy` is the streaming
  variant (forward with a bearer token, copy the response back) and replaces
  the four identical Google tails; binary downloads (Immich, Paperless,
  Google Drive export), Bluesky's mixed route table, local stores, and DAV
  stay hand-rolled on purpose. Inventory and reasoning:
  `docs/proxy-pipeline.md`.

- **Local import stores join the connector family (issue #266).** Signal,
  Quicken, and Timeline — the SQLite-backed filestore imports behind
  `-import-signal` / `-import-quicken` / `-import-timeline` — moved from
  `internal/core` into `internal/connectors` beside the named-service
  adapters. They keep the full `ServiceConnector` and provider-interface
  surfaces (capabilities, maturity, data grants, library fan-in); what
  differs is only transport (local files instead of upstream HTTP) and
  lifecycle (one-shot import). The SQLite lifecycle, import batching, and
  the item cap now have one owner in `internal/connectors/localstore.go`
  (imports commit in 200-row transactions instead of one Exec per row).
  Maturity rows and service names register beside their stores like every
  other connector; the three near-identical admin handlers collapsed into
  one import-store admin handler and the three CLI import runners into one
  `runImportCLI`, both parameterized per store. Identity keys and list
  filters (`TransactionFilters` / `PlaceVisitFilters`) moved to
  `internal/types` beside `TrackIdentityKey` so both transports compute
  them identically. Design note: `docs/local-import-stores.md`. No user-
  visible behavior change; the mail archive / Google Takeout pair is
  explicitly out of scope for this move.

### Fixed

- **Search index no longer stores reduced views or keeps deleted items
  (issue #272).** `indexLibraryDocuments` ran on every uncached library list
  and wrote whatever the *caller* was allowed to see, keyed only on
  `(kind, dataset, item_id)`. A free/busy app listing events (or a
  metadata-only app listing documents) therefore overwrote the owner's full
  rows with the redacted payloads, so a later `?q=` returned fewer fields
  than the owner has — or missed matches on fields the redaction blanked.
  Index writes are now gated on the request being at the partition's
  owner-tier view (`libraryIndexWriteable`); a reduced fetch never touches
  the index, and the cold-index bootstrap behind `?q=` fetches at owner tier
  (`ownerViewRequest`, which drops the app identity and scope-grant
  tightening while keeping data-scope dataset visibility). Separately,
  `SearchIndex.Upsert` never deleted, so a contact removed upstream or a
  deleted Paperless document kept matching `?q=` forever. `SearchIndex.Sync`
  now upserts and then drops rows a complete fetch refutes — absent from the
  fetch and vouched only by sources that answered `ok` without truncating.
  Degraded, truncated, and unseen sources are fail-open: their rows survive,
  and a vouch from a source outside the fetch is preserved on upsert so a
  merged row cannot be refuted behind a sick provider's back. `docs/api.md`
  freshness note rewritten to match.

- **Denial audit rows now carry `client_ip`.** Auth denials (`denyStatus`)
  and service-proxy denials recorded through `recordAccess` directly, which
  skipped the trusted-proxy client-IP fill that `recordAccessFrom` applies —
  so `reason=auth` / proxy denial rows had an empty `client_ip` column while
  every other request-path row resolved it. All five call sites fill it now.

- **Hardening follow-ups from the external-access security review (issue
  #253).** Weak operator-chosen credentials are refused behind a trusted
  edge (`api-key` / `admin-api-key` must be ≥ 32 characters — on a
  published listener those keys are the perimeter and the rate caps only
  slow guessing). Rate-limit counters evict in batches (~10% of the key
  cap per pass) instead of one entry per insert. 429s coalesce to **one
  audit row per key per window** (every damp still bumps the metrics
  counter) so a flood cannot wash real evidence out of `audit_log.db`.
  Failed-authentication counting now ignores credential-less 401s
  ("tailnet identity required", probes) — only presented-credential
  failures are guesswork. Deny logs carry both the raw peer and the
  resolved `client_ip`; `client_ip` is flagged as personal data in the
  observability docs; the fixed-window 2x burst is documented as accepted.

### Added

<<<<<<< HEAD
- **Split listeners for external access (issue #149, phase 1; re-landed —
  the original merge stranded on a stale stacked branch).** The surface
  can be served on two listeners per `docs/external-access.md`: the **API
  listener** (`-addr`) carries the agent/external routes and may be published
  behind TLS; the **admin listener** (`-admin-addr`, tailnet/loopback only —
  anything else is refused at startup) carries policy and setup surfaces and
  stays in-tailnet. The API listener serves an explicit **allowlist**
  (`POST /v1/pre-request`, library reads, `/v1/services/*` content transport,
  mail search/status, `GET /v1/auth/status`, `POST /v1/apps/token`/
  `revoke`, health probes) — unlisted routes answer 404 there and default to
  the admin side, so a newly registered route cannot leak onto the published
  surface. With `-external-access` (requires `-admin-addr`) the API listener
  accepts **invite tokens and app tokens only**: no ambient identity is
  resolved there and the shared agent key is refused, so a transcript leak
  never becomes internet-wide read. The admin listener serves the agent tier
  as well, so Aperture and tagged hosts keep ambient identity and the agent
  key with no migration. Per-listener base URLs (`-admin-base-url`) keep
  OAuth redirects and CSRF origin checks on the right origin. Merge review
  and the copy ledger stay admin-side (they are not data-type grants).
  Single-listener deployments are unchanged. Follow-ons: scope-grant
  enforcement (deny-by-default), invite minting/rotation with hashed
  storage, and rate limiting with trusted-proxy config.
=======
- **External-access hardening (issue #253, phase 4).** The last contract
  item before a public bind is a supported deployment:
  - **Rate limiting on auth and token surfaces** (`rate_limits` config) —
    per-client-IP request damping on `/v1/apps/token`, `/v1/apps/authorize`,
    `/v1/apps/revoke`, and `/v1/auth/*`, plus per-IP and per-token failure
    caps on failed authentications everywhere, **including the admin
    surface** (lockouts answer 429). The per-token counter keys on the
    SHA-256 of the presented credential, so one hammered key is bounded
    whatever the source IP; per-IP keys bucket IPv6 by `/64` (privacy-
    extension rotation makes per-address caps meaningless). Defaults: 60
    req/min, 10 failures per IP and per token per 5 min; negative disables
    a cap and `window_seconds: -1` disables the failure caps (it is not a
    permanent lockout). Per-IP caps are **skipped for loopback peers when
    `trusted_proxies` is empty** — a same-host edge would put every caller
    in one bucket and let one attacker lock out the whole deployment — and
    requests that resolve a tailnet identity are exempt entirely (ambient
    identity is not a guessable credential). Counters are fixed-window,
    memory-bounded, and best-effort — brute-force damping, not an
    availability guarantee (body caps and edge limits still cover volume).
  - **Trusted-proxy client IPs** (`trusted_proxies` + `client_ip_header`
    config) — forwarded headers are believed only from listed proxy
    addresses, and **exactly one header is read** (`x-forwarded-for`, the
    default, with the rightmost-untrusted walk; or `x-real-ip` for edges
    that overwrite it) — never a fallback chain, so a client-supplied value
    in the unused header cannot forge the client IP. Without the list,
    forwarded headers are ignored entirely. Startup **refuses** the
    combination of `trusted_proxies` with Serve identity trust (`auto` /
    `tailnet`): a generic edge forwards client-supplied
    `Tailscale-User-Login`, which the local-peer gate would trust into
    admin. Rate limits and audit both resolve through this.
  - **External-principal audit fields** — invite-token principals now audit
    as `scope:<id>` (previously the shared `api-key` label), and audit rows
    carry the resolved `client_ip` (column added with an automatic
    migration for existing `audit_log.db` files). Rate-limit denials audit
    as reason `rate_limited`.
  - **TLS edge deployment notes** in `docs/external-access.md` (edge
    terminates TLS, sets the client-IP hop, never publishes the admin
    listener) with the `trusted_proxies` / `rate_limits` shape in
    `docs/configuration.md`.
>>>>>>> origin/main

- **Invite lifecycle with hash at rest (issue #253, phase 3).** Data-scope
  credentials — the invite tokens external principals hold — are now
  administered like app tokens instead of by editing `data_scopes.json`:
  `POST /v1/admin/data-scopes` mints, `POST /v1/admin/data-scopes/{id}/rotate`
  re-issues, and `PATCH /v1/admin/data-scopes/{id}` disables/renables (or
  retunes `name` / `grants` / `read_only`). Plaintext invite tokens return
  only from mint and rotate; `data_scopes.json` (version 2) stores just the
  SHA-256 hash (`api_key_hash`) plus a non-secret `api_key_hint`, matching
  the `apps.json` posture. Legacy files with plaintext `api_key` entries are
  migrated to hashes on load. Generated tokens carry an `aeg_invite_` prefix
  so they cannot be confused with app tokens or the agent key; lookup stays
  hash-based and prefix-agnostic for operator-chosen keys.

- **Scope-grant enforcement: deny-by-default for invite tokens (issue #253,
  phase 2).** The fail-closed scope-grants resolver is live and enforced at
  the existing seams — library aggregation, content transport, mail routes,
  pre-request injection, and per-scope cache keys. An external principal's
  `grants` map is now the **complete allow list**: an unlisted type resolves
  to `none`, a reserved `default` entry covers unlisted types only, a listed
  type with an empty entry (or only provider overrides) resolves to `none`,
  and provider entries tighten a stated level without ever opening a closed
  type. Opt-in types (places) still need an explicit name. The resolver is
  `types.ScopeGrantsAccess` / `ScopeTighten` — deliberately separate from
  `DataGrants.Access`, which keeps owner-tier "unset = unrestricted"
  semantics. Effective access ANDs both layers (same as app tokens): a scope
  grant can never open what owner data grants closed. Redaction-level kinds
  (freebusy events, summary transactions, city/precise places, metadata
  documents) now pick up their level from the ANDed view, so a
  freebusy-only principal sees redacted rows even when the owner tier is
  read. **Migration:** scopes relying on "empty grants = full read" must set
  `"grants": {"default": {"default": "read"}}` (the demo pack already
  does) — an empty `grants` map now denies everything. Each row of the
  resolution table is a test case, and the isolation contract gains a
  principal holding no grants.

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

- **Docs/contract: external access is the multi-user path (issue #149) —
  trust-model rewrite, no behavior change in this entry.** The contract
  later code PRs are reviewed against stops assuming "tailnet = perimeter"
  for every surface. The model is now **two listeners**: a publishable
  **API listener** whose perimeter is the *credential* (invite tokens and
  app tokens only — the owner-tier agent key stays tailnet-side), and an
  **admin listener that stays in-tailnet** and is never published. External
  principals authenticate with **invite tokens** (data-scope keys), are
  **deny-by-default** on data types (scope grants are the complete allow
  list; a reserved `default` entry covers "share everything read-only"), and
  are never admin. Ambient Tailscale identity (Serve headers / WhoIs) must
  never be trusted on an externally reachable listener — the old
  always-on `ServeHeaderResolver` trust is called out as a landmine to fix
  before any public bind (startup refusal, not a warning). Hash-at-rest for
  invite tokens, a fail-closed scope-grants resolver, an API route
  allowlist, and rate limiting with trusted-proxy config are contract
  requirements the implementation must meet. No self-service signup,
  passwords, OIDC, or instance federation in this phase; another Aegis
  instance is just a caller holding an invite token. `SECURITY.md`,
  `docs/threat-model.md`, `docs/data-scopes.md`, `docs/glossary.md`,
  `docs/configuration.md`, the demo runbook/nginx (admin now dropped at the
  edge), and the AGENTS.md non-negotiables follow; the listener split,
  grant enforcement, invite lifecycle, and rate limiting land as code next.
  Design note: `docs/external-access.md`.
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

- **Serve identity headers can no longer be forged (issue #149 landmine).**
  `Tailscale-User-Login` and friends are request headers — on any socket a
  LAN or tailnet peer could reach directly (the default `-addr :8080` bound
  all interfaces), one forged header passed the default admin rule and became
  full admin. Three layers close it: (1) `ServeHeaderResolver` trusts headers
  only from a **local peer** (loopback or an address on this host's own
  interfaces — the hop `tailscale serve` or a same-host proxy makes);
  `RemoteAddr` is the accepted connection's address and cannot be forged, so
  from a remote peer the headers are ignored and resolution falls through to
  WhoIs; (2) `-addr` now defaults to `127.0.0.1:8080`; (3) with Serve
  identity trust (`auto`/`tailnet` auth mode), startup **refuses** a wildcard
  or LAN bind — sound binds are loopback behind `tailscale serve`, the
  Tailscale IP, or `-auth-mode api-key` (no ambient identity to forge).
  Existing deployments binding `:8080` in `auto`/`tailnet` mode must pass
  `-addr 127.0.0.1:8080` (or the Tailscale IP) after upgrading. Landed ahead
  of the external-access listener split as its prerequisite.
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
