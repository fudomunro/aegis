# Development

Adding or maturing a unified library data type? Follow
[docs/adding-data-types.md](adding-data-types.md) — the checklist for
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
  -addr :8080 \
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
| `mail-archive.db` | Queryable archive for `/v1/mail/search` |
| `sample-mail.mbox` | Same messages as a Takeout-shaped mbox (importer round-trip) |
| `sample-library.json` | Neutral `Playlist` / `Track` / `Contact` / … fixtures for mock upstreams |

The generator lives in `sampledata.go` (`GenerateSampleMail`,
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

Run the checklist alone:

```bash
go test -count=1 -run 'TestMailQuery|TestLibraryMerge|TestLibraryAggregates|TestGenerateSample' .
```

CI uses only the small set. Do not commit large binary fixtures — generate
stress data locally with `-seed-sample-stress`.

### Project structure

```
aegis/
  main.go                    Thin entry (core.Run())
  internal/types/            Neutral models, merge/identity, LibraryKind data,
                             DataGrant, path rules, provider interfaces
  internal/core/             Runtime: Server, auth, config, tokens/OAuth, proxy,
                             permissions enforcement, library aggregate, admin,
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
  docs/adding-data-types.md  Guide: defining a new library data type (issue #92)
```

Layout is the #15 split: **types** (data) / **core** (runtime) / **connectors**
(named services). See `docs/restructure-core-types-connectors.md`.

