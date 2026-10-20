# Core concepts of Aegis

Living inventory of the domain nouns that define what Aegis is — product
rules, API objects, and the main runtime abstractions. Keep this current as
auth, connectors, and library kinds land; preferred English for overlapping
names is in [`glossary.md`](glossary.md). After the initial listing,
overlapping / colliding vocabulary is called out for cleanup; the remaining
concepts are then defined with file references.

Audience: developers, coding agents, and anyone reviewing the product surface
before a wider launch.

---

## 1. Initial listing

Grouped roughly by layer. Names in **bold** are the ones that currently carry
the most weight in product language.

### Product identity and rules

| Concept | Surface weight |
|---------|----------------|
| **Aegis** (the private tailnet service) | product |
| **Fail-open** | product rule |
| **Aegis owns OAuth** | product rule |
| **Aegis does not store media** / bytes stay on source | product rule |
| **Type-centric surface; named-service connectors** | product rule |
| **Connections, not per-surface OAuth** | product rule |
| **No Tailscale Funnel** | product rule |
| Coverage floor (80%) | process |
| Content/ingest only (`/v1/services`) | product rule |

### Data types and library

| Concept | Surface weight |
|---------|----------------|
| **Library kind** / data type | product + code |
| **Neutral model** (`Playlist`, `Track`, `Contact`, `Photo`, …) | product + code |
| **Unified library** (`/v1/library/{kind}`) | product |
| `sources[]` / multi-source merge | product + code |
| **Canonical / canonical_for** (non-Aegis home) | product + code |
| **Entity resolution / merge** (ISRC/UPC/MBID/name) | product + code |
| Entity links (user-confirmed identity) | product + code |
| Copy ledger / copy info | product + code |
| Mirror / backup sync | product + code |
| Truncated / sync completeness | product + code |
| User-defined types (UDT) / schema packs | product + code |
| Free/busy, summary, city, precise redaction | product + code |

### Access and identity

| Concept | Surface weight |
|---------|----------------|
| **Data grant** (user allows agent/app to use a data type at an access level) | product |
| **DataGrant** (where agent data grants live) | product + code |
| Service grant limits (tighten-only) | product + code |
| **EffectiveAccess** | code |
| Access levels (`none` / `freebusy` / `summary` / `city` / `precise` / `read` / `write` / `read-write`) | product + code |
| Accessor / AccessorKind (issue #160) | code + admin |
| PermissionsOverview / TypeAccess | admin |
| PeerIdentity / auth modes (`auto` / `tailnet` / `api-key`) | product + code |
| **Admin principal** (admin API key / allowed admin logins) | product + code |
| Third-party app tokens + app data grants (issue #131) | product + code |
| Audit log / access events | product + ops |

### Runtime and connectors

| Concept | Surface weight |
|---------|----------------|
| **ServiceConnector** (`Capabilities` + `SystemInstructions` + `HandleProxy`) | code + product |
| **Named service** (Spotify, Gmail, Immich, …) | product |
| **Connection** (OAuth provider credential) | product + code |
| Service definition (`services[]`, action inject/block) | product + code |
| **Host** (connector seam) | code |
| Token store / OAuth token / refresh | code + product |
| Encrypted static credentials (service headers under `-token-pass`) | product + ops |
| CalDAV / CardDAV adapters | product + code |
| Content transport (`/v1/services/{id}/…`) | product |
| IsAgentContentPath / path classification | code |
| **SystemInstructions** / pre-request injection | product (agent-facing) |
| Capabilities / Operation / Parameter / ResponseSpec | code + admin |
| LibraryKind registry / provider interfaces | code |

### Hook and ops

| Concept | Surface weight |
|---------|----------------|
| **Pre-request hook** / GuardrailResponse (`allow`/`block`/`modify`) | product |
| CSRF / Origin protection on state-changing routes | product + ops |
| Hook health / body shape | product + ops |
| Cache (GET 200 only) | code + ops |
| Metrics / health probes | ops |
| Data directory / state paths | product + ops |
| Admin map / flow topology | admin |
| Mail archive / FTS | product |
| Places / visits / trips | product |

---

## 2. Overlapping / colliding concepts

These clusters share a job or share words in ways that will confuse a wider
audience. Tracked in follow-up issue [#174](https://github.com/fudomunro/aegis/issues/174).
Preferred English for each cluster is in [`glossary.md`](glossary.md); use that
glossary in new prose and surface copy.

### 2.1 “Service” vs “connector” vs “connection”

Three near-synonyms:

| Word | Currently means | Where |
|------|-----------------|-------|
| **service** | named upstream brand + config row (`services[]`, service id) | config, connectors, injection |
| **connector** | Go adapter implementing `ServiceConnector` | `internal/connectors` |
| **connection** | OAuth credential entry (`connections{}`) | `connections.go`, tokens |

**Overlap:** docs sometimes say “service” for the adapter and “service” for
the OAuth connection. AGENTS.md already warns against per-surface OAuth dual
paths; the three nouns still read as one concept to newcomers.

### 2.2 “Agent” (runtime LLM) vs coding agent

Product language uses “agent” for the LLM that calls `/v1/library/*`.
Repository language uses `AGENTS.md` and “coding agent” for the automation that
edits the repo. Same word, two audiences (see audience map §F).

### 2.3 Permissions vocabulary cluster

`DataGrant`, `Permissions`, `EffectiveAccess`, `Accessor`, `TypeAccess`,
`PermissionsOverview`, access-level tokens, service `permissions` maps,
`data_grants` config, and admin “Data grants by type”.

**Overlap:** five Go types and two config keys all answer “who can see what”.
Data grant is primary; service maps only tighten; Accessor is a third principal
axis. The words “permission”, “access”, and “policy” are used interchangeably
in English.

**Resolution (glossary):** the product idea is a **data grant** (user allows an
LLM agent or app to use a data type at an access level). Data grant stores
agent data grants; app scopes are **app data grants**.

### 2.4 Canonical vocabulary

`canonical_for` (config), `sources[].canonical` (response), “canonical home”,
“home copy”, “foreign canonical”, `IsCanonicalFor`, `Canonical` field on source
structs, write-policy injection text.

**Overlap:** one idea (non-Aegis authoritative home) expressed as config key,
response field, prose synonym, and method name.

### 2.5 Type / kind / data type / library kind / perm_type

`LibraryKind`, `data type`, `kind` (URL segment), `perm_type` (permissions
keys), `data_grants`, UDT “type”, `LibraryKind.ID`.

**Overlap:** the same registry entry is called four different names in product
prose depending on surface (URL, permissions, admin UI, AGENTS).

### 2.6 Capabilities dialect vs SystemInstructions dialect

`ServiceCapabilities` / `Operation` still describe provider-shaped paths
(`/me/player`, `/search`). `SystemInstructions` teach library-first
(`/v1/library/{kind}`). Product rules say capabilities are admin metadata, not
agent discovery — but the English in both looks agent-consumable.

### 2.7 Content transport vocabulary

`content/ingest`, `content transport`, `IsAgentContentPath`, `/v1/services`,
“bytes stay on the source”, photo “blob_url”.

**Overlap:** three phrases for the same boundary (issue #119).

### 2.8 Identity / merge / entity link

Entity links (`entity_links.json`), merge heuristics (ISRC/UPC/MBID/name),
`entity_id`, `match` field, identity matching (email → phone → handle → name),
`sources[]` provenance.

**Overlap:** “identity”, “entity”, and “match” are three words for folding
provider rows into one neutral item.

### 2.9 UDT naming

User-defined types, UDT, schema packs (`user-types/` directory),
`user_types.json` overlay, `UserDefinedTypeSchema`.

**Resolution (glossary):** the product concept is **user-defined type**;
**schema pack** is the on-disk artifact name. Avoid **type pack**.

---

## 3. Remaining concepts (definitions + key files)

After the overlap call-outs above, the stable concepts.

### Product rules

| Concept | Definition | Key files |
|---------|------------|-----------|
| **Fail-open** | Fetch/parse/match failures pass through; hook returns `allow` on errors. Never invent fail-closed. | `AGENTS.md`, `handler.go`, `internal/types/kind.go` |
| **Aegis owns OAuth** | Agents never see tokens or authorize services. Tokens encrypted on disk; proxy auto-refreshes. | `tokens.go`, `oauth.go`, `SECURITY.md` |
| **Aegis does not store media** | Canonical bytes stay on the source service. Library is metadata; content transport is ephemeral. | `SECURITY.md`, `immich.go`, `google-photos.go` |
| **Type-centric surface** | Product API is organized around data types (`/v1/library/{kind}`), not provider-shaped endpoints. | `AGENTS.md`, `librarykind.go`, `internal/types` |
| **Named-service connectors** | Spotify, Gmail, Immich, … are first-class adapters and match targets, not generic scrapers. | `internal/connectors/`, `run.go` |
| **Connections (not per-surface OAuth)** | Surfaces reference top-level `connections` entries. | `connections.go`, `aegis.example.json` |
| **Content/ingest only** | `/v1/services/{id}/…` is photo bytes, full message bodies, playlist items, local import — not a provider API mirror. | `internal/types/agentpaths.go`, `proxy.go` |
| **No Tailscale Funnel** | Aegis holds music OAuth tokens; tailnet-only. | `SECURITY.md` |

### Data types and library

| Concept | Definition | Key files |
|---------|------------|-----------|
| **LibraryKind** | Type-layer metadata for one unified dataset (id, path, label, usage, instructions, empty/deny/fail messages). | `internal/types/kind.go`, `internal/core/librarykind.go:113` |
| **Neutral model** | Provider-agnostic structs (`Playlist`, `Track`, `Contact`, `Photo`, `CalendarEvent`, `Message`, `Transaction`, `PlaceVisit`) plus per-item `*Source`. | `internal/types/models.go` |
| **Unified library** | Aggregate endpoints under `/v1/library/{kind}` that fan in every connected provider and merge. | `library.go`, `librarykind.go` |
| **sources[]** | Per-item provenance list: service, service_name, id, url, `canonical`, `copy`. | `models.go` (each `*Source`) |
| **canonical_for / sources[].canonical** | Configured non-Aegis home for a kind; stamped onto items for write routing. | `config.go:83`, `canonical.go`, `models.go` |
| **Entity merge / entity_id** | Fold provider rows into one neutral item via ISRC/UPC/MBID/ical_UID preferred over name. | `internal/types/merge.go`, `entity_links.go` |
| **EntityLink** | User-confirmed cross-service identity stored in `entity_links.json`. | `entity_links.go:22` |
| **CopyInfo / copy ledger** | Best-effort record of mirrored/backup copies (issue #76). | `copy_ledger.go`, `models.go:812` |
| **Mirror** | Deliberate copy of a playlist/liked tracks to another service (`POST /v1/library/mirror`). | `mirror.go` |
| **Truncated** | Response flag when a list is incomplete upstream (page cap); agents must check before claiming completeness. Independent of `next_cursor` paging (issue #185). | `kind.go` instructions, `librarykind.go` |
| **next_cursor** | Opaque continue token for `/v1/library/{kind}` pages (`limit` default 100 / max 500). Omitted at end of the filtered set. | `types/page.go` |
| **User-defined type (UDT)** | Schema pack under `user-types/` loaded into the same library pipeline; never self-enables or shadows built-ins. Product concept is “user-defined type”; the file/directory is a schema pack. | `internal/types/udt.go`, `docs/user-defined-types.md` |
| **Redaction levels** | `freebusy` (events), `summary` (transactions), `city`/`precise` (places) — reduced payloads. | `internal/types/policy.go:11`, `places.go`, `events.go` |
| **Provider interfaces** | `PlaylistProvider`, `PhotoProvider`, `TransactionProvider`, … implemented by connectors. | `internal/types/providers.go` |

### Access and identity

| Concept | Definition | Key files |
|---------|------------|-----------|
| **Data grant** | What the user does to allow an LLM agent or app to use a data type at an access level. Hero product term. | `docs/glossary.md`, README “Data grants”, `SECURITY.md` |
| **DataGrant** | Type-owned store of agent data grants: `Default` level + optional per-provider tighten. | `internal/types/policy.go` |
| **Service grant limits** | Service-scoped `map[dataType]level` that can only tighten a data grant. | `policy.go`, `permissions_overlay.go` |
| **EffectiveAccess** | Resolved level for (type, provider) combining data grant + service tighten. | `policy.go:484`, `config.go:437` |
| **Access levels** | `none`, `freebusy`, `summary`, `city`, `precise`, `read`, `write`, `read-write`. Default `read-write`. | `policy.go:11-36` |
| **Accessor** | Principal-level allowed access per data type (issue #160) — distinct from data grant. | `internal/types/accessors.go:31` |
| **PeerIdentity** | Resolved caller identity (tailnet peer / Aperture login / API key). | `identity.go:17`, `SECURITY.md` |
| **Auth modes** | `auto`, `tailnet`, `api-key`. | `SECURITY.md`, `run.go` |
| **Admin principal** | Who may change policy: admin API key (`-admin-api-key`) or allowed tailnet login (`-admin-logins`). Agent credentials are never admin. | `SECURITY.md`, `handler.go`, `identity.go` |
| **CSRF / Origin protection** | State-changing browser routes require same-origin `Origin` / `Sec-Fetch-Site` (plus a single-use consent CSRF token on app authorize). API-key and app-token writes skip the Origin check. | `SECURITY.md`, `handler.go`, `csrf` state store |
| **App token** | Scoped third-party credential; never a provider OAuth token. Carries **app data grants** (`scopes`). | `apps.go`, `apps_grant.go` |
| **Audit log** | Durable access log (`agent`, `surface`, `decision`, …) with retention cap. | `audit.go`, README “Access audit log” |

### Runtime and connectors

| Concept | Definition | Key files |
|---------|------------|-----------|
| **ServiceConnector** | `Capabilities()` + `SystemInstructions(baseURL)` + `HandleProxy(...)`. | `internal/connectors/host.go:49` |
| **Host** | Connector seam: services the runtime offers connectors (tokens, config, HTTP). Not `*Server`. | `host.go:27`, `docs/restructure-core-types-connectors.md` |
| **Named service** | Config row in `services[]` with id, name, action (`inject`/`block`), endpoint, aliases, `canonical_for`. | `config.go`, `aegis.example.json` |
| **Connection** | OAuth credential entry referenced by services (`connection` + `oauth_scopes`). | `connections.go` |
| **TokenStore** | Encrypted-at-rest OAuth tokens (AES-256-GCM + PBKDF2), refresh singleflight. | `tokens.go:23` |
| **Encrypted static credentials** | Non-OAuth secrets (e.g. Immich/YNAB API keys) stored under the same `-token-pass` master key; admin API exposes presence only. | `service_connections.go`, `tokens.go`, `SECURITY.md` |
| **CalDAV / CardDAV** | Named-service adapters for calendars/contacts (Nextcloud, iCloud app-specific passwords). Mirrored events feed freebusy. | `internal/connectors/{caldav,carddav,dav,ical,vcard}.go`, `docs/caldav-carddav.md` |
| **SystemInstructions** | English block injected into the LLM request telling the agent how to use Aegis. | each connector `SystemInstructions`, `librarykind.go` |
| **Capabilities / Operation** | Structured metadata of what a connector can do (admin/connector, not agent discovery). | `internal/types/capabilities.go` |
| **IsAgentContentPath** | Gate on agent-facing `/v1/services` paths (content/ingest only). | `internal/types/agentpaths.go:13` |
| **Path classification** | Ordered table mapping proxy paths → data types for permission checks. | `internal/types/pathclass.go` |
| **LibraryKind registry** | The list of built-in kinds + UDT-published kinds. | `librarykind.go:113` |

### Hook and ops

| Concept | Definition | Key files |
|---------|------------|-----------|
| **Pre-request hook** | `POST /v1/pre-request` — detect named services, inject instructions. | `handler.go`, README “Pre-request hook” |
| **GuardrailResponse** | Hook result: `action` allow/block/modify + optional `request_body` / `message`. | `handler.go:43` |
| **Body shape** | LLM wire format (`chat`, `anthropic`, `responses`) used for injection. | `handler.go`, `hookhealth.go` |
| **Hook health** | Live diagnostics + fix strings for Aperture config. | `hookhealth.go`, `docs/configuring-aperture.md` |
| **Cache** | GET+200 only, bounded. | `cache.go` |
| **Metrics / health** | `/metrics` (Prometheus), `/healthz`, `/readyz`. | `metrics.go`, `metrics_export.go`, `health.go` |
| **Data directory** | One OS app-data root for all mutable state (issue #74). | `datadir.go` |
| **Admin map / flow** | UI topology of services → types → agents. | `flow.go`, `web/admin.html` |
| **Mail archive** | Local FTS store for `/v1/mail/*` history. | `mail_archive.go` |
| **Places / visits / trips** | Location history model with city/read/precise redaction (issue #142). | `places.go`, `timeline.go`, `owntracks.go` |
| **PhotoSourceDiscipline** | Injected rule: photo bytes only via Aegis blob URLs, never local Photos/Immich env. | `internal/types/builders.go` |
| **Write policy notice** | Injected rule: mutations go to the canonical home first; no auto-mirror. | `canonical.go` |

---

## Companion documents

- Audience and communication-surface mapping: [`audience-map.md`](audience-map.md)
- Preferred English for overlapping names: [`glossary.md`](glossary.md)
- Threat model for auditors: [`threat-model.md`](threat-model.md)
- CalDAV/CardDAV setup: [`caldav-carddav.md`](caldav-carddav.md)
- User-defined types: [`user-defined-types.md`](user-defined-types.md)
