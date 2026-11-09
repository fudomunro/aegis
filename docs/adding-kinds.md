# Adding a library kind

This guide is the checklist for defining a new **library kind** in
Aegis. It extracts the patterns that already work for playlists, tracks,
albums, artists, contacts, photos, events, messages, transactions, places,
and documents — so the next kind does not rediscover them.

Use it when you are:

- adding a unified `/v1/library/{kind}` dataset,
- adding a connector provider for an existing kind,
- or extending a kind with locations / identity / sample fixtures
  (see #91, #89, #76, #16, #4).

Related product rules live in `AGENTS.md` and `README.md`. This file is about
**how a kind is structured**, not the full API surface.

Adding a kind touches exactly four places — **no `internal/core` edits**:

1. `internal/kinds`: neutral model, provider interface, `BuiltinLibraryKinds` entry,
2. `internal/kinds/<kind>.go`: the `Kind` implementation,
3. one line in `kinds.Builtin()`,
4. tests and fixtures.

---

## Vocabulary

These three names often mean the same underlying concept. Keep them aligned.

| Name | Where | Example |
|------|-------|---------|
| **Library kind ID** | `LibraryKind.ID`, cache keys, logs, metrics `Kind` | `tracks` |
| **Permission / kind key** | `LibraryKind.PermType`, `Permissions` map, `KindCapability.Name`, `KindForPath` | `tracks` |
| **Agent path** | `LibraryKind.Path` | `/v1/library/tracks` |

A connector's **kind capability** (`KindCapability`) advertises what
that *service* can do for the same key. A library kind is the **unified**
cross-service view of that key.

```
Agent question
    → pre-request keywords (LibraryKind.Keywords)
    → GET /v1/library/{id}   (aggregate)
        → each connector's ListUser… provider
        → merge / match / enrich
        → stamp canonical + copy ledger
        → neutral list JSON
```

---

## Product rules that shape every kind

These are non-negotiable (see `AGENTS.md`). New kinds inherit them.

1. **Kind-centric surface; named-service connectors.** Design the kind first
   (`/v1/library/{kind}`, neutral model, `LibraryKind`). Real providers
   (Spotify, Gmail, …) are the connectors that populate it — not generic
   auto-detected blobs. A kind without a named connector (or a user-defined
   source under #15) is a stub, not a feature.
2. **Fail-open.** Fetch, parse, and match failures must not block the agent.
   Partial `by_service[]` status is fine; a hard 500 for one bad connector is not.
3. **Aegis does not store media.** Bytes and message bodies stay on the source
   service. Neutral models carry metadata and `sources[]` pointers.
4. **Service identity lives in `sources[]`.** Never put service-local IDs,
   URIs, or deep links on the top-level user-facing object.
5. **Permissions are data.** A kind is not agent-visible until the permission
   map and path classifier know its key.
6. **Core never names a built-in kind.** Kind-specific behavior lives in the
   kind's file — routes, filters, redaction, stamping. The hub's
   `kind_literal_hygiene_test.go` enforces this (its per-file allowlist may
   only shrink).
7. **Coverage floor stays.** New write paths need tests before merge
   (`go test` + 80% floor).

---

## Anatomy of a kind

A complete kind touches these layers. Music kinds (`internal/kinds/merge.go`)
and people/media kinds (`internal/kinds/merge_contacts.go`, `merge_photos.go`,
…) are the two templates.

```text
internal/kinds/models.go        Neutral item + *Source + *List
internal/kinds/merge_<kind>.go  Per-kind identity keys + Merge* (one file per kind)
internal/kinds/providers.go     Provider interface (WidgetProvider) + ErrUnauthorized
internal/kinds/librarykinds.go  BuiltinLibraryKinds registry (policy flags live here)
internal/core/pathclass.go      kindPathRules for the provider's proxy paths
internal/kinds/<kind>.go        Kind implementation: routes, predicates, Spec
internal/kinds/stamps.go        Generic stamping over your source projection
<provider>.go (connectors)      Capabilities + ListUser… + optional proxy
core/sampledata/                Deterministic fixtures + deliberate merge pairs
tests                           Merge, kind, aggregate, permissions, sample-data checks
README.md                       Agent-facing semantics for the kind
```

### 1. Neutral model (`internal/kinds/models.go`)

The item, its `*Source` rows, and its `*List` wrapper. Rules:

- Top-level fields are provider-neutral (`Name`, `EntityID`, `Sources`).
  Service-local identifiers live in `sources[]` (`Service`, `ID`, `URL`).
- `*Source` carries `Canonical bool` and `Copy *CopyInfo` when the kind can
  have a home service or tracked copies. Kinds without those concepts
  (albums, artists) omit the fields — the generic stamping only projects
  what exists.
- The `*List` embeds `LibraryListMeta` (`types.MetaForPage`) so paging,
  `truncated`, and `by_service[]` are uniform across kinds (issue #185).

### 2. Identity and merge

Identity keys prefer durable IDs (ISRC/UPC/MBID, `EntityID`) over
name-only matching; name merges need a guard (duration for tracks).
`Merge*(items)` returns merged rows with `Match` labels and combined
`Sources`. Never name-merge without a discriminator — see the music
merge tests for the fixture pattern (deliberate pairs that must and
must not collapse).

### 3. Registry entry (`internal/kinds/librarykinds.go`)

One `LibraryKind` literal in `BuiltinLibraryKinds` — the single source for
policy flags (`AppReadable`, `CanonicalAllowed`, `Search`, default access),
agent copy (`Instructions`, `DenyMsg`, `EmptyMsg`, `FailMsg`), and keywords.
Reserved IDs, app-readable paths, canonical keys, and access-level
allowlists all derive from this slice; nothing copies it by hand.

### 4. Kind implementation (`internal/kinds/<kind>.go`)

One file per kind implementing `kinds.Kind` — see the existing files for
templates (`tracks.go`, `events.go`, and `messages.go` show the three
shapes: plain provider kind, subroutes, local sources).

```go
type Kind interface {
    Def() LibraryKind            // the registry entry
    Routes() []Route             // agent-tier GET/HEAD routes + exposure
    Provides(connector any) bool // provider-interface assertion
    HasProviders(h Host) bool    // connectors + declared local sources
    Readable(h Host) bool        // owner-tier read visibility
}
```

**Routes.** Declare every agent-tier GET/HEAD route the kind owns — the
list route at `Def().Path` and any subroutes (document text, freebusy).
`AppReadable` on the declaration is the app-token exposure: the API-listener
and app-token allowlists derive from these rows, so an undeclared route is
not agent-visible and a subroute never leaks to app tokens. Writes
(mirror, photo share) stay admin-registered in core.

**Serving.** The handler builds a `kinds.Spec[T]` and calls
`kinds.Aggregate(host, engine, w, r, spec)` — the shared pipeline (cache,
permission-aware fan-out, merge, paging, search-index write-behind). The
spec wires:

- `Targets` — `kinds.Collect` (generic over the item and provider types)
  builds permission-filtered fetch targets; append kind-owned local sources
  as `NamedLister{ID, Local: true, List}` (see `messages.go`).
- `ServiceOf` / `SortKey` / `Merge` — stable pre-sort, identity key, merge.
- `Encode` — build the `*List` from the merged items + `types.SlicePage`.
- `MarkCanonical` / `StampCopies` — bind `kinds.Stamps(h, kind, forEach)`
  over your source projection (`forEach*Source`, ~8 lines).
- `Enrich` / `RedactSearch` — optional pre-merge resolution (MusicBrainz)
  and search-path re-redaction (access-level kinds).
- `CacheKey` — only when a second endpoint shares the dataset with a
  different response shape (freebusy) or request filters scope the cache.
- `Status` — `kinds.StatusHooks` for kinds with local sources: source
  naming, sync cursors, and source-overview expansion (see `messages.go`).

**Constructor deps.** Anything the kind needs beyond `Host`/`Engine` is a
constructor argument (`kinds.Deps` for shared ones,
`kinds.UserDefinedDeps` for packs) — store handles, resolvers, local-source
availability. Not every cross-kind need becomes a `Host` method: the Host
budget is "needed by at least two kinds, or by the engine".

### 5. Provider interface (`internal/kinds/providers.go`)

`WidgetProvider` with one `ListUser…(ctx) ([]T, bool, error)` method (the
bool is `truncated`). Connectors implement it; `kinds.Collect` type-asserts.
`ErrUnauthorized` is the only sentinel the aggregate pipeline special-cases.

### 6. Connector surface (`internal/connectors/<provider>.go`)

`Capabilities()` must list the kind in `Kinds` with real `Operations`
(empty `[]Operation{}` means "recognized, not implemented"). Read the
configured `GetService(id).Endpoint` — never hardcode upstream base URLs.

### 7. Locations, copies, and canonical homes

If sources can be a home or hold tracked copies: `CanonicalFor` in config
(`canonical_for`), `CopyInfo` stamping via the generic `Stamps`, and
`CanonicalAllowed` on the kind entry. Mirror writes are a separate,
admin-side surface (`mirror.go`) — kinds declare reads.

### 8. Permissions and connector capabilities

The kind key must be known to the permission map, the path classifier
(`internal/core/pathclass.go`), and any access-level vocabulary in
`internal/core/policy` (like `PlacesLevelForResponse`). Default access for
new kinds should start strict (`none` where the domain is sensitive).

### 9. Sample data (`internal/core/sampledata/`)

Deterministic fixtures with deliberate merge pairs (same identity, two
sources; near-miss names that must stay separate). Sample data feeds the
demo and the query tests.

### 10. Agent-facing behavior

`Instructions`, `EmptyMsg`, `DenyMsg`, `FailMsg`, and `Keywords` live on
the `LibraryKind` entry — that copy is the contract agents see. Keep it
accurate in the same PR as the behavior.

---

## What derives from the registry (issue #183)

You do not hand-write any of these: reserved user-defined IDs, app-readable
paths (`Route.AppReadable` + `LibraryKind.AppReadable`), canonical-kind
checks, access-level allowlists, the API-listener allowlist, and the
pre-request hint set. The sync tests (`TestLibraryKinds_BuiltinInSync`,
`TestLibraryKinds_RoutesInSync`) pin the registry ↔ routes equality in both
directions — a kind missing a route or a route without a kind fails CI.

---

## Checklist (copy into the PR)

- [ ] Neutral model + `*Source` + `*List` in `internal/kinds/models.go`
- [ ] Identity key + `Merge*` with deliberate fixture pairs
- [ ] `BuiltinLibraryKinds` entry (flags, copy, keywords)
- [ ] Provider interface + connector `Capabilities` / `ListUser…`
- [ ] `internal/kinds/<kind>.go`: `Kind` impl, declared routes + exposure,
      `Spec` wiring, `forEach*Source` stamping projection
- [ ] One line in `kinds.Builtin()`
- [ ] Kind-specific filters/redaction/hooks in the kind file (never in core)
- [ ] Path-classification rules for the provider's proxy paths
- [ ] Sample fixtures + sample-data query test
- [ ] Tests: merge pairs, aggregate (hit/miss/permission), search if
      `Search` is declared, stamps
- [ ] README / `docs/api.md` agent-facing semantics
- [ ] `go vet ./...`, `go test ./... -count=1 -race`, coverage ≥ 80%,
      staticcheck clean

---

## Worked references

| Shape | Read first |
|-------|-----------|
| Plain provider kind | `internal/kinds/playlists.go` (stamps + merge deps) |
| Access-level redaction | `internal/kinds/places.go`, `documents.go` |
| Subroute + shared dataset | `internal/kinds/events.go` (freebusy) |
| Subroute with its own gate | `internal/kinds/documents.go` (full text) |
| Local source + status hooks | `internal/kinds/messages.go` (mail archive) |
| Query filters + summary | `internal/kinds/transactions.go` |

---

## User-defined kinds (#15)

Schema packs under `user-types/` load through `kinds.UserDefined(schema)`
into the same pipeline and registry (`kinds.MergeUserDefined` refuses any
pack that shadows a built-in). See `docs/user-defined-kinds.md`. Packs
declare sources and fields as data; they never self-enable (an admin data
grant names the kind) and never store secrets in `http_json` headers.

---

## Out of scope (still open)

- Admin authoring and hot-reload of user-defined packs (#15 follow-ups)
- Photo ownership / provenance proofs (#20)
- Obtaining data from other sources / photo storage (#34, #21)
