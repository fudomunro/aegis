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
| **Data type** | One library dataset in admin/permissions language (playlists, tracks, photos, …) | Also `perm_type` in schema files; prefer “data type” in English |
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
| **User-defined type** | Schema pack under `user-types/` | Prefer “user-defined type” for the concept; **schema pack** is the on-disk artifact (file/dir). Avoid “type pack” |
| **Access level** | See **Data grants** | Strength of a data grant |
| **Free/busy** | Busy intervals only (no titles/attendees) | Also `summary` (transactions), `city`/`precise` (places) |

---

## Data grants (permissions and access)

| Prefer | Means | Notes |
|--------|-------|-------|
| **Data grant** | What the user does to allow an LLM agent or app to use their data — an allow rule on a **data type** at an **access level** | Hero term for product English (`docs/configuration.md` “Data grants”, admin UI, injection) |
| **Access level** | Strength of a data grant: `none` / `freebusy` / `summary` / `city` / `precise` / `read` / `write` / `read-write` | The value inside a data grant |
| **Data grants map** | Where **agent** data grants live (`DataGrant`, `data_grants` in aegis.json / `data_grants.json`) | |
| **App data grant** | Allow rule for a **third-party app** (scoped app token), e.g. `events:freebusy` | Do not call the one-time `aeg_grant_…` code just “a grant” |
| **Service limits** | Per-service map that can only **tighten** a data grant (`grant_limits`) | Admin UI label; also “service limits” in prose |
| **Effective access** | Resolved level after data grant + service tighten | |
| **Accessor** | Principal-level data grant per data type (issue #160) | Distinct axis from data grant |
| **Auth mode** | `auto` / `tailnet` / `api-key` | Who may call Aegis at all (not a data grant) |
| **Admin principal** | Admin API key or allowed tailnet user login | Who may change policy (data grants, apps, UDT file selection); agent credentials are never admin (issue #181) |
| **Tailnet grant** | Tailscale policy rule that allows who can reach Aegis | Tailscale’s `grants` in the policy file — network layer |

One-line shape (also in `docs/configuration.md` “Data grants”): **data grants** are the product
idea; `data_grants` owns agent data grants (default + per-provider floor);
**Service limits** (`grant_limits`) only tighten; app data grants scope
third-party tokens; accessors are the principal axis.

### Tailscale alignment

Users already know Tailscale **grants** (`src` → `dst` + capabilities). Map them
like this:

| Tailscale | Aegis |
|-----------|-------|
| Grant (policy rule) | **Data grant** (product); data-grant / app scope in config |
| Network capability (`ip`) | Tailnet grant/ACL for reaching Aegis |
| Application capability (`app`) | **Access level** on a **data type** (`freebusy`, `read`, …) |
| `ro` / `rw` (e.g. Taildrive) | `read` / `read-write` |
| Deny-by-default | Unset data grant; places default `none` |

Say “data grant” for what the user allows; reserve bare **grant** for Tailscale
policy or the third-party one-time **grant code** (`aeg_grant_…`).

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

JSON uses `data_grants` / `grant_limits` / `data_grants.json`. Other identifiers
stay stable (`canonical_for`, `sources[].canonical`, `perm_type`,
`IsAgentContentPath`, `LibraryKind`). Product rules in `AGENTS.md` are
unchanged by this glossary.
