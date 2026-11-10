# Sync maintenance and self-repair

This doc is the long-term approach for keeping sync reliable as provider APIs
drift (#86). It defines the **self-repair loop** — collect signals, ticket
reproducible failures, draft fix PRs, review, release — and the guardrails that
keep agent-authored fixes safe around OAuth tokens and write paths.

Use it when you are:

- wiring signal collection or canary tests for an instance,
- building the failure scanner, ticket schema, or fixture scrubber,
- reviewing an agent-authored fix PR,
- or deciding whether a new failure class should enter the loop.

Related product rules live in `AGENTS.md`; signal surface lives in
`docs/observability.md`. This file is about the **maintenance loop**, not the
connector or library API surface.

---

## Vocabulary

Keep these apart — "repair" already names two different things.

| Term | Meaning here |
|------|--------------|
| **Sync maintenance** | The program: detect and repair connector reliability over time (#86) |
| **Self-repair loop** | The automated pipeline in this doc (phases 1–5) |
| **Sync job** | Durable queue unit of data-movement work (#227): `ingest`, `import_snapshot`, `mirror`, `freshness_check`, `repair`, `canary` |
| **`repair` job type** | Runtime **data** repair (re-copy, restamp ledger facts). Not code repair |
| **Freshness check** | Data-level drift signal: copy rows re-checked against the source (`in_sync` / `stale` / `missing`) |
| **Repro bundle** | The evidence a ticket carries: scrubbed fixture + red test + failure signature |
| **Ticket** | A GitHub issue in the Aegis repo carrying the structured contract below (label `sync-failure`). The loop never grows a parallel defect tracker |
| **Maintenance signals** | The collected observation bundle (phase 1): API `GET /v1/admin/maintenance/signals`, files under `<data-dir>/maintenance/`. Named "maintenance" to stay clear of the Signal messenger connector (`signal.db`, `/v1/admin/signal`) |
| **Canary** | A scheduled test run — synthetic (fixture replay) or against live provider accounts |

Product English follows `docs/glossary.md` (*copy ledger*, *mirror*, *canonical
home*). "Sync" here is reliability language, not "automatic write sync" —
mirrors stay explicit and user-initiated.

## Why a loop, not a backlog

Aegis fail-opens: fetch, parse, and match failures pass through and land in
logs and metrics. That is correct for the agent request path and wrong as a
maintenance strategy. A drifted connector degrades **silently** — empty lists,
200s with wrong shapes, stale freshness — while every request still "succeeds".
Provider APIs drift on their own schedule, so repair work has to be continuous,
evidence-driven, and cheap enough to run without the maintainer noticing every
breakage first. LLMs are good at adapting to small contract changes; the loop
below exists to feed them the right evidence and to fence what they may change.

## What already exists

The loop composes pieces already in the tree. Greenfield work is the scanner,
repro capture, and the agent harness — not the signals.

| Piece | Where | What the loop gets from it |
|-------|-------|----------------------------|
| Sync metrics | `docs/observability.md`, `internal/core/metrics/` | `aegis_sync_jobs_total{job_type,state}`, `aegis_sync_retries_total{job_type,error_class}`, `aegis_sync_job_duration_seconds`, `aegis_sync_queue_depth{state}`, `aegis_sync_last_success_timestamp_seconds{kind,service}` |
| Upstream metrics | same | `aegis_upstream_duration_seconds{service}` — latency/status drift per connector |
| Logs | `<data-dir>/logs/aegis.log` (`log/slog`) | Error detail behind the counters: `service`, `path`, `error` |
| Audit log | `audit_log.db` | Redaction precedent (no tokens, bodies, query strings, full paths); access anomalies |
| Sync job queue | `internal/core/syncjobs/` (engine; #227) | States `queued`/`running`/`succeeded`/`failed`/`canceled`/`paused`; error classes `retryable`/`terminal`; restartable leased steps |
| Freshness engine | `internal/core/syncfreshness_runner.go` | Per-kind rollup: `checked`/`in_sync`/`stale`/`missing`/`skipped` (messages, playlists, tracks today) |
| CI gates | `.github/workflows/ci.yml` | `go vet`, race tests, **80% coverage floor**, staticcheck, gitleaks, 30s fuzz — the anti-degradation gates every fix PR must pass |
| Replay seam | `httptest.NewServer` + configured `endpoint` | How fixtures replay offline without touching a provider |

Two facts the scanner must honor:

- Counters are process-lifetime and **reset on restart** — treat a drop as a
  counter reset, and pin every observation to an instance + version stamp.
- Fail-open means the absence of errors is not the presence of health —
  `stale`/`missing` freshness, stale `last_success`, and empty-result
  anomalies are first-class signals.

## The loop

```
  live instances ──┐
                   ├──▶ 1 collect signals ──▶ 2 scan + cluster ──▶ ticket
  scheduled tests ─┘         ▲                   │              (repro bundle)
  (canaries)                 │                   ▼
                             │              3 fix PR (red test first)
                             │                   │
                             │                   ▼
                             │              4 review + merge (tiered)
                             │                   │
                             └── 5 release ◀─────┘
```

### Phase 1 — Signal collection

Two sources, deliberately in this order:

1. **Live instances.** Scrape `/metrics` over the tailnet and export error-log
   lines and freshness rollups. Pull-based (scrape) by default: instances need
   no egress, and the trust boundary stays the tailnet (`SECURITY.md` — the
   admin listener is never published; `/metrics` is admin-scoped).
2. **Scheduled tests (canaries).** Escalate by risk tier:

| Tier | What runs | When |
|------|-----------|------|
| Synthetic | Fixture replay against `httptest` mocks, CI-shaped | Now |
| Anonymous | Provider endpoints that answer without auth | Now |
| Read-only live | Real accounts, read scopes only | When test credentials exist |
| Write | Mutating calls | **Disposable accounts only**, ever |

Every observation carries: instance id, Aegis version/commit, timestamp,
`service`/`kind`, and where possible `job_type`. Without the version stamp,
phase 2 cannot tell which code a failure refers to.

### Phase 2 — Scan, cluster, ticket

- **Cluster by failure signature**, not by log line. One upstream breakage
  yields many lines; the signature is `(service, path class, error class,
  normalized error shape)`. This signature is the agreed first cut — add
  response-shape hashing only if clustering proves noisy.
- **Scanner placement (decided).** A local scheduled job reads signal bundles
  from the data dir for now; long-term expect it to move to paid cloud
  compute. Bundles are therefore self-contained files — the scanner reads
  paths, not local IPC.
- **Ticket only what is reproducible.** Two gates. At scan time, failure
  clusters must recur across bundles; freshness findings are state, so the
  latest bundle is the evidence. At pickup time, phase 3 takes a ticket only
  once its repro bundle is attached (below): scanner-opened tickets start
  `repro_state: needs-fixture` and are promoted to `ready` at triage.
  One-off flake stays a metric.
- **Silent degradation counts.** Rising `stale`/`missing`, stale
  `aegis_sync_last_success_timestamp_seconds`, upstream latency shifts, and
  empty-result anomalies ticket just like error-class failures.
- Output is a GitHub issue carrying the structured ticket contract (below)
  — phase 3's input contract. Dedup is a signature marker in the issue body;
  one signature never becomes two issues.

### Phase 3 — Fix PR

The agent's job: turn the ticket's red test green without changing product
semantics. Pickup is label-filtered — `gh issue list --label sync-failure
--label ready` — so `needs-fixture` tickets wait at triage. Sequence:

1. Reproduce: add the repro test from the bundle. **It must be red on main.**
2. Fix the connector (or, rarely, core dispatch) behind that test.
3. Open a PR on a `fix/…` branch, body starting `## Summary` — same
   conventions as human PRs (`AGENTS.md`).

Mechanical gates do most of the safety work: coverage floor, staticcheck,
gitleaks, public-hygiene, fuzz. The behavioral guardrails are in "Guardrails"
below.

### Phase 4 — Review and merge (tiered)

Not all fixes carry the same risk. Tiers:

| Change class | Review gate |
|--------------|-------------|
| Fixtures, tests, docs | Human at first; auto-merge only after a track record exists |
| Connector logic | **Always human** — this code holds OAuth tokens and can write |
| `internal/core` / `internal/kinds` | **Always human** — the product contract |

Reviewer checklist for fix PRs:

- Red-first respected (repro test fails on the parent commit)?
- Fail-open preserved — no pass-through converted into a block?
- Upstream base read from configured `endpoint`, not hardcoded?
- No weakened or skipped tests; coverage floor holds?
- No secrets, no unscrubbed payloads in fixtures?
- Product semantics unchanged (kind-centric surface, media stays on the
  source service, no issue numbers in agent-facing strings)?

### Phase 5 — Release

- **Hold at "merged to main" initially.** A human cuts the release and writes
  the `CHANGELOG.md` entry. Automated point releases only after the loop has a
  track record — auto-release is the amplifier that turns one bad auto-fix
  into fleet-wide damage.
- The release carries the version stamp forward so phase 1 attributes new
  signals correctly. A fix that never ships cannot clear its own failure
  signature.

## Ticket schema (repro bundle)

The ticket is data. The GitHub issue body carries it as an evidence block
the scanner rewrites on every scan (idempotent — no comment spam); prose and
checklist edits around the block are triage's and never touched. A scanner
that cannot fill `signature`, the observation window, and `repro_state` is
not ready to ticket; the repro-bundle fields are filled at triage and gate
phase-3 pickup. Labels mirror `repro_state` and are authoritative on update:
flipping `needs-fixture` → `ready` is how triage promotes a ticket.

| Field | Notes |
|-------|-------|
| `signature` | Stable id: service + path class + error class + hash of normalized error |
| `repro_state` | `needs-fixture` (scanner-opened) or `ready` (repro bundle complete; phase 3 picks up `ready` only) |
| `service_id` / connector | Which adapter; which `LibraryKind`s are affected |
| `observed_in` | Aegis version/commit + instance id |
| `first_seen` / `last_seen` / `count` | From the cluster, not a single line |
| `signals` | References: metric series movement, log-line pointers, freshness deltas |
| `fixture` | Path to the scrubbed fixture (below) |
| `repro_test` | Path to the test that is red on main with that fixture |
| `expected` / `actual` | Neutral-model shape difference — the definition of "fixed" |
| `tier` | Which phase-4 review tier applies |

## Fixture format

A fixture is both the evidence and the regression test. It replays through
`httptest` with the service `endpoint` pointed at the mock — the same seam
existing connector tests use.

- **Location:** `testdata/repro/<service>/<signature>/` beside the package
  whose test replays it (connector fixtures under `internal/connectors/`).
- **Content:** request/response pairs sufficient to reproduce the parse or
  call failure, plus the request the connector issued. One fixture per
  signature; a signature's fixture set may grow as the scanner finds variants.
- **Scrubbing rules — mandatory before anything lands in git:**
  1. No tokens, cookies, or credentials of any kind. Ever.
  2. No raw message bodies, photo bytes, or file contents. Truncate to the
     shape-revealing prefix the parser reads.
  3. No email addresses, real names, or account identifiers — placeholders
     only (`user@example.com`), matching `public_hygiene_test.go`.
  4. Query strings and headers reduced to the fields the parser actually
     reads.
  5. If scrubbing cannot make the capture safe, the ticket carries a
     **synthetic** fixture that reproduces the shape instead of the real one.

Fixture capture happens on-instance and is scrubbed there; only the scrubbed
artifact travels to the repo.

## Signal export policy

Default posture matches the audit log and metrics: **redacted metadata, never
payloads.**

| Leaves the instance | Stays on the instance |
|---------------------|-----------------------|
| `/metrics` counters and histograms | Request/response bodies |
| Scrubbed error-log lines | Library contents (lists, mail, photos) |
| Freshness rollup counts | Agent identities (deliberately not metric labels) |
| Instance id + version stamp | Tokens, config secrets, query strings, full paths |

Collectors run on the tailnet. Pull-based collection by default; push is
acceptable later for instances the project cannot scrape, with the same
scrubbing rules applied before upload.

## Guardrails

These are the loop's non-negotiables. They extend `AGENTS.md`; they do not
replace it.

1. **Red first.** The repro test must fail on main before the fix is drafted.
   A ticket whose test is already green is stale, not fixed.
2. **No test weakening.** Coverage floor and existing tests hold. No `t.Skip`,
   no fixture deletion to make CI green.
3. **Fail-open stays fail-open.** A fix must not convert a pass-through into a
   block or a hard 500.
4. **Endpoint from config.** Never hardcode upstream base URLs — mock tests
   and custom endpoints depend on `ServiceConfig(...).Endpoint`.
5. **No scraping or impersonation in automation.** Repairs stay on
   provider-documented APIs and user-authorized OAuth scopes. #86 floats
   impersonation and scraping as human-judgment fallbacks; they are
   **explicitly outside** the automated loop and need a human decision every
   time.
6. **Secrets hygiene.** gitleaks stays blocking; scrubbing rules above are
   the fixture-level equivalent.
7. **Product semantics are not tradeable for reliability.** No provider-shaped
   API added to the agent surface, no media storage introduced, no
   connections dual-path resurrected — a fix that "works" by changing the
   product is a regression.
8. **Write paths get an extra gate.** Never validated against real user
   accounts. Disposable canary accounts only.

## Relationship to the sync job queue

The queue's `repair` job type fixes **data** (re-copy, restamp). This loop
fixes **code** (connectors). Keep the seam:

- Sync-job failures (`error_class=terminal`, exhausted retries) and freshness
  rollups are phase-2 signals — the queue's own failure history feeds the loop.
- After a fix ships, the release may enqueue `repair` or `freshness_check`
  jobs to re-verify affected rows; say so in the changelog entry.
- Do not overload `repair` to mean code repair. If the loop ever needs to
  enqueue its own work on the queue, it gets a new `job_type` (the queue
  accepts open strings by design).

## Driving the loop without real drift

Before real provider drift appears, the loop can be exercised end to end
against an injectable fake provider. The proof in CI is
`TestMaintenanceLoop_FakeDriftToTicket`; for a manual run against a dev
instance:

1. **Start with the harness** — `-fake-upstream` (dev/test only, never on a
   real instance) serves a Spotify saved-tracks shape at
   `/demo/fake-upstream` and seeds placeholder tokens for any configured
   service whose `endpoint` points there. Control is admin-scoped at
   `POST /demo/fake-upstream/control`.
2. **Point a service at it**, e.g. a `spotify` service with
   `endpoint: http://localhost:8080/demo/fake-upstream`.
3. **Enqueue a canary** — the `canary` job type fetches one saved-tracks
   page through the configured endpoint and fails on errors or on the
   silent-drift shape (pagination metadata claims a library, items yield
   nothing). A genuinely empty library is health:

   ```bash
   curl -X POST -H "Authorization: Bearer $ADMIN_KEY" \
     -d '{"kind":"tracks","job_type":"canary","service":"spotify"}' \
     "$AEGIS/v1/admin/sync"
   ```
4. **Inject a failure mode** and run the canary again:

   | Mode | What the connector sees |
   |------|-------------------------|
   | `healthy` | Normal saved-tracks pages |
   | `empty` | Coherent empty library (total 0) — health |
   | `http-500` / `http-401` | Error statuses |
   | `garbage` | 200 with a body that is not JSON |
   | `drift-items` | Valid JSON with the `items` array renamed to `entries` — the issue #240 migration, which the adapter now reads (regression shape) |
   | `drift-unmapped` | Valid JSON with the `items` array renamed to a name no adapter knows — silent degradation, for the canary's incoherence guard |

   ```bash
   curl -X POST -H "Authorization: Bearer $ADMIN_KEY" \
     -d '{"mode":"drift-items"}' \
     "$AEGIS/demo/fake-upstream/control"
   ```
5. **Collect and scan** (the phase-1/2 surface): capture a bundle, scan, and
   the drift surfaces as a `sync-failure` ticket with `needs-fixture`.
   Record the repro fixture from the fake's drifted response via the
   `ReproRecorder`, attach it with a red test, flip the label to `ready`,
   and the fix cycle (first-slice item 4) picks it up from there.

## Dashboard

The loop's work should be visible, not implied (issue #239). The dashboard is
an **entirely build-time generated** static site — no scripts, no live
queries — with one page per service connection showing history and the
success / stability / confidence marks, and it is **published as part of
every release** (merge to main, for now). It is a **separate tool that
analyzes the progress of the project** (issue #335), not part of the
application code: `cmd/aegis-dashboard` collects its data from the gh CLI
(the same transport as the ticket surface), the committed services
manifest, captured signal bundle files, and the connector metadata catalog,
and writes the pages next to the marketing site (`site/maintenance/`),
linked from the site nav. History therefore grows with the project: each
release publishes an updated view.

The **evidence is the issue record** — the loop's `sync-failure` tickets in
full lifecycle (open with `needs-fixture`/`ready`, or fixed once closed
completed) plus reported issues behind an explicit allowlist: a `bug` label
or closure by a fix-convention PR (`fix:` title or `fix/` branch), naming a
service. Allowlisted problems stay visible while open and count against the
fix rate; anything else is dropped — the page is a maintenance record, not
an issue mirror, and a smaller record is safer to publish than a wrong one.
Triage therefore curates the published record: add the `bug` label to make
an issue count. The marks measure maintenance responsiveness, not live sync
outcomes:

- **success** — share of recorded problems resolved (fix rate). A problem
  closed as not planned counts as recorded but unresolved.
- **stability** — how long the service has been quiet: days since the last
  recorded failure event over a thirty-day window. Quiet is not health — an
  old open problem does not lower it.
- **confidence** — record depth (item-count bands: 1–2 low, 3–9 medium,
  10+ high), so a single early blip cannot read as high confidence.

Each page also carries a **connection maturity** mark (issue #247) — a
coverage statement, not an operational score, and the fourth thing a reader
should not confuse with the three above. It answers: of the kinds this
service offers, how many does Aegis serve through the connection, and how
completely does it fill their neutral-model fields? It moves only when the
connector code moves:

- **kinds** — every kind the service offers in theory, each marked
  supported or not. The unsupported rows are the point: the gap is shown,
  not implied by absence.
- **field coverage** — for supported kinds that map to a neutral model,
  which content fields the connector populates. `sources[]` is structural
  and never graded.
- **transport** — how the connection reaches the service (`api`,
  `protocol`, `reverse-engineered`, `export/archive`). API-based
  connections read as more mature than ones that depend on exported files
  or an undocumented surface; it is shown beside the level rather than
  folded into the number.

The level (`complete` / `substantial` / `partial` / `basic`) bands on the
lower of kind coverage and field coverage, so a connection cannot read as
mature by implementing one complete kind out of ten. `complete` is the
issue's bar: every offered kind served and every graded field populated.

The declaration lives in code (`internal/core/maturity.go`, graded by
`internal/connectors/maturity.go`) next to the connectors it describes — the
same class of hand-maintained metadata as `Capabilities()`. Tests hold it
honest against the connectors themselves: anything the connector serves
(a callable operation or a library feed) must read as supported, and no
entry may claim a kind the connector does not recognize. Both the published
build and the instance build grade the same catalog.

Two inputs shape the published build:

- **The services manifest** (`site/maintenance/services.json`, committed):
  which pages exist, display names, and which entry is the drift-injection
  harness. Harness runs should enqueue canaries with the `fake-upstream`
  service id so their tickets attribute to that page — it is annotated as
  deliberately unstable, and its "a lot of activity, not much stability" is
  exactly what its marks should say.
- **The repo's issue history**, scraped at generation time. A scrape failure
  fails the release build rather than publishing an empty record over a good
  one.

The instance view is the same tool over captured data, not an application
surface (issue #335): instance-level **captures** stay on the admin surface
(`POST /v1/admin/maintenance/signals` writes bundles under
`<data-dir>/maintenance/`), and the tool builds the pages from them —

```bash
go run ./cmd/aegis-dashboard -repo owner/name \
  -bundles "$DATA/maintenance" -out "$DATA/maintenance/dashboard" -private
```

including capture-history tables and scrubbed failure events where bundles
exist, plus the instance's own issue scrape (`-repo`). `-private` writes the
0600/0700 posture for output under a data directory; the default is the
world-readable posture the Pages upload needs. A scrape failure fails the
build rather than publishing an empty record over a good one;
`-allow-scrape-failure` degrades to a noted gap instead. Signal bundles
carry a `by_service` rollup so per-service history is attributable from
bundles alone, keeping the "scanner reads paths" seam intact.

## Non-goals

Deliberately out of scope while the loop proves out:

- **A local or parallel defect tracker.** Tickets are GitHub issues — the
  project's existing issue-driven workflow is the tracker.
- **Declarative patchable connector packs** (hot-reload without a rebuild) —
  promising follow-on so drift-prone call shapes can be fixed without a
  release, but this doc's loop patches Go source and ships a release.
- **A separate connectors repository** — single repo for now; revisit if
  contribution volume or CI cost demands it.
- **Auto-merging core or kind changes** — never in the loop.
- **Fleet management for third-party instances** — collection assumes
  instances this project can reach on its tailnet.

## Open questions

- **Auto-merge thresholds.** What track record and change class justify
  removing the human from the fixture/test tier?
- **Canary account provisioning.** Which providers can even supply disposable
  accounts, and at what cost/ToS position (Amazon Music and YouTube look
  hardest).
- **Export shape for un-scrapeable instances.** Push format and retention if
  we ever accept signals from instances outside the tailnet.

## First slice

Prove the loop on one or two connectors before building fleet collection:

1. **Signal collection** (phase 1): build stamps plus maintenance signal
   bundles (pull via `GET /v1/admin/maintenance/signals`, capture to
   `<data-dir>/maintenance/`).
2. **Fixture capture + scrubber** for one connector, replayable offline
   (repro fixtures under `internal/connectors/testdata/repro/`).
3. A scanner over local signal bundles that opens GitHub issues as tickets
   (a local scheduled job; manual trigger is fine to start).
4. One end-to-end fix cycle — ideally a forced contract drift — done
   agent-assisted with a human reviewer, using the ticket schema as-is.
5. Only then: schedule canaries and automate ticket intake.

Done means: a fixture captured at failure time becomes a red test an agent can
turn green, and a reviewer can judge the PR without re-deriving the bug from
raw logs.
