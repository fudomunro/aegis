# reading-list (user-defined type example)

A portable **schema pack** for Aegis user-defined types (issue #15). Copy this
directory into Aegis's `user-types/` folder (or point `AEGIS_USER_TYPES_DIR`
at a tree that contains it) and restart:

```text
<data-dir>/user-types/reading-list/schema.json
<data-dir>/user-types/reading-list/sample/books.json
```

Then enable the kind (packs cannot self-enable):

```bash
# type policy must name reading-list before agents can read it
curl -s -X PUT https://aegis.example.ts.net/v1/admin/type-policies \
  -H 'Content-Type: application/json' \
  -d '{"types":{"reading-list":{"default":"read"}}}'
```

Then:

```bash
curl -s https://aegis.example.ts.net/v1/library/reading-list | jq .
```

## What this proves

| Concept | Where |
|---------|--------|
| Schema-as-data (fields, identity, keywords) | `schema.json` |
| Source mapping (`file_json` / `http_json` + `field_map`) | `schema.json` → `sources[]` |
| Opt-in visibility (`default_access`) | `schema.json` |
| Entity-id merge + name fallback | `sample/books.json` duplicate rows |
| Neutral envelope (`name`, `entity_id`, `match`, `fields`, `sources[]`) | response shape |

## Own-repo layout

This directory is intentionally self-contained (schema + sample + README) so
it can move to a standalone examples repo without editing Aegis:

```text
aegis-user-types/                  # proposed standalone repo
  README.md                        # how to install packs
  reading-list/
    schema.json
    sample/books.json
    README.md
  garden-plants/
    ...
```

See `docs/user-defined-types.md` for the schema format and the monorepo vs
own-repo tradeoff.

## Trying an HTTP source

Swap the sample source for a live feed (same field map idea):

```json
{
  "id": "notion-reading",
  "name": "Notion",
  "kind": "http_json",
  "endpoint": "https://api.example.com/reading",
  "items_path": "results",
  "id_field": "id",
  "field_map": {
    "id": "id",
    "title": "properties.Name",
    "author": "properties.Author",
    "status": "properties.Status"
  }
}
```

`file_json` is for demos and exports; `http_json` is for a JSON HTTP feed.
Named OAuth providers stay named connectors — not UDT sources.
