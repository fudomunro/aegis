# Glossary

Canonical English names for Aegis concepts. Product docs, admin UI, agent
injection text, and coding-agent notes should prefer these words. Issue
[#174](https://github.com/fudomunro/aegis/issues/174) tracks remaining surface
passes (agent strings, admin copy) that still use older synonyms.

Code identifiers and JSON field names stay as they are — this glossary is for
prose. See [`core-concepts.md`](core-concepts.md) for the full concept
inventory and [`audience-map.md`](audience-map.md) for who each surface talks to.

---

## Service, connector, connection

| Prefer | Means | Avoid as a synonym for |
|--------|-------|------------------------|
| **Service** | Named upstream brand and its `services[]` config row (Spotify, Gmail, Immich, …) | The Go adapter; the OAuth credential |
| **Connector** | The Go adapter implementing `ServiceConnector` (`internal/connectors`) | The named brand in product prose |
| **Connection** | OAuth credential entry in top-level `connections{}` (`connection` + `oauth_scopes`) | The service itself |

Config keys stay `services`, `connections`, `connector` is code-only.

---

## Data types and library

| Prefer | Means | Notes |
|--------|-------|-------|
| **Data type** | One library dataset in admin/permissions language (playlists, tracks, photos, …) | Also `perm_type` in schema packs; prefer “data type” in English |
| **Library kind** | The registry entry / URL kind for that dataset (`/v1/library/{kind}`) | Code: `LibraryKind`. URL segment: `kind` |
| **Neutral model** | Provider-agnostic response shape (`Playlist`, `Track`, `Contact`, …) with `sources[]` | Provider quirks stay in connectors |
| **Unified library** | `/v1/library/{kind}` aggregates across services | |
| **sources[]** | Per-item provenance (service, id, url, `canonical`, `copy`) | Field name unchanged |
| **Canonical home** (or **canonical**) | Non-Aegis authoritative home for a kind (`canonical_for`) | Avoid “home copy”, “foreign canonical” in product prose |
| **Entity merge** | Folding music/photo/event rows into one item (ISRC/UPC/MBID/…) | Not for contacts |
| **Identity matching** | Folding contacts/people on email → phone → handle → name | Not for playlists/tracks |
| **Entity link** | User-confirmed cross-service identity (`entity_links.json`) | |
| **Copy ledger** | Durable record of mirrored/backup copies | |
| **Mirror** | Explicit copy of a playlist/liked tracks to another service | Not automatic write sync |
| **Content transport** | `/v1/services/{id}/…` for bytes and local import only | Avoid “ingest transport”; error text may say “content/ingest only” once |
| **User-defined type** | Schema pack under `user-types/` | Avoid “type pack”, “schema pack” outside the design spike |
| **Access level** | `none` / `freebusy` / `summary` / `city` / `precise` / `read` / `write` / `read-write` | Token values in policy |
| **Free/busy** | Busy intervals only (no titles/attendees) | Also `summary` (transactions), `city`/`precise` (places) |

---

## Permissions and access

| Prefer | Means | Notes |
|--------|-------|-------|
| **Type policy** | Primary owner of agent access per data type (`TypePolicy`, `type_policies`) | Default level + optional per-provider tighten |
| **Service permissions map** | Legacy/service overlay that can only **tighten** the type default | Prefer not to say just “permissions” when you mean the type policy |
| **Effective access** | Resolved level after type policy + service tighten | |
| **Accessor** | Principal-level access per data type (issue #160) | Distinct axis from type policy |
| **Auth mode** | `auto` / `tailnet` / `api-key` | |

One-line shape (also in README “Data permissions”): type policy owns the
default and per-provider floor; service permissions maps only tighten;
accessors are the principal axis.

---

## Runtime and agent language

| Prefer | Means | Notes |
|--------|-------|-------|
| **LLM agent** (or **assistant**) | Runtime model/tool-loop that calls `/v1/library/*` | Not the coding agent |
| **Coding agent** / **maintainer** | Automation or human editing this repo (`AGENTS.md`) | Do not call them just “agents” in product docs |
| **Pre-request hook** | `POST /v1/pre-request` instruction injection | |
| **System instructions** | English injected into the LLM request | |
| **Capabilities** / **Operations** | Admin/connector metadata of what a connector can do | Not agent discovery; provider-shaped lists are not the library API |
| **Fail-open** | Errors pass through; hook allows on parse failure | |
| **Aegis owns OAuth** | Agents never see tokens or authorize services | |

---

## Ops

| Prefer | Means |
|--------|-------|
| **Data directory** | Single OS app-data root for mutable state |
| **Hook health** | Aperture wiring diagnostics + fix strings |
| **Audit log** | Durable access log (agent, surface, decision, …) |
| **Admin map** | UI topology of services → data types → agents |

---

## Deliberate non-renames

JSON field names, Go identifiers, and URL segments stay stable even when the
English above differs (`canonical_for`, `sources[].canonical`, `perm_type`,
`IsAgentContentPath`, `type_policies`, `LibraryKind`). Product rules in
`AGENTS.md` are unchanged by this glossary.
