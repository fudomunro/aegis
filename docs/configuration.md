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
allow rule on a **library route** at an **access level** (`none`, `read`,
`write`, `read-write`). This is the application-layer side of Tailscale-style
access — you already use a tailnet grant so callers can *reach* Aegis; data
grants say what they may *use*.

Prefer the glossary terms here ([`docs/glossary.md`](glossary.md)):
**data grant** is the product idea; **data grants map** is where the grants
live; **app data grants** scope third-party tokens. JSON/config keys stay
`data_grants` / `data_grants.json`.

Data grants live with **routes**, not service providers. Each recognized
key (agent key, invite token, app token) carries one route → access-level
map, and authorization is a single lookup over it: an explicit route entry
wins, else the reserved `default` entry, else `none`.

**Accessors.** Data is not only reached by LLM agents. Aegis also
runs sync pipelines (mail archive, copy ledger, timeline/quicken import) and
third-party apps hold scoped tokens. The data grants editor is **kind-centric**:
each kind shows the agent data grant (editable) plus only the other
accessors that apply to that kind (read-only). `GET /v1/admin/permissions`
returns that same model. The map on Home answers the other half of confidence:
what each has *actually* accessed. Multi-user principals will
appear as additional accessor rows.

| Accessor kind | Where levels come from | In the kind editor |
|---------------|------------------------|--------------------|
| `agent` | kind-owned policy (`data_grants` / `data_grants.json`) | editable — admin “Data grants” / `PUT /v1/admin/data-grants` |
| `app` | app `scopes` on the third-party record (fail closed) | read-only (edit via `PATCH /v1/admin/apps/{id}`); shown when the kind is named or the level is not `none` |
| `sync` | fixed by the pipeline (what Aegis may store while archiving/mirroring) | read-only; shown only for kinds the pipeline stores |
| `service` | configured connector that syncs that library kind (e.g. Spotify, YouTube Music under `playlists`) | read-only for now |

Primary (aegis.json `data_grants` or data_grants.json `data_grants`, or admin “Data grants”):

```json
"data_grants": {
  "tracks": { "default": "read-write" },
  "player": { "default": "none" },
  "freebusy": { "default": "read" },
  "places/city": { "default": "read" },
  "default": { "default": "read" }
}
```

| Level | Allowed methods |
|-------|-----------------|
| `none` | none — 403 for that route |
| `read` | `GET`, `HEAD` |
| `write` | `POST`, `PUT`, `PATCH`, `DELETE` |
| `read-write` | both (also accepts `full` / `rw`) |

Narrowed disclosure is **its own route** with its own grant key — never a
level on the full route:

| Grant key | Route | Returns |
|-----------|-------|---------|
| `freebusy` | `GET /v1/library/freebusy` | availability only (`start_at`, `end_at`, `all_day`, `status`) |
| `transactions/totals` | `GET /v1/library/transactions/totals` | category/month totals only |
| `transactions/items` | `GET /v1/library/transactions/items` | line items without account identifiers |
| `documents/metadata` | `GET /v1/library/documents/metadata` | titles, dates, tags, sources — no excerpt |
| `places/city` | `GET /v1/library/places/city` | locality, country, and the visit window |
| `places/names` | `GET /v1/library/places/names` | names and addresses, no coordinates |

**Resolution** (uniform deny-by-default):

1. **Explicit route entry** — `data_grants[route].default`, normalized.
2. **Reserved `default` key** — the level for kinds not listed, except
 opt-in kinds (below), which never open through `default`.
3. Otherwise **`none`**. Malformed level tokens deny.

`providers` keys inside a `DataGrant` are removed and ignored on load
(they were per-connector tighten-only overrides in the two-policy era).
- **The full routes serve their richest shape** (`events`, `transactions`,
 `documents`, `places` with coordinates). Grant a narrowed route instead of
 the full one when a caller needs less — a `freebusy`-only key is denied
 `/v1/library/events` outright, and full text
 (`GET /v1/library/documents/{service}/{id}/text`) is a read on the
 `documents` route.
- **The places family is opt-in**: `places`, `places/names`, and
 `places/city` stay closed until named — the reserved `default` key never
 opens location history. Pre-request injection never advertises them until
 named. Import (Google Timeline) and live (OwnTracks) sources honor the
 same shapes. Per-tag allow/deny for documents (e.g. hide `medical`) is a
 design spike, not implemented.
- **Unknown proxy paths** (not a mapped kind) resolve like any unlisted
 kind: the reserved `default` key, else `none`.
- Proxy paths are **canonicalized before classification**. Duplicate slashes
 (including `%2f`) collapse to the real kind; `.` / `..` segments are rejected
 with `400`. Classification and upstream routing always see the same path.
- The owner key is materialized with a permissive `default` entry when no
 grants are configured, so fresh installs stay unrestricted out of the box.
- **Bootstrap vs overlay.** `aegis.json` may set bootstrap `data_grants`.
 Admin UI saves write a separate `data_grants.json` overlay (flag
 `-data-grants-file`) schema **version 3**: a `data_grants` map. A present
 map (including empty) replaces the bootstrap map; a missing map leaves it
 in place. Legacy `service_limits` keys in old files are ignored. The
 overlay holds no secrets — admin grant edits never rewrite OAuth client
 credentials.
- Grants **further constrain** the connector; they never add capabilities.
 A write level never unlocks mutating calls the connector does not
 implement (picker/session POSTs are not agent data writes).
- Enforcement is on the content/ingest transport, the unified library endpoint, and connector metadata (denied types/operations are hidden from agents).
- Pre-request injection also lists denied types so agents do not try them.
- Admin API: `GET/PUT /v1/admin/data-grants` is the editor.

Known kinds: `user`, `tracks`, `albums`, `artists`, `playlists`, `search`,
`player`, `channels`, `library`, `recommendations`, `contacts`, `photos`,
`events`, `messages`, `transactions`, `places`, `documents`. Paths that do not map to a known kind fall through to the
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

To keep a **backup** of a playlist or liked-tracks list on another service
(admin surface — the agent key is refused; use the admin key or a tailnet
admin login):

```
POST /v1/library/mirror
Authorization: Bearer <admin-key> # or tailnet admin login
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
professional-data connectors remain follow-up work.

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
| `messages` | `optimistic` | Takeout watermark / ingest cursor | Only material newer than the watermark; force-full is repair work |
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
auto-sync secondary copies. Repair / re-mirror is a separate operator action.

**Admin UI** surfaces the same fields read-only: library cards
show a per-item copy badge (“2 copies · 1 stale”), source chips/detail show
`role` / `sync_mode` / `as_of` / `checked_at` / `sync_status`, the Library
tab filters by copy status (in sync / stale / missing / unchecked), and
Advanced → Copy ledger lists `GET /v1/library/copies` rows with the same
filters.

### Sync queue

Data movement (mail ingest, local imports, mirroring) runs as **sync jobs** on
a small durable queue drained by an in-process worker. The job row is the
*work*; the copy ledger row is the *fact* — jobs are what produce
`last_synced_at`, `last_checked_at`, and `status` on copies.

Stored in SQLite alongside the copy ledger by default (one file, so a job's
completion and its copy stamp can commit in one transaction). `-sync-jobs`
overrides the path; an explicitly empty value disables the queue and the
worker, and every request-driven sync surface keeps working as before.

| Flag | Default | Description |
|------|---------|-------------|
| `-sync-jobs` | the `-copy-ledger` path | Sync job queue path; empty disables |
| `-sync-workers` | `2` | Worker pool size; `0` disables the worker |
| `-sync-service-concurrency` | `1` per service | Per-service sync concurrency caps, e.g. `spotify=1,youtube-music=2` |

Each job holds a lease while it runs and renews it while the step is alive,
so a long step — a mirror doing hundreds of name-based lookups — is not
reclaimed by another worker mid-write.

Each job is one bounded step — a mail page batch, an import chunk, a mirror
batch. Long work chains through the queue rather than living in a caller
loop, so closing a browser tab or restarting the process loses nothing: the
step's cursor is already durable, and an expired lease is reclaimed on the
next start. Failures retry with backoff (provider quota windows back off
longer); revoked credentials and other unfixable errors park the job as
terminal instead of burning the attempt budget.

**Admin UI** (Advanced → Data sync) shows queue state, per-job progress, and
next run times, with a **Sync mail now** button that enqueues work instead of
driving a client-side batch loop. The same surface is available as JSON:

```
GET  /v1/admin/sync                            # queue rollup, recent jobs, copy freshness, schedules, paused scopes
POST /v1/admin/sync                            # enqueue: {kind, job_type, service, payload}
POST /v1/admin/sync/{id}/cancel                # park a queued or running job
POST /v1/admin/sync/pause                      # hold work {"service":"spotify"}; empty service = whole queue
POST /v1/admin/sync/resume                     # release a paused scope
POST /v1/admin/sync/schedules/{id}/enable      # resume a paused schedule
POST /v1/admin/sync/schedules/{id}/disable     # pause a schedule
```

Job types today:

| Type | What it does |
|------|--------------|
| `ingest` | Live cursor paging into the mail archive (Gmail) |
| `import_snapshot` | Snapshot import of a read-only export (Google Takeout) |
| `freshness_check` | Observational copy-freshness pass (see below) |
| `mirror` | Copies a playlist or liked-tracks list from one service to another, one source page per step |
| `repair` | Re-places a copy that freshness marked stale or missing |

Enqueue is idempotent on `(kind, job_type, service, entity_key)`, so a
repeated request does not stack duplicate work. `POST /v1/admin/mail/sync`
and `POST /v1/admin/mail/import` run the same ingest implementation as the
queue and return the same bounded result, so the two surfaces cannot drift
apart.

### Mirror and repair jobs

`mirror` turns the one-shot `POST /v1/library/mirror` copy into queued work
that finishes the whole list without a caller loop. Each step reads one page
of the source and **appends to a single target playlist** — a 500-track
playlist is one playlist on the target, not three. The source cursor and the
target playlist id ride in the job payload, so a restart resumes exactly where
it stopped.

`repair` is the executor surface for "this backup drifted, fix it". It
re-runs the mirror for one entity and **skips what the target already holds**
(matched by name and artist, the same identity mirror uses across services),
so a repair converges instead of duplicating tracks. A repair job is a `mirror`
job with `force: true` and the target playlist id from the copy ledger row.

Both write through to the target service's API using the stored OAuth token.
Aegis still stores no media.

### Pausing

`POST /v1/admin/sync/pause` holds work so nothing new runs — useful when a
provider quota is exhausted or the link is metered. An empty `service` pauses
the whole queue; a service id pauses just that provider. Queued jobs for the
scope move to `paused` (so the counts stay honest) and new enqueues land there
too. `POST /v1/admin/sync/resume` puts them back in the queue. In-flight steps
are not interrupted; cancel them individually if they must stop.

Per-service concurrency is capped at one job at a time by default, so one
provider's quota cannot starve the rest of the queue. Raise it with
`-sync-service-concurrency` for services that tolerate more parallelism.

### Copy freshness

A `freshness_check` job answers "is this backup still current?" without
re-syncing anything. It walks the copy ledger, reads each copy's live version
descriptor from its location, and stamps `status` and `last_checked_at`.
Checks are observational: they never write to a source service.

| Copy | Compared on | Result |
|------|-------------|--------|
| Mirrored playlists / liked tracks (Spotify, YouTube Music) | `snapshot_id` / `item_count` from one metadata call | changed ⇒ `stale`, gone ⇒ `missing`, same ⇒ `in_sync` |
| Google Takeout snapshots | the recorded export path still exists | gone ⇒ `missing`, otherwise `in_sync` |

Only version tokens present on **both** sides are compared, so a provider that
omits `snapshot_id` never produces a false `stale`, and a renamed playlist is
not treated as a drifted backup. Optimistic copies (mail-like snapshots) can
only report presence — their contract is that material newer than the
watermark is expected, so a check confirms the copy still exists and leaves
drift to a re-import.

Services with **no checker** yet (Bluesky photo shares, per-item CalDAV /
CardDAV / Google etags) are left completely untouched: their rows keep
`last_checked_at` empty and the admin panel counts them as **unchecked**
rather than guessing. Failures are the same — an expired token or a provider
error is not evidence about the copy, so the row is skipped.

`GET /v1/admin/sync` returns the per-kind rollup under `freshness`:
`in_sync`, `stale`, `missing`, `unknown`, `unchecked`, `total`,
`last_checked_at`. That is the data to show a user where their copies stand.

### Data ownership

Per-kind completeness feedback (see `docs/data-ownership.md`): until the
user confirms that Aegis already holds all the data of a kind they care
about, the admin surface keeps a call to action to bring more of that kind
under management. The only persisted state is the confirmation itself —
`ownership.json` under the data directory. Sources currently in use, unused
declared routes (from the connection-maturity catalog and the ingest-source
declarations), and the copy-freshness rollup are derived at read time.

```
GET    /v1/admin/ownership          # per-kind status
POST   /v1/admin/ownership/{kind}   # confirm {"note":"…"}
DELETE /v1/admin/ownership/{kind}   # reopen (clear the confirmation)
```

Unconfirmed ownership never blocks reads. The admin UI merges the controls
into the Library tab's sources display: the box showing a kind's configured
sources also carries the "I have all my …" toggle (tinting the box when
on) and the declared call-to-action routes.

### Schedules

Recurring work feeds the queue on its own so freshness accumulates without
anyone clicking anything. A schedule is a row with its own `next_run_at`; when
it comes due the worker enqueues the job (idempotent, so a still-queued job is
not duplicated) and re-arms the schedule one interval out.

On first start Aegis seeds one template per checked kind — **Daily copy
freshness** for `messages`, `playlists`, and `tracks`, every 24 hours. The
seed only fills in what is missing: a paused or edited schedule is left as the
operator set it. Pause and resume from the panel or the
`/v1/admin/sync/schedules/{id}/enable|disable` routes.

Schedules live in the same SQLite file as the job queue and copy ledger.

### Environment variables

| Variable | Flag equivalent | Description |
|----------|----------------|-------------|
| `AEGIS_API_KEY` | `-api-key` | Agent API key for library/hook/content calls (optional in `auto`/`tailnet` modes; never admin) |
| `AEGIS_ADMIN_API_KEY` | `-admin-api-key` | Admin API key for admin surfaces; distinct from `AEGIS_API_KEY` |
| `AEGIS_AUTH_MODE` | `-auth-mode` | `auto` (default), `tailnet`, or `api-key` |
| `AEGIS_ALLOWED_LOGINS` | `-allowed-logins` | Comma-separated Tailscale logins allowed to call the API |
| `AEGIS_ADMIN_LOGINS` | `-admin-logins` | Comma-separated Tailscale logins allowed to use admin surfaces |
| `AEGIS_BASE_URL` | `-base-url` | Public URL of this Aegis instance |
| `AEGIS_ADMIN_BASE_URL` | `-admin-base-url` | Public URL of the admin listener (OAuth redirects, CSRF origin checks) |
| `AEGIS_APERTURE_URL` | `-aperture-url` | Optional public URL of the Aperture gateway (admin live links) |
| `AEGIS_TOKEN_PASS` | `-token-pass` | Passphrase for token encryption |
| `AEGIS_DATA_DIR` | `-data-dir` | Data directory for local state (see [Data directory](configuration.md)) |
| `AEGIS_DEMO` | `-demo` | `1` enables demo mode |
| `AEGIS_USER_KINDS_DIR` | | User-defined kind schema-pack directory (default `<data-dir>/user-kinds`) |

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
 data_scopes.json # principal → dataset registry (0600; invite key hashes)
 service_connections.json # admin Immich/static-auth + Tailscale Admin overlay (non-secret fields; secrets in tokens.json)
 user_kinds.json # admin UDK local-file selection overlay (beside -config; see below)
 user-kinds/ # user-defined kind schema packs (or AEGIS_USER_KINDS_DIR)
 entity_links.json # confirmed playlist/entity links
 ownership.json # per-kind data-ownership confirmation (issue #165; see docs/data-ownership.md)
 tokens.json # encrypted OAuth tokens + static credentials (0600;)
 copy_ledger.db # SQLite copy ledger + sync job queue (empty -copy-ledger / -sync-jobs disables)
 mail-archive.db # SQLite+FTS mail archive (omit -mail-archive to disable)
 search-index.db # SQLite library search index (omit -search-index to disable)
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
| `-sync-jobs` | the copy-ledger path | Sync job queue; empty (explicitly set) disables the sync worker |
| `-mail-archive` | `mail-archive.db` | Empty string (explicitly set) disables the local mail archive source |
| `-search-index` | `search-index.db` | Empty string (explicitly set) disables `q=` search on `/v1/library/{kind}` |
| `-quicken-store` | `quicken.db` | Empty string (explicitly set) disables the Quicken import source |
| `-timeline-store` | `timeline.db` | Empty string (explicitly set) disables the Google Timeline import source |
| `-signal-store` | `signal.db` | Empty string (explicitly set) disables the Signal import source |
| `-audit-log` | `audit_log.db` | Empty string (explicitly set) disables the access audit trail |
| `-log-file` | `logs/aegis.log` | Empty string (explicitly set) logs to stderr only |

**`user_kinds.json` has no path flag** and is always written **beside
`-config`** (same directory as `aegis.json`). It sits under the data directory
only while `-config` keeps its default `<data-dir>/aegis.json`; an explicit
`-config /elsewhere/aegis.json` moves it to `/elsewhere/user_kinds.json`. The
`user-kinds/` schema-pack directory is a separate path: default
`<data-dir>/user-kinds`, override with `AEGIS_USER_KINDS_DIR`.

Startup logs the resolved data directory (`data directory ready` / `data_dir`
on `starting server`). The admin **Runtime** panel shows the same path.

**Migrating from cwd defaults:** if you previously kept `aegis.json`,
`tokens.json`, and friends next to the binary, either move them into the data
directory or keep passing the explicit path flags.

### All flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `127.0.0.1:8080` | Listen address. Loopback by default; with Serve identity trust (`auto`/`tailnet` auth mode) a wildcard or LAN bind is refused at startup — bind loopback behind `tailscale serve`, the Tailscale IP, or use `-auth-mode api-key` |
| `-base-url` | | Public base URL (required) |
| `-admin-addr` | | Listen address for admin surfaces (tailnet/loopback only). Empty = single-listener mode where admin shares `-addr`. See [External access](external-access.md) |
| `-admin-base-url` | | Public base URL of the admin listener (required with `-admin-addr`; used for OAuth redirects and CSRF origin checks) |
| `-external-access` | `false` | Publish the API listener for external principals: invite/app tokens only on `-addr`, admin surfaces only on `-admin-addr`. Requires `-admin-addr` |
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
| `-sync-jobs` | the `-copy-ledger` path | Sync job queue path (co-located with the copy ledger by default); empty disables |
| `-sync-workers` | `2` | Sync worker pool size; `0` disables the worker |
| `-sync-service-concurrency` | `1` per service | Per-service sync concurrency caps, e.g. `spotify=1,youtube-music=2` |
| `-mail-archive` | `<data-dir>/mail-archive.db` | Mail archive path; empty disables the local mail archive source |
| `-import-takeout` | | Read-only Google Takeout path (mbox/zip/dir) to sync into the mail archive, then exit |
| `-import-takeout-batch` | `20000` | Max messages per `-import-takeout` call (repeat runs continue) |
| `-search-index` | `<data-dir>/search-index.db` | Library search index path; empty disables `q=` search |
| `-quicken-store` | `<data-dir>/quicken.db` | Quicken import store; empty disables |
| `-import-quicken` | | Import a QIF/QFX file or directory, then exit |
| `-timeline-store` | `<data-dir>/timeline.db` | Google Timeline import store; empty disables |
| `-import-timeline` | | Import a Timeline JSON file/dir or Takeout zip, then exit |
| `-signal-store` | `<data-dir>/signal.db` | Signal import store; empty disables |
| `-import-signal` | | Import a Signal export (JSONL/JSON or plaintext SQLite), then exit |
| `-audit-log` | `<data-dir>/audit_log.db` | Access audit log path; empty disables |
| `-audit-retention-days` | `30` | Days of audit events to retain before pruning |
| `-fake-upstream` | | **Dev/test only** — serve an injectable fake provider at `/demo/fake-upstream` and seed placeholder tokens for services pointed at it. Never enable on a real instance |
| `-seed-sample` | `false` | Write synthetic sample mail + library fixtures under the data directory, then exit |
| `-seed-sample-count` | `24` | Mail messages to generate with `-seed-sample` |
| `-seed-sample-stress` | `false` | With `-seed-sample`, generate the large stress set (10 000 mail rows) for local perf |
| `-seed-lotr` | `false` | Write named demo dataset packs (`shire`, `gondor`, `lincoln`) under the data directory, then exit |
| `-seed-demo-datasets` | `false` | Same as `-seed-lotr`: write all named demo dataset packs |
| `-demo` | `false` | Demo mode: api-key + data scopes only, `sample://` fixtures only, read-only surface (`AEGIS_DEMO=1`) |
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

1. **Tailscale identity** (recommended for agents) — via [Serve identity headers](https://tailscale.com/kb/1312/serve) or WhoIs against local `tailscaled`. Same trust model Aperture uses: identity is cryptographic, not a shared secret agents might paste into a chat. Trusted only on listeners that are not externally reachable.
2. **Agent API key** — `Authorization: Bearer <key>` or `X-Api-Key`, for bridges and non-tailnet tools. Never opens admin surfaces.
3. **Admin API key** (`-admin-api-key`) — for curl/scripts that manage data grants, apps, and other admin writes. Admin surfaces live on the in-tailnet admin listener.
4. **Invite token** — `Authorization: Bearer <key>` or `X-Api-Key`, one per external principal (a data-scope key). Deny-by-default grants; never admin. See [external access](external-access.md).
5. **Third-party app token** — `Authorization: Bearer aeg_app_…` for registered non-agent clients. See [Third-party app tokens](#third-party-app-tokens). Never admin.

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

### External access (split listeners)

By default one listener serves every route. For external principals — family,
friends, another Aegis instance — split the surface instead:

```bash
./aegis \
 -addr 0.0.0.0:8080 \
 -base-url https://aegis.example.com \
 -admin-addr 100.64.0.5:8081 \
 -admin-base-url https://aegis-admin.your-tailnet.ts.net \
 -external-access \
 -auth-mode tailnet
```

- The **API listener** (`-addr`, `-base-url`) may be published behind TLS.
  It serves an **allowlist** of agent/external routes — `POST /v1/pre-request`,
  library reads, `/v1/services/*` content transport, mail search/status,
  `GET /v1/auth/status`, `POST /v1/apps/token`, `POST /v1/apps/revoke`,
  `/healthz`, `/readyz`. Unlisted routes answer 404 there and are served only
  by the admin listener, so a new route cannot leak onto the public surface
  by default. Under `-external-access` it accepts **invite tokens and app
  tokens only** — no ambient identity, and the shared agent key is refused.
- The **admin listener** (`-admin-addr`, `-admin-base-url`) stays in-tailnet
  (loopback or Tailscale IP — anything else is refused at startup) and serves
  everything: admin surfaces plus the agent tier for tailnet callers, so
  Aperture and tagged hosts keep working with ambient identity or the agent
  key. It is never published.
- `read_only` on a data scope still permits the pre-request hook (it is a
  read-only classification webhook).

External access requires `-admin-addr`; running it on a single listener would
publish the admin surface. The full contract — including the route allowlist
and the trust rationale — lives in [`docs/external-access.md`](external-access.md).

### Third-party app tokens

Non-agent clients (for example a hosted scheduling service) can read library
data with a **scoped app token** instead of the shared API key. Apps are
registered in the admin API; the plaintext token is shown once.

```
POST /v1/admin/apps
{"id":"eco","name":"Event Coordinator","scopes":{"freebusy":"read","default":"none"}}
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
(`none`, `read`, `write`, `read-write`). Unlisted types fall back to
`default`, which is forced to `none` when omitted (fail closed).

**What an app token can call:**

- `GET /v1/library/{playlists,tracks,albums,artists,contacts,photos,events,freebusy,messages,transactions,places,documents}` only
- Not admin, OAuth, service proxy, mail, pre-request, mirror, review, or links
- Not document full-text (`…/{service}/{id}/text`) and not user-defined kinds. Built-in list paths are derived from `LibraryKind.AppReadable`.

When an app holds only the `freebusy` route grant, `/v1/library/events` is
denied and `/v1/library/freebusy` serves availability only.
Cache entries are isolated per grant map so a freebusy app
never reads a full-events cache.

**Reachability.** App tokens are an authorization layer, not a public ingress.
The third party must be able to reach Aegis over a path you control: the
tailnet, a VPN, or [external access](external-access.md) (a published API
listener with invite tokens). Keep the admin listener in-tailnet. For
interactive browser use, the user’s machine can call Aegis on
localhost/MagicDNS while it is awake; background backend pull needs an
always-on host.

**Storage.** `apps.json` (mode 0600) holds SHA-256 token hashes and hints only.
Rotate or delete an app at any time; tokens are not recoverable.

### Sample calendar (demo / query testing)

A service with `endpoint: "sample://calendar"` uses the built-in
`sample-calendar` connector: synthetic near-term events, no OAuth.
The third-party demo uses this so free/busy has busy blocks without a live
Google connection. Cancelled rows are included and omitted from free/busy.

### Data scopes (principal → dataset)

`data_scopes.json` (0600, under the data directory) maps invite tokens to
**data scopes**: which dataset partition that principal may read. This is the
multi-user isolation seam — see [`docs/data-scopes.md`](data-scopes.md).

```json
{
  "version": 2,
  "data_scopes": [
    {
      "id": "shire",
      "name": "Shire demo",
      "dataset": "shire",
      "api_key_hash": "9f2c…",
      "api_key_hint": "aeg_invite_…",
      "read_only": true
    }
  ]
}
```

- Each scope credential authenticates as that scope (agent tier only — never
  admin). Under [external access](external-access.md) this key is the
  **invite token** issued to an external principal. Only its SHA-256 hash is
  stored (`api_key_hash`); the plaintext is shown once at mint
  (`POST /v1/admin/data-scopes`) or rotate. Legacy files with plaintext
  `api_key` are migrated to hashes on load.
- `dataset` selects the partition. Services with a matching `dataset` field
  and mail rows tagged with that dataset are visible; other partitions are
  invisible (not 403). Empty `dataset` is the owner/default view (no filter).
- `read_only` rejects mutating agent methods except the pre-request hook.
- `grants` is the **complete allow list** for that principal —
  **deny-by-default**: an unlisted kind resolves to `none`. A reserved
  `default` entry sets the level for unlisted kinds. Ambiguity is fail-closed:
  a listed kind with an empty entry, or with only `providers` overrides,
  resolves to `none` (provider entries tighten, they never open a closed
  type — set the kind's `default` field to grant access). (Owner-tier
  credentials — the shared agent key and tailnet logins — keep the usual
  agent data grant semantics, where omit means unrestricted.) Scope grants
  also never exceed agent data grants.
- Admin lifecycle (`/v1/admin/data-scopes`): mint, rotate, and disable scope
  keys — see [`docs/data-scopes.md`](data-scopes.md).

A service with `endpoint: "sample://library"` serves synthetic library
fixtures bound to `Service.Dataset` — useful for demos and isolation tests
without OAuth.

### Trusted proxies and rate limits

Two `aegis.json` fields harden a published API listener
([external access](external-access.md)):

```json
{
  "trusted_proxies": ["127.0.0.1/32"],
  "client_ip_header": "x-forwarded-for",
  "rate_limits": {
    "auth_per_minute": 60,
    "auth_failures_per_ip": 10,
    "auth_failures_per_token": 10,
    "window_seconds": 300
  }
}
```

- `trusted_proxies` lists proxy addresses (IP or CIDR) whose forwarded
  headers are believed when resolving the client IP for rate limits and
  audit. Empty (default): forwarded headers are ignored and the peer
  address is used — never widen this past your actual edge, since any
  client connecting *from* a listed address can forge its client IP.
  Invalid entries refuse config load. With Serve identity trust (`auto` /
  `tailnet` auth mode), a non-empty list also refuses startup: a generic
  edge forwards client-supplied `Tailscale-User-Login`, which the
  local-peer gate would trust into admin (see
  [external-access.md](external-access.md)). Behind a trusted edge,
  `api-key` / `admin-api-key` must also be at least **32 characters** —
  those keys are the perimeter and the rate caps only slow guessing.
- `client_ip_header` selects which forwarded header carries the client
  address from a trusted proxy: `x-forwarded-for` (default) or
  `x-real-ip`. **Exactly one header is read** — never a fallback chain —
  because an edge that only appends `X-Forwarded-For` passes a
  client-supplied `X-Real-IP` through unchanged. Set it to whichever
  header your edge *overwrites* (the demo nginx sets `X-Real-IP`). The
  XFF walk takes the rightmost hop that is not itself a trusted proxy.
- `rate_limits` tunes brute-force damping on authentication and token
  routes (`/v1/apps/token`, `/v1/apps/authorize`, `/v1/apps/revoke`,
  `/v1/auth/*`) plus failed authentications everywhere:
  `auth_per_minute` caps requests per client IP per minute;
  `auth_failures_per_ip` and `auth_failures_per_token` lock out a client
  IP or one presented credential after that many failed authentications in
  `window_seconds`. Lockouts answer **429**. Zero fields use the defaults
  shown; a **negative** value disables that cap (`window_seconds: -1`
  disables both failure caps — it does not mean "no expiry"). Per-IP keys
  bucket IPv6 by `/64` (per-address keys would be meaningless against
  privacy-extension rotation) and are **skipped for loopback peers when
  `trusted_proxies` is empty**, since a same-host edge would put every
  caller in one bucket; requests that resolve a tailnet identity are
  exempt from lockouts entirely. Fail-open by design: the counters bound
  credential guessing, not availability (body caps and edge rate limits
  cover volume).

### Demo mode (public synthetic instance)

`-demo` (or `AEGIS_DEMO=1`) is a deployment profile for a public demo host:

- Forces `auth-mode=api-key` (scope keys + admin key only).
- Requires `data_scopes.json` with at least one principal.
- Forces every data scope `read_only`.
- Allows only `sample://…` fixture services (live providers are refused at startup).
- Refuses mutating routes and OAuth (`/v1/auth/*`, except `/v1/auth/status`).
- `/v1/pre-request` stays allowed (classification is read-only).

```bash
./aegis -demo -seed-lotr -data-dir /var/lib/aegis-demo
# then run with the same -demo flag and data_scopes.json in place
```

Demo mode is not a second product: it is Aegis with the synthetic/read-only
guardrails the public instance must not bypass.

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
 `freebusy:read` only). Allow the grant.
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

