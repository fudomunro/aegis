# Local import stores: shape decision

Status: **landed** — Signal, Quicken, and Timeline live in
`internal/connectors` as local import stores; this note is the design record.
Scope: Signal (`-import-signal`), Quicken (`-import-quicken`), Google
Timeline (`-import-timeline`). The mail archive / Google Takeout pair is
decided below but not moved by this note.

## The principle

Signal, Quicken, and Timeline are local filestore imports: SQLite stores
under the data directory filled by one-shot import flags. They are awkward
residents of `internal/core` because they look exactly like connectors
everywhere it matters — `types.MessageProvider` /
`types.TransactionProvider` / `types.PlaceVisitProvider`, the
`LibraryKind` registry, the maturity catalog, data grants, freshness —
and differ only in **transport** (local files/SQLite instead of upstream
HTTP via `Host`) and **lifecycle** (an import step; no OAuth, no refresh).

What must stay shared: provider interfaces, capabilities/maturity surface,
data grants, service naming, library fan-in, admin surfacing. What is
genuinely different is the transport and the import step. The shape below
maximizes the former and gives the latter exactly one owner.

## Decision: one connector family, two transports

**Signal/Quicken/Timeline move into `internal/connectors` as local import
stores — the second transport beside named services.** No new package.

```text
internal/connectors
├── host.go            ServiceConnector + Host (service transport)
├── localstore.go      shared local-store machinery (store transport)  ← one owner
├── signal.go          SignalStore + importer + connector
├── quicken.go         QuickenStore + importer + connector
├── timeline.go        TimelineStore + importer + connector
└── maturity.go        both transports declare rows here
```

Rejected alternatives:

- **`internal/stores` sibling package.** Splits the registries (maturity,
  service names, `ServiceConnector`) across packages and forces stores to
  import connectors just to satisfy the interface — the opposite of "as
  much in common as possible". AGENTS.md keeps the #15 three-package
  split; a fourth package needs a stronger reason than file placement.
- **`connectors/services/` + `connectors/stores/` subpackages.** Same
  split cost, more import plumbing. The services/stores boundary is not
  clean anyway: a store can be the companion input path to a service
  (Gmail + Google Takeout mbox), so the pair must not live across a
  package wall.
- **A local-transport variant of `Host`.** Stores need no `Host` at all —
  no tokens, no upstream, no cache. The service seam is `Host`; the store
  seam is the store constructor. `NewQuickenConnector`'s unused host
  field is dropped rather than formalized.

### Full `ServiceConnector`, not a narrower seam

Stores keep the full `ServiceConnector` surface (`Capabilities`,
`SystemInstructions`, `HandleProxy`) and their provider interfaces.
Library fan-in, capabilities, pre-request injection, and admin all key off
those surfaces; a narrower local seam would reintroduce the special cases
this work removes. What changes is only the constructor: services take
`Host`, stores take their store.

### The shared machinery: `localstore.go`

One owner for what was copy-pasted three times:

- SQLite lifecycle: empty path disables, `MkdirAll(0700)`, open, WAL,
  close, `Path`/`Enabled`.
- Import batching: the walk → dedupe → upsert loop, with row upserts
  wrapped in transactions instead of one `Exec` per row.
- `ImportStore` seam (`Path`, `Enabled`, `Count`, `ImportPath`) so core
  runs the CLI flags and the admin surface against one shape.

Per-store SQL schemas and parsers (QIF/QFX, Signal JSONL/JSON/SQLite,
Timeline JSON/zip) stay beside their store — they are provider quirks, not
shared machinery. Each store also declares its admin surface
(`connectors.ImportAdmin`: flag name, path hint, sample path, library
route, counters, hint copy, import runner), so core's admin handler
renders no provider-facing string (issue #254).

### What core keeps

Core keeps what is core: flag parsing and the one-shot import runners
(both thin wrappers over `ImportStore`), data-dir path resolution
(`datadir.go` already owns the layout), `cfg.EnsureService` wiring driven
by a per-store service descriptor exported beside the connector, and the
admin routes (`admin` stays on the core side of `Host`). The three
near-identical `HandleAdmin*` handlers collapse into one import-store
admin handler parameterized by the descriptor; response shapes are
unchanged so `admin.html` keeps working. `Server` fields for the three
stores stay (permissions overview and kind presence read them) but are
typed as the connectors' store types.

Service IDs/names, maturity rows, grant limits, and admin copy live
beside their store as one declaration per store, consumed by
`services.go`, `maturity.go`, `run.go`, and the generic admin handler — the
same "express themselves through the same surfaces" contract services
already have.

## Mail archive / Google Takeout

The mail archive is the clearest service+store pair (Gmail is canonical;
the mbox archive is the companion input path and search mirror), which is
exactly why it is not a simple third import store: it has a different
lifecycle (continuous mirror + FTS + `/v1/mail`), not a one-shot import.
That carve-out is resolved by `docs/mail-ingest.md` (issue #286, landed):
the archive stays core-owned — sibling to `copy_ledger.db`, not to the
Quicken importer — while the Gmail+Takeout pair moved into
`internal/connectors` as two `MailIngest` implementations behind a
core-owned `MailSink`. `-import-takeout` keeps its own CLI runner as a
thin wrapper over the ingest.

## Import flags and data-dir stores

Flags stay in `run.go` (the CLI entry point is the right owner for flag
parsing) and delegate to the shared import runner; `-import-takeout`
stays separate per above. Data-dir paths stay in `datadir.go`; an empty
explicit path disables the store — unchanged contract.
