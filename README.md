# Aegis

A service proxy for [Tailscale Aperture](https://tailscale.com/docs/aperture) that intercepts LLM requests referencing named services (Spotify, YouTube Music, Amazon Music), mediates access through local proxy endpoints, and enforces rules with OAuth-based authentication and caching.

## How It Works

Aegis runs as two components:

1. **Pre-request hook** — Aperture calls Aegis before forwarding requests to LLM providers. Aegis detects service references in the user's message and injects instructions telling the agent to use Aegis's proxy endpoints instead of external APIs.

2. **Service proxy** — Agents call `GET/POST /v1/services/{id}/...` instead of hitting external APIs directly. Aegis adds OAuth tokens, forwards to the real service, caches responses, and returns the data.

```
Agent → Aperture → Aegis (hook: inject proxy URL) → LLM
Agent → Aegis (proxy) → Spotify API / YouTube Music API / ...
```

## Prerequisites

- **Go 1.21+** (tested with 1.27)
- **Tailscale** with an [Aperture gateway](https://tailscale.com/docs/aperture/get-started) configured
- **OAuth app credentials** for each service you want to connect (see [Setup](#setup))

## Quick Start

```bash
# Clone and build
git clone https://github.com/fudomunro/aegis.git
cd aegis
go build -o aegis .

# Create your config (see aegis.example.json)
cp aegis.example.json aegis.json
# Edit aegis.json with your OAuth client IDs and secrets

# Run (recommended: tailnet identity via Tailscale Serve or WhoIs)
./aegis \
  -addr :8080 \
  -base-url https://aegis.your-tailnet.ts.net \
  -auth-mode tailnet \
  -allowed-logins you@example.com \
  -config aegis.json \
  -token-pass your-encryption-passphrase

# Or keep a shared API key for non-tailnet clients
./aegis \
  -addr :8080 \
  -base-url https://aegis.your-tailnet.ts.net \
  -api-key your-secret-key \
  -config aegis.json \
  -token-pass your-encryption-passphrase
```

Aegis starts with these endpoints:
- `POST /v1/pre-request` — Aperture hook
- `GET /v1/library/playlists` — unified playlists across all connected services
- `GET /v1/library/tracks` — unified liked/saved tracks across all connected services
- `GET /v1/library/albums` — unified saved albums across all connected services
- `GET /v1/library/artists` — unified followed/top artists across all connected services
- `GET /v1/library/contacts` — unified contacts across all contact-capable services
- `GET /v1/library/photos` — unified photo metadata across all photo-capable services (bytes stay on the source service)
- `GET /v1/library/events` — unified calendar events across all calendar-capable services
- `GET /v1/library/messages` — unified messages/email metadata across all message-capable services (bodies stay on the source service)
- `GET /v1/mail/search` — search the local compressed mail archive (FTS + from/to/date filters)
- `GET /v1/mail/messages/{service}/{id}` — one archived message (body included)
- `GET /v1/mail/messages/{service}/{id}/raw` — decompressed provider payload JSON
- `GET /v1/mail/status` — archive size and sync cursor
- `POST /v1/mail/sync` — ingest a bounded batch of mail into the archive (resumable)
- `POST /v1/library/mirror` — copy a playlist or liked-tracks list from one service to another (canonical backup; see [Mirror / backup sync](#mirror--backup-sync-issue-16-phase-3))
- `GET/POST /v1/services/{id}/...` — service proxy
- `GET /v1/admin` — admin dashboard for data-flow map, services, library, and advanced ops (cache, runtime, datasets)
- `GET /v1/admin/flow` — data-flow topology + recent access stats (`?window=5m|1h|24h`) for the Map tab (issue #32)
- `GET /v1/admin/library/{kind}` — admin Library tab preview (same data as `/v1/library/{kind}`, does not count as Map access)

See [SECURITY.md](SECURITY.md) for the Tailscale identity model, recommended ACLs, and why Funnel should stay off.
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
  "description": "Proxies Spotify API calls",
  "connection": "spotify",
  "oauth_scopes": ["user-library-read", "user-follow-read", "playlist-read-private"]
}
```

| Field | Description |
|-------|-------------|
| `id` | Unique identifier, used in proxy URLs (`/v1/services/{id}`) |
| `name` | Display name, also used for matching in user messages |
| `aliases` | Additional names to match (case-insensitive substring) |
| `action` | `inject` (redirect to proxy) or `block` (reject requests) |
| `endpoint` | Upstream API base URL |
| `headers` | Static headers to add to upstream requests (optional) |
| `cache_ttl` | How long to cache GET responses (e.g. `5m`, `1h`) |
| `permissions` | Optional map of data type → access level for agents (see [Data permissions](#data-permissions)) |
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
      "client_secret": "…",
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

### Data permissions

Beyond the coarse `action: inject|block` switch, each service can restrict
**what kind of access agents have to specific data types**:

```json
"permissions": {
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
| `read` | `GET`, `HEAD` |
| `write` | `POST`, `PUT`, `PATCH`, `DELETE` |
| `read-write` | both (also accepts `full` / `rw`) |

- The reserved key `default` applies to data types not listed.
- If `permissions` is omitted, agents keep unrestricted access (previous behavior).
- **Bootstrap vs overlay (issue #71).** `aegis.json` may set default
  `permissions` per service. Admin UI saves write a separate
  `permissions.json` overlay (flag `-permissions-file`). A service present in
  the overlay uses those permissions; a present key set to `null` means
  unrestricted; services absent from the overlay keep the bootstrap map from
  `aegis.json`. The overlay holds no secrets — admin permission edits never
  rewrite OAuth client credentials.
- Permissions **further constrain** the connector; they never add capabilities.
  The admin form only offers **Editable** for types with a true agent data-write
  operation on the connector. Mirror backup writes and photo-picker session
  creates are not Editable. Write / read-write on a type the form cannot offer
  is treated as read when saved from the admin UI (proxy still allows mutating
  methods the connector implements if the config already has write and you do
  not re-save). Enable write explicitly when a real agent write op appears.
- Enforcement is on the service proxy, the unified library endpoint, and the
  `/capabilities` response (denied types/operations are hidden from agents).
- Pre-request injection also lists denied types so agents do not try them.

Known data types: `user`, `tracks`, `albums`, `artists`, `playlists`, `search`,
`player`, `channels`, `library`, `recommendations`, `contacts`, `photos`. Paths that do not map to a
known type fall through to the `default` level.

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

Valid kinds: `playlists`, `tracks`, `albums`, `artists`, `contacts`, `photos`, `events`.
At most **one** non-blocked service may claim each kind. After a library fetch,
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

### Environment variables

| Variable | Flag equivalent | Description |
|----------|----------------|-------------|
| `AEGIS_API_KEY` | `-api-key` | API key for authenticating requests (optional in `auto`/`tailnet` modes) |
| `AEGIS_AUTH_MODE` | `-auth-mode` | `auto` (default), `tailnet`, or `api-key` |
| `AEGIS_ALLOWED_LOGINS` | `-allowed-logins` | Comma-separated Tailscale logins allowed to call the API |
| `AEGIS_BASE_URL` | `-base-url` | Public URL of this Aegis instance |
| `AEGIS_TOKEN_PASS` | `-token-pass` | Passphrase for token encryption |

### All flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `:8080` | Listen address |
| `-base-url` | | Public base URL (required) |
| `-auth-mode` | `auto` | `auto`, `tailnet`, or `api-key` — see [SECURITY.md](SECURITY.md) |
| `-api-key` | | Shared API key (required only for `api-key` mode) |
| `-allowed-logins` | | Optional allowlist of Tailscale logins |
| `-ts-socket` | auto-detect | tailscaled LocalAPI socket for WhoIs |
| `-config` | `aegis.json` | Bootstrap config path (services, connections, secrets) |
| `-permissions-file` | `permissions.json` beside `-config` | Admin permissions overlay (non-secret). All permission saves write here; `aegis.json` is never rewritten |
| `-token-file` | `tokens.json` | Encrypted token storage path |
| `-token-pass` | | Token encryption passphrase (required) |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-log-format` | `json` | `json` or `text` |

### Authentication

Aegis can authenticate callers two ways:

1. **Tailscale identity** (recommended) — via [Serve identity headers](https://tailscale.com/kb/1312/serve) or WhoIs against local `tailscaled`. Same trust model Aperture uses: identity is cryptographic, not a shared secret agents might paste into a chat.
2. **API key** — `Authorization: Bearer <key>` or `X-Api-Key`, for bridges and non-tailnet tools.

Default mode is `auto` (identity if present, else API key). For a solo tailnet, prefer `-auth-mode tailnet` and no key. Full policy guidance lives in [SECURITY.md](SECURITY.md).

## Connecting Services

1. Register an OAuth app with each service (see below for per-service guides)
2. Add the client ID and secret to your `aegis.json` (`connections` + service `connection` reference)
3. Start Aegis and visit `https://your-aegis/v1/admin`
4. Open the **Services** tab and click **Connect** for each service — you'll be redirected to authorize
5. Tokens are stored encrypted on disk and auto-refresh when expired

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

### Two-legged auth

Aegis has **two** callers, and they authenticate differently:

| Leg | Who calls Aegis | How they authenticate |
|-----|--------------------|------------------------|
| **Hook** | Aperture (`POST /v1/pre-request`) | Usually the shared API key in the Aperture hook config (`"apikey"`). In `tailnet` mode Aperture must also present tailnet identity (Serve headers / WhoIs). |
| **Agent data** | The LLM agent (`/v1/library/playlists`, `/v1/services/...`) | Preferred: Tailscale identity from the agent’s host (Serve or WhoIs). Fallback: API key if `auth-mode` is `auto` or `api-key`. |

Agents do **not** need Spotify/YouTube/Amazon OAuth tokens — Aegis attaches those when proxying. With `-auth-mode tailnet` and no API key, agents on the tailnet call Aegis without a shared secret.

## API Reference

Protected endpoints accept **either** a valid API key (`Authorization: Bearer <key>` or `X-Api-Key`) **or** a Tailscale identity, depending on `-auth-mode` (see [SECURITY.md](SECURITY.md)).

### Pre-request hook

```
POST /v1/pre-request
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Receives Aperture's `pre_request` payload. Returns one of:
- `{"action": "allow"}` — no service matched
- `{"action": "modify", "request_body": {...}}` — LLM request with injected proxy instructions (Aperture requires the `request_body` field)
- `{"action": "block", "message": "..."}` — request rejected (403)

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
| `sources[]` | Service-local copies: `service`, `service_name`, `id`, `url`, `canonical`, `extra` |
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

Linked playlists merge even when names differ (`match: "link"`). Links persist in `entity_links.json` (`-link-file`).

**Merge review:** `GET /v1/library/review` lists multi-source entities joined by **name only** (no shared ISRC/UPC/MBID/link) so they can be checked or explicitly linked.

### Migration from name-key merge

1. Library responses now include `match` (`isrc` / `upc` / `mbid` / `link` / `name`) and `entity_id` when known.
2. Name-only joins still happen; they are marked `match: "name"` and appear in `/v1/library/review`.
3. Prefer durable IDs for identity; treat name merges as provisional until reviewed or linked.
4. Clients that only compared display names should prefer `entity_id` + `sources[]` instead.

Tracked in [issue #4](https://github.com/fudomunro/aegis/issues/4).

Service-level playlist endpoints (`GET /v1/services/{id}/me/playlists`) return the same neutral shape for a single service (one `sources[]` entry). Per-service `by_service` status values: `ok`, `unauthorized`, `error`, `skipped`.

### Sync completeness

Aegis aims to surface a user’s **full** library, not a prefix. Two layers matter:

1. **Pagination safety valve** — connectors and proxy auto-pagination follow up to `libraryPageCap` pages (currently 200, about 10k items at page size 50 / 20k at 100). That cap exists only to stop a looping upstream; it is not a product limit. When a connector hits it with more pages still upstream, it sets `by_service[service].truncated = true`, and the library response also sets a top-level `"truncated": true`. Proxy auto-pagination responses set `"truncated": true` in the combined payload the same way.

2. **Provider / API gaps** — some datasets are incomplete for reasons Aegis cannot fix by paginating harder:

| Gap | Why | How it is reported |
|-----|-----|--------------------|
| **Google Photos full library** | Library API reads require Google app verification; unverified apps get 403. Aegis uses the Picker API instead — only the user’s last selection. | Empty library until a pick; system instructions tell agents to run the picker flow. Not a `truncated` flag — the library is intentionally a selection, not a full dump. |
| **YouTube Music albums** | No public saved-albums API. | `/v1/library/albums` simply has no YouTube source. |
| **Amazon Music** | No public developer REST API; playlist pagination may return an unfollowed `nextToken`. | When `nextToken` is present and not followed, `truncated` is set rather than claiming completeness. |
| **Playlist track bodies** | Library playlist entries carry `track_count` and metadata, not every track object. | `track_count` is authoritative; fetch items via the service proxy for a specific playlist. |
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
  "by_service": {
    "spotify": { "status": "ok", "count": 120 },
    "youtube-music": { "status": "ok", "count": 45 }
  }
}
```

When any service hits its page cap, the response also sets `"truncated": true` and that service’s `by_service` entry sets `"truncated": true`.

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
| `sources[]` | Service-local copies: `service`, `id`, `url`, `canonical`, `blob_url`, `thumbnail_url` |

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
[issue #20](https://github.com/fudomunro/aegis/issues/20).

#### Immich (local / foreign canonical)

Immich is a self-hosted photo library. Aegis treats it as a **foreign/local
canonical** source: metadata is listed through Aegis; **bytes stay on
Immich**. Auth is an API key in service headers — no OAuth app.

```json
{
  "id": "immich",
  "name": "Immich",
  "action": "inject",
  "endpoint": "http://photos.home.ts.net:2283/api",
  "headers": { "X-API-Key": "YOUR_IMMICH_API_KEY" },
  "cache_ttl": "5m",
  "canonical_for": ["photos"],
  "permissions": { "default": "read", "photos": "read" }
}
```

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
POST /v1/services/immich/search/metadata
GET  /v1/services/immich/assets/{id}/original
GET  /v1/services/immich/assets/{id}/thumbnail
```

This is the reference pattern for other local/foreign stores: configure
`endpoint` + static `headers`, implement a library provider, mark
`canonical_for`, and never cache content bytes in Aegis.

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

Agent-facing proxy paths return neutral shapes:

```
GET /v1/services/google-calendar/me/events
GET /v1/services/google-calendar/me/calendars
GET /v1/services/google-calendar/calendars/{calendarId}/events
GET /v1/services/google-calendar/events/{id}
```

This phase is **read-only**. Capabilities do not advertise agent write ops
(create/update/delete events), so the admin permissions form will not offer
Editable for `events` and proxy mutations stay blocked
([issue #61](https://github.com/fudomunro/aegis/issues/61)).
Event content stays on Google Calendar.

### Messages as a data type

`Message` is Aegis's neutral representation of a message — email first,
with reserved channel values for SMS/WhatsApp when a named OAuth-capable
connector exists. Service identity lives in `sources[]`, same as playlists,
contacts, photos, and calendar events. **Aegis does not store mailboxes or
message bodies as the home copy** — the authoritative content stays on a
non-Aegis service marked `canonical` (issue #16). Library listings carry
subject/snippet metadata; agents fetch full body through the service proxy.
Initial provider: **Gmail**
([issue #60](https://github.com/fudomunro/aegis/issues/60)).

Neutral message fields (user-facing):

| Field | Description |
|-------|-------------|
| `channel` | `email`, `sms`, or `whatsapp` |
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
| `sources[]` | Service-local copies: `service`, `id`, `thread_id`, `url`, `mailbox`, `canonical` |

Cross-service merge prefers **RFC Message-ID** when a provider supplies it
(`match=message_id`); otherwise **subject + from + sent_at**
(`match=subject_from_time`). Replies that share a subject but different
Message-IDs or send times stay distinct.

```
GET /v1/library/messages
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's messages from every connected message-capable service.
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

Agent-facing proxy paths return neutral shapes:

```
GET /v1/services/google-gmail/me/messages
GET /v1/services/google-gmail/messages/{id}          # ?format=full includes decoded body
GET /v1/services/google-gmail/me/threads
GET /v1/services/google-gmail/threads/{id}
```

This phase is **read-only**. Capabilities do not advertise agent write ops
(send/delete), so the admin permissions form will not offer Editable for
`messages` and proxy mutations stay blocked
([issue #61](https://github.com/fudomunro/aegis/issues/61)).
Message bodies stay on Gmail as the **canonical** home copy.

#### Local mail archive (`/v1/mail`)

For full-history agent queries, Aegis can keep a **local compressed
archive** (SQLite FTS5 + gzip provider payloads). This is a query mirror, not
a new home copy — Gmail remains canonical for the mailbox (issue #16). Optimize
for Aegis/agents: search and fetch by id without paging Gmail every turn.

```bash
./aegis ... -mail-archive mail-archive.db   # empty string disables
```

| Endpoint | Purpose |
|----------|---------|
| `GET /v1/mail/search` | FTS + filters: `q`, `from`, `to`, `after`, `before`, `label`, `limit`, `offset`, `sort=asc\|desc`, `include_body` |
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

`mail-archive.db` is local state (like `tokens.json`) — do not commit it.

##### Google Takeout (read-only sync location)

A historical [Google Takeout](https://takeout.google.com) mail export is a
**read-only sync location** alongside live Gmail — not a write target and not
the canonical home. Point Aegis at an `.mbox` file, a directory of mbox
files, or a Takeout `.zip`. Re-sync skips messages already stored (by RFC
Message-ID) so you can run it as often as you like.

```
POST /v1/admin/mail/import
{"path": "/Users/you/Downloads/Takeout", "max_messages": 5000}
```

- `max_messages: 0` + `path` only saves the location
- Repeat with `max_messages > 0` until `"done": true` (already-present IDs count as `skipped`)
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

### Service proxy

```
GET/POST /v1/services/{service_id}/{path}
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Forwards to the configured upstream endpoint with OAuth tokens. Callers authenticate to Aegis; they do not need the upstream service’s OAuth tokens.
- `X-Aegis-Cache: hit` or `miss` in response headers
- GET responses cached per-service
- POST requests forwarded without caching

### Auth endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/auth/{id}/login` | GET | Start OAuth flow |
| `/v1/auth/{id}/callback` | GET | OAuth callback (redirect target) |
| `/v1/auth/status` | GET | Token status for all services |
| `/v1/auth/{id}` | DELETE | Revoke stored token |

### Admin dashboard

```
GET /v1/admin              — HTML dashboard (shell only; no secrets)
GET /v1/admin/summary      — redacted services, token status, cache stats
GET /v1/admin/flow         — data-flow topology (nodes, edges, libraries)
GET /v1/admin/cache        — in-memory cache inventory
POST /v1/admin/cache/clear — clear the response cache
GET /v1/admin/library/{kind}  — admin Library tab preview (issue #62; not counted as agent Map access)
GET /v1/admin/services/{id}/permissions  — current agent permission map + available/writable types
PUT /v1/admin/services/{id}/permissions  — replace the map (clamped to connector capability; writes permissions.json overlay, not aegis.json)
```

`/v1/admin` loads in the browser and fetches the JSON APIs. On a tailnet with
Serve identity the page works with no extra setup. In API-key mode, enter the
key when prompted (kept only in that tab’s `sessionStorage`).

Tabs: **Map** (default), **Services**, **Library**, and **Advanced**.
Connection management lives on **Services** — Connect/Reconnect, permissions,
and empty-state help when nothing is authorized yet. There is no separate
setup wizard.

The **Library** tab loads datasets via `/v1/admin/library/{kind}` so browsing
the admin UI does not inflate Map access counts. Agents still use
`/v1/library/{kind}`; those requests are the ones the Map counts.

The **Map** tab (default) is a live SVG overview of how data moves. Aperture
is drawn as a container around Aegis; Aegis nests the token vault and
response cache and lists every data type available on this instance (unified
library kinds plus proxy-only types). Agents connect via the hook and
proxy/library APIs; edges show OAuth reads, library fan-in, and blocked
services. Service nodes are colored by status (connected, needs OAuth, expired,
blocked). Photo services note that bytes stay upstream. Use it as a quick
“what is this thing doing?” picture; the Services tab remains the place to
edit permissions.

The **Advanced** tab consolidates operational detail that used to sit below
the map or on separate tabs: unified library dataset cards, recent access
samples, the response cache inventory (with clear), and runtime/auth endpoint
info.

On the **Services** tab, shared **Agent data access** instructions sit above
the filter (Available / Off always; **Editable** only when agents can truly
change that data through Aegis — not mirror backups or picker sessions).
Each service card keeps its own live permissions form under the title; every
change saves immediately to `permissions.json` (the admin overlay). Bootstrap
defaults remain in `aegis.json`; the overlay wins for any service the admin
UI has saved. Keys not shown in the form are preserved on save. Data-type rows
use the Map icons.

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
  -mail-archive mail-archive.db \
  -log-level debug \
  -log-format text
```

CI fails if statement coverage drops below the floor in `.github/workflows/ci.yml`
(currently 80%). Raise the floor when the suite grows; do not lower it to land a
change that only adds untested write paths.

### Project structure

```
aegis/
  main.go           Entry point, CLI flags, route wiring
  config.go         Service/Config types, JSON loading, matching
  atomicfile.go     Atomic temp+rename writes for the permissions overlay
  permissions_overlay.go  Admin permissions.json overlay (issue #71)
  handler.go        Pre-request hook: parse payload, block/modify
  identity.go       Tailscale Serve headers + WhoIs identity resolvers
  models.go         Neutral data types (Playlist, Track, Contact, Photo, CalendarEvent, and their list wrappers)
  library.go        Library providers + thin per-kind handlers
  librarykind.go    LibraryKind registry + shared generic aggregate pipeline
  contacts.go       Contact identity, merge, ContactProvider, /v1/library/contacts
  photos.go         Photo identity/merge, PhotoProvider, /v1/library/photos
  events.go         CalendarEvent identity/merge, CalendarEventProvider, /v1/library/events
  messages.go       Message identity/merge, MessageProvider, /v1/library/messages
  google-gmail.go   Gmail connector (MessageProvider; metadata/snippet library path)
  mail_archive.go   Local SQLite FTS5 mail archive (compressed payloads + search)
  mail_archive_http.go  /v1/mail/search|status|sync|messages routes
  mail_archive_sync.go  Resumable Gmail → archive ingest
  mail_takeout.go   Read-only Google Takeout mbox/zip/directory sync
  match.go          Cross-service name identity + playlist sync merge
  proxy.go          Service proxy: cache, forward, inject tokens
  permissions.go    Agent data-type access levels, path classification, enforcement
  cache.go          In-memory TTL cache with RWMutex
  oauth.go          OAuth2 login/callback/refresh flows
  tokens.go         AES-256-GCM encrypted token storage
  statestore.go     CSRF state validation with TTL
  admin.go          Admin dashboard HTML + summary/cache APIs
  flow.go           Data-flow topology builder + /v1/admin/flow
  SECURITY.md       Tailnet auth model, ACLs, operational guidance
  spotify.go        Spotify connector (neutral playlists + ListUserPlaylists)
  youtube.go        YouTube Music connector (neutral playlists + ListUserPlaylists)
  amazonmusic.go    Amazon Music connector (neutral playlists + ListUserPlaylists)
  google-photos.go  Google Photos Picker connector (user-selected photos; bytes stay upstream)
  google-contacts.go Google Contacts / People API connector (ContactProvider)
  google-calendar.go Google Calendar connector (CalendarEventProvider)
  aegis.example.json   Example configuration
  docs/index.html   Code walkthrough (Go vs Python)
```

## License

Private repository — internal use only.
