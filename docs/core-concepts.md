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
| **Aegis** (the personal library service) | product |
| **Fail-open** | product rule |
| **Aegis owns OAuth** | product rule |
| **Aegis does not store media** / bytes stay on source | product rule |
| **Kind-centric surface; named-service connectors** | product rule |
| **Connections, not per-surface OAuth** | product rule |
| **Admin listener never published** (Funnel on the API listener only) | product rule |
| Coverage floor (80%) | process |
| Content/ingest only (`/v1/services`) | product rule |

### Kinds and library

| Concept | Surface weight |
|---------|----------------|
| **Library kind** (short: kind) | product + code |
| **Neutral model** (`Playlist`, `Track`, `Contact`, `Photo`, …) | product + code |
| **Unified library** (`/v1/library/{kind}`) | product |
| `sources[]` / multi-source merge | product + code |
| **Canonical / canonical_for** (non-Aegis home) | product + code |
| **Entity resolution / merge** (ISRC/UPC/MBID/name) | product + code |
| Entity links (user-confirmed identity) | product + code |
| Copy ledger / copy info | product + code |
| Mirror / backup sync | product + code |
| Truncated / sync completeness | product + code |
| User-defined kinds (UDK) / schema packs | product + code |
| Free/busy, summary, city, precise redaction | product + code |

### Access and identity

| Concept | Surface weight |
|---------|----------------|
| **Data grant** (user allows agent/app to use a library kind at an access level) | product |
| **DataGrant** (where agent data grants live) | product + code |
| Service grant limits (tighten-only) | product + code |
| **EffectiveAccess** | code |
| Access levels (`none` / `freebusy` / `summary` / `city` / `precise` / `read` / `write` / `read-write`) | product + code |
| Accessor / AccessorKind (issue #160) | code + admin |
| PermissionsOverview / KindAccess | admin |
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
| **Connection maturity** (coverage: kinds served, fields, transport) | product |
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

`DataGrant`, `Permissions`, `EffectiveAccess`, `Accessor`, `KindAccess`,
`PermissionsOverview`, access-level tokens, service `permissions` maps,
`data_grants` config, and admin “Data grants by kind”.

**Overlap:** five Go types and two config keys all answer “who can see what”.
Data grant is primary; service maps only tighten; Accessor is a third principal
axis. The words “permission”, “access”, and “policy” are used interchangeably
in English.

**Resolution (glossary):** the product idea is a **data grant** (user allows an
LLM agent or app to use a library kind at an access level). Data grant stores
agent data grants; app scopes are **app data grants**.

### 2.4 Canonical vocabulary

`canonical_for` (config), `sources[].canonical` (response), “canonical home”,
“home copy”, “foreign canonical”, `IsCanonicalFor`, `Canonical` field on source
structs, write-policy injection text.

**Overlap:** one idea (non-Aegis authoritative home) expressed as config key,
response field, prose synonym, and method name.

### 2.5 Type / kind / data type / library kind / perm_type

`LibraryKind`, `data type`, `kind` (URL segment), `perm_type` (permissions
keys), `data_grants`, UDK “type”, `LibraryKind.ID`.

**Overlap:** the same registry entry was called four different names in product
prose depending on surface (URL, permissions, admin UI, AGENTS), and “data
type” also collided with the field-shape sense of `type` (`"type": "string"`
in `capabilities.go` and `udk.go`).

**Resolution (glossary, issue #305):** two axes, each named once. The registry
axis is **library kind** (short: **kind**) — already the URL segment and
`LibraryKind`. The field-shape axis is **type** / **field type**. “Data type”
is retired from product prose. `perm_type` and `data_types` stay as
config/response keys that name a kind. **Data grant** keeps its name — it
names what is granted, not the axis it is keyed by; do not write “kind grant”.

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

### 2.9 UDK naming

User-defined kinds (UDK), schema packs (`user-kinds/` directory),
`user_kinds.json` overlay, `UserDefinedKindSchema`.

**Resolution (glossary):** the product concept is **user-defined kind**;
**schema pack** is the on-disk artifact name. Retired synonyms:
“user-defined type”, “UDT”, “type pack”.

This section previously resolved the same question toward “user-defined type
(UDT)”. That resolution is superseded: a pack registers as a `LibraryKind`,
so the name says kind. On-disk names follow: `user_kinds.json`,
`user-kinds/`, `AEGIS_USER_KINDS_DIR`.

---

## 3. Remaining concepts (definitions + key files)

After the overlap call-outs above, the stable concepts.

### Product rules

| Concept | Definition | Key files |
|---------|------------|-----------|
| **Fail-open** | Fetch/parse/match failures pass through; hook returns `allow` on errors. Never invent fail-closed. | `AGENTS.md`, `handler.go`, `internal/kinds/librarykind.go` |
| **Aegis owns OAuth** | Agents never see tokens or authorize services. Tokens encrypted on disk; proxy auto-refreshes. | `tokens.go`, `oauth.go`, `SECURITY.md` |
| **Aegis does not store media** | Canonical bytes stay on the source service. Library is metadata; content transport is ephemeral. | `SECURITY.md`, `immich.go`, `google-photos.go` |
| **Kind-centric surface** | Product API is organized around library kinds (`/v1/library/{kind}`), not provider-shaped endpoints. | `AGENTS.md`, `internal/kinds` |
| **Named-service connectors** | Spotify, Gmail, Immich, … are first-class adapters and match targets, not generic scrapers. | `internal/connectors/`, `run.go` |
| **Connections (not per-surface OAuth)** | Surfaces reference top-level `connections` entries. | `connections.go`, `aegis.example.json` |
| **Content/ingest only** | `/v1/services/{id}/…` is photo bytes, full message bodies, playlist items, local import — not a provider API mirror. | `internal/core/agentpaths.go`, `proxy.go` |
| **External access (invite-only)** | Two listeners: the API listener may be published (credential perimeter — invite tokens, deny-by-default grants); the admin listener stays in-tailnet. No signup/OIDC/federation. | `SECURITY.md`, `docs/external-access.md` |

### Kinds and library

| Concept | Definition | Key files |
|---------|------------|-----------|
| **LibraryKind** | Kind-registry metadata for one unified dataset (id, path, label, usage, instructions, empty/deny/fail messages). | `internal/kinds/librarykind.go`, `internal/kinds/librarykinds.go` |
| **Neutral model** | Provider-agnostic structs (`Playlist`, `Track`, `Contact`, `Photo`, `CalendarEvent`, `Message`, `Transaction`, `PlaceVisit`) plus per-item `*Source`. | `internal/kinds/models.go` |
| **Unified library** | Aggregate endpoints under `/v1/library/{kind}` that fan in every connected provider and merge. | `internal/kinds` (aggregate pipeline) |
| **sources[]** | Per-item provenance list: service, service_name, id, url, `canonical`, `copy`. | `models.go` (each `*Source`) |
| **canonical_for / sources[].canonical** | Configured non-Aegis home for a kind; stamped onto items for write routing. | `config.go:83`, `canonical.go`, `models.go` |
| **Entity merge / entity_id** | Fold provider rows into one neutral item via ISRC/UPC/MBID/ical_UID preferred over name. | `internal/kinds/merge.go`, `entity_links.go` |
| **EntityLink** | User-confirmed cross-service identity stored in `entity_links.json`. | `entity_links.go:22` |
| **CopyInfo / copy ledger** | Best-effort record of mirrored/backup copies (issue #76). | `copy_ledger.go`, `models.go:812` |
| **Mirror** | Deliberate copy of a playlist/liked tracks to another service (`POST /v1/library/mirror`). | `mirror.go` |
| **Truncated** | Response flag when a list is incomplete upstream (page cap); agents must check before claiming completeness. Independent of `next_cursor` paging (issue #185). | `kind.go` instructions, `internal/kinds` |
| **next_cursor** | Opaque continue token for `/v1/library/{kind}` pages (`limit` default 100 / max 500). Omitted at end of the filtered set. | `types/page.go` |
| **User-defined kind (UDK)** | Schema pack under `user-kinds/` loaded into the same library pipeline; never self-enables or shadows built-ins. Product concept is “user-defined kind”; the file/directory is a schema pack. | `internal/kinds/userdefined_schema.go`, `docs/user-defined-kinds.md` |
| **Redaction levels** | `freebusy` (events), `summary` (transactions), `city`/`precise` (places) — reduced payloads. | `internal/core/policy/policy.go`, `internal/kinds` |
| **Provider interfaces** | `PlaylistProvider`, `PhotoProvider`, `TransactionProvider`, … implemented by connectors. | `internal/kinds/providers.go` |

### Access and identity

| Concept | Definition | Key files |
|---------|------------|-----------|
| **Data grant** | What the user does to allow an LLM agent or app to use a library kind at an access level. Hero product term. | `docs/glossary.md`, `docs/configuration.md` “Data grants”, `SECURITY.md` |
| **DataGrant** | Kind-owned store of agent data grants: `Default` level + optional per-provider tighten. | `internal/core/policy/policy.go` |
| **Service grant limits** | Service-scoped `map[dataType]level` that can only tighten a data grant. | `internal/core/policy/policy.go`, `permissions_overlay.go` |
| **EffectiveAccess** | Resolved level for (type, provider) combining data grant + service tighten. | `internal/core/policy/policy.go`, `config.go` |
| **Access levels** | `none`, `freebusy`, `summary`, `city`, `precise`, `read`, `write`, `read-write`. Default `read-write`. | `internal/core/policy/policy.go` |
| **Accessor** | Principal-level allowed access per library kind (issue #160) — distinct from data grant. | `internal/core/policy/accessors.go` |
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
| **Connection maturity** | Coverage of one connection: kinds the service offers vs those Aegis serves, content-field coverage, and transport tier. A code-declared mark on the sync dashboard — not an operational score. | `internal/connectors/maturity.go`, `internal/core/maturity.go`, `docs/sync-maintenance.md` |
| **TokenStore** | Encrypted-at-rest OAuth tokens (AES-256-GCM + PBKDF2), refresh singleflight. | `tokens.go:23` |
| **Encrypted static credentials** | Non-OAuth secrets (e.g. Immich/YNAB API keys) stored under the same `-token-pass` master key; admin API exposes presence only. | `service_connections.go`, `tokens.go`, `SECURITY.md` |
| **CalDAV / CardDAV** | Named-service adapters for calendars/contacts (Nextcloud, iCloud app-specific passwords). Mirrored events feed freebusy. | `internal/connectors/{caldav,carddav,dav,ical,vcard}.go`, `docs/caldav-carddav.md` |
| **SystemInstructions** | English block injected into the LLM request telling the agent how to use Aegis. | each connector `SystemInstructions`, `internal/connectors` |
| **Capabilities / Operation** | Structured metadata of what a connector can do (admin/connector, not agent discovery). | `internal/shared/capabilities.go` |
| **IsAgentContentPath** | Gate on agent-facing `/v1/services` paths (content/ingest only). | `internal/core/agentpaths.go:13` |
| **Path classification** | Ordered table mapping proxy paths → library kinds for permission checks. | `internal/core/pathclass.go` |
| **LibraryKind registry** | The list of built-in kinds + UDK-published kinds. | `internal/kinds/librarykinds.go` |

### Hook and ops

| Concept | Definition | Key files |
|---------|------------|-----------|
| **Pre-request hook** | `POST /v1/pre-request` — detect named services, inject instructions. | `handler.go`, README “Pre-request hook” |
| **GuardrailResponse** | Hook result: `action` allow/block/modify + optional `request_body` / `message`. | `handler.go:43` |
| **Body shape** | LLM wire format (`chat`, `anthropic`, `responses`) used for injection. | `handler.go`, `hookhealth.go` |
| **Hook health** | Live diagnostics + fix strings for Aperture config. | `hookhealth.go`, `docs/configuring-aperture.md` |
| **Cache** | GET+200 only, bounded. | `cache.go` |
| **Metrics / health** | `/metrics` (Prometheus), `/healthz`, `/readyz`. | `core/metrics/`, `metrics.go`, `health.go` |
| **Data directory** | One OS app-data root for all mutable state (issue #74). | `datadir.go` |
| **Admin map / flow** | UI topology of services → types → agents. | `flow.go`, `web/admin.html` |
| **Mail archive** | Local FTS store behind the messages library source (full-history search and fetch). | `core/mail/archive.go` |
| **Places / visits / trips** | Location history model with city/read/precise redaction (issue #142). | `internal/kinds/places.go`, `internal/connectors/{timeline,owntracks}.go` |
| **PhotoSourceDiscipline** | Injected rule: photo bytes only via Aegis blob URLs, never local Photos/Immich env. | `internal/kinds/builders.go` |
| **Write policy notice** | Injected rule: mutations go to the canonical home first; no auto-mirror. | `canonical.go` |

---

## Companion documents

- Audience and communication-surface mapping: [`audience-map.md`](audience-map.md)
- Preferred English for overlapping names: [`glossary.md`](glossary.md)
- Threat model for auditors: [`threat-model.md`](threat-model.md)
- CalDAV/CardDAV setup: [`caldav-carddav.md`](caldav-carddav.md)
- User-defined kinds: [`user-defined-kinds.md`](user-defined-kinds.md)
