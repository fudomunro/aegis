# Configuration

Aegis is configured via a JSON file. See [`aegis.example.json`](../aegis.example.json) for the full format.

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
| `aliases` | Additional names to match (case-insensitive substring). Photo services should include singular forms (`photo`, `picture`) as well as plurals so questions like “oldest photo in my collection” match. |
| `action` | `inject` (redirect to proxy) or `block` (reject requests) |
| `endpoint` | Upstream API base URL |
| `headers` | Static headers to add to upstream requests (optional) |
| `cache_ttl` | How long to cache GET responses (e.g. `5m`, `1h`) |
| `grant_limits` | Optional map of data type → access level for agents (migration override; can only tighten data grant — see [Data grants](configuration.md)) |
| `canonical_for` | Optional list of library kinds where this service is the authoritative non-Aegis home (see [Canonical sources](configuration.md)) |
| `connection` | Name of a shared OAuth provider connection (required for OAuth-enabled surfaces) |
| `oauth_scopes` | Scopes this surface requires from its connection; missing scopes degrade that surface to `unauthorized` |

### Pre-request matching skips

The pre-request hook matches service names and library questions as
case-insensitive substrings of Aperture's `user_message`. That is correct for
consumer questions ("play jazz on Spotify") but wrong for software development
that mentions those names as identifiers (connectors, tests, docs). Aegis must
not redirect agents that are developing Aegis itself — or any other codebase —
when matching language shows up as code.

Matching is skipped (request allowed unchanged) when the user message looks
like development work:

- fenced code blocks (```)
- path-like tokens ending in a source/build extension (`.go`, `.ts`, `.py`, …)
- Aegis repo identifiers (`AGENTS.md`, `internal/core`, `LibraryKind`,
  `go.mod`, …)

Operators can widen the net with `hook.skip_patterns`. Each entry is a
**case-insensitive regular expression**; if any matches the user message, all
service and library matching is skipped:

```json
{
  "hook": {
    "skip_patterns": ["my-other-repo", "feat/issue-", "worktree"]
  }
}
```

Invalid patterns fail config load (same as a bad service section). Skip is
fail-open to Aperture: the hook still returns `{"action":"allow"}`. Hook-health
observations record `action: "skip"` with `skip_reason` (`development` or
`skip_pattern`) so you can see when this fires.

Heuristics stay conservative on purpose — ordinary product questions do not
hit them. If a real request is still intercepted during development work, add
a `skip_patterns` entry rather than weakening consumer matching.

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

**Secret indirection.** `client_secret` and service `headers`
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

Prefer the glossary terms here ([`docs/glossary.md`](glossary.md)):
**data grant** is the product idea; **data grants map** is where **agent** data
grants live; **Service limits** can only tighten that default;
**app data grants** scope third-party tokens. JSON/config keys stay
`data_grants`, `grant_limits`, `data_grants.json`.

Data grants live with **data types**, not service providers. The
primary model is a per-data grant: a type-level default plus optional
per-provider overrides that can only **tighten** that default. Service
`permissions` maps remain as migration overrides and can only further
restrict what a data grant allows.

**Accessors.** Data is not only reached by LLM agents. Aegis also
runs sync pipelines (mail archive, copy ledger, timeline/quicken import) and
third-party apps hold scoped tokens. The data grants editor is **type-centric**:
each data type shows the agent data grant (editable) plus only the other
accessors that apply to that type (read-only). `GET /v1/admin/permissions`
returns that same model. The map on Home answers the other half of confidence:
what each has *actually* accessed. Multi-user principals will
appear as additional accessor rows.

| Accessor kind | Where levels come from | In the type editor |
|---------------|------------------------|--------------------|
| `agent` | type-owned policy (`data_grants` / `data_grants.json`) | editable — admin “Data grants” / `PUT /v1/admin/data-grants` |
| `app` | app `scopes` on the third-party record (fail closed) | read-only (edit via `PATCH /v1/admin/apps/{id}`); shown when the type is named or the level is not `none` |
| `sync` | fixed by the pipeline (what Aegis may store while archiving/mirroring) | read-only; shown only for types the pipeline stores |
| `service` | configured connector that syncs that library type (e.g. Spotify, YouTube Music under `playlists`) | read-only for now |

Primary (aegis.json `data_grants` or data_grants.json `data_grants`, or admin “Data grants”):

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

Service limits (still accepted; tighten-only once a data grant
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
| `freebusy` | `GET`, `HEAD` on `events`, but responses reduced to availability only |
| `summary` | `GET`, `HEAD` on `transactions`, but responses reduced to totals by category/month only — no line items |
| `city` | `GET`, `HEAD` on `places`, reduced to locality, country, and the visit/trip time window |
| `precise` | `GET`, `HEAD` on `places` with coordinates |
| `metadata` | `GET`, `HEAD` on `documents`, reduced to titles, dates, tags, correspondents, and sources — no excerpt or text |
| `read` | `GET`, `HEAD` |
| `write` | `POST`, `PUT`, `PATCH`, `DELETE` |
| `read-write` | both (also accepts `full` / `rw`) |

**Resolution order**:

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
- **`freebusy` is events-only**. Scheduling apps can see when you
 are busy without reading titles, attendees, locations, or descriptions.
 Responses keep only `start_at`, `end_at`, `all_day`, and `status`. Prefer
 `GET /v1/library/freebusy` for availability; `/v1/library/events` and the
 calendar proxy apply the same redaction when events access is `freebusy`.
 The level is rejected on other data types and on `default`.
- **`summary` is transactions-only**. Apps can see how much was
 spent by category or month without reading line items or account names.
 Responses keep only `summary` totals (`count`, `total_amount`, `by_category`,
 `by_month`). Prefer `GET /v1/library/transactions` with `summary` access for
 budget dashboards; `read` adds line items with merchant but still strips
 account identifiers. The level is rejected on other data types and on
 `default`.
- **`places` is opt-in and uses a city / read / precise ladder**.
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
- **`metadata` is documents-only**. Life-admin paperwork
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
 unmapped endpoint.
- Proxy paths are **canonicalized before classification**. Duplicate slashes
 (including `%2f`) collapse to the real type; `.` / `..` segments are rejected
 with `400`. Classification and upstream routing always see the same path.
- If neither data grant nor service `permissions` is set, agents keep
 unrestricted access (previous behavior).
- **Bootstrap vs overlay.** `aegis.json` may set bootstrap
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

### Canonical sources (non-Aegis)

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
[POST /v1/library/mirror](configuration.md) for a
deliberate copy to a secondary service.

#### Mirror / backup sync

To keep a **backup** of a playlist or liked-tracks list on another service:

```
POST /v1/library/mirror
Authorization: Bearer <api-key> # or tailnet identity
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
Cross-service track matching is **name-based** — expect skips and
review them before treating a mirror as a complete backup. A single call
copies at most 200 tracks (`truncated: true` when more remain).

Requires **write** permission on the target for that kind and **read** on the
source. Aegis stores no media; it reads source metadata and writes through
the target's API using the stored OAuth token.

Immich is supported as a **local/foreign canonical photo source** (see
[Immich](connect/immich.md)). Additional foreign-DB /
professional-data connectors remain follow-up work on
[](https://github.com/fudomunro/aegis/issues/16).

### Copy ledger

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

**Sync fidelity is per kind**:

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

**Admin UI** surfaces the same fields read-only: library cards
show a per-item copy badge (“2 copies · 1 stale”), source chips/detail show
`role` / `sync_mode` / `as_of` / `checked_at` / `sync_status`, the Library
tab filters by copy status (in sync / stale / missing / unchecked), and
Advanced → Copy ledger lists `GET /v1/library/copies` rows with the same
filters.

### Environment variables

| Variable | Flag equivalent | Description |
|----------|----------------|-------------|
| `AEGIS_API_KEY` | `-api-key` | Agent API key for library/hook/content calls (optional in `auto`/`tailnet` modes; never admin) |
| `AEGIS_ADMIN_API_KEY` | `-admin-api-key` | Admin API key for admin surfaces; distinct from `AEGIS_API_KEY` |
| `AEGIS_AUTH_MODE` | `-auth-mode` | `auto` (default), `tailnet`, or `api-key` |
| `AEGIS_ALLOWED_LOGINS` | `-allowed-logins` | Comma-separated Tailscale logins allowed to call the API |
| `AEGIS_ADMIN_LOGINS` | `-admin-logins` | Comma-separated Tailscale logins allowed to use admin surfaces |
| `AEGIS_BASE_URL` | `-base-url` | Public URL of this Aegis instance |
| `AEGIS_APERTURE_URL` | `-aperture-url` | Optional public URL of the Aperture gateway (admin live links) |
| `AEGIS_TOKEN_PASS` | `-token-pass` | Passphrase for token encryption |
| `AEGIS_DATA_DIR` | `-data-dir` | Data directory for local state (see [Data directory](configuration.md)) |

### Data directory

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
 aegis.json # bootstrap config (OAuth clients live here)
 data_grants.json # admin data-grant overlay (non-secret)
 apps.json # third-party app registry (0600; token hashes only)
 data_scopes.json # principal → dataset registry (0600; holds API keys)
 service_connections.json # admin Immich/static-auth + Tailscale Admin overlay (non-secret fields; secrets in tokens.json)
 user_types.json # admin UDT local-file selection overlay
 user-types/ # user-defined type schema packs
 entity_links.json # confirmed playlist/entity links
 tokens.json # encrypted OAuth tokens + static credentials (0600;)
 copy_ledger.db # SQLite copy ledger (; empty -copy-ledger disables)
 mail-archive.db # SQLite+FTS mail archive (omit -mail-archive to disable)
 quicken.db # Quicken QIF/QFX import store (; empty -quicken-store disables)
 timeline.db # Google Timeline import store (; empty -timeline-store disables)
 signal.db # Signal contacts/messages import store (; empty -signal-store disables)
 audit_log.db # durable access audit trail (; empty -audit-log disables)
 logs/aegis.log # process log (also mirrored to stderr)
```

Every path above is the default **when the corresponding flag is omitted**.
An explicit flag always wins:

| Flag | Default under `-data-dir` | Notes |
|------|---------------------------|-------|
| `-config` | `aegis.json` | Bootstrap config (services, connections, secrets) |
| `-data-grants-file` | `data_grants.json` | Admin data-grant overlay; |
| `-service-connections-file` | `service_connections.json` | Admin Immich/static-auth overlay; secrets live in the encrypted token store |
| `-token-file` | `tokens.json` | Encrypted token + static-credential storage |
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
| `-auth-mode` | `auto` | `auto`, `tailnet`, or `api-key` — see [SECURITY.md](../SECURITY.md) |
| `-api-key` | | Agent API key (library/hook/content; never admin). Required only if `auth-mode=api-key` and no admin key |
| `-admin-api-key` | | Admin API key. Required for key-based admin; the agent key never opens admin surfaces |
| `-allowed-logins` | | Optional allowlist of Tailscale logins |
| `-admin-logins` | | Optional admin allowlist. Empty: allowed user logins are admin; tagged devices are not |
| `-ts-socket` | auto-detect | tailscaled LocalAPI socket for WhoIs |
| `-data-dir` | OS app data dir | Local state root (see [Data directory](configuration.md)) |
| `-config` | `<data-dir>/aegis.json` | Bootstrap config path (services, connections, secrets) |
| `-data-grants-file` | `<data-dir>/data_grants.json` | Admin data-grant overlay (non-secret). All data-grant saves write here; `aegis.json` is never rewritten |
| `-token-file` | `<data-dir>/tokens.json` | Encrypted token + static-credential storage path |
| `-service-connections-file` | `<data-dir>/service_connections.json` | Admin service-connections overlay path (non-secret fields; secrets in the token store) |
| `-link-file` | `<data-dir>/entity_links.json` | Entity link store path |
| `-copy-ledger` | `<data-dir>/copy_ledger.db` | Copy ledger path; empty disables |
| `-mail-archive` | `<data-dir>/mail-archive.db` | Mail archive path; empty disables `/v1/mail` |
| `-quicken-store` | `<data-dir>/quicken.db` | Quicken import store; empty disables |
| `-import-quicken` | | Import a QIF/QFX file or directory, then exit |
| `-timeline-store` | `<data-dir>/timeline.db` | Google Timeline import store; empty disables |
| `-import-timeline` | | Import a Timeline JSON file/dir or Takeout zip, then exit |
| `-signal-store` | `<data-dir>/signal.db` | Signal import store; empty disables |
| `-import-signal` | | Import a Signal export (JSONL/JSON or plaintext SQLite), then exit |
| `-audit-log` | `<data-dir>/audit_log.db` | Access audit log path; empty disables |
| `-audit-retention-days` | `30` | Days of audit events to retain before pruning |
| `-log-file` | `<data-dir>/logs/aegis.log` | Process log file; empty disables file logging |
| `-aperture-url` | (none) | Aperture gateway base URL for admin setup links |
| `-token-pass` | | Secret-encryption passphrase (required). Covers OAuth tokens **and** static service-connection credentials. Wrong passphrase refuses to start and never overwrites `tokens.json` |
| `-token-new-pass` | | One-shot: re-encrypt `tokens.json` (tokens + static secrets) under this passphrase and exit |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-log-format` | `json` | `json` or `text` |

### Token encryption

**All secrets at rest are encrypted under `-token-pass`.** That includes
OAuth tokens **and** static service-connection credentials (Immich/YNAB
headers, Tailscale Admin API key). `service_connections.json` keeps only
non-secret overlay fields (endpoint, canonical claims, tailnet host metadata).

`tokens.json` is sealed with AES-256-GCM under a PBKDF2-HMAC-SHA256 master
key. The file records its own KDF parameters so costs can be raised later
without breaking existing stores.

| Field | Meaning |
|-------|---------|
| `version` | `3` is current; `2` and `1` (legacy) still load |
| `kdf` | `pbkdf2-sha256` |
| `iterations` | PBKDF2 cost for the master key (new files: 600 000, OWASP guidance) |
| `salt` | Master salt (base64) |
| `key_check` | Wrong-passphrase detector sealed under the master key |
| `tokens.<id>.salt` | Per-entry salt (base64); entry key = HKDF(master, salt) |
| `tokens.<id>.data` | AES-GCM ciphertext of the OAuth token JSON |
| `secrets.<key>.data` | AES-GCM ciphertext of a static credential |

**Migration:** on first startup after upgrade, plaintext
credentials found in `service_connections.json` are moved into `tokens.json`,
the overlay is rewritten without secrets, and Aegis logs one
`migrated plaintext service-connection credentials` line.

**Legacy files (version 1)** — string ciphertexts, implicit 100 000 iterations,
no per-entry salts — still load. The next write migrates them to the current
layout (same master cost until you rekey).

**Rotating the passphrase / raising KDF cost:**

```bash
# Stop Aegis first, then re-encrypt under the new passphrase.
# -token-pass is the current secret; -token-new-pass is the new one.
# Rekey covers OAuth tokens and static credentials in one shot.
# Rekey always writes the current format with a fresh master salt and the
# default iteration count (never lowers a harder existing cost).
AEGIS_TOKEN_PASS='old-secret' \
./aegis -token-new-pass 'new-secret' \
 -data-dir /path/to/aegis-data
```

Passing the same value for both secrets still rotates the master salt and
raises iterations on older files. Update `AEGIS_TOKEN_PASS` (or `-token-pass`)
to the new secret before restarting the service.

### Authentication

Aegis authenticates callers in two tiers:

1. **Tailscale identity** (recommended for agents) — via [Serve identity headers](https://tailscale.com/kb/1312/serve) or WhoIs against local `tailscaled`. Same trust model Aperture uses: identity is cryptographic, not a shared secret agents might paste into a chat.
2. **Agent API key** — `Authorization: Bearer <key>` or `X-Api-Key`, for bridges and non-tailnet tools. Never opens admin surfaces.
3. **Admin API key** (`-admin-api-key`) — for curl/scripts that manage data grants, apps, mirror, and other admin writes.
4. **Third-party app token** — `Authorization: Bearer aeg_app_…` for registered non-agent clients. See [Third-party app tokens](#third-party-app-tokens). Never admin.

Default mode is `auto` (identity if present, else API key). For a solo tailnet, prefer `-auth-mode tailnet` and no key: your browser login is admin, tagged agent hosts are not. Full policy guidance lives in [SECURITY.md](../SECURITY.md).

API key comparison is constant-time (`crypto/subtle`). HTML and JSON responses
carry `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, a restrictive
CSP, and `Referrer-Policy: no-referrer`. State-changing requests
authenticated by **ambient tailnet identity** must be same-origin (`Origin` or
`Sec-Fetch-Site: same-origin` matching `-base-url`); JSON write surfaces
require `Content-Type: application/json`; the third-party consent form carries
a per-render CSRF token. API-key and app-token writes are not
browser-ambient and skip the Origin check. See
[SECURITY.md](../SECURITY.md#csrf--origin-protection-on-writes-issue-182).

### Third-party app tokens

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
| `GET /v1/admin/apps` | Admin | List apps (token hints only) |
| `POST /v1/admin/apps` | Admin | Create; returns token once |
| `GET/PATCH/DELETE /v1/admin/apps/{id}` | Admin | Inspect, update scopes/name/disable, revoke |
| `POST /v1/admin/apps/{id}/rotate` | Admin | New token; old one dies |

**Scopes** use the same access levels as [Data grants](configuration.md)
(`none`, `freebusy`, `read`, `write`, `read-write`). Unlisted types fall back to
`default`, which is forced to `none` when omitted (fail closed). `freebusy` is
events-only.

**What an app token can call:**

- `GET /v1/library/{playlists,tracks,albums,artists,contacts,photos,events,freebusy,messages,transactions,places,documents}` only
- Not admin, OAuth, service proxy, mail, pre-request, mirror, review, or links
- Not document full-text (`…/{service}/{id}/text`) and not user-defined kinds. Built-in list paths are derived from `LibraryKind.AppReadable`.

When an app’s `events` scope is `freebusy`, `/v1/library/events` is redacted to
availability fields even if service permissions would allow full events ( + #131). Cache entries are isolated per app scope level so a freebusy app
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
`sample-calendar` connector: synthetic near-term events, no OAuth.
The third-party demo uses this so free/busy has busy blocks without a live
Google connection. Cancelled rows are included and omitted from free/busy.

### Data scopes (principal → dataset)

`data_scopes.json` (0600, under the data directory) maps agent API keys to
**data scopes**: which dataset partition that principal may read. This is the
multi-user isolation seam — see [`docs/data-scopes.md`](data-scopes.md).

```json
{
  "version": 1,
  "data_scopes": [
    {
      "id": "shire",
      "name": "Shire demo",
      "dataset": "shire",
      "api_key": "shire-demo-key",
      "read_only": true
    }
  ]
}
```

- Each `api_key` authenticates as that scope (agent tier only — never admin).
- `dataset` selects the partition. Services with a matching `dataset` field
  and mail rows tagged with that dataset are visible; other partitions are
  invisible (not 403). Empty `dataset` is the owner/default view (no filter).
- `read_only` rejects mutating agent methods except the pre-request hook.
- Optional `grants` tighten type-level data grants for that principal.

A service with `endpoint: "sample://library"` serves synthetic library
fixtures bound to `Service.Dataset` — useful for demos and isolation tests
without OAuth.

### Third-party connect button (demo)

This also covers the “Aegis icon on a third-party site” grant UX.
This build ships a working demo of that handoff:

| Route | Auth | Purpose |
|-------|------|---------|
| `GET /demo/third-party` | public | Fake scheduling app with **Continue with Aegis** |
| `GET /v1/apps/authorize` | public | Consent page (shows app name + scopes) |
| `POST /v1/apps/authorize` | Admin | Owner allows; returns one-time `aeg_grant_…` code (10 min) |
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



### HTTP server operations

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
- **Inbound body caps**: hook 8 MiB, service proxy 16 MiB, admin/mail
 4 MiB / 1 MiB. Oversized bodies return **413 Payload Too Large** and are not
 buffered. The pre-request hook still fail-opens (`allow`) on parse/read errors
 that are not size overflows.

