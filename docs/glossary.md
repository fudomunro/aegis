# Glossary

Canonical English names for Aegis concepts. Product docs, admin UI, agent
injection text, and coding-agent notes should prefer these words. Issue
[#174](https://github.com/fudomunro/aegis/issues/174) tracks remaining surface
passes (agent strings, admin copy) that still use older synonyms. Issue
[#305](https://github.com/fudomunro/aegis/issues/305) retired “data type” in
favor of **library kind**.

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
| **Connection maturity** | Coverage of one connection: of the kinds the service offers, how many Aegis serves through it, and how completely it fills their fields | Not an operational score — the sync dashboard's success / stability / confidence are separate; prefer **maturity** alone once the page is in view |
| **Transport** | How a connection reaches its service: `api` / `protocol` / `reverse-engineered` / `export/archive` | The maturity modifier; not the content-transport APIs |

Config keys stay `services`, `connections`, `connector` is code-only.

---

## Kinds and library

| Prefer | Means | Notes |
|--------|-------|-------|
| **Library kind** | One library dataset (playlists, tracks, photos, …) — the registry entry and URL kind for it (`/v1/library/{kind}`) | Code: `LibraryKind`. URL segment: `kind`. Short form **kind** once in context. Do not call this a “data type” |
| **Field type** (or **type**) | Shape of one field: `string` / `integer` / `boolean` | Schema `type:` key, `Parameter.Type`, `UserDefinedField.Type`. Never a “kind” |
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
| **User-defined kind (UDK)** | Schema pack under `user-kinds/` | The extension mechanism, in plain English — it publishes a **library kind**, which stays the registry axis. **Schema pack** is the on-disk artifact (file/dir). Retired: “user-defined type”, “UDT”, “type pack” |
| **Access level** | See **Data grants** | Strength of a data grant |
| **Free/busy** | Busy intervals only (no titles/attendees) | Also `summary` (transactions), `city`/`precise` (places) |

### Why not “data type”

“Data type” reads as general development language (Go’s `type`, int/string) and
collides with the **field type** axis, whose own `type` key already lives beside
it in `internal/shared/capabilities.go` and `internal/kinds/userdefined_schema.go`. The
registry axis is already shipped as `kind` in the URL and in `LibraryKind`, so
prose follows the API. Keep the three policy nouns distinct — **data grant**
(what the user allows), **library kind** (which slice), **access level** (how
much). Do not write “kind grant”; a data grant names what is granted, not the
axis it is keyed by.

---

## Data grants (permissions and access)

| Prefer | Means | Notes |
|--------|-------|-------|
| **Data grant** | What the user does to allow an LLM agent or app to use their data — an allow rule on a **library kind** at an **access level** | Hero term for product English (`docs/configuration.md` “Data grants”, admin UI, injection). Never “kind grant” |
| **Access level** | Strength of a data grant: `none` / `freebusy` / `summary` / `city` / `precise` / `read` / `write` / `read-write` | The value inside a data grant |
| **Data grants map** | Where **agent** data grants live (`DataGrant`, `data_grants` in aegis.json / `data_grants.json`) | |
| **App data grant** | Allow rule for a **third-party app** (scoped app token), e.g. `events:freebusy` | Do not call the one-time `aeg_grant_…` code just “a grant” |
| **Service limits** | Per-service map that can only **tighten** a data grant (`grant_limits`) | Admin UI label; also “service limits” in prose |
| **Effective access** | Resolved level after data grant + service tighten | |
| **Accessor** | Principal-level data grant per library kind (issue #160) | Distinct axis from data grant |
| **Auth mode** | `auto` / `tailnet` / `api-key` | Who may call Aegis at all (not a data grant) |
| **Admin principal** | Admin API key or allowed tailnet user login, on the **admin listener** | Who may change policy (data grants, apps, UDK file selection); agent credentials are never admin (issue #181) |
| **External principal** | Caller holding an **invite token** (a data-scope key) | Deny-by-default grants; never admin; may be a person, backend, or another Aegis operator |
| **Invite token** | The credential issued to an external principal | Data-scope key, **hashed at rest** (`api_key_hash`, 0600) like app tokens. Minted/rotated by the admin (`/v1/admin/data-scopes`), plaintext shown once; legacy plaintext entries migrate on load |
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
| Application capability (`app`) | **Access level** on a **library kind** (`freebusy`, `read`, …) |
| `ro` / `rw` (e.g. Taildrive) | `read` / `read-write` |
| Deny-by-default | Owner tier: unset data grant; places default `none`. **External principals**: any unlisted kind is `none` |

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
| **API listener** | The listener serving agent/external surfaces; publishable behind TLS under external access |
| **Admin listener** | The listener serving policy/setup surfaces; in-tailnet only, never published |
| **External access** | Deployment shape where the API listener is reachable outside the tailnet and the credential is the perimeter |
| **Hook health** | Aperture wiring diagnostics + fix strings |
| **Audit log** | Durable access log (agent, surface, decision, …) |
| **Admin map** | UI topology of services → kinds → agents |

---

## Deliberate non-renames

JSON uses `data_grants` / `grant_limits` / `data_grants.json`. Other identifiers
stay stable (`canonical_for`, `sources[].canonical`, `perm_type`,
`IsAgentContentPath`, `LibraryKind`). Product rules in `AGENTS.md` are
unchanged by this glossary.

Keys that survive the “data type” → **library kind** rename, and what they now
mean in prose:

- `perm_type` (schema packs, permissions maps) — a **library kind**. The key
  keeps its name because live schema packs read it.
- `data_types` (capabilities, maturity, accessors, flow responses) and
  `types` (permissions overview) — the **kinds** of a service. JSON shapes
  stay stable; the Go identifiers behind them are now `Kind*`
  (`KindCapability`, `KindMaturity`, `KindAccess`, `KindAccessor`,
  `FlowLibraryKind`, `KindForPath`, `kindPathRules`) and
  `PermissionsOverview.Types` became `Rows`. `DataTypeCapability`,
  `TypeAccess`, and friends are gone from the Go surface — if you meet one
  in an old branch, it means **library kind**. Field-shape `Type` /
  `type` is untouched.
- **Data grant** — deliberately *not* “kind grant”. It names what is granted
  (the user's data); **library kind** names the slice it is keyed by. “Kind
  grant on the kind `tracks`” loads one word with two senses.
- **User-defined kind (UDK)** — the extension mechanism in plain English; it
  publishes a **library kind**, which stays the registry axis. Retired
  synonyms: “user-defined type”, “UDT”, “type pack”. Wire names follow the
  concept (`/v1/admin/udk`, `user_kinds_dir`), as do the on-disk names —
  `user_kinds.json`, `user-kinds/`, `AEGIS_USER_KINDS_DIR`.
