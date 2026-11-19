# Development

Adding or maturing a unified library kind? Follow
[docs/adding-kinds.md](adding-kinds.md) — the checklist for
neutral models, provider interfaces, merge identity, locations/copies,
permissions, and sample fixtures (issue #92).

```bash
# Run tests
go test ./...

# Run with verbose output
go test -v ./...

# Race + coverage profile (matches CI)
go test ./... -count=1 -race -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -n 1
go tool cover -func=coverage.out | sort -t$'\t' -k3 -n | head -n 20

# Build
go build -o aegis .

# Run with text logging for development
./aegis \
  -addr 127.0.0.1:8080 \
  -base-url http://localhost:8080 \
  -api-key dev-key \
  -token-pass dev-pass \
  -log-level debug \
  -log-format text
```

CI fails if statement coverage drops below the floor in `.github/workflows/ci.yml`
(currently 80%). Raise the floor when the suite grows; do not lower it to land a
change that only adds untested write paths.

### Sample data

Synthetic fixtures cover every neutral library kind plus the mail archive so
search, sort, filters, merge, and agent-facing endpoints can be exercised
without OAuth, provider quotas, or real personal mail. All generated data is
fictional (`example.com` addresses, invented titles) and safe to regenerate.

```bash
# Small CI-sized set (default 24 mail rows + edge cases) into the data dir
./aegis -seed-sample

# Larger stress set for local ingest/search benchmarks (issue #75)
./aegis -seed-sample -seed-sample-stress

# Explicit row count
./aegis -seed-sample -seed-sample-count 500
```

`-seed-sample` writes under `<data-dir>/sample/` and loads the mail set into
the configured mail archive (`mail-archive.db`), then exits:

| File | Purpose |
|------|---------|
| `mail-archive.db` | Queryable archive behind `/me/messages` |
| `sample-mail.mbox` | Same messages as a Takeout-shaped mbox (importer round-trip) |
| `sample-library.json` | Neutral `Playlist` / `Track` / `Contact` / … fixtures for mock upstreams |

The generator lives in `internal/core/sampledata/` (`GenerateSampleMail`,
`GenerateSampleLibrary`). Tests load it via `SeedMailArchive` /
`SeedSampleArchive` and run the query checklist in `sampledata_query_test.go`:

- oldest / newest (`sort=asc|desc`) — dateless rows sort last so they cannot win
- date window (`after` / `before`)
- from / to / label filters
- FTS `q=` multi-word and hostile operator strings
- pagination (`limit` / `offset`) with stable order
- fetch by id + `raw`
- library merge (`sources[]`, `entity_id`, `match`) for ISRC tracks, contact
  identity chains, and Message-ID / subject+from+time messages
- permission-denied services and `truncated` page caps

Dataset-namespaced variants (`GenerateSampleLibraryFor` / `GenerateSampleMailFor`)
feed data-scope isolation tests (`datascope_test.go`) and `sample://library`
fixture connectors. See [`docs/data-scopes.md`](data-scopes.md).

LotR demo packs (`core/sampledata/dataset_lotr.go`, `-seed-lotr`) provide two themed
partitions (`shire` / `gondor`) for the public demo — see
[`docs/demo-datasets.md`](demo-datasets.md).

Run the checklist alone:

```bash
go test -count=1 -run 'TestMailQuery|TestLibraryMerge|TestLibraryAggregates|TestGenerateSample' .
```

CI uses only the small set. Do not commit large binary fixtures — generate
stress data locally with `-seed-sample-stress`.

### Performance benchmarks

Measurement infrastructure for the I/O and CPU performance work (issue #75).
Two layers: Go microbenchmarks for repeatable per-operation cost, and one
manual end-to-end procedure for real disk-read numbers. Post before/after
numbers on the performance issue when changing anything these cover — a
benchmark without a recorded number is not evidence.

**Microbenchmarks.** All are in `_bench_test.go` files beside the code they
exercise and none run in CI (CI machines are too noisy to gate on):

```bash
# Mail archive write and read paths (per-message SQL + inline FTS)
go test -bench 'BenchmarkArchive' -benchtime=2s ./internal/core/mail/

# Takeout export walk + RFC822 parse only (no archive writes)
go test -bench 'BenchmarkTakeoutWalk' -benchtime=2s ./internal/connectors/

# Full -import-takeout run: walk + parse + identity check + SQLite ingest
go test -bench 'BenchmarkImportTakeout' -benchtime=1x ./internal/cli/

# Response cache hit cost and Set eviction churn
go test -bench 'BenchmarkCache' -benchtime=2s ./internal/shared/

# Pre-request hook injection (JSON round trip over a realistic body)
go test -bench 'BenchmarkRequestInstructions' -benchtime=2s ./internal/core/inject/
```

`BenchmarkImportTakeout` is the headline number for ingest work: one
iteration ingests 3000 messages across three batches, so the durable walk
cursor resume is on the measured path. It reports `msgs/sec` and source
`MB/s` alongside wall time; run it with `-benchtime=1x` because one
iteration already costs hundreds of milliseconds.

**Manual end-to-end procedure.** For real disk-read byte counts, which the
microbenchmarks cannot see:

```bash
# 1. Generate a stress corpus (10k mail rows). This also fills
#    <data-dir>/mail-archive.db — the import below must target a FRESH
#    archive to measure ingest rather than skips.
./aegis -data-dir /tmp/aegis-bench -seed-sample -seed-sample-stress

# 2. Import the generated mbox into a fresh archive, watching I/O
time ./aegis -import-takeout /tmp/aegis-bench/sample/sample-mail.mbox \
  -mail-archive /tmp/aegis-bench/import-target.db \
  -copy-ledger /tmp/aegis-bench/import-ledger.db \
  -import-takeout-batch 2000
```

Record per run: wall time, `messages/sec` from the batch log lines, process
read/write bytes (macOS `fs_usage -w -f filesys` or `pid_rusage`; Linux
`/proc/<pid>/io` and `iostat -w 1 -W`), and archive size after (`du -h`).
Compare the archive size against the source size — the gap is the write
amplification being tracked. Repeat with the server also running against the
same archive to see contention.

The generated corpus is ~3.2MB (10k rows, short bodies — that scale is for
search and read-path benchmarks, not byte volume). For ingest I/O numbers
at real export scale, point the same two commands at an actual Takeout
export, or rely on `BenchmarkImportTakeout`, whose fixture is deliberately
multi-batch so the cursor resume is on the measured path.

**What each number is for.** `BenchmarkImportTakeout` wall time and
`msgs/sec` move when ingest SQL changes (batch transactions, deferred FTS,
import PRAGMAs). `BenchmarkTakeoutWalk` moves when the export walk or
parser changes. `BenchmarkCacheSetChurn` should stay flat as entry count
grows — if per-op cost climbs, eviction is scanning more than it should.
`BenchmarkRequestInstructions` is the chat-latency-path guard.

### Project structure

```
aegis/
  main.go                    Thin entry (core.Run())
  internal/kinds/            Neutral models, merge/identity, LibraryKind data,
                             provider interfaces, aggregate pipeline
  internal/core/             Runtime: Server, auth, config, tokens/OAuth, proxy,
                             permissions enforcement, path rules, admin,
                             state stores (mail, copy ledger, Quicken, Timeline),
                             sample data, web/ embeds
  internal/connectors/       Named services (Spotify, YouTube Music, Amazon Music,
                             Google *, Immich, YNAB, OwnTracks, sample calendar)
                             implementing ServiceConnector + *Provider via Host
  web/ → internal/core/web/  Admin SPA + brand assets (//go:embed)
  SECURITY.md                Tailnet auth model, ACLs, operational guidance
  docs/threat-model.md       Assets, actors, threats, residual risks (auditors)
  docs/glossary.md           Canonical product vocabulary
  docs/caldav-carddav.md     Nextcloud/iCloud/Fastmail CalDAV & CardDAV setup
  docs/configuring-aperture.md  Aperture operator guide
  aegis.example.json         Example configuration
  experiments/index.html     Code walkthrough (experimental; regenerate via experiments/generate_walkthrough.py)
  docs/adding-kinds.md  Guide: defining a new library kind (issue #92)
```

Layout is the #15 split: **types** (neutral models + kind registry) / **core** (runtime) / **connectors**
(named services). See `docs/restructure-core-types-connectors.md`.

