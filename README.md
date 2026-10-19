# Aegis

A personal data service for [Tailscale Aperture](https://tailscale.com/docs/aperture) that intercepts LLM requests referencing named services (Spotify, YouTube Music, Amazon Music), serves the user's data from a type-centric library, and enforces **data grants** with OAuth-based authentication.

> Looking for the pitch rather than the manual? The marketing site lives in
> [`site/`](site/) (issue #129).

## How It Works

Aegis runs as two components:

1. **Pre-request hook** — Aperture calls Aegis before forwarding requests to LLM providers. Aegis detects service references in the user's message and injects instructions telling the agent to use Aegis's unified library (`/v1/library/{kind}`) instead of external APIs.

2. **Unified library + content transport** — Agents call `GET /v1/library/{kind}` for the user's data (playlists, tracks, photos, messages, …). Service provenance lives in each item's `sources[]` for sync and backup. A narrow `/v1/services/{id}/...` transport remains only for content the library cannot store (photo bytes, full message bodies, playlist items) and local export import (issue #119).

```
Agent → Aperture → Aegis (hook: inject library URLs) → LLM
Agent → Aegis (/v1/library, content transport) → provider APIs
```

## Prerequisites

- **Go 1.27.1** (matches `go.mod`)
- **Tailscale** with an [Aperture gateway](https://tailscale.com/docs/aperture/get-started) configured
- **OAuth app credentials** for each service you want to connect (see [Setup](#setup))

## Quick Start

```bash
# Clone and build
git clone https://github.com/fudomunro/aegis.git
cd aegis
go build -o aegis .

# Local state lives under one data directory (issue #74):
#   macOS:   ~/Library/Application Support/Aegis/
#   Linux:   ${XDG_DATA_HOME:-~/.local/share}/aegis/
# Override anytime with -data-dir /path or AEGIS_DATA_DIR.
mkdir -p ~/Library/Application Support/Aegis   # or the Linux path above
cp aegis.example.json ~/Library/Application\ Support/Aegis/aegis.json
# Edit aegis.json with your OAuth client IDs and secrets

# Run (recommended: tailnet identity via Tailscale Serve or WhoIs)
./aegis \
  -addr :8080 \
  -base-url https://aegis.your-tailnet.ts.net \
  -auth-mode tailnet \
  -allowed-logins you@example.com \
  -token-pass your-encryption-passphrase

# Or keep a shared API key for non-tailnet clients
./aegis \
  -addr :8080 \
  -base-url https://aegis.your-tailnet.ts.net \
  -api-key your-secret-key \
  -token-pass your-encryption-passphrase
```

Path flags (`-config`, `-token-file`, `-data-grants-file`, `-link-file`,
`-mail-archive`, `-audit-log`, `-log-file`) still accept absolute or relative paths and
override the data-dir defaults — useful for existing ops that keep files
beside the binary.

Aegis starts with these endpoints:
- `POST /v1/pre-request` — Aperture hook
- `GET /v1/library/playlists` — unified playlists across all connected services
- `GET /v1/library/tracks` — unified liked/saved tracks across all connected services
- `GET /v1/library/albums` — unified saved albums across all connected services
- `GET /v1/library/artists` — unified followed/top artists across all connected services
- `GET /v1/library/contacts` — unified contacts across all contact-capable services (plus imported Signal contacts)
- `GET /v1/library/photos` — unified photo metadata across all photo-capable services (bytes stay on the source service)
- `POST /v1/library/photos/share` — publish one photo to Bluesky and return a PoC ownership claim (sha256 + public post; issue #20)
- `GET /v1/library/events` — unified calendar events across all calendar-capable services
- `GET /v1/library/freebusy` — busy intervals only (start/end/all_day) for scheduling apps; no titles or attendees (issue #128)
- `GET /v1/library/messages` — the user's mail collection from live services plus the local archive (Gmail + Google Takeout), merged on Message-ID (bodies stay on the source service); also imported Signal chats (issue #168)
- `GET /v1/library/transactions` — unified financial transactions (YNAB + imported Quicken files); signed decimal amounts, filters `after`/`before`/`account`/`q`/`currency` (issue #141)
- `GET /v1/library/places` — location history as visits and trips (Google Timeline import + OwnTracks); filters `after`/`before`/`locality`/`kind` (issue #142). **Defaults to `none`** — enable `city`, `read`, or `precise` to let agents query it.
- `GET /v1/library/documents` — documents (receipts, warranties, leases, letters) from Paperless-ngx and other sources; filters `q`/`tag`/`after`/`before` (issue #143). `metadata` access returns titles/dates/tags only; `read` adds short excerpts.
- `GET /v1/library/documents/{service}/{id}/text` — full extracted text for one document (issue #143). Requires `documents=read`; metadata is list-only.
- `GET /v1/library/{user-defined}` — user-defined types under `<data-dir>/user-types/` or `AEGIS_USER_TYPES_DIR` (issue #15). **Always opt-in** — enable the kind in data grant before agents can read it. See [User-defined types](#user-defined-types-issue-15).
- `GET /v1/library/copies` — durable copy ledger: where copies exist, sync mode, freshness (issue #76)
- `GET /v1/mail/search` — search the local compressed mail archive (FTS + from/to/date filters)
- `GET /v1/mail/messages/{service}/{id}` — one archived message (body included)
- `GET /v1/mail/messages/{service}/{id}/raw` — decompressed provider payload JSON
- `GET /v1/mail/status` — archive size and sync cursor
- `POST /v1/mail/sync` — ingest a bounded batch of mail into the archive (resumable)
- `POST /v1/library/mirror` — copy a playlist or liked-tracks list from one service to another (canonical backup; see [Mirror / backup sync](#mirror--backup-sync-issue-16-phase-3))
- `GET/POST /v1/services/{id}/...` — content/ingest transport only (photo bytes, full message bodies, playlist items, local import); other service paths return 404 — use `/v1/library/{kind}` (issue #119)
- `GET /healthz` — liveness probe (unauthenticated; no secrets)
- `GET /readyz` — readiness probe (config + token store loaded; unauthenticated)
- `GET /metrics` — Prometheus text metrics (auth required; hook outcomes, cache hit/miss, upstream latency, permission denials)
- `GET /` `/home` `/library` `/services` `/advanced` — admin dashboard shell (friendly durable SPA routes, issue #150; refresh keeps the same section). Legacy `GET /v1/admin` still serves the shell and the SPA canonicalizes the URL to `/`.
- `GET /v1/admin/flow` — data-flow topology + recent access stats (`?window=5m|1h|24h`) for the Home tab (issue #32). Includes per-agent nodes and access arrows to library data types so the Home map can draw which agents touched which data (issue #114)
- `GET /v1/admin/hook-health` — Aperture pre-request hook wiring diagnostics (stale Guardian names/URLs, auth failures, missing `send` fields)
- `GET /v1/admin/library/{kind}` — admin Library tab preview (same data as `/v1/library/{kind}`, does not count as Home access)
- `GET /v1/admin/udt/status` — loaded user-defined kinds + local file selection (issue #15)
- `GET /v1/admin/udt/browse?path=` — list folders and `.json` files for file selection (admin auth; see SECURITY.md)
- `PUT/DELETE /v1/admin/udt/kinds/{id}/sources/{sid}` — select/clear local JSON files for a schema source
- `POST/DELETE /v1/admin/udt/kinds/{id}/extra[/{extraId}]` — attach/remove an extra local file feed

See [SECURITY.md](SECURITY.md) for the Tailscale identity model, recommended ACLs, and why Funnel should stay off.

Vocabulary for this manual (service vs connector vs connection, data type vs
library kind, **data grant**, data grant, canonical home, content transport,
LLM agent vs coding agent) is defined in [`docs/glossary.md`](docs/glossary.md).

## User-defined types (issue #15)

Bring your own library kind with a **user-defined type** (JSON schema pack)
under `<data-dir>/user-types/` (or `AEGIS_USER_TYPES_DIR`). A user-defined type
becomes a real `GET /v1/library/{id}` endpoint on the same aggregate pipeline
as built-ins. Prefer “user-defined type” in product English (see
[`docs/glossary.md`](docs/glossary.md)); “schema pack” is the on-disk format.

```text
user-types/
  reading-list/
    schema.json          # fields, identity, sources
    sample/books.json    # demo file_json feed
```

Rules that keep packs safe:

- **Opt-in.** A pack cannot create a data grant. Enable the kind in data grants / data grant
  (`PUT /v1/admin/data-grants` or the admin UI) before agents can read it.
- **No hijacking.** `id` / `perm_type` / `path` cannot collide with built-in
  data types or routes (`path` is always `/v1/library/{id}`).
- **Fail-open.** A bad source becomes `by_service` status; a bad pack is
  skipped at load with a log line.
- **Local file selection** lives in `user_types.json` (admin Advanced →
  User-defined types). Schema packs are never rewritten.

```bash
export AEGIS_USER_TYPES_DIR=$PWD/examples/user-types   # or copy into <data-dir>/user-types/
# enable after load (data grant), then:
curl -s https://aegis.example.ts.net/v1/library/reading-list | jq .
```

See `docs/user-defined-types.md` for the schema format and `examples/user-types/` for a portable pack.

## Observability (issue #105)

Aegis exposes cheap probes, Prometheus metrics, and a durable access audit
trail so you can see who reached which data — including denials — after a
restart.

### Health probes

| Endpoint | Auth | Meaning |
|----------|------|---------|
| `GET /healthz` | none | Liveness: process and HTTP stack are up |
| `GET /readyz` | none | Readiness: config and token store are loaded (503 otherwise) |

Both return a tiny JSON body (`{"status":"ok"}` / `{"status":"ready"}`) and
never include config, tokens, or service inventory. Use them from a process
supervisor or local health check.

### Metrics (`GET /metrics`)

Prometheus text exposition format. **Auth required** (same credentials as
other protected APIs) because series include service ids and denial reasons.

| Series | Type | Labels |
|--------|------|--------|
| `aegis_process_start_time_seconds` / `aegis_process_uptime_seconds` | gauge | — |
| `aegis_hook_outcomes_total` | counter | `action` (`allow`, `modify`, `block`, `auth_fail`, `parse_fail`) |
| `aegis_access_total` | counter | `surface`, `target`, `decision` |
| `aegis_cache_events_total` | counter | `result` (`hit`/`miss`) |
| `aegis_permission_denials_total` | counter | `surface`, `target` |
| `aegis_audit_events_total` | counter | `decision` |
| `aegis_upstream_duration_seconds` | histogram | `service` |

Counters are process-lifetime and **reset on restart**. That is reset-safe for
Prometheus: `rate()` / `increase()` treat a drop as a counter reset. Agent
identities are deliberately **not** used as label values (cardinality and
privacy); per-agent detail stays in the admin Map and the audit log.

Scrape example:

```bash
curl -H "Authorization: Bearer $KEY" "$AEGIS/metrics"
```

### Access audit log

Durable, append-only record of allowed and denied data access. Stored in
SQLite at `<data-dir>/audit_log.db` (issue #105).

Each row is redacted like the in-memory access ring (issue #32): agent login
or `app:<id>`, surface (`library` / `service` / `auth` / …), target (service
id or library kind), data type, decision, and a short reason
(`permission`, `app_scope`, `service_blocked`, `auth`). **No tokens, request
bodies, query strings, or full paths.**

- **Retention:** 30 days by default (`-audit-retention-days`). Older rows are
  pruned on startup and periodically on write. A hard row cap (500k) drops the
  oldest rows first so a noisy agent cannot fill the disk.
- **Fail-open:** audit write errors never block agent requests.
- **Disable:** pass an empty `-audit-log` (explicitly set) to turn persistence off.

```bash
# Example: 90-day retention
./aegis ... -audit-retention-days 90
```

Admin Library previews (`/v1/admin/library/{kind}`) are **not** audited as
agent access, matching Map counting rules (issue #62).

## Configuration

Aegis is configured via a JSON file. See [`aegis.example.json`](aegis.example.json) for the full format.

### Service definition

```json
{
  "id": "spotify",
  "name": "Spotify",
  "aliases": ["spotify music"],
  "action": "inject",
  "endpoint": "https://api.spotify.com/v1",
  "cache_ttl": "10m",
  "description": "Lists Spotify playlists and liked tracks into /v1/library/*",
  "connection": "spotify",
  "oauth_scopes": ["user-library-read", "user-follow-read", "playlist-read-private"]
}
```

| Field | Description |
|-------|-------------|
| `id` | Unique identifier, used in proxy URLs (`/v1/services/{id}`) |
| `name` | Display name, also used for matching in user messages |
| `aliases` | Additional names to match (case-insensitive substring). Photo services should include singular forms (`photo`, `picture`) as well as plurals so questions like “oldest photo in my collection” match (issue #125). |
| `action` | `inject` (redirect to proxy) or `block` (reject requests) |
| `endpoint` | Upstream API base URL |
| `headers` | Static headers to add to upstream requests (optional) |
| `cache_ttl` | How long to cache GET responses (e.g. `5m`, `1h`) |
| `grant_limits` | Optional map of data type → access level for agents (migration override; can only tighten data grant — see [Data grants](#data-grants)) |
| `canonical_for` | Optional list of library kinds where this service is the authoritative non-Aegis home (see [Canonical sources](#canonical-sources-non-aegis-issue-16)) |
| `connection` | Name of a shared OAuth provider connection (required for OAuth-enabled surfaces) |
| `oauth_scopes` | Scopes this surface requires from its connection; missing scopes degrade that surface to `unauthorized` |

### Provider connections

Every OAuth-enabled service must reference a top-level **connection** so
multiple surfaces can share one provider registration and one token:

```json
{
  "connections": {
    "google": {
      "name": "Google",
      "client_id": "…",
      "client_secret": "$ENV:GOOGLE_CLIENT_SECRET",
      "auth_url": "https://accounts.google.com/o/oauth2/v2/auth",
      "token_url": "https://oauth2.googleapis.com/token",
      "scopes": [
        "https://www.googleapis.com/auth/youtube.readonly",
        "https://www.googleapis.com/auth/photospicker.mediaitems.readonly",
        "https://www.googleapis.com/auth/contacts.readonly"
      ]
    }
  },
  "services": [
    {
      "id": "youtube-music",
      "connection": "google",
      "oauth_scopes": ["https://www.googleapis.com/auth/youtube.readonly"]
    },
    {
      "id": "google-photos",
      "connection": "google",
      "oauth_scopes": ["https://www.googleapis.com/auth/photospicker.mediaitems.readonly"]
    }
  ]
}
```

**Secret indirection (issue #102).** `client_secret` and service `headers`
values may reference the environment instead of embedding credentials, so
`aegis.json` can live in docs, backups, or dotfiles without leaking secrets:

| Form | Example | Resolves to |
|------|---------|-------------|
| `$ENV:NAME` | `"$ENV:SPOTIFY_CLIENT_SECRET"` | `$SPOTIFY_CLIENT_SECRET` |
| `${NAME}` | `"${SPOTIFY_CLIENT_SECRET}"` | same |
| literal | `"inline-secret"` | unchanged |

Resolution runs at config load and on SIGHUP reload. A missing or empty
environment variable logs a warning and leaves the secret empty — set the
variable and reload (or restart) before connecting. Command substitution
(`!cmd`) is not supported.

- Aegis stores **one** encrypted token (access + refresh) per connection ID.
- Login at `/v1/auth/{service_id}/login` resolves to the surface’s connection
  and requests the connection’s scope union.
- Google logins send `access_type=offline` and `include_granted_scopes=true`,
  and only force `prompt=consent` when no refresh token exists yet — so adding
  Photos later does not drop YouTube access.
- A connection token missing a surface’s `oauth_scopes` makes that surface
  report library status `unauthorized` (or proxy 401) without failing the
  whole connection.

Register **all** surface callback URLs on the same OAuth client, e.g.:

- `https://your-aegis/v1/auth/youtube-music/callback`
- `https://your-aegis/v1/auth/google-photos/callback`
- `https://your-aegis/v1/auth/google/callback`

### Data grants

What you do to let an LLM agent or app use your data is a **data grant**: an
allow rule on a **data type** at an **access level** (`none`, `freebusy`,
`summary`, `city`, `precise`, `metadata`, `read`, `write`, `read-write`). This is the
application-layer side of Tailscale-style access — you already use a tailnet
grant so callers can *reach* Aegis; data grants say what they may *use*.

Prefer the glossary terms here ([`docs/glossary.md`](docs/glossary.md)):
**data grant** is the product idea; **data grant** is where **agent** data
grants live; a service **permissions map** can only tighten that default;
**app data grants** scope third-party tokens. JSON/config keys stay
`data_grants`, `grant_limits`, `data_grants.json`.

Data grants live with **data types**, not service providers (issue #148). The
primary model is a per-data grant: a type-level default plus optional
per-provider overrides that can only **tighten** that default. Service
`permissions` maps remain as migration overrides and can only further
restrict what a data grant allows.

**Accessors (issue #160).** Data is not only reached by LLM agents. Aegis also
runs sync pipelines (mail archive, copy ledger, timeline/quicken import) and
third-party apps hold scoped tokens. The data grants editor is **type-centric**:
each data type shows the agent data grant (editable) plus only the other
accessors that apply to that type (read-only). `GET /v1/admin/permissions`
returns that same model. The map on Home answers the other half of confidence:
what each has *actually* accessed. Multi-user principals (issue #149) will
appear as additional accessor rows.

| Accessor kind | Where levels come from | In the type editor |
|---------------|------------------------|--------------------|
| `agent` | type-owned policy (`data_grants` / `data_grants.json`) | editable — admin “Data grants” / `PUT /v1/admin/data-grants` |
| `app` | app `scopes` on the third-party record (fail closed) | read-only (edit via `PATCH /v1/admin/apps/{id}`); shown when the type is named or the level is not `none` |
| `sync` | fixed by the pipeline (what Aegis may store while archiving/mirroring) | read-only; shown only for types the pipeline stores |
| `service` | configured connector that syncs that library type (e.g. Spotify, YouTube Music under `playlists`) | read-only for now |

Primary (aegis.json `data_grants` or data_grants.json `data_grants`, or admin “Data data grants”):

```json
"types": {
  "tracks": {
    "default": "read-write",
    "providers": {
      "spotify": "read"
    }
  },
  "player": { "default": "none" },
  "places": {
    "default": "none",
    "providers": {
      "google-timeline": "city"
    }
  }
}
```

Service grant-limits map (still accepted; tighten-only once a data grant
exists for that key):

```json
"grant_limits": {
  "default": "read",
  "playlists": "read",
  "tracks": "read",
  "player": "none",
  "recommendations": "none"
}
```

| Level | Allowed methods |
|-------|-----------------|
| `none` | none — 403 for that data type |
| `freebusy` | `GET`, `HEAD` on `events`, but responses reduced to availability only (issue #128) |
| `summary` | `GET`, `HEAD` on `transactions`, but responses reduced to totals by category/month only — no line items (issue #141) |
| `city` | `GET`, `HEAD` on `places`, reduced to locality, country, and the visit/trip time window (issue #142) |
| `precise` | `GET`, `HEAD` on `places` with coordinates (issue #142) |
| `metadata` | `GET`, `HEAD` on `documents`, reduced to titles, dates, tags, correspondents, and sources — no excerpt or text (issue #143) |
| `read` | `GET`, `HEAD` |
| `write` | `POST`, `PUT`, `PATCH`, `DELETE` |
| `read-write` | both (also accepts `full` / `rw`) |

**Resolution order** (issue #148):

1. **Type default** — `types[type].default`. Empty default keeps previous
   unrestricted behavior (`read-write`) except for opt-in types.
2. **Provider override** — `types[type].providers[service_id]`, which can
   only tighten the type default (e.g. `read` → `none`, `precise` → `city`).
3. **Legacy service `permissions`** — during the migration window these are
   provider overrides and can only tighten. When **no data grant exists** for
   a key, the service map remains authoritative (so existing configs and
   places opt-in keep working).

- The reserved key `default` in a service map applies to data types not listed —
  **except `places`**, which is opt-in (see below). Data grant has no cross-type
  `default` key: each type owns its own default.
- **`freebusy` is events-only** (issue #128). Scheduling apps can see when you
  are busy without reading titles, attendees, locations, or descriptions.
  Responses keep only `start_at`, `end_at`, `all_day`, and `status`. Prefer
  `GET /v1/library/freebusy` for availability; `/v1/library/events` and the
  calendar proxy apply the same redaction when events access is `freebusy`.
  The level is rejected on other data types and on `default`.
- **`summary` is transactions-only** (issue #141). Apps can see how much was
  spent by category or month without reading line items or account names.
  Responses keep only `summary` totals (`count`, `total_amount`, `by_category`,
  `by_month`). Prefer `GET /v1/library/transactions` with `summary` access for
  budget dashboards; `read` adds line items with merchant but still strips
  account identifiers. The level is rejected on other data types and on
  `default`.
- **`places` is opt-in and uses a city / read / precise ladder** (issue #142).
  Location history is the most sensitive data type we ship: **default is
  `none`**, and neither a type-level empty default nor a service `default` key
  enables it. Name it explicitly — type default, type `providers[id]`, or a
  service `permissions.places` entry. Pre-request injection never advertises
  `places` until it is named. Granularity:
  - `city` — locality, country, and the visit/trip window only (enough for
    "was I out of town?"). No names, addresses, coordinates, or raw point IDs.
  - `read` — full place names and addresses, still no coordinates.
  - `precise` — names, addresses, and coordinates (also what `write` /
    `read-write` map to for reads; there is no agent write surface).
  `city` and `precise` are places-only and rejected on other keys. Import
  (Google Timeline) and live (OwnTracks) sources both honor the same ladder.
- **`metadata` is documents-only** (issue #143). Life-admin paperwork
  (receipts, warranties, leases, insurance, tax/medical letters) can be listed
  without reading content. Responses keep title, mime_type, created/modified,
  correspondent, document_type, tags[], people[], and sources[] — **no
  excerpt and no text**. `read` (or unrestricted `write` / `read-write`) adds
  a short excerpt on list rows. Full text is only at
  `GET /v1/library/documents/{service}/{id}/text`, which requires `read` —
  metadata access is list-only and cannot fetch text. The level is rejected
  on other data types and on `default`. Per-tag allow/deny (e.g. hide
  `medical`) is a design spike, not implemented.
- **Unknown proxy paths** (not a mapped data type) use the **strictest**
  configured level, not a type default, so a tight type cannot be bypassed by an
  unmapped endpoint (issue #94).
- Proxy paths are **canonicalized before classification**. Duplicate slashes
  (including `%2f`) collapse to the real type; `.` / `..` segments are rejected
  with `400`. Classification and upstream routing always see the same path.
- If neither data grant nor service `permissions` is set, agents keep
  unrestricted access (previous behavior).
- **Bootstrap vs overlay (issue #71, #148).** `aegis.json` may set bootstrap
  `permissions` per service. Admin UI saves write a separate
  `data_grants.json` overlay (flag `-data-grants-file`) schema **version 3**:
  `data_grants` (primary DataGrant map) and `service_limits` (tighten-only migration
  overrides). A service present in the overlay uses those permissions; a
  present key set to `null` means unrestricted; services absent from the
  overlay keep the bootstrap map from `aegis.json`. Version 1 files
  (`services` only) still load. The overlay holds no secrets — admin
  permission edits never rewrite OAuth client credentials.
- Permissions **further constrain** the connector; they never add capabilities.
  The admin form only offers **Editable** for types with a true agent data-write
  operation on the connector. Mirror backup writes and photo-picker session
  creates are not Editable. Write / read-write on a type the form cannot offer
  is treated as read when saved from the admin UI (proxy still allows mutating
  methods the connector implements if the config already has write and you do
  not re-save). Enable write explicitly when a real agent write op appears.
- Enforcement is on the content/ingest transport, the unified library endpoint, and connector metadata (denied types/operations are hidden from agents).
- Pre-request injection also lists denied types so agents do not try them.
- Admin API: `GET/PUT /v1/admin/data-grants` is the primary editor;
  `GET/PUT /v1/admin/services/{id}/grant-limits` remains the secondary
  per-service override (and still reports `effective_permissions` after
  data-grant resolution).

Known data types: `user`, `tracks`, `albums`, `artists`, `playlists`, `search`,
`player`, `channels`, `library`, `recommendations`, `contacts`, `photos`,
`events`, `messages`, `transactions`, `places`, `documents`. Paths that do not map to a known type fall through to the
`default` level.

### Canonical sources (non-Aegis, issue #16)

Aegis does **not** store library content. The authoritative copy for each
item lives on a service outside Aegis. That location is marked
`canonical` on the item's `sources[]` entry.

Two layers set the flag:

1. **Connector** — a provider may mark inherent ownership. Google Photos always
   sets `canonical: true` because photo bytes stay on Google (never in Aegis).
2. **Config** — declare a home service per library kind:

```json
{
  "id": "spotify",
  "canonical_for": ["playlists", "tracks"]
}
```

Valid kinds: `playlists`, `tracks`, `albums`, `artists`, `contacts`, `photos`,
`events`, `messages`, `transactions`, `places`, `documents`. At most **one** non-blocked service may claim each kind. After a library fetch,
sources from a configured home service are stamped `canonical: true`.

Merge sorts canonical sources first. `CanonicalService()` (and the JSON
`sources[]` order) tell agents where the home copy is. System instructions say
to prefer that service for writes or mirrors. The admin service card shows a
`canonical: …` badge; library cards mark items with a canonical source.

#### Write / access policy

When a matched service allows **write** (or `read-write`) for a library kind,
pre-request injection adds a short write-policy block:

- The **home** service is told: “send mutations of &lt;type&gt; here first.”
- A **secondary** service is told: “prefer &lt;home&gt; before this service;
  secondary copies are not auto-synced.”

Library instructions always include the multi-source mutation rule: write to
`sources[].canonical` first, then ask the user before updating other copies.
Aegis still does **not** route or mirror writes automatically — agents call
the service proxy for whichever service they choose. Use
[POST /v1/library/mirror](#mirror--backup-sync-issue-16-phase-3) for a
deliberate copy to a secondary service.

#### Mirror / backup sync (issue #16, phase 3)

To keep a **backup** of a playlist or liked-tracks list on another service:

```
POST /v1/library/mirror
Authorization: Bearer <api-key>   # or tailnet identity
Content-Type: application/json

{
  "kind": "playlists",
  "source_service": "spotify",
  "source_id": "37i9dQZF1DXcBWIGoYBM5M",
  "target_service": "youtube-music",
  "name": "Today's Top Hits (backup)",
  "dry_run": false
}
```

| Field | Description |
|-------|-------------|
| `kind` | `playlists` or `tracks` (liked/saved tracks) |
| `source_service` | Service to read from; empty uses the configured `canonical_for` home |
| `source_id` | Service-local playlist ID (required for `playlists`) |
| `target_service` | Service to write to (required; must differ from source) |
| `name` | Optional playlist title on the target |
| `dry_run` | Plan only — no writes to the target |

Response reports `planned`, `copied`, `skipped`, and per-track `skips`. Set
`dry_run` first when the match quality matters.

**Supported services:** `spotify` and `youtube-music` as either side.
Cross-service track matching is **name-based** (issue #4) — expect skips and
review them before treating a mirror as a complete backup. A single call
copies at most 200 tracks (`truncated: true` when more remain).

Requires **write** permission on the target for that kind and **read** on the
source. Aegis stores no media; it reads source metadata and writes through
the target's API using the stored OAuth token.

Immich is supported as a **local/foreign canonical photo source** (see
[Immich](#immich-local--foreign-canonical)). Additional foreign-DB /
professional-data connectors remain follow-up work on
[issue #16](https://github.com/fudomunro/aegis/issues/16).

### Copy ledger (issue #76)

`sources[]` is **live inventory** (what each provider returns right now).
The copy ledger is Aegis’s **durable memory** of where copies exist and which
version of the data sits there — so the system can answer “is this backup
current?” without re-scanning entire mailboxes.

Stored in SQLite at `<data-dir>/copy_ledger.db` (`-copy-ledger` overrides;
empty path disables). Rows are written when Aegis actually places or imports a
copy:

| Producer | What is recorded |
|----------|------------------|
| `POST /v1/library/mirror` | One row per mirrored playlist / liked-tracks list (target service + id, item count, name) |
| Google Takeout import | One **optimistic snapshot** row per export path (watermark + stored counts) |

**Sync fidelity is per kind** (issue #76):

| Kind | Mode | Version token | Incremental rule |
|------|------|---------------|------------------|
| `photos` | `full` | `content_hash` / `modified_at` | Re-check metadata; change ⇒ stale |
| `playlists`, `tracks` | `full` | `snapshot_id` / `item_count` | Snapshot mismatch ⇒ stale |
| `messages` | `optimistic` | Takeout watermark / ingest cursor | Only material newer than the watermark; force-full is repair work ([#86](https://github.com/fudomunro/aegis/issues/86)) |
| `contacts`, `events` | `full` | etag / `updated` | Cheap metadata check |

**Read surface:**

```
GET /v1/library/copies?kind=&service=&status=&entity_key=
```

Returns `copies[]` (`role`, `sync_mode`, `status`, `version`, `watermark`,
`last_synced_at`, `last_checked_at`, `provenance`) plus `by_status` /
`sync_modes` / `by_service` counts.

Library `sources[]` also carry a `copy` stamp **only when** a ledger row
matches that `(service, id)`:

```json
"copy": {
  "role": "mirror",
  "sync_mode": "full",
  "sync_status": "in_sync",
  "as_of": "2026-09-25T12:00:00Z",
  "provenance": "mirror"
}
```

Missing ledger rows never hide or alter library items (fail-open). Status
values: `unknown`, `in_sync`, `stale`, `missing`. Freshness **checks**
(`last_checked_at` / `stale`) are observational — Aegis still does not
auto-sync secondary copies. Repair / re-mirror is
[#86](https://github.com/fudomunro/aegis/issues/86).

**Admin UI (issue #90)** surfaces the same fields read-only: library cards
show a per-item copy badge (“2 copies · 1 stale”), source chips/detail show
`role` / `sync_mode` / `as_of` / `checked_at` / `sync_status`, the Library
tab filters by copy status (in sync / stale / missing / unchecked), and
Advanced → Copy ledger lists `GET /v1/library/copies` rows with the same
filters.

### Environment variables

| Variable | Flag equivalent | Description |
|----------|----------------|-------------|
| `AEGIS_API_KEY` | `-api-key` | API key for authenticating requests (optional in `auto`/`tailnet` modes) |
| `AEGIS_AUTH_MODE` | `-auth-mode` | `auto` (default), `tailnet`, or `api-key` |
| `AEGIS_ALLOWED_LOGINS` | `-allowed-logins` | Comma-separated Tailscale logins allowed to call the API |
| `AEGIS_BASE_URL` | `-base-url` | Public URL of this Aegis instance |
| `AEGIS_APERTURE_URL` | `-aperture-url` | Optional public URL of the Aperture gateway (admin live links) |
| `AEGIS_TOKEN_PASS` | `-token-pass` | Passphrase for token encryption |
| `AEGIS_DATA_DIR` | `-data-dir` | Data directory for local state (see [Data directory](#data-directory-issue-74)) |

### Data directory (issue #74)

Aegis keeps **all local state** under one data directory so backups, migration,
and wipe are one folder — not whatever directory the binary happened to start in.

| OS | Default `-data-dir` |
|----|---------------------|
| macOS | `~/Library/Application Support/Aegis/` |
| Linux / other | `$XDG_DATA_HOME/aegis` (or `~/.local/share/aegis`) |
| Windows | `%AppData%\Aegis` |

Override with `-data-dir /path` or `AEGIS_DATA_DIR`. The directory is created
on first run with mode `0700` (it holds secrets).

```
<data-dir>/
  aegis.json              # bootstrap config (OAuth clients live here)
  data_grants.json        # admin data-grant overlay (non-secret)
  apps.json               # third-party app registry (0600; token hashes only)
  service_connections.json # admin Immich/static-auth + Tailscale Admin overlay (0600; may hold API keys)
  user_types.json         # admin UDT local-file selection overlay (issue #15)
  user-types/             # user-defined type schema packs (issue #15)
  entity_links.json       # confirmed playlist/entity links
  tokens.json             # encrypted OAuth tokens (0600)
  copy_ledger.db          # SQLite copy ledger (issue #76; empty -copy-ledger disables)
  mail-archive.db         # SQLite+FTS mail archive (omit -mail-archive to disable)
  quicken.db              # Quicken QIF/QFX import store (issue #141; empty -quicken-store disables)
  timeline.db             # Google Timeline import store (issue #142; empty -timeline-store disables)
  signal.db               # Signal contacts/messages import store (issue #168; empty -signal-store disables)
  audit_log.db            # durable access audit trail (issue #105; empty -audit-log disables)
  logs/aegis.log          # process log (also mirrored to stderr)
```

Every path above is the default **when the corresponding flag is omitted**.
An explicit flag always wins:

| Flag | Default under `-data-dir` | Notes |
|------|---------------------------|-------|
| `-config` | `aegis.json` | Bootstrap config (services, connections, secrets) |
| `-data-grants-file` | `data_grants.json` | Admin data-grant overlay; |
| (none) | `service_connections.json` | Admin Immich/static-auth overlay (sibling of `-config`); `aegis.json` is never rewritten |
| `-token-file` | `tokens.json` | Encrypted token storage |
| `-link-file` | `entity_links.json` | User-confirmed entity links |
| `-copy-ledger` | `copy_ledger.db` | Empty string (explicitly set) disables the copy ledger |
| `-mail-archive` | `mail-archive.db` | Empty string (explicitly set) disables `/v1/mail` |
| `-quicken-store` | `quicken.db` | Empty string (explicitly set) disables the Quicken import source |
| `-timeline-store` | `timeline.db` | Empty string (explicitly set) disables the Google Timeline import source |
| `-signal-store` | `signal.db` | Empty string (explicitly set) disables the Signal import source |
| `-audit-log` | `audit_log.db` | Empty string (explicitly set) disables the access audit trail |
| `-log-file` | `logs/aegis.log` | Empty string (explicitly set) logs to stderr only |

Startup logs the resolved data directory (`data directory ready` / `data_dir`
on `starting server`). The admin **Runtime** panel shows the same path.

**Migrating from cwd defaults:** if you previously kept `aegis.json`,
`tokens.json`, and friends next to the binary, either move them into the data
directory or keep passing the explicit path flags.

### All flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `:8080` | Listen address |
| `-base-url` | | Public base URL (required) |
| `-auth-mode` | `auto` | `auto`, `tailnet`, or `api-key` — see [SECURITY.md](SECURITY.md) |
| `-api-key` | | Shared API key (required only for `api-key` mode) |
| `-allowed-logins` | | Optional allowlist of Tailscale logins |
| `-ts-socket` | auto-detect | tailscaled LocalAPI socket for WhoIs |
| `-data-dir` | OS app data dir | Local state root (see [Data directory](#data-directory-issue-74)) |
| `-config` | `<data-dir>/aegis.json` | Bootstrap config path (services, connections, secrets) |
| `-data-grants-file` | `<data-dir>/data_grants.json` | Admin data-grant overlay (non-secret). All data-grant saves write here; `aegis.json` is never rewritten |
| `-token-file` | `<data-dir>/tokens.json` | Encrypted token storage path |
| `-link-file` | `<data-dir>/entity_links.json` | Entity link store path |
| `-copy-ledger` | `<data-dir>/copy_ledger.db` | Copy ledger path; empty disables (issue #76) |
| `-mail-archive` | `<data-dir>/mail-archive.db` | Mail archive path; empty disables `/v1/mail` |
| `-quicken-store` | `<data-dir>/quicken.db` | Quicken import store; empty disables (issue #141) |
| `-import-quicken` | | Import a QIF/QFX file or directory, then exit (issue #141) |
| `-timeline-store` | `<data-dir>/timeline.db` | Google Timeline import store; empty disables (issue #142) |
| `-import-timeline` | | Import a Timeline JSON file/dir or Takeout zip, then exit (issue #142) |
| `-signal-store` | `<data-dir>/signal.db` | Signal import store; empty disables (issue #168) |
| `-import-signal` | | Import a Signal export (JSONL/JSON or plaintext SQLite), then exit (issue #168) |
| `-audit-log` | `<data-dir>/audit_log.db` | Access audit log path; empty disables (issue #105) |
| `-audit-retention-days` | `30` | Days of audit events to retain before pruning |
| `-log-file` | `<data-dir>/logs/aegis.log` | Process log file; empty disables file logging |
| `-aperture-url` | (none) | Aperture gateway base URL for admin setup links |
| `-token-pass` | | Token encryption passphrase (required). Wrong passphrase refuses to start and never overwrites `tokens.json` (issue #100) |
| `-token-new-pass` | | One-shot: re-encrypt `tokens.json` under this passphrase and exit (issue #101) |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-log-format` | `json` | `json` or `text` |

### Token encryption (issue #101)

`tokens.json` is sealed with AES-256-GCM under a PBKDF2-HMAC-SHA256 master
key. The file records its own KDF parameters so costs can be raised later
without breaking existing stores.

| Field | Meaning |
|-------|---------|
| `version` | `2` is current; `1` (legacy) still loads |
| `kdf` | `pbkdf2-sha256` |
| `iterations` | PBKDF2 cost for the master key (new files: 600 000, OWASP guidance) |
| `salt` | Master salt (base64) |
| `key_check` | Wrong-passphrase detector sealed under the master key |
| `tokens.<id>.salt` | Per-entry salt (base64); entry key = HKDF(master, salt) |
| `tokens.<id>.data` | AES-GCM ciphertext of the token JSON |

**Legacy files (version 1)** — string ciphertexts, implicit 100 000 iterations,
no per-entry salts — still load. The next write migrates them to the version 2
layout (same master cost until you rekey).

**Rotating the passphrase / raising KDF cost:**

```bash
# Stop Aegis first, then re-encrypt under the new passphrase.
# -token-pass is the current secret; -token-new-pass is the new one.
# Rekey always writes version 2 with a fresh master salt and the current
# default iteration count (never lowers a harder existing cost).
AEGIS_TOKEN_PASS='old-secret' \
  ./aegis -token-new-pass 'new-secret' \
  -data-dir /path/to/aegis-data
```

Passing the same value for both secrets still rotates the master salt and
raises iterations on older files. Update `AEGIS_TOKEN_PASS` (or `-token-pass`)
to the new secret before restarting the service.

### Authentication

Aegis can authenticate callers three ways:

1. **Tailscale identity** (recommended for agents) — via [Serve identity headers](https://tailscale.com/kb/1312/serve) or WhoIs against local `tailscaled`. Same trust model Aperture uses: identity is cryptographic, not a shared secret agents might paste into a chat.
2. **API key** — `Authorization: Bearer <key>` or `X-Api-Key`, for bridges and non-tailnet tools.
3. **Third-party app token** — `Authorization: Bearer aeg_app_…` for registered non-agent clients (issue #131). See [Third-party app tokens](#third-party-app-tokens-issue-131).

Default mode is `auto` (identity if present, else API key). For a solo tailnet, prefer `-auth-mode tailnet` and no key. Full policy guidance lives in [SECURITY.md](SECURITY.md).

API key comparison is constant-time (`crypto/subtle`). HTML and JSON responses
carry `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, a restrictive
CSP, and `Referrer-Policy: no-referrer` (issue #98).

### Third-party app tokens (issue #131)

Non-agent clients (for example a hosted scheduling service) can read library
data with a **scoped app token** instead of the shared API key. Apps are
registered in the admin API; the plaintext token is shown once.

```
POST /v1/admin/apps
{"id":"eco","name":"Event Coordinator","scopes":{"events":"freebusy","default":"none"}}
→ {"app":{...},"token":"aeg_app_…"}

GET /v1/library/freebusy
Authorization: Bearer aeg_app_…
```

| Endpoint | Auth | Notes |
|----------|------|-------|
| `GET /v1/admin/apps` | API key / tailnet | List apps (token hints only) |
| `POST /v1/admin/apps` | API key / tailnet | Create; returns token once |
| `GET/PATCH/DELETE /v1/admin/apps/{id}` | API key / tailnet | Inspect, update scopes/name/disable, revoke |
| `POST /v1/admin/apps/{id}/rotate` | API key / tailnet | New token; old one dies |

**Scopes** use the same access levels as [Data grants](#data-grants)
(`none`, `freebusy`, `read`, `write`, `read-write`). Unlisted types fall back to
`default`, which is forced to `none` when omitted (fail closed). `freebusy` is
events-only.

**What an app token can call:**

- `GET /v1/library/{playlists,tracks,albums,artists,contacts,photos,events,freebusy,messages,transactions,places,documents}` only
- Not admin, OAuth, service proxy, mail, pre-request, mirror, review, or links
- Not document full-text (`…/{service}/{id}/text`) and not user-defined kinds
  (issue #183). Built-in list paths are derived from `LibraryKind.AppReadable`.

When an app’s `events` scope is `freebusy`, `/v1/library/events` is redacted to
availability fields even if service permissions would allow full events (issue
#128 + #131). Cache entries are isolated per app scope level so a freebusy app
never reads a full-events cache.

**Reachability.** App tokens are an authorization layer, not a public ingress.
The third party must be able to reach Aegis (tailnet, VPN, or a tunnel you
operate). Do not use Tailscale Funnel. For interactive browser use, the user’s
machine can call Aegis on localhost/MagicDNS while it is awake; background
backend pull needs an always-on host.

**Storage.** `apps.json` (mode 0600) holds SHA-256 token hashes and hints only.
Rotate or delete an app at any time; tokens are not recoverable.

### Sample calendar (demo / query testing)

A service with `endpoint: "sample://calendar"` uses the built-in
`sample-calendar` connector (issue #77): synthetic near-term events, no OAuth.
The third-party demo uses this so free/busy has busy blocks without a live
Google connection. Cancelled rows are included and omitted from free/busy.

### Third-party connect button (demo)

Issue #131 also explores the “Aegis icon on a third-party site” grant UX.
This build ships a working demo of that handoff:

| Route | Auth | Purpose |
|-------|------|---------|
| `GET /demo/third-party` | public | Fake scheduling app with **Continue with Aegis** |
| `GET /v1/apps/authorize` | public | Consent page (shows app name + scopes) |
| `POST /v1/apps/authorize` | API key / tailnet | Owner allows; returns one-time `aeg_grant_…` code (10 min) |
| `POST /v1/apps/token` | code is the credential | Redeem code → `aeg_app_…` token |
| `POST /v1/apps/revoke` | app token | Disconnect: destroys the token and disables the app until the next grant |

Demo flow:

1. Open `http://<aegis>/demo/third-party` and toggle **Aegis** on.
2. Consent page lists what the app may read (this demo asks for
   `events:freebusy` only). Allow the grant.
3. The browser returns with `?code=` and exchanges it for an app token.
4. The page calls `GET /v1/library/freebusy` and shows busy intervals.
5. Toggle Aegis **off** to clear the page token and `POST /v1/apps/revoke`.

`redirect_uri` must be a path on the same host (no open redirects). Reuse of a
code fails; grant creates or updates the app under `/v1/admin/apps` so you can
revoke it later. For local testing without Tailscale identity, the consent page
will prompt for an API key once and keep it in `sessionStorage` for the tab.



### HTTP server operations (issues #96–#97)

Aegis runs an explicit `http.Server` (not `ListenAndServe` defaults):

| Setting | Value | Why |
|---------|-------|-----|
| `ReadHeaderTimeout` | 10s | Stops Slowloris |
| `ReadTimeout` | 5m | Bounds slow uploads |
| `WriteTimeout` | unlimited | Library fan-out / proxy streams may outlive a fixed write deadline |
| `IdleTimeout` | 2m | Reaps idle keep-alives |

- **SIGINT / SIGTERM** drain in-flight requests (30s) and exit 0.
- **SIGHUP** reloads `aegis.json` and the permissions overlay (`Config.Reload`).
  Reloadable: services, connections, permissions bootstrap. **Not** reloadable:
  `-addr`, `-data-dir`, `-token-pass`, `-api-key`, `-auth-mode` (restart required).
- **Inbound body caps** (issue #97): hook 8 MiB, service proxy 16 MiB, admin/mail
  4 MiB / 1 MiB. Oversized bodies return **413 Payload Too Large** and are not
  buffered. The pre-request hook still fail-opens (`allow`) on parse/read errors
  that are not size overflows.

## Connecting Services

1. Register an OAuth app with each service (see below for per-service guides)
2. Add the client ID and secret to your `aegis.json` (`connections` + service `connection` reference). Prefer `"$ENV:…"` for `client_secret` so the file stays shareable — see [Provider connections](#provider-connections).
3. Start Aegis and visit `https://your-aegis/` (or `/services`)
4. Open the **Services** tab and click **Connect** for each service — you'll be redirected to authorize
5. Tokens are stored encrypted on disk and auto-refresh when expired. A wrong `-token-pass` refuses to start (so a typo cannot wipe `tokens.json`); if some entries cannot be decrypted, Aegis refuses to rewrite the file until that is fixed. KDF parameters are recorded in the file; see [Token encryption](#token-encryption-issue-101) to rotate the passphrase.

If no services are connected yet, the Services tab shows extra help describing the OAuth flow.

### Spotify

1. Go to [Spotify Developer Dashboard](https://developer.spotify.com/dashboard)
2. Create an app
3. Set the redirect URI to `https://your-aegis/v1/auth/spotify/callback`
4. Copy the Client ID and Client Secret into `aegis.json`

Spotify scopes (see `aegis.example.json`):

| Scope | Needed for |
|-------|------------|
| `user-library-read` | Liked tracks (`/v1/library/tracks`), saved albums (`/v1/library/albums`), playlists |
| `user-follow-read` | Followed artists (`/v1/library/artists` via `/me/following`) |
| `playlist-read-private` / `playlist-read-collaborative` | Private and collaborative playlists |
| `user-read-playback-state` | Player state (optional; often denied via permissions) |

**Reconnect after adding scopes.** OAuth tokens keep the scopes from the original consent. After you add `user-follow-read` (or any new scope) to `aegis.json`, visit `/v1/auth/spotify/login` again so Spotify re-issues a token with the expanded grant. Without that, `/v1/library/artists` returns an upstream 403 even though saved tracks still work.

### YouTube Music (Google)

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create an OAuth 2.0 Client ID (Web application)
3. Add redirect URIs for every Google surface you use:
   - `https://your-aegis/v1/auth/youtube-music/callback`
   - `https://your-aegis/v1/auth/google-photos/callback`
   - `https://your-aegis/v1/auth/google/callback`
4. Enable the APIs you need (YouTube Data API v3, People API, Photos Picker API)
5. Put the **same** client ID/secret in the shared `connections.google` block
   (see [Provider connections](#provider-connections-issue-26))

### Amazon Music (Login with Amazon)

1. Go to [Login with Amazon](https://developer.amazon.com/loginwithamazon/console/site/lwa/overview.html)
2. Create a new application
3. Add `https://your-aegis/v1/auth/amazon-music/callback` as an allowed return URL
4. Copy the Client ID and Client Secret into `aegis.json`

## Integrating with Aperture

Full operator guide (minimal config, config API apply, Guardian migration, verification): **[docs/configuring-aperture.md](docs/configuring-aperture.md)**.

Add Aegis as a pre-request hook in your Aperture configuration:

```json
{
  "hooks": {
    "aegis": {
      "url": "https://your-aegis/v1/pre-request",
      "apikey": "your-aegis-api-key",
      "timeout": "5s"
    }
  },
  "grants": [
    {
      "src": ["*"],
      "app": {
        "tailscale.com/cap/aperture": [
          {
            "models": "**",
            "send_hooks": [
              {
                "name": "aegis",
                "events": ["pre_request"],
                "send": ["user_message", "request_body"]
              }
            ]
          }
        ]
      }
    }
  ]
}
```

### Hook health check

Aegis can tell whether the Aperture hook is still wired for the **Aegis**
names and URL, not leftover Guardian config.

```bash
# Live traffic diagnostics + copyable expected hooks/grants snippet
curl -H "Authorization: Bearer $KEY" "$AEGIS/v1/admin/hook-health"

# Validate a pasted Aperture policy (or bare hooks map)
curl -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  --data-binary @aperture-policy.json \
  "$AEGIS/v1/admin/hook-health/check"
```

Reported issues include:

| Code | Meaning |
|------|---------|
| `stale_hook_host` / `legacy_hook_url` | Call or config still uses `guardian.*` |
| `legacy_hook_name` / `legacy_send_hook_name` | Hook key still named `guardian` |
| `hook_url_mismatch` | Hook `url` does not match Aegis `-base-url` + `/v1/pre-request` |
| `hook_auth_failures` | Pre-request calls failing auth (bad `apikey` / auth-mode) |
| `missing_user_message` / `send_missing_user_message` | Aperture is not sending `user_message` (service matching breaks) |
| `missing_request_body` / `send_missing_request_body` | Aperture is not sending `request_body` (instruction injection breaks) |
| `unrecognized_body_shape` | Request body is not a supported LLM wire format, so instructions were not injected |
| `no_hook_traffic` | No `POST /v1/pre-request` observed since startup |
| `no_pre_request_hook` / `grants_missing_pre_request` | Config never enables the pre_request hook |

`status` is `ok`, `warn`, or `error`. `expected_hook_config` is a ready-to-paste
`hooks` + `grants` snippet using the live base URL and the hook name `aegis`.

### Two-legged auth

Aegis has **two** callers, and they authenticate differently:

| Leg | Who calls Aegis | How they authenticate |
|-----|--------------------|------------------------|
| **Hook** | Aperture (`POST /v1/pre-request`) | Usually the shared API key in the Aperture hook config (`"apikey"`). In `tailnet` mode Aperture must also present tailnet identity (Serve headers / WhoIs). |
| **Agent data** | The LLM agent (`/v1/library/playlists`, content transport under `/v1/services/...`) | Preferred: Tailscale identity from the agent’s host (Serve or WhoIs). Fallback: API key if `auth-mode` is `auto` or `api-key`. |

Agents do **not** need Spotify/YouTube/Amazon OAuth tokens — Aegis attaches those when fetching user data. With `-auth-mode tailnet` and no API key, agents on the tailnet call Aegis without a shared secret.

After wiring the hook, confirm it with [`GET /v1/admin/hook-health`](docs/configuring-aperture.md#verify-with-hook-health) — it flags leftover Guardian names, wrong URLs, and missing `send` fields.

## API Reference

Protected endpoints accept **either** a valid API key (`Authorization: Bearer <key>` or `X-Api-Key`) **or** a Tailscale identity, depending on `-auth-mode` (see [SECURITY.md](SECURITY.md)).

### Pre-request hook

```
POST /v1/pre-request
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Receives Aperture's `pre_request` payload. Returns one of:
- `{"action": "allow"}` — no service matched, or the LLM request already carries the Aegis instructions
- `{"action": "modify", "request_body": {...}}` — LLM request with injected proxy instructions (Aperture requires the `request_body` field)
- `{"action": "block", "message": "..."}` — request rejected (403)

Instruction injection understands three provider wire shapes (issue #103):

| Wire shape | How Aegis detects it | Where instructions go |
|------------|----------------------|------------------------|
| OpenAI chat completions | top-level `messages` | prepended `{"role":"system"}` message |
| Anthropic Messages | top-level `system` and/or `metadata` provider/model (`anthropic`, `claude`) | top-level `system` (string or content blocks) |
| OpenAI Responses | top-level `input` (without `messages`) | prepended system item in `input` (or `instructions` when `input` is absent) |

Injection is idempotent: if the same instruction block is already present, Aegis
returns `allow` and leaves the body unchanged. Unrecognized body shapes fail open
(`allow`) and are counted in hook health as `unrecognized_body_shape`.

When the user asks about playlists without naming a service (e.g. "what playlists do I have?"), Aegis injects instructions for the unified library endpoint so agents can answer across every connected service. The same applies to liked songs ("what songs have I liked?") via `/v1/library/tracks`.

### Unified library

```
GET /v1/library/playlists
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's playlists from every connected music service in Aegis's **neutral format**, regardless of whether they live on Spotify, YouTube Music, Amazon Music, etc.

```json
{
  "playlists": [
    {
      "name": "Today's Top Hits",
      "description": "The hottest tracks right now.",
      "owner": "Spotify",
      "track_count": 50,
      "sources": [
        {
          "service": "spotify",
          "service_name": "Spotify",
          "id": "37i9dQZF1DXcBWIGoYBM5M",
          "url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
          "extra": { "uri": "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", "public": true }
        }
      ]
    },
    {
      "name": "Road Trip",
      "track_count": 15,
      "sources": [
        {
          "service": "spotify",
          "service_name": "Spotify",
          "id": "sp-roadtrip",
          "url": "https://open.spotify.com/playlist/sp-roadtrip"
        },
        {
          "service": "youtube-music",
          "service_name": "YouTube Music",
          "id": "PLxyz...",
          "url": "https://www.youtube.com/playlist?list=PLxyz...",
          "extra": { "published_at": "2024-01-15T00:00:00Z" }
        }
      ]
    }
  ],
  "total": 2,
  "synced": true,
  "by_service": {
    "spotify": { "status": "ok", "count": 2 },
    "youtube-music": { "status": "ok", "count": 1 },
    "amazon-music": { "status": "unauthorized", "error": "not authorized — connect at /v1/auth/amazon-music/login" }
  }
}
```

Neutral playlist fields (user-facing):

| Field | Description |
|-------|-------------|
| `name` | Playlist title |
| `description` | Playlist description, if any |
| `owner` | Owner display name, when known |
| `track_count` | Item count (max across synced sources) |
| `sources[]` | Service-local copies: `service`, `service_name`, `id`, `url`, `canonical`, `copy` (issue #76), `extra` |
| `extra` | Other service-specific fields |

### Cross-service sync (v1)

The library endpoint treats **same-named playlists** on different services as one user-facing playlist: Aegis merges their `sources` and sets `"synced": true`. It does **not** copy tracks between services yet.

Tracks carry unified `artists` / `album` objects. Cross-service matching prefers a durable **`entity_id`** when a service provides one or Aegis resolves one (track **ISRC** or MusicBrainz **recording** MBID, album **UPC** or **release-group** MBID, artist **MBID**); otherwise it falls back to **text** (`NormalizeIdentityKey`) (`match: "name"`). MusicBrainz lookups are budgeted and cached; failures keep name matching.

**Playlists** have no catalog IDs. Same-name playlists still merge (`match: "name"`), but users can confirm identity explicitly:

```
GET    /v1/library/links
POST   /v1/library/links   {"kind":"playlist","sources":[{"service":"spotify","id":"…"},{"service":"youtube-music","id":"…"}]}
DELETE /v1/library/links/{id}
```

Linked playlists merge even when names differ (`match: "link"`). Links persist in `entity_links.json` under the [data directory](#data-directory-issue-74) (`-link-file` overrides).

**Merge review:** `GET /v1/library/review` lists multi-source entities joined by **name only** (no shared ISRC/UPC/MBID/link) so they can be checked or explicitly linked.

### Migration from name-key merge

1. Library responses now include `match` (`isrc` / `upc` / `mbid` / `link` / `name`) and `entity_id` when known.
2. Name-only joins still happen; they are marked `match: "name"` and appear in `/v1/library/review`.
3. Prefer durable IDs for identity; treat name merges as provisional until reviewed or linked.
4. Clients that only compared display names should prefer `entity_id` + `sources[]` instead.

Tracked in [issue #4](https://github.com/fudomunro/aegis/issues/4).

Service-level playlist lists (`GET /v1/services/{id}/me/playlists`) are **not** agent API (issue #119). Per-service status values in library `by_service` remain: `ok`, `unauthorized`, `error`, `skipped`.

### Library source overview (issue #162)

Aggregate library responses enrich each `by_service` entry so clients can render a **source overview** — relative-sized boxes for every source of that data type (admin Library does this at the top of each kind):

| Field | Description |
|-------|-------------|
| `name` | Human-readable service label |
| `count` | Items returned (or archive inventory size for mail source rows) |
| `status` / `truncated` / `error` | Fetch health (unchanged) |
| `last_synced_at` | When Aegis last ingested or fetched this source, if known |
| `canonical` | Configured home copy for this kind (`canonical_for`, issue #16) |
| `role` | `"canonical"` / `"mirror"` when the kind has a configured home |
| `parent` | Optional nest key (e.g. `google-takeout` under `google-gmail` for messages) |

For messages, when the local mail archive knows both, `by_service` also includes separate `google-gmail` and `google-takeout` rows with archive inventory counts and per-source `last_synced_at`, so the overview can show Gmail and Google Takeout side by side (with Takeout nested under Gmail). The `mail-archive` fetch target remains for compatibility.

### Pagination and filters (issue #185)

Every `GET /v1/library/{kind}` (built-in and user-defined) shares one agent-facing page window so large libraries stay inside an LLM context:

| Request | Meaning |
|---------|---------|
| `limit` | Page size. Default **100**, max **500**. Out-of-range values clamp. |
| `cursor` | Opaque continue token from the previous response. Omit for the first page. Invalid cursor → `400`. |

| Response field | Meaning |
|----------------|---------|
| `total` | Full **filtered** count for this request (not the page length). |
| `next_cursor` | Pass as the next `cursor`. **Omitted when the walk is complete.** |
| `truncated` | Upstream page-cap incompleteness (see below) — **not** “more pages in this response.” |
| `synced` / `by_service` | Multi-source merge flag and per-service fetch status (unchanged). |

Time filters are always **`after` / `before`** (inclusive lower / exclusive upper). Kind-specific filters stay kind-specific (`q`, `tag`, `account`, `currency`, `locality`, `kind`, …). Mail **`from` / `to` are sender/recipient addresses only** — never time bounds. `/v1/mail/search` accepts the same `limit` + `cursor` (and still accepts `offset` for the admin UI).

Walk the full set by repeating the request with `next_cursor` until it is omitted. Do not stop at the first page and do not treat a short page as “that’s everything” when `next_cursor` is present.

### Sync completeness

Aegis aims to surface a user’s **full** library, not a prefix. Two layers matter:

1. **Pagination safety valve** — connectors and proxy auto-pagination follow up to `libraryPageCap` pages (currently 200, about 10k items at page size 50 / 20k at 100). That cap exists only to stop a looping upstream; it is not a product limit. When a connector hits it with more pages still upstream, it sets `by_service[service].truncated = true`, and the library response also sets a top-level `"truncated": true`. Proxy auto-pagination responses set `"truncated": true` in the combined payload the same way.

2. **Provider / API gaps** — some datasets are incomplete for reasons Aegis cannot fix by paginating harder:

| Gap | Why | How it is reported |
|-----|-----|--------------------|
| **Google Photos full library** | Library API reads require Google app verification; unverified apps get 403. Aegis uses the Picker API instead — only the user’s last selection. | Empty library until a pick; system instructions tell agents to run the picker flow. Not a `truncated` flag — the library is intentionally a selection, not a full dump. |
| **YouTube Music albums** | No public saved-albums API. | `/v1/library/albums` simply has no YouTube source. |
| **Amazon Music** | No public developer REST API; playlist pagination may return an unfollowed `nextToken`. | When `nextToken` is present and not followed, `truncated` is set rather than claiming completeness. |
| **Playlist track bodies** | Library playlist entries carry `track_count` and metadata, not every track object. | `track_count` is authoritative; fetch items via content transport `GET /v1/services/{id}/playlists/{playlistId}/items`. |
| **Cross-service entity resolution** | Tracks merge on ISRC when present; name+artist is the fallback. Artist MBIDs / playlist links not yet implemented. | Partial — [issue #4](https://github.com/fudomunro/aegis/issues/4). |

**Always check `truncated` before answering “is this everything?”** Library system instructions tell agents to do the same. Admin Library badges turn orange and say `truncated` when a service hit its page cap.

### Unified liked tracks

```
GET /v1/library/tracks
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's liked/saved tracks from every connected music service in Aegis's neutral `Track` format. Connectors opt in by implementing `LikedTrackProvider` (`ListUserLikedTracks`). Permission key: `tracks` (also covers proxy paths `/me/tracks` and `/me/liked`).

```json
{
  "tracks": [
    {
      "name": "Blinding Lights",
      "artists": [{ "name": "The Weeknd", "sources": [{ "service": "spotify", "name": "The Weeknd" }] }],
      "album": { "name": "After Hours", "sources": [{ "service": "spotify", "id": "al1", "name": "After Hours" }] },
      "sources": [
        { "service": "spotify", "service_name": "Spotify", "id": "t1", "url": "https://open.spotify.com/track/t1" },
        { "service": "youtube-music", "service_name": "YouTube Music", "id": "vid1", "url": "https://www.youtube.com/watch?v=vid1" }
      ]
    }
  ],
  "total": 1,
  "synced": true,
  "next_cursor": "MTAw",
  "by_service": {
    "spotify": { "status": "ok", "count": 120 },
    "youtube-music": { "status": "ok", "count": 45 }
  }
}
```

`total` is the full filtered count; `next_cursor` (when present) continues the page walk — see [Pagination and filters](#pagination-and-filters-issue-185). When any service hits its **upstream** page cap, the response also sets `"truncated": true` and that service’s `by_service` entry sets `"truncated": true`. That flag is independent of `next_cursor`.

Cross-service merge prefers shared **ISRC** (`entity_id`, `match: "isrc"`), then **title + primary artist** via `NormalizeIdentityKey` (`match: "name"`). Spotify maps `/me/tracks` (saved library) and supplies ISRC when present; YouTube Music maps the channel liked-videos playlist, keeps only music-shaped items (category Music or a `" - Topic"` catalog channel), and strips YouTube title decoration so music videos can name-match catalog tracks ([#43](https://github.com/fudomunro/aegis/issues/43), [#4](https://github.com/fudomunro/aegis/issues/4)).

### Unified albums and artists

```
GET /v1/library/albums
GET /v1/library/artists
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Saved albums and followed/top artists follow the same library pattern. Connectors opt in via `AlbumProvider` (`ListUserSavedAlbums`) and `ArtistProvider` (`ListUserFollowedArtists`). Permission keys: `albums` and `artists`.

- Spotify albums: `GET /me/albums`; artists: `GET /me/following?type=artist`
- YouTube Music artists: channel subscriptions (`GET /subscriptions?mine=true`). No public saved-albums API — albums remain Spotify-only until one is available.
- Permission key `artists` also gates the underlying proxy paths `/me/following` and `/me/subscriptions` so `artists: none` blocks both the library endpoint and the raw proxy.

### Contact as a data type

`Contact` is Aegis's neutral representation of a **person or other entity**
who appears in personal data — someone in a photo, an email correspondent, a
playlist collaborator, etc. Future data types (mail, photos, calendar) can
embed `Contact` references the same way tracks embed `Artist`/`Album`.

Neutral contact fields (user-facing):

| Field | Description |
|-------|-------------|
| `name` | Preferred display name |
| `emails[]` | Known addresses (lowercased for stable matching) |
| `phones[]` | Known numbers in digit form (`+` kept when present) |
| `handles[]` | Usernames / social IDs / service accounts |
| `organization` | Company, team, or group, when known |
| `sources[]` | Service-local copies: `service`, `service_name`, `id`, `name`, `emails`, `phones` |

Cross-service matching prefers the strongest shared identifier:

1. **Email** — normalized (case-insensitive)
2. **Phone** — digits only, optional leading `+`
3. **Handle** — lowercased, leading `@` stripped
4. **Name** — `NormalizeIdentityKey`, used **only** when a contact has no
   email/phone/handle, so two different people who share a display name do
   not merge

Contacts that share any of those keys are merged into one entity with multiple
`sources[]` (union-find, so A~email B and B~phone C form one contact).

```
GET /v1/library/contacts
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's contacts from every connected contact-capable service.
Connectors opt in by implementing `ContactProvider` (`ListUserContacts`).
Permission key: `contacts` (paths `/contacts/*`, `/me/contacts`, and `/people/*`
on the service proxy).

```json
{
  "contacts": [
    {
      "name": "Ada Lovelace",
      "emails": ["ada@example.com"],
      "phones": ["+15551234567"],
      "organization": "Analytical Engines",
      "sources": [
        {
          "service": "google",
          "service_name": "Google Contacts",
          "id": "people/c123",
          "name": "Ada Lovelace",
          "emails": ["ada@example.com"]
        }
      ]
    }
  ],
  "total": 1,
  "synced": true,
  "by_service": {
    "google": { "status": "ok", "count": 1 }
  }
}
```

#### Google Contacts

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create an OAuth 2.0 Client ID (Web application)
3. Add `https://your-aegis/v1/auth/google/callback` as an authorized redirect URI
4. Enable the **People API**
5. Copy credentials into `aegis.json` (see `aegis.example.json`)

Scope: `https://www.googleapis.com/auth/contacts.readonly`. The connector lists
address-book connections via `GET /people/me/connections` and maps them to
neutral `Contact` values. Service-local IDs are People resource names
(`people/c123`).

Contacts are the first cross-type identity: `Photo.people[]` holds the same
`Contact` shape, so a person from this address book can be matched to someone
listed on a photo via email/phone/name. Google Photos itself does not currently
expose face/people labels through the public API, so photo `people[]` stays
empty until a provider supplies them — the merge path is ready.

### Photos as a data type

`Photo` is Aegis's neutral representation of a photo (or video still).
Service identity lives in `sources[]`, same as playlists and contacts.

**Aegis never stores photo bytes.** The authoritative copy stays on a
non-Aegis service marked `canonical` (see
[Canonical sources](#canonical-sources-non-aegis-issue-16) /
[issue #16](https://github.com/fudomunro/aegis/issues/16)).
`blob_url` points at the retrievable image when the source exposes one; Google
Photos `baseUrl` values expire in about an hour, so agents must not treat them
as permanent links.

Neutral photo fields (user-facing):

| Field | Description |
|-------|-------------|
| `taken_at` | Capture time (RFC3339), when known |
| `width` / `height` | Pixel dimensions |
| `mime_type` | Media type (e.g. `image/jpeg`) |
| `filename` | Original filename or service display name |
| `caption` | Description, when present |
| `people[]` | Known people as `Contact` values; merged with contacts-library identity matching |
| `sources[]` | Service-local copies: `service`, `id`, `url`, `canonical`, `copy` (issue #76), `blob_url`, `thumbnail_url` |

Cross-service merge uses a **content hash** when a provider supplies one
(`extra.content_hash`), otherwise **taken_at + dimensions**. Filename alone is
never an identity — two different `IMG_0001.jpg` files must not merge.

```
GET /v1/library/photos
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's photo metadata from every connected photo-capable service.
Connectors opt in by implementing `PhotoProvider` (`ListUserPhotos`).
Permission key: `photos` (paths `/photos/*`, `/mediaItems/*`, `/picker/*`, and
`/me/photos` on the service proxy).

```json
{
  "photos": [
    {
      "taken_at": "2023-07-04T18:30:00Z",
      "width": 4032,
      "height": 3024,
      "mime_type": "image/jpeg",
      "filename": "beach.jpg",
      "sources": [
        {
          "service": "google-photos",
          "service_name": "Google Photos",
          "id": "AGxyz...",
          "canonical": true,
          "blob_url": "https://lh3.googleusercontent.com/...",
          "thumbnail_url": "https://lh3.googleusercontent.com/..."
        }
      ]
    }
  ],
  "total": 1,
  "synced": true,
  "by_service": {
    "google-photos": { "status": "ok", "count": 1 }
  }
}
```

#### Google Photos (Picker API)

Full Library API reads (`photoslibrary.readonly`) require Google app
verification; unverified apps get 403. Aegis therefore uses the
**[Photos Picker API](https://developers.google.com/photos/picker/guides/get-started-picker)**:
the user explicitly selects photos once; agents only see that selection.

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create or reuse an OAuth 2.0 Client ID (Web application)
3. Add `https://your-aegis/v1/auth/google-photos/callback` as an authorized redirect URI
4. Enable the **Photos Picker API** (not the Library API)
5. Copy credentials into `aegis.json` (see `aegis.example.json`) with scope
   `https://www.googleapis.com/auth/photospicker.mediaitems.readonly`
6. Re-authorize at `/v1/auth/google-photos/login` if you previously used the library scope

Picker flow:

```
POST /v1/services/google-photos/picker/sessions
  → { "id", "picker_uri", "media_items_set": false }

# User opens picker_uri in a browser and selects photos

GET /v1/services/google-photos/picker/sessions/{id}
  → poll until media_items_set is true

GET /v1/services/google-photos/picker/mediaItems?sessionId={id}
  → neutral Photo[] (also cached as the last pick for /v1/library/photos)
```

`/v1/library/photos` returns the **last completed Google Photos pick** (empty
until the user has picked once). Metadata only — Aegis still never downloads
or stores image bytes; `blob_url` values expire in about an hour.

Ownership / provenance proofs for photos are intentionally deferred:
[issue #20](https://github.com/fudomunro/aegis/issues/20). A **Bluesky share
PoC** is available so a published photo carries a lightweight claim (content
hash + public post locator) — see [Photo share to Bluesky](#photo-share-to-bluesky-issue-20-poc).

#### Photo share to Bluesky (issue #20 PoC)

```
POST /v1/library/photos/share
Authorization: Bearer <api-key>
Content-Type: application/json

{
  "source_service": "immich",
  "source_id": "asset-uuid",
  "caption": "optional post text",
  "alt": "optional image alt text"
}
```

Aegis fetches the original bytes from the canonical photo source **for this
request only** (nothing is cached or stored), hashes them, uploads the image
to Bluesky, and creates an `app.bsky.feed.post` with an images embed. Response:

```json
{
  "target_service": "bluesky",
  "source_service": "immich",
  "source_id": "asset-uuid",
  "post_uri": "at://did:plc:…/app.bsky.feed.post/…",
  "post_url": "https://bsky.app/profile/you.bsky.social/post/…",
  "claim": {
    "type": "sha256+public-post",
    "sha256": "…",
    "mime_type": "image/jpeg",
    "byte_size": 123456,
    "target_service": "bluesky",
    "post_uri": "at://…",
    "note": "PoC ownership claim for issue #20 …"
  }
}
```

The share is also recorded in the copy ledger (`provenance: "share"`), so
`GET /v1/library/copies` and `sources[].copy` can surface the breadcrumb.

**Limits (PoC):** Bluesky image embeds cap at 1 MB. The claim is a content
hash plus a public post locator — not a cryptographic proof (no signature or
trusted timestamp). Auth is a Bluesky **app password** + handle in service
headers (`X-Bluesky-Handle`, `X-Bluesky-App-Password`); Aegis owns the
credential and exchanges it for a session. Permissions: source needs
`photos` read, target needs `photos` write.

The connector also **syncs library metadata** (bytes stay on Bluesky):

- `GET /v1/library/photos` includes the user's Bluesky image posts
  (`sources[].blob_url` is the Aegis `getBlob` proxy — not a permanent CDN link).
- `GET /v1/library/messages` includes **only the user's own posts and
  replies** (`channel: "social"`, `direction: "outbound"`). Reposts, likes,
  follows, and other people's content are excluded.
- `GET /v1/library/contacts` includes the user's Bluesky **following list**
  as contacts (handle + display name + DID).
- Proxy reads: `GET /v1/services/bluesky/xrpc/com.atproto.sync.getBlob`,
  `app.bsky.feed.getAuthorFeed`, `app.bsky.graph.getFollows`.

#### Immich (local / foreign canonical)

Immich is a self-hosted photo library (open-source “Google Photos on your own
hardware”; quick start at `https://docs.immich.app/overview/quick-start/` —
Aegis ships a ready starter `.env` in `docs/immich-starter.env` so first-time
compose works without guessing values). Aegis treats it as a **foreign/local
canonical** source: metadata is listed through Aegis; **bytes stay on
Immich**. Auth is an API key in service headers — no OAuth app. Use Immich when
you want the photo archive on hardware you control; skip it if Google Photos is
enough.

```json
{
  "id": "immich",
  "name": "Immich",
  "action": "inject",
  "endpoint": "https://immich.your-tailnet.ts.net/api",
  "headers": { "X-API-Key": "$ENV:IMMICH_API_KEY" },
  "cache_ttl": "5m",
  "canonical_for": ["photos"],
  "grant_limits": { "default": "read", "photos": "read" }
}
```

`endpoint` must end in `/api`. Portable forms also work: same machine
`http://localhost:2283/api`, or MagicDNS + port
`http://photos.home.ts.net:2283/api`. For a **friendly tailnet HTTPS name**
(no `:2283`, real certs), host Immich as a Tailscale **VIP Service**
(`svc:immich`) — Admin → Services → Immich → Setup guide → **Tailnet URL**
emits the copyable walk-through (issue [#124](https://github.com/fudomunro/aegis/issues/124)).
Serve is tailnet-only; never Funnel.

**Tailscale Admin API automation (optional).** Paste a Tailscale access token
(Keys page; scopes `services` + `devices:core`) in the same wizard step. Aegis
stores it in `service_connections.json` (`tailscale.api_key`, mode 0600; never
echoed back), **checks each walkthrough step** against `api.tailscale.com`, and
offers a **Fix** action only for steps that still need work:

| Step | Check | Auto-fix |
|------|-------|----------|
| `acl_tag` | `tagOwners` has the host tag | yes — adds `tag → ["autogroup:admin"]` (prerequisite for the next three) |
| `create_service` | `svc:immich` exists | yes — `PUT …/services/svc:immich` |
| `tag_host` | selected host device carries the tag | yes — `POST /device/{id}/tags` (preserves existing tags) |
| `serve` | probes the VIP/Immich `/api/server/ping` | no (mapping is host-local; probe turns green when it answers) |
| `approve` | service host approved | yes — `POST …/device/{id}/approved` |
| `connect` | Aegis Immich endpoint | yes — saves the VIP `…/api` endpoint |

Apply is explicit (`POST /v1/admin/immich/tailnet/apply`) and re-checks first so
already-OK steps are skipped. `create_service` / `tag_host` / `approve` first
ensure the host tag is declared in `tagOwners` (Tailscale rejects undeclared
tags). Aegis never mutates tailnet topology from the pre-request hook.

Only **one** non-blocked service may claim `canonical_for: ["photos"]`. If
Immich is the home copy, remove that key from `google-photos` (or omit it).

The connector implements `PhotoProvider` via Immich `POST /search/metadata`
(with EXIF). Library photos get `sources[].canonical: true` and
`blob_url` / `thumbnail_url` pointing at the **Aegis proxy**
(`/v1/services/immich/assets/{id}/original`), so agents never need the Immich
API key. Proxy paths under `/assets/*` and `/search/metadata` are classified
as the `photos` permission.

```
GET /v1/library/photos
GET  /v1/services/immich/assets/{id}/original   # content transport only
GET  /v1/services/immich/assets/{id}/thumbnail
```

Asset search (`POST /search/metadata`) is not agent API — use
`/v1/library/photos`. This is the reference pattern for other local/foreign
stores: configure `endpoint` + static `headers`, implement a library provider,
mark `canonical_for`, and never cache content bytes in Aegis.

**Photo source discipline (issue #125):** pre-request injection for photo
questions always includes `/v1/library/photos` and a hard rule to use only
Aegis URLs — not Photos.app / `Photos Library.photoslibrary` / Photo Booth /
`~/Pictures`, not Immich at `:2283`, and not Immich Docker/Postgres. Keep
photo service `aliases` broad enough (`photo`, `picture`, `photos`) that
singular prompts still match.

**Admin UI setup (preferred for operators):** **Admin → Services → Immich —
local canonical photos**. Enter the Immich endpoint + API key, optionally
claim `canonical_for: ["photos"]`, then **Save to Aegis** / **Run checks**.
The setup guide includes a **Tailnet URL** step (VIP Service commands for a
stable `https://…ts.net` Immich name). Saves write `service_connections.json`
under the data directory (never rewrites `aegis.json`); the API key is
presence-only in admin APIs. Backing endpoints: `GET /v1/admin/immich/setup`,
`POST /v1/admin/immich/test`, `POST /v1/admin/immich/config`,
`POST /v1/admin/immich/check`.

Live acceptance checklist (issue #39) is documented in
[docs/immich-live-testing.md](docs/immich-live-testing.md).

### Calendar events as a data type

`CalendarEvent` is Aegis's neutral representation of a calendar entry
(meeting, appointment, all-day block). Service identity lives in `sources[]`,
same as playlists, contacts, and photos. **Aegis does not store calendar
content** — the authoritative copy stays on a non-Aegis service marked
`canonical` (issue #16). Initial provider: **Google Calendar**
([issue #65](https://github.com/fudomunro/aegis/issues/65)).

Neutral calendar-event fields (user-facing):

| Field | Description |
|-------|-------------|
| `title` | Event summary/title |
| `description` | Event notes/body, when present |
| `start_at` / `end_at` | RFC3339 times (all-day uses midnight UTC of that date) |
| `all_day` | Date-bounded events without a clock time |
| `location` | Free-form place, room, or meeting link |
| `status` | Provider status when known (`confirmed`, `tentative`, `cancelled`) |
| `calendar_id` / `calendar_name` | Service-local calendar this entry belongs to |
| `organizer` | Organizer as a `Contact` value |
| `attendees[]` | Invitees as `Contact` values; merged with contacts-library identity matching |
| `entity_id` | Durable id when present (Google `iCalUID`) |
| `match` | Cross-service identity: `ical_uid` or `title_start` (empty on single-source) |
| `sources[]` | Service-local copies: `service`, `id`, `calendar_id`, `url`, `status`, `canonical` |

Cross-service merge prefers **iCalUID** when a provider supplies it
(`match=ical_uid`); otherwise **title + start time + all-day flag**
(`match=title_start`). Two different meetings at the same time must not merge.

```
GET /v1/library/events
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's calendar events from every connected calendar-capable
service. Connectors opt in by implementing `CalendarEventProvider`
(`ListCalendarEvents`). Permission key: `events` (paths `/events/*`,
`/me/events`, `/calendars/*`, `/me/calendars`, and `/calendarList*` on the
service proxy).

```json
{
  "events": [
    {
      "title": "Standup",
      "description": "Daily team sync",
      "start_at": "2024-06-01T09:00:00Z",
      "end_at": "2024-06-01T09:30:00Z",
      "location": "Room 1",
      "status": "confirmed",
      "calendar_id": "primary",
      "calendar_name": "Work",
      "entity_id": "standup@google.com",
      "organizer": {
        "name": "Ada Lovelace",
        "emails": ["ada@example.com"]
      },
      "attendees": [
        {
          "name": "Grace",
          "emails": ["grace@example.com"]
        }
      ],
      "sources": [
        {
          "service": "google-calendar",
          "service_name": "Google Calendar",
          "id": "ev1",
          "calendar_id": "primary",
          "url": "https://calendar.google.com/event?eid=...",
          "canonical": true
        }
      ]
    }
  ],
  "total": 1,
  "synced": true,
  "by_service": {
    "google-calendar": { "status": "ok", "count": 1 }
  }
}
```

Organizer/attendee `Contact` values use the same identity matching as
`/v1/library/contacts` and `Photo.people[]` (email → phone → handle → name).

#### Google Calendar

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create or reuse an OAuth 2.0 Client ID (Web application)
3. Add `https://your-aegis/v1/auth/google-calendar/callback` as an authorized redirect URI
4. Enable the **Google Calendar API**
5. Copy credentials into `aegis.json` (see `aegis.example.json`) with scope
   `https://www.googleapis.com/auth/calendar.readonly`
6. Authorize at `/v1/auth/google-calendar/login` (shares the Google OAuth
   connection with YouTube Music, Photos, and Contacts; tokens are stored per
   service id)

The connector lists calendars via `GET /users/me/calendarList`, then events
via `GET /calendars/{id}/events` with `singleEvents=true` and
`orderBy=startTime`. The library window defaults to **30 days back + 90 days
ahead** so "my calendar" covers recent meetings and upcoming plans; proxy
calls may pass explicit `timeMin` / `timeMax`. Free/busy-only calendars are
skipped. A failure on one calendar is logged and skipped (fail-open) so other
calendars still appear.

Agent data is the unified library (issue #119). Calendar list/proxy paths are
not agent API.

```
GET /v1/library/events
GET /v1/library/freebusy
```

This phase is **read-only**. Capabilities do not advertise agent write ops
(create/update/delete events), so the admin permissions form will not offer
Editable for `events` and mutations stay blocked
([issue #61](https://github.com/fudomunro/aegis/issues/61)).
Event content stays on Google Calendar.

### Messages as a data type

`Message` is Aegis's neutral representation of a **text communication
between people** — either written by the user or written to the user by
another person. Typical sources are email (Gmail), SMS/WhatsApp when a named
connector exists, and social posts/replies the user authored or that form a
conversation with them.

**What is a message**

- Person-to-person correspondence: email, chat/SMS, replies addressed to the user, and (for social) writing the user themselves posted.
- One logical conversation may have several `sources[]` when the same item exists on more than one service.

**What is not a message**

- App or service **notifications** (likes, follows, shipping updates, marketing blasts, system alerts, digest mail).
- Automated receipts and transactional mail that nobody "wrote" to the user as a person.

Today some Gmail/Google Takeout mail still matches that notification shape
(newsletters, alerts, receipts). Connectors should prefer real correspondence
where they can tell the difference; do not add new notification-like items to
this type. Longer term, operators should be able to reclassify notification
items under a dedicated **Notifications** data type (not implemented yet)
rather than growing `messages`.

Service identity lives in `sources[]`, same as playlists, contacts, photos,
and calendar events. **Aegis does not store mailboxes or message bodies as
the home copy** — the authoritative content stays on a non-Aegis service
marked `canonical` (issue #16). Library listings carry subject/snippet
metadata; agents fetch full body through the service proxy.
Initial provider: **Gmail**
([issue #60](https://github.com/fudomunro/aegis/issues/60)); social posts and
replies via **Bluesky** (`channel: "social"`, `direction: "outbound"` for
authored items).

Neutral message fields (user-facing):

| Field | Description |
|-------|-------------|
| `channel` | `email`, `sms`, `whatsapp`, `social`, or `signal` |
| `subject` | Email subject or chat topic |
| `snippet` | Short service-supplied body preview |
| `body` | Full body when a detail proxy call includes it (usually empty in library lists) |
| `from` / `to[]` / `cc[]` | Participants as `Contact` values |
| `sent_at` / `received_at` | RFC3339 times |
| `direction` | `inbound`, `outbound`, or `unknown` |
| `read` | True when not marked unread on the source |
| `thread_id` | Service-local conversation id |
| `entity_id` | Durable id when present (RFC 5322 Message-ID) |
| `match` | Cross-service identity: `message_id` or `subject_from_time` |
| `labels[]` | Service-local labels (`INBOX`, `SENT`, `UNREAD`, …) |
| `sources[]` | Service-local copies: `service`, `id`, `thread_id`, `url`, `mailbox`, `canonical`, `copy` (issue #76) |

Cross-service merge prefers **RFC Message-ID** when a provider supplies it
(`match=message_id`); otherwise **subject + from + sent_at**
(`match=subject_from_time`). Replies that share a subject but different
Message-IDs or send times stay distinct.

**Gmail and Google Takeout are sources of one mail collection**
([issue #139](https://github.com/fudomunro/aegis/issues/139)). Live Gmail,
Gmail archive sync, and Takeout import all feed the same identity space:
either ingest order is safe, a Message-ID match attaches another `sources[]`
entry instead of creating a second message, and `/v1/library/messages` plus
`/v1/mail/search` present the merged collection.

```
GET /v1/library/messages
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's mail collection from every message-capable service and,
when the local mail archive is enabled, archive rows (including Takeout).
Connectors opt in by implementing `MessageProvider` (`ListMessages`).
Permission key: `messages` (paths `/messages/*`, `/me/messages`, `/threads/*`,
`/me/threads`, `/users/me/messages`, and `/gmail*` on the service proxy).

Participant `Contact` values use the same identity matching as
`/v1/library/contacts` and calendar attendees (email → phone → handle → name).

#### Gmail

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create or reuse an OAuth 2.0 Client ID (Web application)
3. Add `https://your-aegis/v1/auth/google-gmail/callback` as an authorized redirect URI
4. Enable the **Gmail API**
5. Copy credentials into `aegis.json` (see `aegis.example.json`) with scope
   `https://www.googleapis.com/auth/gmail.readonly`
6. Authorize at `/v1/auth/google-gmail/login` (shares the Google OAuth
   connection with YouTube Music, Photos, Contacts, and Calendar; tokens are
   stored per service id)

The connector lists messages via `GET /users/me/messages` (default query
`newer_than:30d`), then hydrates each id via
`GET /users/me/messages/{id}?format=metadata`. The library hydrates at most
**50 messages** per fetch so "my inbox" stays useful without unbounded detail
calls; remaining upstream items set `truncated`. Individual message fetch
failures are logged and skipped (fail-open).

Agent data is the unified library; content transport serves full bodies only
(issue #119):

```
GET /v1/library/messages
GET /v1/services/google-gmail/messages/{id}          # ?format=full includes decoded body
GET /v1/services/google-gmail/threads/{id}
```

Message lists (`/me/messages`) are not agent API — use the library.
This phase is **read-only**. Capabilities do not advertise agent write ops
(send/delete), so the admin permissions form will not offer Editable for
`messages` and mutations stay blocked
([issue #61](https://github.com/fudomunro/aegis/issues/61)).
Message bodies stay on Gmail as the **canonical** home copy.

#### Local mail archive (`/v1/mail`)

For full-history agent queries, Aegis can keep a **local compressed
archive** (SQLite FTS5 + gzip provider payloads). This is a query mirror, not
a new home copy — Gmail remains canonical for the mailbox (issue #16). The
archive is another **source of the user mail collection**: Gmail archive sync
and Google Takeout import both land here, merge on RFC Message-ID into
multi-source `sources[]`, and feed `/v1/library/messages` (issue #139).
Optimize for Aegis/agents: search and fetch by id without paging Gmail every turn.

```bash
./aegis ... -mail-archive mail-archive.db   # default under <data-dir>; empty disables
```

| Endpoint | Purpose |
|----------|---------|
| `GET /v1/mail/search` | FTS + filters: `q`, `from`/`to` (addresses), `after`/`before`, `label`, `limit`, `cursor` (or `offset`), `sort=asc\|desc`, `include_body` |
| `GET /v1/mail/messages/{service}/{id}` | Neutral `Message` with `body` from the archive |
| `GET /v1/mail/messages/{service}/{id}/raw` | Decompressed provider payload JSON |
| `GET /v1/mail/status` | Message counts, last sync time/error, page cursors |
| `POST /v1/mail/sync` | Ingest a bounded batch; body `{"service","max_messages","query","reset_cursor"}` |

Sync is **resumable**: each call stores up to `max_messages` (default 200) and
persists the Gmail page token. Repeat until `"done": true`. Default query is
`in:anywhere` so spam/trash are included. Permission key: `messages`.

The **Advanced** tab in `/v1/admin` exposes the same mechanism: archive counts,
per-service page cursors, **Sync mail** (runs batches until complete or the
client batch cap), and **Restart sync**. Admin routes
`GET /v1/admin/mail/status` and `POST /v1/admin/mail/sync` skip the agent
messages gate so the operator can maintain the archive even when agents are
restricted.

```bash
# First fill (continue until done)
curl -X POST "$AEGIS/v1/mail/sync" -H "Authorization: Bearer $KEY" \
  -d '{"max_messages":200}'

# Search history
curl "$AEGIS/v1/mail/search?q=invoice%20from:ada&after=2024-01-01&include_body=1" \
  -H "Authorization: Bearer $KEY"
```

`mail-archive.db` is local state (like `tokens.json`) and lives under the
[data directory](#data-directory-issue-74) — do not commit it.

##### Google Takeout (read-only sync location)

A historical [Google Takeout](https://takeout.google.com) mail export is a
**read-only sync location** alongside live Gmail — not a write target and not
the canonical home. Point Aegis at an `.mbox` file, a directory of mbox
files, or a Takeout `.zip`. Takeout and Gmail are **two sources of one
collection** (issue #139): re-import skips only copies this source already
stored; a Message-ID that Gmail already has **attaches** a `google-takeout`
source instead of skipping. Re-sync of the same export is cheap (by RFC
Message-ID per source) so you can run it as often as you like.

```
POST /v1/admin/mail/import
{"path": "/Users/you/Downloads/Takeout", "max_messages": 5000}
```

- `max_messages: 0` + `path` only saves the location
- Repeat with `max_messages > 0` until `"done": true` (already-present IDs from **this** source count as `skipped`)
- A durable byte cursor resumes mid-export so each batch continues instead of
  re-reading the whole mbox (issue #75). `"reset_cursor": true` restarts.
- Admin **Advanced → Google Takeout** exposes the same controls
- Messages are stored as service `google-takeout` with `TAKEOUT` + mbox labels

```bash
curl -X POST "$AEGIS/v1/admin/mail/import" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"path":"/Users/you/Downloads/Takeout","max_messages":5000}'
```

**SMS / WhatsApp:** the neutral `channel` field and library kind are ready,
but no connector is registered for them yet. Personal SMS history has no
simple OAuth surface; WhatsApp consumer history is not available through a
Aegis-compatible named API. Follow-ups can plug in connectors that
implement `MessageProvider` without changing the library contract.

#### Signal import (issue #168)

Signal has no public chat API. Contacts and messages arrive as a
**file-based import** (like mail Takeout / Quicken), not OAuth:

```bash
# Android: Settings → Chats → Chat backups → Create backup, then decrypt
# with signalbackup-tools to a plaintext database.sqlite
./aegis -import-signal /path/to/database.sqlite

# Or a JSONL export (one object per line):
#   {"type":"contact","id":"alice","name":"Alice","phones":["+15551234567"]}
#   {"type":"message","id":"m1","body":"hello","sent_at":"2024-01-01T12:00:00Z",
#    "direction":"inbound","from":{"name":"Alice","phones":["+15551234567"]}}
./aegis -import-signal ~/Downloads/signal-export.jsonl
```

Or use **Advanced → Signal import** (`/v1/admin/signal/import`).

| Endpoint | Purpose |
|----------|---------|
| `GET /v1/library/messages` | Imported Signal chats (source service `signal`) |
| `GET /v1/library/contacts` | Imported Signal contacts |
| `GET /v1/services/signal/import/status` | Contact + message counts |
| `POST /v1/services/signal/import` | `{"path": "/path/to/export"}` |

Signal list paths under `/v1/services/signal/messages` / `contacts` are not
agent API (issue #119) — use the library.

Accepted inputs: `.jsonl` / `.ndjson`, `.json` (bundle or array), and
plaintext `.db` / `.sqlite` dumps in either Signal **Desktop** schema
(`conversations` + `messages`) or Android / **signalbackup-tools** schema
(`recipient` + `message` + `thread`). Encrypted app databases are not
supported — decrypt or export first. Re-import skips rows already stored.
Attachments and media are never stored (text metadata only). The import
source is service `signal` with `channel: "signal"` and feeds both
`/v1/library/messages` and `/v1/library/contacts`.

A minimal JSONL sample lives at `examples/signal-export-sample.jsonl`.

### Transactions as a data type

```
GET /v1/library/transactions
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's financial transactions from every connected source.
Connectors opt in by implementing `TransactionProvider` (`ListUserTransactions`).
Permission key: `transactions` (paths `/budgets*`, `/accounts*`, `/transactions*`,
`/categories*`, `/payees*`, `/quicken*`, `/me/transactions` on the service proxy).

Query filters: `after`, `before`, `account`, `q`, `currency`.

Amounts are **signed decimal strings** (negative = outflow) so agents never do
float money math on JSON numbers. Account fields are coarse references only
(name, type, last 4) — never full account or card numbers.

Cross-service merge:

1. Durable provider IDs first (YNAB `import_id`, Quicken FITID) → `match: "provider_id"`
2. Otherwise amount + posted day + normalized merchant → `match: "amount_date_merchant"`
3. An adjacent-day (±1) pass catches the same card charge visible through two
   providers under different local IDs

`summary` always includes `summary` rollups (`total_amount`, `by_category`,
`by_month`). When the permission is `summary` (issue #141), `transactions[]`
is omitted entirely.

Example:

```json
{
  "transactions": [
    {
      "posted_at": "2024-03-05",
      "amount": "-45.67",
      "currency": "USD",
      "description": "AMAZON.COM",
      "merchant": "Amazon",
      "category": ["Shopping"],
      "status": "posted",
      "account": {"name": "Everyday Checking", "type": "checking", "last4": "4421"},
      "entity_id": "AQB-20240305-0001",
      "sources": [{"service": "ynab", "service_name": "YNAB", "id": "ynab-tx-1"}]
    }
  ],
  "total": 1,
  "summary": {
    "count": 1,
    "currency": "USD",
    "total_amount": "-45.67",
    "by_category": {"Shopping": "-45.67"},
    "by_month": {"2024-03": "-45.67"}
  }
}
```

`category` is a **list** so a YNAB split can name every category on the line
item without splitting the amount. A Costco run tagged Groceries + Household
Goods becomes one transaction, `amount: "-200.00"`, `category: ["Groceries",
"Household Goods"]`. Summary `by_category` credits the full amount to each
listed category (so the transaction shows up in grocery and household views);
`total_amount` stays the true net. For exact per-category dollars, dig into
YNAB — Aegis does not invent split amounts.

#### YNAB

Live budget data from the [YNAB API](https://api.ynab.com). Auth is a Personal
Access Token on the service (or a stored OAuth token):

```json
{
  "id": "ynab",
  "name": "YNAB",
  "endpoint": "https://api.ynab.com/v1",
  "headers": {"Authorization": "Bearer <your-ynab-personal-access-token>"},
  "grant_limits": {"transactions": "read"}
}
```

The connector lists budgets → accounts → transactions and maps YNAB milliunits
to signed decimal strings (`-45670` → `"-45.67"`). Payee becomes `merchant`;
parent + split category names become `category[]` (YNAB's placeholder
`"Split"` is dropped). Splits contribute **categories only** — the parent
amount is not redistributed. `import_id` becomes `entity_id` when present.
Read-only — no budget mutations are advertised.

Agent data is the unified library (issue #119):

```
GET /v1/library/transactions
```

YNAB budget/account/transaction proxy paths are not agent API.

#### Quicken files (QIF / QFX / OFX)

Historical Quicken exports are a **read-only import location** — parse once into
the local store, then agents query them like any other transaction source.
Statements and PDFs are never stored (issue #141 / product rule: Aegis does not
store media).

```bash
./aegis -import-quicken /path/to/export.qif     # or a directory of .qif/.qfx/.ofx
# or keep the store next to other local state:
./aegis ... -quicken-store "$DATA_DIR/quicken.db"
```

| Endpoint | Purpose |
|----------|---------|
| `GET /v1/library/transactions` | Neutral transactions from the import store (source `quicken`) |
| `GET /v1/services/quicken/import/status` | Row count |
| `POST /v1/services/quicken/import` | `{"path": "/path/to/file-or-dir"}` |

Quicken list paths under `/v1/services/quicken/transactions` are not agent API
(issue #119) — use the library.

Re-import skips rows already stored (by FITID / stable hash). CLI:

```bash
./aegis -quicken-store quicken.db -import-quicken ~/Downloads/Quicken
```

FITIDs become `entity_id` so the same bank import shows up once when YNAB also
has it (or when the same export is imported twice under different filenames).

**Plaid / SimpleFIN / Actual Budget:** not registered yet. The
`TransactionProvider` interface is the plug-in point (issue #141).

### Location history (places, issue #142)

`GET /v1/library/places` returns **visits and trips** (time windows + place),
not a firehose of GPS points. Query filters: `after`, `before`, `locality`,
`kind` (`visit` | `trip`).

```json
{
  "visits": [{
    "start_at": "2023-07-04T16:00:00Z",
    "end_at": "2023-07-04T21:00:00Z",
    "place": {"name": "Halifax Harbour", "locality": "Halifax", "country": "Canada"},
    "kind": "visit",
    "confidence": "high",
    "sources": [{"service": "google-timeline", "service_name": "Google Timeline", "id": "tl-1"}]
  }],
  "total": 1
}
```

**Data-grant ladder (default `none` — opt-in).** See [Data grants](#data-grants).
Pre-request injection never advertises `places` until a service permission
names `city`, `read`, or `precise`. Coordinates require `precise`.

#### Google Maps Timeline import

Timeline moved on-device in 2024, so this is a **local import** (like mail
Takeout), not a live API. Export Location History from Google Takeout, then:

```bash
./aegis -import-timeline ~/Downloads/Takeout/Location\ History
# or a monthly Semantic Location History JSON / Takeout zip
```

Or use **Advanced → Timeline import** (`/v1/admin/timeline/import`). Semantic
`placeVisit` rows become visits; `activitySegment` rows become trips with a
normalized `mode` (flight/drive/walk/…). Re-import skips stored rows.

#### OwnTracks

Live, local-first location history that fits the Immich/self-hosted audience.
Configure a service named `owntracks` with `endpoint` pointing at an OwnTracks
Recorder:

```json
{
  "id": "owntracks",
  "name": "OwnTracks",
  "endpoint": "https://recorder.example:8083",
  "headers": {"X-API-Key": "…"},
  "grant_limits": {"places": "read"}
}
```

The connector prefers Recorder `/api/v0/visit` rows and otherwise clusters
`/api/v0/locations` samples into dwell visits (and short trips between them).

#### Photos ↔ places join

Photos join to visits by `taken_at` inside the visit window
(`PhotosTakenDuring` in `places.go`). Sample fixtures include a harbour
evening visit with a fireworks photo taken during it — see
`TestPhotosPlacesJoinDemo`. Aegis does not store photo bytes; the join is
metadata only.

### Documents as a data type (issue #143)

`GET /v1/library/documents` returns life-admin paperwork — receipts,
warranties, leases, insurance policies, tax letters, medical letters — as
neutral **Document** metadata. Files stay on the source service (Paperless-ngx
and Google Docs/Drive; Nextcloud remains a candidate). Aegis never stores
document bytes and never runs OCR.

```json
{
  "documents": [{
    "title": "Apartment lease 2024",
    "mime_type": "application/pdf",
    "created_at": "2024-01-15T12:00:00Z",
    "modified_at": "2024-01-16T09:00:00Z",
    "correspondent": "Harbour Property Management",
    "document_type": "lease",
    "tags": ["housing", "legal"],
    "excerpt": "This lease agreement is entered into…",
    "people": [{"name": "Harbour Property Management"}],
    "entity_id": "sha256:…",
    "sources": [{
      "service": "paperless",
      "service_name": "Paperless-ngx",
      "id": "11",
      "url": "https://paperless.example.com/documents/11/details",
      "canonical": true,
      "blob_url": "https://paperless.example.com/api/documents/11/download"
    }]
  }],
  "total": 1
}
```

Query filters: `q`, `tag`, `after`, `before`.

**Metadata vs text (issue #143).** List rows never carry full text.

| Access | List | Text endpoint |
|--------|------|---------------|
| `metadata` | title, dates, tags, correspondent, document_type, people[], sources[] — **no excerpt** | denied |
| `read` / `read-write` | same + short `excerpt` | `GET /v1/library/documents/{service}/{id}/text` |

The text endpoint is a separate gate so `metadata` (or “read the list”) never
implies reading content. Prefer `canonical` / `blob_url` on `sources[]` for
the real file. Out of scope: upload/edit, Aegis-side OCR or file storage, and
per-tag allow/deny (design spike).

#### Paperless-ngx

Self-hosted document store that already OCRs, tags, and classifies — a natural
fit for the Immich audience. Configure a service named `paperless` with the
API base (`…/api`) and a token:

```json
{
  "id": "paperless",
  "name": "Paperless-ngx",
  "endpoint": "http://192.168.1.10:8000/api",
  "headers": {"Authorization": "Token …"},
  "canonical_for": ["documents"],
  "grant_limits": {"documents": "read"}
}
```

Use `documents: "metadata"` in a data grant (or `grant_limits`) to let agents
browse titles/tags without excerpts or full text.

#### CalDAV / CardDAV (Nextcloud, iCloud, Fastmail)

One adapter family for contacts and calendars on open standards (issue #146).
`dav-contacts` implements `ContactProvider` (vCard 3/4 → `Contact`);
`dav-calendar` implements `CalendarEventProvider` (iCalendar `VEVENT` →
`CalendarEvent`, with RRULE expansion in the requested window). Auth is HTTP
Basic username + app-specific password in static headers (Immich-style
credentials). Discovery walks `.well-known/carddav` / `caldav`. Aegis never
stores address-book or calendar content.

```json
{
  "id": "dav-contacts",
  "name": "DAV Contacts",
  "endpoint": "https://cloud.example.com",
  "headers": {
    "X-DAV-Username": "alice",
    "X-DAV-Password": "$ENV:DAV_APP_PASSWORD"
  },
  "canonical_for": ["contacts"],
  "grant_limits": {"contacts": "read"}
}
```

```json
{
  "id": "dav-calendar",
  "name": "DAV Calendar",
  "endpoint": "https://cloud.example.com",
  "headers": {
    "X-DAV-Username": "alice",
    "X-DAV-Password": "$ENV:DAV_APP_PASSWORD"
  },
  "canonical_for": ["events"],
  "grant_limits": {"events": "read"}
}
```

When Nextcloud (or another DAV server) is the home copy, claim
`canonical_for: ["contacts"]` / `["events"]` here and remove those keys from
Google. Mirror with `POST /v1/library/mirror` (`kind`: `contacts` or
`events`): writes use `If-Match` / etags so a newer remote edit is never
overwritten — conflicts are reported in `conflicts[]`. Per-item etag
watermarks land in the copy ledger for incremental sync. Mirrored CalDAV
events feed `GET /v1/library/freebusy` (issue #128).

**iCloud** needs an app-specific password (appleid.apple.com → App-Specific
Passwords); endpoint is `https://contacts.icloud.com` /
`https://caldav.icloud.com`. Best-effort in this phase — see
[`docs/caldav-carddav.md`](docs/caldav-carddav.md) for Nextcloud/iCloud setup
notes and the canonical-for example.

#### Google Docs / Google Drive

Google Docs are documents too. The `google-drive` connector lists native
Google Docs (`application/vnd.google-apps.document`) via Drive `files.list`
and pulls content with `files.export` as **HTML or plain text** — markdown
conversion is deferred. Files stay on Drive (non-Aegis canonical).

```json
{
  "id": "google-drive",
  "name": "Google Drive",
  "endpoint": "https://www.googleapis.com/drive/v3",
  "connection": "google",
  "oauth_scopes": ["https://www.googleapis.com/auth/drive.readonly"],
  "grant_limits": {"documents": "read"}
}
```

Shares the Google connection with Contacts/Calendar/Gmail/Photos.
`drive.readonly` covers both listing and export (a narrower content scope can
replace it later). Watch OAuth app verification limits, same class as Photos.

| What | Format |
|------|--------|
| `GET /v1/library/documents/{service}/{id}/text` | `text/plain` export |
| `GET /v1/services/google-drive/files/{id}/export?mimeType=text/html` | HTML (structure-preserving intermediate for a later markdown pass) |

### Content & ingest transport (`/v1/services/{id}/...`)

```
GET/POST /v1/services/{service_id}/{path}
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

After issue #119 this is **not** a general service API. Library (`/v1/library/{kind}`) is the only data surface. `/v1/services` stays open only for user-data content the library cannot include or for ingesting a local export:

| Allowed | Why |
|---------|-----|
| `/assets/{id}/original`, `/assets/{id}/thumbnail`, Bluesky `getBlob` | Photo/asset bytes (Aegis does not store media) |
| `/messages/{id}`, `/threads/{id}` | Full message bodies (library lists carry snippets) |
| `/playlists/{id}`, `/playlists/{id}/items` | Playlist items (library entries carry `track_count` only) |
| `/documents/{id}/download`, `/documents/{id}/preview` | Document file bytes (library lists carry metadata + short excerpt) |
| `/files/{id}/export` | Google Docs HTML/plain-text export (library text endpoint is plain text) |
| `/picker/*` | Google Photos user pick → library |
| `/import`, `/import/status` | Local export ingest (Signal, Quicken) |

Provider-shaped lists (`/me/...`), playback, catalog search, profiles, capabilities, and generic OAuth pass-through return **404**. Callers authenticate to Aegis; they never see upstream OAuth tokens.
- `X-Aegis-Cache: hit` or `miss` on connector content GETs
- GET responses cached per-service; POST not cached

### Auth endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/auth/{id}/login` | GET | Start OAuth flow |
| `/v1/auth/{id}/callback` | GET | OAuth callback (redirect target) |
| `/v1/auth/status` | GET | Token status for all services |
| `/v1/auth/{id}` | DELETE | Revoke stored token |

### Admin dashboard

All `/v1/admin` routes — including the HTML shell — require the same auth as
other protected APIs (issue #98). The shell is markup-only (no secrets or live
data), but is not advertised to unauthenticated peers. OAuth callback pages
under `/v1/auth/` remain public so browsers can finish the redirect.

```
GET /v1/admin              — HTML dashboard (shell only; no secrets; auth required)
GET /v1/admin/summary      — redacted services, token status, cache stats
GET /v1/admin/flow         — data-flow topology (nodes, edges, libraries) + per-agent access detail (issue #114)
GET /v1/admin/hook-health  — Aperture hook health + expected config snippet
POST /v1/admin/hook-health/check — validate a pasted Aperture policy/hooks JSON
GET /v1/admin/cache        — in-memory cache inventory
POST /v1/admin/cache/clear — clear the response cache
GET /v1/admin/library/{kind}  — admin Library tab preview (issue #62; not counted as agent Home access)
GET /v1/admin/permissions    — type-centric data grants editor model (issue #160): agent level + relevant apps/sync per data type
GET /v1/admin/data-grants  — agent data-grant map (primary, issue #148)
PUT /v1/admin/data-grants  — replace data grants {"types":{"tracks":{"default":"read","providers":{"spotify":"none"}}}}
GET /v1/admin/services/{id}/grant-limits  — service override map + effective_permissions + available/writable types
PUT /v1/admin/services/{id}/grant-limits  — replace the service grant_limits (tighten-only vs data grants; writes data_grants.json overlay, not aegis.json)
GET /v1/admin/immich/setup   — Immich connection status + issue #39 checks
POST /v1/admin/immich/test   — probe Immich endpoint/API key (key not stored from this call)
POST /v1/admin/immich/config — save Immich endpoint/API key/canonical claim (service_connections.json)
POST /v1/admin/immich/check  — re-run the live acceptance checklist
POST /v1/admin/immich/tailnet/config — save Tailscale Admin API key/host (service_connections.json; key never echoed)
POST /v1/admin/immich/tailnet/check  — live-check VIP Service setup steps (issue #124)
POST /v1/admin/immich/tailnet/apply  — fix only the named steps that still need work
```

Public brand assets (no auth; issue #87) are served from the process root:

```
GET /favicon.ico, /favicon.svg, /favicon-16.png, /favicon-32.png
GET /apple-touch-icon.png
GET /logo.svg
```

`/` loads in the browser and fetches the JSON APIs. On a tailnet with
Serve identity the page works with no extra setup. In API-key mode, enter the
key when prompted (kept only in that tab’s `sessionStorage`). Friendly SPA
paths (issue #150) are durable: `https://your-aegis/`, `/library`, `/services`,
and `/advanced` each open that section and survive a refresh. Legacy
`/v1/admin` still serves the same shell.

Tabs: **Home** (default, `/`), **Services** (`/services`), **Library**
(`/library`), and **Advanced** (`/advanced`).
Connection management lives on **Services** — Connect/Reconnect, permissions,
and empty-state help when nothing is authorized yet. There is no separate
setup wizard. Aperture setup and hook health live under **Advanced**.

The **Library** tab loads datasets via `/v1/admin/library/{kind}` so browsing
the admin UI does not inflate Home access counts. Agents still use
`/v1/library/{kind}`; those requests are the ones the Home map counts.

The **Home** tab (default) is a live SVG overview of how data moves. Aperture
is drawn as a container around Aegis; Aegis nests the token vault and
response cache and lists every data type available on this instance (unified
library kinds plus proxy-only types). Agents connect via the hook and
proxy/library APIs; edges show OAuth reads, library fan-in, and blocked
services. Service nodes are colored by status (connected, needs OAuth, expired,
blocked). Photo services note that bytes stay upstream. Use it as a quick
“what is this thing doing?” picture; the Services tab remains the place to
edit permissions.

The **Advanced** tab consolidates operational detail: Aperture setup and
hook health, unified library dataset cards, recent access
samples, the response cache inventory (with clear), and runtime/auth endpoint
info.

On the **Services** tab, shared **Data grants by type** instructions sit above
the filter. The data-grant editor lists each data type with its **agent**
level (editable) and any other accessors that apply to that type (third-party
apps, sync pipelines) as read-only chips — only when relevant to the type
(issue #160). Agent form controls (Available / Off always; **Editable** only
when agents can truly
change that data through Aegis — not mirror backups or picker sessions).
Each service card keeps its own live permissions form under the title; every
change saves immediately to `data_grants.json` (the admin overlay). Bootstrap
defaults remain in `aegis.json`; the overlay wins for any service the admin
UI has saved. Keys not shown in the form are preserved on save. Data-type rows
use the Home map icons.

Each service card leads with **name** on the title line and **connection
status** on the same line to the right — badge, time left,
Connect/Reconnect, and **Pick photos…** (when supported) sit inline with no
line breaks between them. **Connect** / **Reconnect** appear only when the
service is not connected. Everything else (data flow rows, description, OAuth
config, cache TTL, paths) is collapsed under an **Advanced** disclosure.

Photo connectors that expose a picker session create operation (Google Photos)
also show **Pick photos…** on the service card: it creates a session, opens
Google’s picker in a new tab, polls until the user finishes selecting, then
refreshes the photos library view. (Session create is a POST — the service’s
`photos` permission must allow write.)

Admin APIs use the same auth as the rest of Aegis. Secrets never appear in
admin responses: client secrets, access tokens, and refresh tokens are omitted;
only presence flags, scopes, and expiry status are shown.

## Development

Adding or maturing a unified library data type? Follow
[docs/adding-data-types.md](docs/adding-data-types.md) — the checklist for
neutral models, provider interfaces, merge identity, locations/copies,
permissions, and sample fixtures (issue #92).

```bash
# Run tests
go test ./...

# Run with verbose output
go test -v ./...

# Race + coverage profile (matches CI)
go test ./... -count=1 -race -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -n 1
go tool cover -func=coverage.out | sort -t$'\t' -k3 -n | head -n 20

# Build
go build -o aegis .

# Run with text logging for development
./aegis \
  -addr :8080 \
  -base-url http://localhost:8080 \
  -api-key dev-key \
  -token-pass dev-pass \
  -log-level debug \
  -log-format text
```

CI fails if statement coverage drops below the floor in `.github/workflows/ci.yml`
(currently 80%). Raise the floor when the suite grows; do not lower it to land a
change that only adds untested write paths.

### Sample data (issue #77)

Synthetic fixtures cover every neutral library kind plus the mail archive so
search, sort, filters, merge, and agent-facing endpoints can be exercised
without OAuth, provider quotas, or real personal mail. All generated data is
fictional (`example.com` addresses, invented titles) and safe to regenerate.

```bash
# Small CI-sized set (default 24 mail rows + edge cases) into the data dir
./aegis -seed-sample

# Larger stress set for local ingest/search benchmarks (issue #75)
./aegis -seed-sample -seed-sample-stress

# Explicit row count
./aegis -seed-sample -seed-sample-count 500
```

`-seed-sample` writes under `<data-dir>/sample/` and loads the mail set into
the configured mail archive (`mail-archive.db`), then exits:

| File | Purpose |
|------|---------|
| `mail-archive.db` | Queryable archive for `/v1/mail/search` |
| `sample-mail.mbox` | Same messages as a Takeout-shaped mbox (importer round-trip) |
| `sample-library.json` | Neutral `Playlist` / `Track` / `Contact` / … fixtures for mock upstreams |

The generator lives in `sampledata.go` (`GenerateSampleMail`,
`GenerateSampleLibrary`). Tests load it via `SeedMailArchive` /
`SeedSampleArchive` and run the query checklist in `sampledata_query_test.go`:

- oldest / newest (`sort=asc|desc`) — dateless rows sort last so they cannot win
- date window (`after` / `before`)
- from / to / label filters
- FTS `q=` multi-word and hostile operator strings
- pagination (`limit` / `offset`) with stable order
- fetch by id + `raw`
- library merge (`sources[]`, `entity_id`, `match`) for ISRC tracks, contact
  identity chains, and Message-ID / subject+from+time messages
- permission-denied services and `truncated` page caps

Run the checklist alone:

```bash
go test -count=1 -run 'TestMailQuery|TestLibraryMerge|TestLibraryAggregates|TestGenerateSample' .
```

CI uses only the small set. Do not commit large binary fixtures — generate
stress data locally with `-seed-sample-stress`.

### Project structure

```
aegis/
  main.go                    Thin entry (core.Run())
  internal/types/            Neutral models, merge/identity, LibraryKind data,
                             DataGrant, path rules, provider interfaces
  internal/core/             Runtime: Server, auth, config, tokens/OAuth, proxy,
                             permissions enforcement, library aggregate, admin,
                             state stores (mail, copy ledger, Quicken, Timeline),
                             sample data, web/ embeds
  internal/connectors/       Named services (Spotify, YouTube Music, Amazon Music,
                             Google *, Immich, YNAB, OwnTracks, sample calendar)
                             implementing ServiceConnector + *Provider via Host
  web/ → internal/core/web/  Admin SPA + brand assets (//go:embed)
  SECURITY.md                Tailnet auth model, ACLs, operational guidance
  aegis.example.json         Example configuration
  experiments/index.html     Code walkthrough (experimental; regenerate via experiments/generate_walkthrough.py)
  docs/adding-data-types.md  Guide: defining a new library data type (issue #92)
```

Layout is the #15 split: **types** (data) / **core** (runtime) / **connectors**
(named services). See `docs/restructure-core-types-connectors.md`.

## License

Private repository — internal use only.
