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
| **Sync job** | Durable queue unit of data-movement work (#227): `ingest`, `import_snapshot`, `mirror`, `freshness_check`, `repair` |
| **`repair` job type** | Runtime **data** repair (re-copy, restamp ledger facts). Not code repair |
| **Freshness check** | Data-level drift signal: copy rows re-checked against the source (`in_sync` / `stale` / `missing`) |
| **Repro bundle** | The evidence a ticket carries: scrubbed fixture + red test + failure signature |
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
| Sync metrics | `docs/observability.md`, `internal/core/metrics_export.go` | `aegis_sync_jobs_total{job_type,state}`, `aegis_sync_retries_total{job_type,error_class}`, `aegis_sync_job_duration_seconds`, `aegis_sync_queue_depth{state}`, `aegis_sync_last_success_timestamp_seconds{kind,service}` |
| Upstream metrics | same | `aegis_upstream_duration_seconds{service}` — latency/status drift per connector |
| Logs | `<data-dir>/logs/aegis.log` (`log/slog`) | Error detail behind the counters: `service`, `path`, `error` |
| Audit log | `audit_log.db` | Redaction precedent (no tokens, bodies, query strings, full paths); access anomalies |
| Sync job queue | `internal/core/syncjobs.go` + `syncworker.go` (#227) | States `queued`/`running`/`succeeded`/`failed`/`canceled`/`paused`; error classes `retryable`/`terminal`; restartable leased steps |
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
   no egress, and the trust boundary stays the tailnet (`SECURITY.md` — no
   Funnel).
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
- Output is a structured ticket, not prose — it is phase 3's input contract.

### Phase 3 — Fix PR

The agent's job: turn the ticket's red test green without changing product
semantics. Sequence:

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
| `internal/core` / `internal/types` | **Always human** — the product contract |

Reviewer checklist for fix PRs:

- Red-first respected (repro test fails on the parent commit)?
- Fail-open preserved — no pass-through converted into a block?
- Upstream base read from configured `endpoint`, not hardcoded?
- No weakened or skipped tests; coverage floor holds?
- No secrets, no unscrubbed payloads in fixtures?
- Product semantics unchanged (type-centric surface, media stays on the
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

The ticket is data. A scanner that cannot fill `signature`, the observation
window, and `repro_state` is not ready to ticket; the repro-bundle fields
are filled at triage and gate phase-3 pickup.

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

## Non-goals

Deliberately out of scope while the loop proves out:

- **Declarative patchable connector packs** (hot-reload without a rebuild) —
  promising follow-on so drift-prone call shapes can be fixed without a
  release, but this doc's loop patches Go source and ships a release.
- **A separate connectors repository** — single repo for now; revisit if
  contribution volume or CI cost demands it.
- **Auto-merging core or type changes** — never in the loop.
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
3. A scanner over local signal bundles that opens structured tickets
   (a local scheduled job; manual trigger is fine to start).
4. One end-to-end fix cycle — ideally a forced contract drift — done
   agent-assisted with a human reviewer, using the ticket schema as-is.
5. Only then: schedule canaries and automate ticket intake.

Done means: a fixture captured at failure time becomes a red test an agent can
turn green, and a reviewer can judge the PR without re-deriving the bug from
raw logs.
