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

# Run
./guardian \
  -addr :8080 \
  -base-url https://guardian.your-tailnet.ts.net \
  -api-key your-secret-key \
  -config guardian.json \
  -token-pass your-encryption-passphrase
```

Guardian starts with three endpoints:
- `POST /v1/pre-request` — Aperture hook
- `GET /v1/library/playlists` — unified playlists across all connected services
- `GET/POST /v1/services/{id}/...` — service proxy
- `GET /v1/setup` — web-based setup wizard

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
    "scopes": ["user-library-read", "playlist-read-private"]
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
| `oauth` | OAuth2 configuration for user authorization (optional) |

### Environment variables

| Variable | Flag equivalent | Description |
|----------|----------------|-------------|
| `APERTURE_GUARDIAN_API_KEY` | `-api-key` | API key for authenticating requests |
| `GUARDIAN_BASE_URL` | `-base-url` | Public URL of this Guardian instance |
| `GUARDIAN_TOKEN_PASS` | `-token-pass` | Passphrase for token encryption |

### All flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `:8080` | Listen address |
| `-base-url` | | Public base URL (required) |
| `-api-key` | | API key for auth (required) |
| `-config` | `guardian.json` | Config file path |
| `-token-file` | `tokens.json` | Encrypted token storage path |
| `-token-pass` | | Token encryption passphrase (required) |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-log-format` | `json` | `json` or `text` |

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

## API Reference

### Pre-request hook

```
POST /v1/pre-request
Authorization: Bearer <api-key>
```

Receives Aperture's `pre_request` payload. Returns one of:
- `{"action": "allow"}` — no service matched
- `{"action": "modify", "body": {...}}` — LLM request with injected proxy instructions
- `{"action": "block", "message": "..."}` — request rejected (403)

When the user asks about playlists without naming a service (e.g. "what playlists do I have?"), Guardian injects instructions for the unified library endpoint so agents can answer across every connected service.

### Unified library

```
GET /v1/library/playlists
Authorization: Bearer <api-key>
```

Returns the user's playlists from every connected music service in Guardian's **neutral format**, regardless of whether they live on Spotify, YouTube Music, Amazon Music, etc.

```json
{
  "playlists": [
    {
      "service": "spotify",
      "service_name": "Spotify",
      "id": "37i9dQZF1DXcBWIGoYBM5M",
      "name": "Today's Top Hits",
      "description": "The hottest tracks right now.",
      "owner": "Spotify",
      "track_count": 50,
      "url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
      "extra": { "uri": "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", "public": true }
    },
    {
      "service": "youtube-music",
      "service_name": "YouTube Music",
      "id": "PLxyz...",
      "name": "Focus Mix",
      "owner": "Alice",
      "track_count": 42,
      "url": "https://www.youtube.com/playlist?list=PLxyz...",
      "extra": { "published_at": "2024-01-15T00:00:00Z" }
    }
  ],
  "total": 2,
  "by_service": {
    "spotify": { "status": "ok", "count": 1 },
    "youtube-music": { "status": "ok", "count": 1 },
    "amazon-music": { "status": "unauthorized", "error": "not authorized — connect at /v1/auth/amazon-music/login" }
  }
}
```

Neutral playlist fields (always present unless noted):

| Field | Description |
|-------|-------------|
| `service` | Connector ID (`spotify`, `youtube-music`, `amazon-music`) |
| `service_name` | Human-readable service label |
| `id` | Service-local playlist ID (use with that service's `/playlists/{id}`) |
| `name` | User-facing playlist title |
| `description` | Playlist description, if any |
| `owner` | Owner display name, when known |
| `track_count` | Number of items in the playlist |
| `url` | Deep link to the playlist on the service, when available |
| `extra` | Service-specific fields that don't fit the neutral shape |

Service-level playlist endpoints (`GET /v1/services/{id}/me/playlists`) return the same neutral shape for a single service. Per-service `by_service` status values: `ok`, `unauthorized`, `error`, `skipped`.

### Service proxy

```
GET/POST /v1/services/{service_id}/{path}
Authorization: Bearer <api-key>
```

Forwards to the configured upstream endpoint with OAuth tokens.
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
  models.go         Neutral data types (Playlist, Track, PlaylistList)
  library.go        Cross-service library endpoint + PlaylistProvider
  proxy.go          Service proxy: cache, forward, inject tokens
  cache.go          In-memory TTL cache with RWMutex
  oauth.go          OAuth2 login/callback/refresh flows
  tokens.go         AES-256-GCM encrypted token storage
  statestore.go     CSRF state validation with TTL
  setup.go          Web-based setup wizard
  spotify.go        Spotify connector (neutral playlists + ListUserPlaylists)
  youtube.go        YouTube Music connector (neutral playlists + ListUserPlaylists)
  amazonmusic.go    Amazon Music connector (neutral playlists + ListUserPlaylists)
  guardian.example.json   Example configuration
  docs/index.html   Code walkthrough (Go vs Python)
```

## License

Private repository — internal use only.
