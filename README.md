# Aperture Guardian

A service proxy for [Tailscale Aperture](https://tailscale.com/docs/aperture) that intercepts LLM requests referencing named services (Spotify, YouTube Music, Amazon Music), mediates access through local proxy endpoints, and enforces rules with OAuth-based authentication and caching.

## How It Works

Guardian runs as two components:

1. **Pre-request hook** — Aperture calls Guardian before forwarding requests to LLM providers. Guardian detects service references in the user's message and injects instructions telling the agent to use Guardian's proxy endpoints instead of external APIs.

2. **Service proxy** — Agents call `GET/POST /v1/services/{id}/...` instead of hitting external APIs directly. Guardian adds OAuth tokens, forwards to the real service, caches responses, and returns the data.

```
Agent → Aperture → Guardian (hook: inject proxy URL) → LLM
Agent → Guardian (proxy) → Spotify API / YouTube Music API / ...
```

## Prerequisites

- **Go 1.21+** (tested with 1.27)
- **Tailscale** with an [Aperture gateway](https://tailscale.com/docs/aperture/get-started) configured
- **OAuth app credentials** for each service you want to connect (see [Setup](#setup))

## Quick Start

```bash
# Clone and build
git clone https://github.com/fudomunro/aperture-guardian.git
cd aperture-guardian
go build -o guardian .

# Create your config (see guardian.example.json)
cp guardian.example.json guardian.json
# Edit guardian.json with your OAuth client IDs and secrets

# Run (recommended: tailnet identity via Tailscale Serve or WhoIs)
./guardian \
  -addr :8080 \
  -base-url https://guardian.your-tailnet.ts.net \
  -auth-mode tailnet \
  -allowed-logins you@example.com \
  -config guardian.json \
  -token-pass your-encryption-passphrase

# Or keep a shared API key for non-tailnet clients
./guardian \
  -addr :8080 \
  -base-url https://guardian.your-tailnet.ts.net \
  -api-key your-secret-key \
  -config guardian.json \
  -token-pass your-encryption-passphrase
```

Guardian starts with these endpoints:
- `POST /v1/pre-request` — Aperture hook
- `GET /v1/library/playlists` — unified playlists across all connected services
- `GET /v1/library/tracks` — unified liked/saved tracks across all connected services
- `GET /v1/library/albums` — unified saved albums across all connected services
- `GET /v1/library/artists` — unified followed/top artists across all connected services
- `GET /v1/library/contacts` — unified contacts across all contact-capable services
- `GET /v1/library/photos` — unified photo metadata across all photo-capable services (bytes stay on the source service)
- `GET/POST /v1/services/{id}/...` — service proxy
- `GET /v1/setup` — web-based setup wizard
- `GET /v1/admin` — admin dashboard for services, tokens, library, and cache

See [SECURITY.md](SECURITY.md) for the Tailscale identity model, recommended ACLs, and why Funnel should stay off.
## Configuration

Guardian is configured via a JSON file. See [`guardian.example.json`](guardian.example.json) for the full format.

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
  "oauth": {
    "client_id": "...",
    "client_secret": "...",
    "auth_url": "https://accounts.spotify.com/authorize",
    "token_url": "https://accounts.spotify.com/api/token",
    "scopes": ["user-library-read", "user-follow-read", "playlist-read-private"]
  }
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
| `oauth` | OAuth2 configuration for user authorization (optional) |

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
- Enforcement is on the service proxy, the unified library endpoint, and the
  `/capabilities` response (denied types/operations are hidden from agents).
- Pre-request injection also lists denied types so agents do not try them.

Known data types: `user`, `tracks`, `albums`, `artists`, `playlists`, `search`,
`player`, `channels`, `library`, `recommendations`, `contacts`, `photos`. Paths that do not map to a
known type fall through to the `default` level.

### Environment variables

| Variable | Flag equivalent | Description |
|----------|----------------|-------------|
| `APERTURE_GUARDIAN_API_KEY` | `-api-key` | API key for authenticating requests (optional in `auto`/`tailnet` modes) |
| `GUARDIAN_AUTH_MODE` | `-auth-mode` | `auto` (default), `tailnet`, or `api-key` |
| `GUARDIAN_ALLOWED_LOGINS` | `-allowed-logins` | Comma-separated Tailscale logins allowed to call the API |
| `GUARDIAN_BASE_URL` | `-base-url` | Public URL of this Guardian instance |
| `GUARDIAN_TOKEN_PASS` | `-token-pass` | Passphrase for token encryption |

### All flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `:8080` | Listen address |
| `-base-url` | | Public base URL (required) |
| `-auth-mode` | `auto` | `auto`, `tailnet`, or `api-key` — see [SECURITY.md](SECURITY.md) |
| `-api-key` | | Shared API key (required only for `api-key` mode) |
| `-allowed-logins` | | Optional allowlist of Tailscale logins |
| `-ts-socket` | auto-detect | tailscaled LocalAPI socket for WhoIs |
| `-config` | `guardian.json` | Config file path |
| `-token-file` | `tokens.json` | Encrypted token storage path |
| `-token-pass` | | Token encryption passphrase (required) |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-log-format` | `json` | `json` or `text` |

### Authentication

Guardian can authenticate callers two ways:

1. **Tailscale identity** (recommended) — via [Serve identity headers](https://tailscale.com/kb/1312/serve) or WhoIs against local `tailscaled`. Same trust model Aperture uses: identity is cryptographic, not a shared secret agents might paste into a chat.
2. **API key** — `Authorization: Bearer <key>` or `X-Api-Key`, for bridges and non-tailnet tools.

Default mode is `auto` (identity if present, else API key). For a solo tailnet, prefer `-auth-mode tailnet` and no key. Full policy guidance lives in [SECURITY.md](SECURITY.md).

## Connecting Services

1. Register an OAuth app with each service (see below for per-service guides)
2. Add the client ID and secret to your `guardian.json`
3. Start Guardian and visit `https://your-guardian/v1/setup`
4. Click **Connect** for each service — you'll be redirected to authorize
5. Tokens are stored encrypted on disk and auto-refresh when expired

### Spotify

1. Go to [Spotify Developer Dashboard](https://developer.spotify.com/dashboard)
2. Create an app
3. Set the redirect URI to `https://your-guardian/v1/auth/spotify/callback`
4. Copy the Client ID and Client Secret into `guardian.json`

Spotify scopes (see `guardian.example.json`):

| Scope | Needed for |
|-------|------------|
| `user-library-read` | Liked tracks (`/v1/library/tracks`), saved albums (`/v1/library/albums`), playlists |
| `user-follow-read` | Followed artists (`/v1/library/artists` via `/me/following`) |
| `playlist-read-private` / `playlist-read-collaborative` | Private and collaborative playlists |
| `user-read-playback-state` | Player state (optional; often denied via permissions) |

**Reconnect after adding scopes.** OAuth tokens keep the scopes from the original consent. After you add `user-follow-read` (or any new scope) to `guardian.json`, visit `/v1/auth/spotify/login` again so Spotify re-issues a token with the expanded grant. Without that, `/v1/library/artists` returns an upstream 403 even though saved tracks still work.

### YouTube Music (Google)

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create an OAuth 2.0 Client ID (Web application)
3. Add `https://your-guardian/v1/auth/youtube-music/callback` as an authorized redirect URI
4. Enable the YouTube Data API v3
5. Copy credentials into `guardian.json`

### Amazon Music (Login with Amazon)

1. Go to [Login with Amazon](https://developer.amazon.com/loginwithamazon/console/site/lwa/overview.html)
2. Create a new application
3. Add `https://your-guardian/v1/auth/amazon-music/callback` as an allowed return URL
4. Copy the Client ID and Client Secret into `guardian.json`

## Integrating with Aperture

Add Guardian as a pre-request hook in your Aperture configuration:

```json
{
  "hooks": {
    "guardian": {
      "url": "https://your-guardian/v1/pre-request",
      "apikey": "your-guardian-api-key",
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
                "name": "guardian",
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

Guardian has **two** callers, and they authenticate differently:

| Leg | Who calls Guardian | How they authenticate |
|-----|--------------------|------------------------|
| **Hook** | Aperture (`POST /v1/pre-request`) | Usually the shared API key in the Aperture hook config (`"apikey"`). In `tailnet` mode Aperture must also present tailnet identity (Serve headers / WhoIs). |
| **Agent data** | The LLM agent (`/v1/library/playlists`, `/v1/services/...`) | Preferred: Tailscale identity from the agent’s host (Serve or WhoIs). Fallback: API key if `auth-mode` is `auto` or `api-key`. |

Agents do **not** need Spotify/YouTube/Amazon OAuth tokens — Guardian attaches those when proxying. With `-auth-mode tailnet` and no API key, agents on the tailnet call Guardian without a shared secret.

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

When the user asks about playlists without naming a service (e.g. "what playlists do I have?"), Guardian injects instructions for the unified library endpoint so agents can answer across every connected service. The same applies to liked songs ("what songs have I liked?") via `/v1/library/tracks`.

### Unified library

```
GET /v1/library/playlists
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's playlists from every connected music service in Guardian's **neutral format**, regardless of whether they live on Spotify, YouTube Music, Amazon Music, etc.

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
| `sources[]` | Service-local copies: `service`, `service_name`, `id`, `url`, `extra` |
| `extra` | Other service-specific fields |

### Cross-service sync (v1)

The library endpoint treats **same-named playlists** on different services as one user-facing playlist: Guardian merges their `sources` and sets `"synced": true`. It does **not** copy tracks between services yet.

Tracks carry unified `artists` / `album` objects. Cross-service matching is **text-based** (`NormalizeIdentityKey`: case, whitespace, strip YouTube “ - Topic”). “Britney Spears” on Spotify and Tidal become one `Artist` with two `sources`. Better entity resolution (MBIDs, ISRC, fuzzy names) is intentionally deferred — tracked in [issue #4](https://github.com/fudomunro/aperture-guardian/issues/4).

Service-level playlist endpoints (`GET /v1/services/{id}/me/playlists`) return the same neutral shape for a single service (one `sources[]` entry). Per-service `by_service` status values: `ok`, `unauthorized`, `error`, `skipped`.

### Unified liked tracks

```
GET /v1/library/tracks
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Returns the user's liked/saved tracks from every connected music service in Guardian's neutral `Track` format. Connectors opt in by implementing `LikedTrackProvider` (`ListUserLikedTracks`). Permission key: `tracks` (also covers proxy paths `/me/tracks` and `/me/liked`).

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

Cross-service merge is **title + primary artist** via `NormalizeIdentityKey` (same v1 quality as playlists). Spotify maps `/me/tracks` (saved library); YouTube Music maps the liked-videos playlist.

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

`Contact` is Guardian's neutral representation of a **person or other entity**
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
3. Add `https://your-guardian/v1/auth/google/callback` as an authorized redirect URI
4. Enable the **People API**
5. Copy credentials into `guardian.json` (see `guardian.example.json`)

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

`Photo` is Guardian's neutral representation of a photo (or video still).
Service identity lives in `sources[]`, same as playlists and contacts.

**Guardian never stores photo bytes.** The authoritative copy stays on a
non-Guardian service marked `canonical` (the minimal slice of
[issue #16](https://github.com/fudomunro/aperture-guardian/issues/16)).
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
verification; unverified apps get 403. Guardian therefore uses the
**[Photos Picker API](https://developers.google.com/photos/picker/guides/get-started-picker)**:
the user explicitly selects photos once; agents only see that selection.

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create or reuse an OAuth 2.0 Client ID (Web application)
3. Add `https://your-guardian/v1/auth/google-photos/callback` as an authorized redirect URI
4. Enable the **Photos Picker API** (not the Library API)
5. Copy credentials into `guardian.json` (see `guardian.example.json`) with scope
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
until the user has picked once). Metadata only — Guardian still never downloads
or stores image bytes; `blob_url` values expire in about an hour.

Ownership / provenance proofs for photos are intentionally deferred:
[issue #20](https://github.com/fudomunro/aperture-guardian/issues/20).

### Service proxy

```
GET/POST /v1/services/{service_id}/{path}
Authorization: Bearer <api-key>   # or tailnet identity in auto/tailnet mode
```

Forwards to the configured upstream endpoint with OAuth tokens. Callers authenticate to Guardian; they do not need the upstream service’s OAuth tokens.
- `X-Guardian-Cache: hit` or `miss` in response headers
- GET responses cached per-service
- POST requests forwarded without caching

### Auth endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/auth/{id}/login` | GET | Start OAuth flow |
| `/v1/auth/{id}/callback` | GET | OAuth callback (redirect target) |
| `/v1/auth/status` | GET | Token status for all services |
| `/v1/auth/{id}` | DELETE | Revoke stored token |
| `/v1/setup` | GET | Web setup wizard |

### Admin dashboard

```
GET /v1/admin              — HTML dashboard (shell only; no secrets)
GET /v1/admin/summary      — redacted services, token status, cache stats
GET /v1/admin/cache        — in-memory cache inventory
POST /v1/admin/cache/clear — clear the response cache
GET /v1/admin/services/{id}/permissions  — current agent permission map
PUT /v1/admin/services/{id}/permissions  — replace the map (writes guardian.json)
```

`/v1/admin` loads in the browser and fetches the JSON APIs. On a tailnet with
Serve identity the page works with no extra setup. In API-key mode, enter the
key when prompted (kept only in that tab’s `sessionStorage`).

Each service card has an **Edit permissions** form: set levels per data type,
add custom types, clear to unrestricted, or save. Saves write the config file
and clear the response cache so denials take effect immediately.

Photo connectors that expose a picker session create operation (Google Photos)
also show **Pick photos…** on the service card: it creates a session, opens
Google’s picker in a new tab, polls until the user finishes selecting, then
refreshes the photos library view. (Session create is a POST — the service’s
`photos` permission must allow write.)

Admin APIs use the same auth as the rest of Guardian. Secrets never appear in
admin responses: client secrets, access tokens, and refresh tokens are omitted;
only presence flags, scopes, and expiry status are shown.

## Development

```bash
# Run tests
go test ./...

# Run with verbose output
go test -v ./...

# Build
go build -o guardian .

# Run with text logging for development
./guardian \
  -addr :8080 \
  -base-url http://localhost:8080 \
  -api-key dev-key \
  -token-pass dev-pass \
  -log-level debug \
  -log-format text
```

### Project structure

```
aperture-guardian/
  main.go           Entry point, CLI flags, route wiring
  config.go         Service/Config types, JSON loading, matching
  handler.go        Pre-request hook: parse payload, block/modify
  identity.go       Tailscale Serve headers + WhoIs identity resolvers
  models.go         Neutral data types (Playlist, Track, Contact, Photo, and their list wrappers)
  library.go        Library providers + thin per-kind handlers
  librarykind.go    LibraryKind registry + shared generic aggregate pipeline
  contacts.go       Contact identity, merge, ContactProvider, /v1/library/contacts
  photos.go         Photo identity/merge, PhotoProvider, /v1/library/photos
  match.go          Cross-service name identity + playlist sync merge
  proxy.go          Service proxy: cache, forward, inject tokens
  permissions.go    Agent data-type access levels, path classification, enforcement
  cache.go          In-memory TTL cache with RWMutex
  oauth.go          OAuth2 login/callback/refresh flows
  tokens.go         AES-256-GCM encrypted token storage
  statestore.go     CSRF state validation with TTL
  setup.go          Web-based setup wizard
  admin.go          Admin dashboard HTML + summary/cache APIs
  SECURITY.md       Tailnet auth model, ACLs, operational guidance
  spotify.go        Spotify connector (neutral playlists + ListUserPlaylists)
  youtube.go        YouTube Music connector (neutral playlists + ListUserPlaylists)
  amazonmusic.go    Amazon Music connector (neutral playlists + ListUserPlaylists)
  google-photos.go  Google Photos Picker connector (user-selected photos; bytes stay upstream)
  google-contacts.go Google Contacts / People API connector (ContactProvider)
  guardian.example.json   Example configuration
  docs/index.html   Code walkthrough (Go vs Python)
```

## License

Private repository — internal use only.
