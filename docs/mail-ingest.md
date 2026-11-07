# Mail ingest: the Gmail + Takeout seam

Status: **design** — this note is the plan for issue #286; the slices below
land as separate PRs. Nothing here has moved yet.
Scope: the Google Takeout / mail-archive pipeline that still lives in
`internal/core` (`mail_takeout.go`, `mail_archive_sync.go`,
`syncrunner_mail.go`, and the service-ID branches in `librarykind.go` /
`syncfreshness.go` / `mail_archive_http.go`).

Related issues: **#286** (this note), **#266** (local import stores; the
carve-out this resolves), **#265** (onboarding seam pattern), **#264**
(freshness-probe pattern), **#254** (parent cleanup), **#215** (attachment
gap — first follow-up consumer, not in scope), **#16** / **#139** (canonical
home, multi-source mail).
Related product rules: fail-open; canonical home stays the live provider
(#16); `/v1/mail` agent surface unchanged; Aegis does not store media
beyond the compressed mirror; coverage floor 80%.

---

## 1. Evaluation

`docs/local-import-stores.md` moved Signal/Quicken/Timeline into
`internal/connectors` but carved the mail pair out explicitly: the archive is
"the clearest service+store pair (Gmail is canonical; the mbox archive is the
companion input path and search mirror)" and yet "not a simple third import
store: it has a different lifecycle (continuous mirror + FTS + `/v1/mail`),
not a one-shot import". That carve-out left the largest service-specific
block in core — and the "more complicated feature" #254 predicted would not
fit the connectors structure as-is:

| File | Service-specific content |
|------|--------------------------|
| `internal/core/mail_takeout.go` (~633 ln) | Takeout parser: mbox/zip/dir walk with durable cursor, `takeoutServiceID`/`takeoutServiceName`, `X-GM-LABELS` → mailbox/direction/read mapping, Gmail header mapping via `connectors.MapGmailAddressHeader` / `GmailHeaderDateRFC3339` |
| `internal/core/mail_archive_sync.go` (~253 ln) | `gmailArchiveSyncer` over `*connectors.GoogleGmailConnector`; Gmail quota marker, pacing (`gmailArchiveGetInterval`), default query `in:anywhere` |
| `internal/core/syncrunner_mail.go` | `mailArchiveSyncer` type-asserts `*connectors.GoogleGmailConnector`; `doMailIngest` branches on `takeoutServiceID`; quota backoff keyed on the Gmail quota string |
| `internal/core/librarykind.go` | `expandMailArchiveSources` nests Takeout under Gmail; `librarySourceDisplayName` / `librarySourceLastSyncedAt` switch on `"mail-archive"` / `takeoutServiceID` / `GoogleGmailServiceID` |
| `internal/core/syncfreshness.go` | `takeoutChecker` (export-path stat) registered in the core checker registry |
| `internal/core/mail_archive.go` | Store is generic except `mailServiceName` and `Canonical: r.Service != takeoutServiceID` |
| `internal/core/mail_archive_http.go` | Routes are generic except the `google-takeout` branches and source-named hint copy |

Why design first: the block touches four owners at once — the archive store
(FTS, cursors, `/v1/mail`), the sync runner (bounded batches, retry/backoff),
the library overviews (source grouping), and the copy ledger (optimistic
Takeout snapshots). A blind `git mv` would drag `/v1/mail` routing and
ledger writes into connectors, or split the Gmail+Takeout pair across the
package wall #266 deliberately kept open.

---

## 2. Decisions

### 2.1 The archive store stays core-owned

**The mail archive is genuinely core state** — sibling to `copy_ledger.db`
and `entity_links.json`, not to the Quicken importer. It owns the FTS
search mirror, per-service sync cursors, dataset partitions (data scopes),
the `/v1/mail` agent surface, and the copy-ledger interplay; those are
library/policy surfaces, not provider transport. `/v1/mail` routes stay in
core under `AuthMiddleware` exactly as today.

What must change is that the store becomes **provider-neutral**: no
service-ID switches (`mailServiceName`, `Canonical: r.Service !=
takeoutServiceID`), names and canonical-ness read from declarations
(§2.3). Its write seam is already nearly neutral —
`RecordFromMessage(service, serviceName, msg, payload)` consumes
`types.Message`.

Rejected:

- *Move the archive into `internal/connectors` as an `ImportStore`.* Wrong
  lifecycle (continuous mirror, not one-shot import — #266's own reason),
  and it would drag `/v1/mail` routing, data grants, and merge hydration
  across the package line.
- *An `internal/stores` package.* Same registry-split cost #266 rejected.
- *Extract the SQLite open/WAL lifecycle now.* `localstore.go` is the right
  owner for that machinery, but the archive needs FTS5 and migrations the
  simple stores do not; converging the two is optional cleanup, not this
  work.

### 2.2 One `MailIngest` seam, two implementations — Takeout is an ingest source, not a store

Takeout is read-only ingest into the same archive Gmail live sync writes:
exactly the #266 case "a service may still need a filestore when the API is
incomplete: Gmail + Google Takeout". So the pair lands **together in
`internal/connectors`** — `google-gmail.go` (live paging) and
`google-takeout.go` (export walk) — behind one contract, with core handing
them a narrow write surface. This is the `Onboarder` shape (#265): an
optional capability beside `ServiceConnector`, looked up by service id,
implemented by whatever can feed the archive. The `*GoogleGmailConnector`
type assertion and the `takeoutServiceID` branch in `doMailIngest` both die.

```go
// internal/connectors — beside Onboarder / ImportStore.

// MailIngest feeds the local mail archive one bounded batch at a time.
// Callers repeat until Result.Done. Implementations: the Gmail connector
// (live cursor paging) and the Takeout export walk.
type MailIngest interface {
	IngestMail(ctx context.Context, sink MailSink, opts types.MailIngestOptions) (types.MailIngestResult, error)
}

// MailSink is the core-owned write surface (core implements it over
// *MailArchive). Connectors never see the archive type or its SQL.
type MailSink interface {
	// Store upserts one message plus its raw provider payload; core
	// stamps service name, dataset, and the FTS row.
	Store(service string, msg types.Message, payload []byte) error
	// Meta round-trips the ingest's durable state (export path, walk
	// cursor) namespaced by service. The same table carries the
	// last-sync stamp the overviews read.
	Meta(service, key string) (string, error)
	SetMeta(service, key, value string) error
}
```

Shape notes (exact folding lands in Phase 2 with the tests that pin it):

- `types.MailIngestOptions` / `types.MailIngestResult` are pure data (the
  current `MailSyncOptions` / `MailSyncResult` shapes), like
  `OnboardingSpec`. The result grows **`RetryAfter time.Duration`**: quota
  and pacing are provider knowledge, so the *ingest* classifies its own
  quota errors and asks for a backoff — core never matches on
  `"Quota exceeded"` again.
- Gmail's page token and Takeout's walk cursor (`root/file_idx/entry_idx/
  offset/done`) both round-trip through the sink, under the service's own
  meta keys. Cursor durability and "resume without full rescan" semantics
  are pinned by tests and must not change.
- Registration: a small mail-ingest lookup in core (filled in `run.go`
  beside `srv.connectors`) keyed by service id. Takeout is **not** forced
  into `srv.connectors` — it is a sync location, not an agent-callable API
  ("not a live service in aegis.json" today), and its `Capabilities()`
  surface would be empty. Gmail's entry is the connector itself.

Rejected:

- *Takeout as a third `ImportStore`.* One-shot import lifecycle, `ImportAdmin`
  counters — wrong shape for a durable mid-export cursor and a bounded
  batch chain.
- *One ingest implementation with a "source kind" parameter.* Gmail paging
  and mbox walking share nothing but the sink; a shared implementation
  would reintroduce the branches this removes.
- *Moving `/v1/mail` routes into the connectors.* Agent surfaces, auth, and
  grants stay core (the same line #265 drew for admin).

### 2.3 Provider knowledge and source grouping become declarations

Everything Gmail/Takeout-specific moves beside its ingest in
`internal/connectors`:

- `X-GM-LABELS` semantics (`takeoutLabels`, `takeoutMailboxFromLabels`,
  `takeoutDirection`, `Read = !UNREAD`), the `msgid:`/`blob:` id scheme,
  and record building — next to `MapGmailAddressHeader` /
  `GmailHeaderDateRFC3339`, which already live there.
- Gmail quota marker, pacing interval, default query, and quota
  classification — inside the Gmail ingest; outward only as
  `Result.RetryAfter`.
- `takeoutServiceID` / `takeoutServiceName` — exported connector constants
  beside `GoogleGmailServiceID`, consumed by the name registry.
- The generic mbox/zip/dir walk with byte-offset resume is **transport, not
  provider knowledge**, but it has one consumer. It stays beside the
  Takeout ingest (fuzz targets move with it) — no premature `internal/shared`
  extraction.

Source grouping and freshness become declared metadata (the `LocalStoreService`
pattern of #266, the `PlaylistMetaProvider` pattern of #264):

```go
// One declaration beside each ingest — consumed by library overviews,
// name lookup, and canonical stamping. Core renders; it never branches.
type MailSource struct {
	ServiceID string
	Name      string
	// Parent nests this source under another in overviews when that
	// source is present (google-takeout under google-gmail).
	Parent string
	// Canonical marks rows as the mailbox home (issue #16).
	// Gmail: true. Takeout: false.
	Canonical bool
}
```

- `expandMailArchiveSources` reads `Parent` instead of switching on
  `takeoutServiceID`; `librarySourceDisplayName` / `mailServiceName` read
  `Name`; `librarySourceLastSyncedAt` becomes a generic per-service
  cursor read (the `"mail-archive"` row keeps its own name — that is the
  core store's identity, not provider copy).
- `Canonical` stamps at write time (sink side); `Message()`'s
  `r.Service != takeoutServiceID` switch dies.
- Freshness: `takeoutChecker` becomes a **types-level optional probe**
  (like `types.PlaylistMetaProvider`) with one generic core checker in the
  existing registry — "does this source's declared snapshot location still
  exist". Core keeps the registry and the `CopyVersion` comparison; the
  path-stat and its copy move to the connector.

### 2.4 What moves, what stays

| | `internal/connectors` | `internal/core` |
|---|---|---|
| **Moves** | Takeout mbox/zip walk + label/header semantics + id scheme; Gmail archive paging, quota/pacing classification; `takeoutServiceID`/name; `MailIngest` implementations and `MailSource` declarations; the Takeout freshness probe | |
| **Stays** | | `MailArchive` (FTS, search, cursors, meta KV, merge hydration, dataset), `/v1/mail` + admin mail routes, `doMailIngest` queue chaining (`mailSyncMu`, `mergeMailProgress`), `MailSink` implementation, copy-ledger snapshot stamping, checker registry, data grants |
| **Stays** | | `run.go` flag parsing (`-mail-archive`, `-import-takeout`), `datadir.go` paths, `AuthMiddleware` perimeter |

Copy-ledger stamping stays core-owned on purpose: the connector ingests,
core records the optimistic snapshot (today's `recordTakeoutSnapshot`
placement) — the ledger is core state.

---

## 3. Phased plan

One concern per branch, worktrees `../aegis-issue-286-<slug>`, tests move
with the code, coverage floor never drops, behavior and user-visible copy
unchanged (ported verbatim where they move).

### Phase 0 — this note (`docs/mail-ingest-seam`)

Comment on #286 linking this plan. No behavior change.

### Phase 1 — provider knowledge out of core (`refactor/issue-286-takeout-semantics`)

The pure semantics move first, so the seam PR is structure only.

1. `internal/connectors/google-takeout.go` — exported label/mailbox/
   direction mapping, `Read = !UNREAD`, `msgid:`/`blob:` id scheme,
   `TakeoutServiceID` / `TakeoutServiceName`. Core delegates
   (`mail_takeout.go`, `mail_archive.go`, `librarykind.go` call the
   exports; no behavior change).
2. Gmail quota marker / pacing / default query / `IsGmailQuotaError`
   move beside the Gmail connector; `syncrunner_mail.go` keeps calling
   them through the exports.
3. Tests move with the functions (label-mapping fixtures, quota
   classification); fuzz seeds stay green.

**Done when:** core references the connector constants and helpers instead
of literals (`"google-takeout"`, `"Quota exceeded"`, label names) — the
delegation sites remain, the knowledge does not; all tests pass unchanged.

### Phase 2 — the ingest seam (`refactor/issue-286-mail-ingest-seam`)

1. `types.MailIngestOptions` / `MailIngestResult` (with `RetryAfter`);
   `connectors.MailIngest` + `MailSink` + `MailSource` as in §2.2–2.3.
2. `gmailArchiveSyncer` becomes the Gmail connector's `IngestMail`
   (moves to `google-gmail.go` or `google_gmail_ingest.go`);
   `mail_archive_sync.go` disappears.
3. The Takeout parser moves wholesale to `google-takeout.go` as the
   second `IngestMail`; `ImportTakeout` / `SetTakeoutPath` /
   `ResetTakeoutCursor` leave `MailArchive` (path and walk cursor live
   behind sink meta). `mail_takeout.go` disappears.
4. Core keeps one ingest lookup; `mailArchiveSyncer`'s type assertion,
   `doMailIngest`'s `takeoutServiceID` branch, and
   `handleTakeoutSyncRequest`'s service branch become generic calls.
   `/v1/mail/sync` request/response shapes are unchanged.
5. `mailSyncMu` serialization, bounded-batch chaining, fail-open
   mid-batch reporting, and the quota "do not advance the page token"
   rule stay core-side and stay pinned by
   `mail_archive_sync_test.go` / `mail_takeout_test.go` (moved with the
   code). Fuzz targets (`RecordFromTakeoutMessage`, `WalkMboxFrom`,
   `IsMboxFromLine`, `TakeoutMessageID`) move beside the parser.

**Done when:** both ingests run through `MailIngest` with identical
observable behavior (same batches, cursors, fail-open, response shapes);
core holds no Takeout parser and no Gmail sync loop.

### Phase 3 — declared source metadata (`refactor/issue-286-source-metadata`)

1. `MailSource` declarations drive `expandMailArchiveSources`, display
   names, `Parent` nesting, and `Canonical` stamping;
   `librarySourceLastSyncedAt` loses its service switch.
2. Freshness probe becomes a types-level optional interface + one
   generic core checker (`takeoutChecker` dies; registry unchanged).
3. `mailServiceName` dies; the archive read path is provider-neutral.

**Done when:** `librarykind.go` and `syncfreshness.go` contain no
mail/takeout service-ID branching; overviews behave as before.

### Phase 4 — cleanup (`docs/issue-286-mail-ingest-cleanup`)

1. Core holds no `google-takeout` / Gmail provider literals — a hygiene
   test (the `docs_hygiene_test.go` / onboarding-copy pattern) rejects
   them; pre-request hint copy either builds its source list from the
   declarations or is reworded source-generic (merge-on-Message-ID
   semantics are product copy and stay).
2. AGENTS.md: `MailIngest` entry under extension points ("declare, don't
   branch") and a pitfalls row for service-ID switches in core.
3. `docs/local-import-stores.md`'s "Mail archive / Google Takeout"
   section points at this note's landed outcome.

**Done when:** the definition of done below is all true.

---

## 4. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Cursor/FTS semantics drift while moving the parser | Move the pinning tests first (resume-without-full-rescan, quota no-skip-page, per-message fail-open, batch cap); they must fail before the code moves and pass after |
| `/v1/mail` shape changes leak to agents | Route/response DTOs are untouched; only internals move; `mail_sources_test.go` (multi-source merge, #139) and the HTTP tests stay green |
| Copy-ledger snapshot behavior changes | `recordTakeoutSnapshot` stays core-side, called on the same success condition as today |
| Takeout forced into `srv.connectors` and surfaces as an agent service | Explicitly rejected (§2.2); ingest lookup is a separate registry keyed by service id |
| Scope creep into `ImportAdmin` / one-shot import stores | Out of scope: Takeout keeps its routes and bounded-batch lifecycle; revisit only if the shape is later asked for |
| Coverage floor noise from moved files | Tests move with the source in the same PR; floor holds at every slice |
| #215 attachment gap reshapes the seam mid-flight | Out of scope: `MailSink.Store` already receives the raw payload, so an attachment pass is a later reader of stored payloads, not a seam change |

---

## 5. Definition of done

- [ ] Takeout parser and Gmail archive sync run in `internal/connectors`
      as two `MailIngest` implementations; `internal/core` holds no
      `X-GM-LABELS`, quota, or Takeout-walk knowledge.
- [ ] The mail archive is provider-neutral core state: no service-ID
      switches in `mail_archive.go`, `librarykind.go`, `syncfreshness.go`,
      `syncrunner_mail.go`, or `mail_archive_http.go`.
- [ ] Source grouping (Takeout under Gmail), display names, canonical-ness
      (#16), and freshness probes are declared beside the ingest and
      rendered/registered generically by core.
- [ ] `/v1/mail` and `/v1/library/messages` behavior is unchanged:
      cursors resume mid-export, fail-open holds, Gmail quota pauses
      without skipping a page, multi-source merge on RFC Message-ID
      intact.
- [ ] `go vet ./...`, `go test ./... -count=1 -race`, coverage ≥ 80%,
      staticcheck clean, `go build -o aegis .`
- [ ] AGENTS.md documents the `MailIngest` extension point; the #266
      carve-out note points at the landed result.

After that, a new mail ingest source (another provider's export, #215's
attachment pass) ships by implementing `MailIngest` + a `MailSource`
declaration beside its connector — no core branches, no overview changes.
