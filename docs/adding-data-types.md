# Adding a data type

This guide is the checklist for defining a new **data type** (library kind) in
Aegis. It extracts the patterns that already work for playlists, tracks,
albums, artists, contacts, photos, events, and messages — so the next type
does not rediscover them.

Use it when you are:

- adding a unified `/v1/library/{kind}` dataset,
- adding a connector provider for an existing kind,
- or extending a kind with locations / identity / sample fixtures
  (see #91, #89, #76, #16, #4).

Related product rules live in `AGENTS.md` and `README.md`. This file is about
**how a type is structured**, not the full API surface.

---

## Vocabulary

These three names often mean the same underlying concept. Keep them aligned.

| Name | Where | Example |
|------|-------|---------|
| **Library kind ID** | `LibraryKind.ID`, cache keys, logs, metrics `DataType` | `tracks` |
| **Permission / data-type key** | `LibraryKind.PermType`, `Permissions` map, `DataTypeCapability.Name`, `DataTypeForPath` | `tracks` |
| **Agent path** | `LibraryKind.Path` | `/v1/library/tracks` |

A connector's **data-type capability** (`DataTypeCapability`) advertises what
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

## Product rules that shape every type

These are non-negotiable (see `AGENTS.md`). New types inherit them.

1. **Type-centric surface; named-service connectors.** Design the type first
   (`/v1/library/{kind}`, neutral model, `LibraryKind`). Real providers
   (Spotify, Gmail, …) are the connectors that populate it — not generic
   auto-detected blobs. A type without a named connector (or a user-defined
   source under #15) is a stub, not a feature.
2. **Fail-open.** Fetch, parse, and match failures must not block the agent.
   Partial `by_service[]` status is fine; a hard 500 for one bad connector is not.
3. **Aegis does not store media.** Bytes and message bodies stay on the source
   service. Neutral models carry metadata and `sources[]` pointers.
4. **Service identity lives in `sources[]`.** Never put service-local IDs,
   URIs, or deep links on the top-level user-facing object.
5. **Permissions are data.** A type is not agent-visible until the permission
   map and path classifier know its key.
6. **Coverage floor stays.** New write paths need tests before merge
   (`go test` + 80% floor).

---

## Anatomy of a data type

A complete type touches these layers. Music types (`library.go` + `match.go`)
and people/media types (`contacts.go`, `photos.go`, …) are the two templates.

```text
models.go          Neutral item + *Source + *List
<kind>.go          Provider interface, identity keys, merge, thin handler
librarykind.go     Registry entry (instructions, keywords, messages)
<provider>.go      Connector: Capabilities + ListUser… + optional proxy
canonical.go       canonicalKindIDs + stamp*Canonical (if sources can be home)
copy_ledger_stamp.go  stamp*Copies (if sources can hold tracked copies)
sampledata.go      Deterministic fixtures + deliberate merge pairs
pathclass.go       dataTypePathRules for the provider's proxy paths
tests              Merge, aggregate, permissions, sample-data query checks
README.md          Agent-facing semantics for the type
```

### Target package map (issue #15)

The logical layers above are moving into packages. While `package main` is
still flat, write new code so the move is a `git mv` + package rename:

| Target package | Contents |
|----------------|----------|
| `internal/types` | Neutral models, identity/merge, `LibraryKind` **data**, `TypePolicy` / access levels, capability shapes, path rules |
| `internal/core` | Auth, config, tokens/OAuth, proxy/cache, permission **enforcement**, `handleLibraryAggregate`, admin, state stores |
| `internal/connectors` | Named services implementing `ServiceConnector` + `*Provider`, using `Host` (not `*Server`) |

Connectors take `Host` (`host.go`). Classification lives in `pathclass.go`.
Type-level policy shape (`policy.go`) is the #148 home — service
`permissions` remain a migration override, not the primary model.

You do **not** need every optional hook on day one. Ship the spine first
(model + provider + kind + handler + tests), then add locations (#76), durable
IDs (#4), and sample edge cases (#89) as the type matures.

---

### 1. Neutral model (`models.go`)

Define three types: the user-facing item, its per-service source, and the list
wrapper.

```go
// Widget is the user-facing widget. Service identity lives in Sources.
type Widget struct {
    Name     string         `json:"name"`
    // EntityID is a durable catalog ID when a provider supplies one.
    // Preferred cross-service identity over display-name matching (#4).
    EntityID string         `json:"entity_id,omitempty"`
    // Match records how multi-source identity was established.
    // Empty on single-source items.
    Match    string         `json:"match,omitempty"`
    Sources  []WidgetSource `json:"sources,omitempty"`
    Extra    map[string]any `json:"extra,omitempty"`
}

type WidgetSource struct {
    Service     string `json:"service"`       // connector ID
    ServiceName string `json:"service_name,omitempty"`
    ID          string `json:"id,omitempty"`  // service-local ID
    URL         string `json:"url,omitempty"` // deep link when available
    // Canonical is true when this service holds the authoritative copy (#16).
    Canonical bool `json:"canonical,omitempty"`
    // Copy is copy-ledger metadata when this location is tracked (#76).
    Copy  *CopyInfo      `json:"copy,omitempty"`
    Extra map[string]any `json:"extra,omitempty"`
}

type WidgetList struct {
    Widgets   []Widget                   `json:"widgets"`
    Total     int                        `json:"total"`
    Synced    bool                       `json:"synced,omitempty"`
    Truncated bool                       `json:"truncated,omitempty"`
    ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}
```

**Conventions that matter:**

| Rule | Why |
|------|-----|
| Top-level fields are user-facing identity only | One logical item can exist on many services |
| `sources[]` carries service IDs, names, URLs | Agents need a way back to the real copy |
| `EntityID` + `Match` record durable vs name matches | Merge review and agents can trust `isrc` more than `name` |
| `Extra map[string]any` for provider quirks | Keeps the neutral shape stable |
| List wrapper has `Total`, `Synced`, `Truncated`, `ByService` | Shared aggregate contract (`handleLibraryAggregate`) |
| `Truncated` is a **first-class** honesty flag | Agents must not claim a list is complete when a page cap hit |

People-shaped fields (photo `people[]`, event attendees, message from/to)
reuse `Contact` and the contact identity order — do not invent a second person
schema.

---

### 2. Provider interface (in the kind file)

One method, neutral types, explicit truncation:

```go
// WidgetProvider is implemented by connectors that can return the user's
// widgets in Aegis's neutral format.
type WidgetProvider interface {
    // ListUserWidgets returns neutral widgets (one WidgetSource each).
    // Return ErrUnauthorized when no valid token is available.
    // truncated is true when the page cap was hit with more items still upstream.
    ListUserWidgets(ctx context.Context) (widgets []Widget, truncated bool, err error)
}
```

**Conventions:**

- Return **already-mapped** neutral values. Provider quirks stay in the connector.
- Each item gets **one** `*Source` for the calling service.
- `ErrUnauthorized` is the only sentinel error the aggregate pipeline special-cases
  (it becomes `by_service[].status = "unauthorized"`).
- `truncated=true` is success-with-partial-data, not an error. Connectors must
  set it whenever `libraryPageCap` (or an upstream cap) left items unfetched.
- Do not download media. Metadata + URLs only.

---

### 3. Registry entry (`librarykind.go`)

Append to `libraryKinds`. This is the single place that teaches agents how to
use the dataset.

```go
{
    ID:              "widgets",
    Path:            "/v1/library/widgets",
    PermType:        "widgets",
    Label:           "widgets",
    Usage:           "widgets or their widget library",
    Summary:         "all user widgets from every connected service in one neutral list (name, entity_id, sources[])",
    SyncRule:        "Widgets merge on durable entity_id when present; otherwise same normalized name (match=name).",
    ExampleQuestion: "what widgets do I have?",
    Keywords:        []string{"my widgets", "widget library", "widgets do i have"},
    EmptyMsg:        "no widget-capable services configured",
    DenyMsg:         "permission denied: agent access to widgets is not allowed",
    FailMsg:         "failed to fetch widgets",
    HasProviders:    func(s *Server) bool { return s.hasProvider[WidgetProvider]() },
    Readable:        func(s *Server) bool { return s.libraryReadableFor("widgets") },
},
```

Field guidance:

| Field | Best practice |
|-------|----------------|
| `ID` | Plural noun matching the path segment and permission key |
| `Summary` | Field list agents can rely on; mention `sources[]` always |
| `SyncRule` | Name the **match keys** and the `match=` values you will emit |
| `Keywords` | Service-less questions that should inject this endpoint; include common misspellings and the service brand only if users say it as the data ("gmail inbox", "google calendar") |
| `EmptyMsg` / `DenyMsg` / `FailMsg` | Short, agent-safe; no secrets, no stack traces |
| `HasProviders` / `Readable` | Wire to `hasProvider[P]` and `libraryReadableFor` — do not hand-roll |

`LibraryKind.Instructions` automatically adds truncated-flag wording and the
canonical-source hint. `libraryInstructionsFor` appends the multi-source
mutation rule. Do not duplicate those strings per kind.

Register the handler in `libraryRouteHandlers()`:

```go
"/v1/library/widgets": s.HandleLibraryWidgets,
```

---

### 4. Aggregate handler (thin)

Do **not** copy an aggregate pipeline. Call `handleLibraryAggregate` with a
`libraryKindSpec`:

```go
func (s *Server) HandleLibraryWidgets(w http.ResponseWriter, r *http.Request) {
    kind := *LibraryKindByID("widgets")
    handleLibraryAggregate(s, w, r, libraryKindSpec[Widget]{
        kind: kind,
        targets: func(s *Server) ([]namedLister[Widget], []string) {
            return collectProviders[Widget, WidgetProvider](s, kind.PermType,
                func(p WidgetProvider) func(context.Context) ([]Widget, bool, error) {
                    return p.ListUserWidgets
                })
        },
        serviceOf: widgetServiceName,
        sortKey:   widgetIdentityKey,
        merge:     MergeWidgets,
        encode: func(merged []Widget, bySvc map[string]LibraryServiceStatus) any {
            return WidgetList{Widgets: merged, Total: len(merged), Synced: true,
                Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
        },
        markCanonical: s.stampWidgetCanonical, // if sources can be home
        stampCopies:   s.stampWidgetCopies,    // if sources can hold copies
        enrich:        nil,                    // optional: resolveEntityIDs
    })
}
```

The shared pipeline already handles: method check, cache (`library:{id}`),
permission-aware fan-out, panic isolation per provider, unauthorized vs error
status, stable sort, merge, canonical/copy stamping, JSON encode, metrics.

**You own only:** provider collection, identity sort key, merge, encode shape,
and optional enrich/stamps.

---

### 5. Identity and merge

Cross-service identity is the hardest part of a new type. Follow the ladder
already used by music and contacts:

**Prefer durable IDs over names.**

| Kind | Preferred `EntityID` | `Match` values | Fallback |
|------|----------------------|----------------|----------|
| tracks | ISRC / MBID | `isrc`, `mbid`, `name` | title + primary artist, **duration-guarded** |
| albums | UPC / MBID | `upc`, `mbid`, `name` | title + primary artist |
| artists | MusicBrainz MBID | `mbid`, `name` | normalized name |
| playlists | explicit link | `link`, `name` | same title |
| contacts | (identity keys) | — | email → phone → handle → name |
| photos | content hash | — | `taken_at` + dimensions (never filename alone) |
| events | iCalUID | `ical_uid`, `title_start` | title + start + all-day |
| messages | Message-ID | `message_id`, `subject_from_time` | subject + from + sent_at |

Rules that keep merges honest:

1. **Record `Match`** whenever you join multi-source items. Empty means single-source.
2. **Name-only joins are weaker.** They still happen (and should appear in
   `/v1/library/review`), but durable IDs win when both sides have them.
3. **Duration guards** for music: same title/artist with wildly different
   lengths must not merge.
4. **Never merge on filename or free text alone** when a collision would be
   harmful (photos).
5. **Union-find** when identities chain (contact A~email, B~phone, C~handle →
   one person). See `MergeContacts`.
6. **Canonical sources sort first** after merge (`sortContactSourcesCanonicalFirst`
   and friends).
7. Fold fields with "fill empty, union lists, keep first extra" — see
   `mergeContactInto` / `mergePhotoInto` / `mergeMessageInto`.

When a type has no good durable ID yet, leave `EntityID` empty and use a
narrow name key — then add enrichment later (`enrich` / MusicBrainz / etc.)
rather than inventing a fake stable ID.

---

### 6. Locations, copies, and canonical homes

Issue #91 (copy ledger) and #16 (canonical sources) are how Aegis answers
"where does this live?" without storing the bytes.

Every source that can be a **home** or a **tracked copy** should support:

| Field | Meaning | Set by |
|-------|---------|--------|
| `sources[].canonical` | Authoritative non-Aegis location | Connector (inherent) and/or config `canonical_for` |
| `sources[].copy` | Durable copy-ledger row (role, sync mode, version) | `stamp*Copies` after fetch |

Checklist when adding a type that participates:

1. Add the kind ID to `canonicalKindIDs` in `canonical.go` (config validation).
2. Implement `stampWidgetCanonical` next to the other stamps.
3. Implement `stampWidgetCopies` in `copy_ledger_stamp.go` using
   `s.copyInfoFor(kindID, service, id)` — missing rows stay nil (fail-open).
4. Wire both into the `libraryKindSpec`.
5. If the type can be mirrored, teach the mirror path to write ledger rows
   (`copy_ledger.go`) and document full vs optimistic fidelity in the README.

Kinds that are pure references (albums/artists today) may omit `Canonical` /
`Copy` on their source structs. Prefer including them if the kind has a
"home service" story.

Agents are already instructed to prefer `sources[].canonical` for writes and
to use `POST /v1/library/mirror` for deliberate copies — do not invent
auto-mirroring.

---

### 7. Permissions and connector capabilities

A type is invisible until permissions and path classification know it.

**Permission key** (`PermType` / `DataTypeCapability.Name`) must match what
`DataTypeForPath` returns for that provider's proxy paths.

1. **`pathclass.go` → `dataTypePathRules`**  
   Map provider paths (`/me/widgets`, `/widgets/...`, …) to `"widgets"` via
   `DataTypeForPath`.  
   Unknown paths fall through to `default` — that is fail-open for access, not
   a substitute for classification.

2. **Connector `Capabilities()`**  
   Advertise `DataTypeCapability{Name: "widgets", ...}` with real
   `Operations[]`:
   - `Operations` is `[]Operation` (method, path, parameters, response spec),
     not `[]string`.
   - Empty `[]Operation{}` means "type recognized, not implemented" — Amazon
     Music does this. That is valid.
   - Do not advertise methods the connector cannot serve.
   - Permission filtering (`FilterCapabilities`) drops denied types/ops; it
     never **adds** capability.

3. **Service proxy**  
   Prefer `runConnectorProxy` / `doUpstreamRequest` / `aegisHTTPClient`. Read
   the configured `GetService(id).Endpoint` — never hardcode provider base URLs.

4. **Admin surface**  
   Available/writable data types come from capabilities; writable excludes
   session/search-style side effects (picker, search POSTs). Do not special-case
   the UI when the capability model can express it.

---

### 8. Sample data (#89)

Synthetic fixtures are how APIs mature without OAuth or real personal data.
Extend `sampledata.go` whenever a type gains identity rules.

**Requirements:**

- **Deterministic** — stable timestamps, IDs, and order so tests can assert
  extremes (oldest/newest) and merge outcomes.
- **Fictional** — `example.com` addresses, invented titles. Never real mail.
- **Deliberate merge pairs** — at least one shared durable ID pair, one
  name-only pair, and one near-miss that must **not** merge.
- **Edge cases** — unicode/RTL names, missing timestamps, empty identity,
  truncated-page simulation when the type has paging.
- **Every kind** in `SampleLibrarySet` — a new registry kind without fixtures
  is incomplete (see #89 / #77).

Then cover them in `sampledata_query_test.go` (or a kind-specific test):

- merge identity (`sources[]`, `entity_id`, `match=…`)
- sort / filter / pagination if the type is queryable
- permission-denied service still appears in `by_service` as denied
- `truncated` when a provider hits the page cap

---

### 9. Agent-facing behavior (#78)

Types exist so agents use Aegis instead of scraping providers. After the
spine works, make the type **easy to use correctly**:

| Surface | What to get right |
|---------|-------------------|
| `Keywords` | Catch service-less questions; inject the unified endpoint |
| `Summary` / `SyncRule` | Field names and merge behavior agents can quote |
| `truncated` | Always populated; instructions already tell agents to check it |
| `sources[].canonical` | Home copy for writes |
| `sources[].copy` | Freshness / location when tracked |
| Error messages | Actionable (`connect at /v1/auth/{id}/login`), never raw upstream dumps |
| Logging | `slog` with `service`, `path`, `data type` fields for inefficient-use tracing |

Do not require agents to call three provider APIs when one library endpoint
can answer the question. Prefer expanding the unified type over teaching
agents more upstream shapes.

---

## Checklist (copy into the PR)

**Spine**

- [ ] Neutral `Widget` / `WidgetSource` / `WidgetList` in `models.go` (sources[], Match, Extra)
- [ ] `WidgetProvider` with `ListUser…(ctx) ([]Widget, bool, error)` + `ErrUnauthorized`
- [ ] `LibraryKind` registry entry (ID/Path/PermType/Label/Usage/Summary/SyncRule/keywords/messages)
- [ ] Route in `libraryRouteHandlers()`
- [ ] Thin `HandleLibraryWidgets` via `handleLibraryAggregate`
- [ ] Identity key + `MergeWidgets` (durable ID first; record `Match`)
- [ ] Connector implements `WidgetProvider` + `Capabilities()` data type
- [ ] `DataTypeForPath` cases for the provider's paths
- [ ] Tests: merge cases, aggregate happy path + unauthorized/error/truncated, permission deny

**Locations (when applicable)**

- [ ] `canonicalKindIDs` includes the kind
- [ ] `stamp*Canonical` + `markCanonical` wired
- [ ] `stamp*Copies` + `stampCopies` wired (fail-open on missing ledger)
- [ ] README copy fidelity (full vs optimistic) if the type can be mirrored

**Maturation (#89 / #4 / #78)**

- [ ] `GenerateSampleLibrary` fixtures + non-merge near-miss
- [ ] Sample query/merge tests
- [ ] Optional `enrich` for durable ID resolution
- [ ] README section ("X as a data type") with example JSON
- [ ] Pre-request keywords smoke-tested with a service-less question

**Definition of done**

- [ ] `go vet ./...` clean
- [ ] `go test ./... -count=1 -race` passing
- [ ] Coverage ≥ 80%
- [ ] README + example config updated for user-visible fields
- [ ] No secrets or live tokens

---

## Worked references

| Look at | When you need |
|---------|----------------|
| `library.go` + `match.go` | Music-shaped types: durable IDs, duration guard, playlist links |
| `contacts.go` | Chained identity (email/phone/handle), union-find merge |
| `photos.go` | Non-name identity (hash/time+dim), "bytes stay upstream" sources |
| `events.go` | Time-windowed items, iCalUID vs title+start matching |
| `messages.go` | Snippet-only lists, Message-ID identity, bodies stay upstream |
| `librarykind.go` | Registry + shared aggregate pipeline contract |
| `sampledata.go` | Fixture style and merge-pair seeding |
| `copy_ledger_stamp.go` | How location stamps stay fail-open |
| `canonical.go` | Home-service stamping + agent write policy text |
| `spotify.go` / `google-gmail.go` | Capability + provider implementation style |

---

## Out of scope (still open)

Do not assume these are solved when designing a type:

- User-defined / bring-your-own types (#15)
- Photo ownership / provenance proofs (#20)
- Live Immich canonical photos (#39)
- Generic auto-detected type expansion (not the default; named services first)

If a design needs those, link the issue in the PR rather than half-shipping a
parallel concept.
