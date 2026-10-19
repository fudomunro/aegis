# User-defined types (issue #15) — design spike + PoC

Status: **draft PoC** on `feat/issue-15-user-defined-types`.
Related: restructure plan (`docs/restructure-core-types-connectors.md` Phase 6),
type policy (#148), places opt-in pattern (#142).

This note is the Phase 6 spike: schema-as-data for bring-your-own library
kinds, a working proof of concept, and a recommendation on where example packs
should live.

---

## 1. What #15 is asking for

A user defines a **source and schema** for personal data; Aegis exposes it on
the same type-centric surface as built-ins:

- `GET /v1/library/{kind}`
- neutral list (`name`, `fields`, `sources[]`)
- type-owned permissions (opt-in by default)
- pre-request keyword injection

Named connectors (Spotify, Gmail, …) stay the path for OAuth providers. UDTs
cover **user-supplied** shapes: a spreadsheet export, a JSON HTTP feed, a wiki
API — data that will never be a compiled-in Aegis connector.

Open product questions intentionally **not** closed by this PoC:

| Question | PoC stance |
|----------|------------|
| v1 sources | `file_json` + `http_json` only. SQL / CalDAV / scrapers later. |
| Who authors schemas | Config files on disk (`user-types/`). Admin UI later. |
| Canonical ownership | UDTs can set `canonical_for` via existing config; no new semantics. |
| Field-level redaction | Schema marks `sensitive: true`; enforcement is future #148 work. |
| Write-back / mirrors | Out of scope. UDTs are read-only lists in this PoC. |

---

## 2. Schema format (version 1)

One JSON file per kind. Example: `examples/user-types/reading-list/schema.json`.

```json
{
  "version": "1",
  "id": "reading-list",
  "label": "reading list",
  "usage": "books or articles they want to read",
  "summary": "…",
  "example_question": "what's on my reading list?",
  "keywords": ["reading list", "want to read"],
  "fields": {
    "id":     { "type": "string" },
    "title":  { "type": "string", "required": true },
    "author": { "type": "string" },
    "status": { "type": "string" }
  },
  "identity": {
    "fields": ["title", "author"],
    "entity_id_field": "id"
  },
  "sources": [
    {
      "id": "sample-export",
      "kind": "file_json",
      "path": "sample/books.json",
      "items_path": "items",
      "id_field": "id",
      "field_map": {
        "title": "title",
        "author": "author",
        "status": "status"
      }
    }
  ]
}
```

### Field semantics

| Key | Meaning |
|-----|---------|
| `fields` | Declared item shape. Values land in the neutral `fields{}` object. |
| `identity.fields` | Name-normalized merge key when no durable id. |
| `identity.entity_id_field` | Field carrying a durable id (preferred merge key). |
| `sources[].kind` | `file_json` (path relative to the schema file) or `http_json`. |
| `sources[].items_path` | Dotted path to the item array ("" = document root). |
| `sources[].field_map` | Schema field → dotted path on each source row. |
| `default_access` | Informational only (admin display). Packs cannot self-enable. |

### Neutral response envelope

Built-in kinds share one envelope; UDTs keep it:

```json
{
  "items": [
    {
      "name": "The Dispossessed",
      "entity_id": "isbn:9780061054884",
      "match": "entity_id",
      "fields": { "title": "…", "author": "…", "status": "reading" },
      "sources": [ { "service": "sample-export", "id": "isbn:…" } ]
    }
  ],
  "total": 1,
  "schema_id": "reading-list",
  "fields": { "title": { "type": "string", "required": true } },
  "by_service": { "sample-export": { "status": "ok", "count": 5 } }
}
```

Product rules that stay true for UDTs:

1. **Service identity lives in `sources[]`** — never top-level.
2. **Fail-open** — one bad source degrades `by_service`; the list still returns.
3. **Aegis does not store media** — only the mapped metadata.
4. **Permissions are data** — UDTs are opt-in until type policy enables them.
5. **Coverage floor** — schema load / map / merge are unit-tested in this PR.

---

## 3. Architecture (fits the #15 layer split)

```text
schema.json
    │  LoadUserDefinedTypes
    ▼
types.UserDefinedTypeSchema ──► types.LibraryKind
    │                               │
    │  field_map + dottedPath       │  allLibraryKinds()
    ▼                               ▼
types.UserDefinedItem         pre-request hints / admin / flow
    │
    │  MergeUserDefinedItems
    ▼
handleLibraryAggregate  (same pipeline as playlists/tracks/…)
    │
    ▼
GET /v1/library/{id}
```

| Layer | UDT responsibility |
|-------|-------------------|
| `internal/types` | Schema, neutral item, identity/merge, opt-in mark, list encode |
| `internal/core` | Load dir, map source JSON, generic fetch, aggregate handler |
| `internal/connectors` | *Not used* — UDT sources are data feeds, not OAuth connectors |

Named-service connectors remain first-class. UDTs do **not** replace them and
do not invent a second proxy path.

---

## 4. Permissions

- Every UDT id is marked **opt-in** (`types.MarkOptInDataType`) so a new pack
  is never agent-visible by accident.
- **Packs cannot self-enable.** `default_access` is informational only; type
  policy must name the kind (admin UI / `PUT /v1/admin/data-grants`).
- `id` must match `^[a-z][a-z0-9-]*$` and must not collide with built-in data
  types or `/v1/library` reserved segments (`links`, `review`, `copies`, …).
  `perm_type` is always the id; `path` is always `/v1/library/{id}`.
- Empty / `none` keeps the kind hidden until the admin enables it — same as
  places (#142).
- Provider overrides do not apply: UDT sources are not OAuth services.

---

## 5. Proof of concept in this PR

| Piece | Location |
|-------|----------|
| Schema + item + merge | `internal/types/udt.go` |
| Opt-in registration | `internal/types/optin.go` |
| Loader + field map + fetch | `internal/core/udt.go` |
| Aggregate handler | `internal/core/udt_handlers.go` |
| Example pack | `examples/user-types/reading-list/` |
| Tests | `internal/types/udt_test.go`, `internal/core/udt_test.go` |

Try it:

```bash
export AEGIS_USER_TYPES_DIR=$PWD/examples/user-types
# or copy examples/user-types/reading-list into <data-dir>/user-types/
./aegis -addr :8080 -api-key dev-key -token-pass dev-pass
curl -s -H "Authorization: Bearer dev-key" \
  http://localhost:8080/v1/library/reading-list | jq .
```

Expected: three merged rows (ISBN pair, name pair, singleton), `match` labels
on the multi-source rows, `by_service.sample-export` ok.

### Deliberately not in this draft

- Admin UI to author schemas from scratch (file selection **is** in: Advanced → User-defined types)
- Write / mirror / copy-ledger stamping for UDTs
- SQL / spreadsheet / CalDAV sources
- Field-level sensitive redaction enforcement
- Schema hot-reload (restart to pick up new packs; **file path selection** hot-applies via overlay)

### Admin local-file selection (this PR)

Advanced → **User-defined types** lists loaded packs and their `file_json`
sources. Operators can:

1. **Browse** the local disk (folders + `.json`) and click a file to select it
2. **Select** one or more files per schema source (replaces the pack’s relative path)
3. **Add extra files** with name-matched fields (or an explicit field_map)
4. Clear back to the schema default

Selections persist in `user_types.json` next to `aegis.json` (never rewrites
schema packs). Library fetches apply the overlay at request time and clear the
aggregate cache on save.

---

## 6. Example packs: monorepo vs own repo

The example directory is a **portable pack** (`schema.json` + `sample/` +
`README.md`). That shape is the unit we would publish.

### Option A — monorepo `examples/user-types/` (this PR)

**Pros**

- Schema format and examples version together; CI can load every pack.
- One clone to try the feature; no “where do I find examples?” problem.
- Aegis maintainers can break the format and fix packs in one PR.

**Cons**

- Service repo grows every time someone adds a garden/homework/tarot pack.
- Example PRs mix with product PRs in review queue.
- Non-Go contributors still need to clone the service repo.

### Option B — standalone `aegis-user-types` (or `aegis-udt-examples`) repo

**Pros**

- Packs evolve on their own cadence; community PRs never touch the service.
- Clear ownership: “this repo is content, that repo is the runtime.”
- Multiple packs / themes can ship without bloating `aegis`.
- Schema packs stay language-agnostic (JSON + data) — good for non-Go authors.

**Cons**

- Format version drift: pack `version` must be pinned; CI in the service repo
  cannot cheaply validate every external pack.
- Discovery needs a README link and maybe a gallery later.
- Secrets risk: pack authors might paste API keys into `headers`; the service
  repo can lint that more easily than a content repo.

### Recommendation

**Start in monorepo (this PR), extract once the format stabilizes.**

1. Keep `examples/user-types/reading-list/` as the reference pack and the
   regression fixture (`internal/core/udt_test.go` loads it).
2. When schema v1 is no longer expected to break, move packs to
   `github.com/fudomunro/aegis-user-types` (git subtree split or copy), leave
   a one-line pointer in `examples/user-types/README.md`.
3. Service CI keeps a **minimal** inline fixture (tiny schema + sample) so
   the runtime is tested without depending on the content repo.
4. Content repo CI: JSON schema lint + optional integration job against a
   released Aegis binary (`AEGIS_USER_TYPES_DIR=…`).

That split matches the product rule: Aegis owns the **type surface and
permissions**; users own **their types and sources**.

---

## 7. Risks

| Risk | Mitigation |
|------|------------|
| Schema id collides with built-in kind | Loader skips and logs (already implemented). |
| Hostile / huge JSON feed | Item cap (`maxUserDefinedItems`), 4 MiB body limit, fail-open. |
| UDT becomes a parallel permissions path | Uses the same `DataGrant` + `AllowsMethod` enforcement. |
| Name-only merge over-merges | Entity id preferred; name key is schema-declared fields only. |
| Pack authors leak secrets in `headers` | Docs say non-secret headers only; later lint in content-repo CI. |

---

## 8. Next steps after this draft

1. Agree schema v1 (fields table above) and the own-repo extraction trigger.
2. Admin: author packs in the UI (currently select files only), enable/disable kinds.
3. Hot-reload of schema packs (file selection already hot-applies).
4. Sensitive-field redaction hooks on the type policy attributes (#148).
5. Optional second pack (e.g. `garden-plants`) to stress multi-field identity.
