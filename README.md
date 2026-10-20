# Aegis

A personal data service for [Tailscale Aperture](https://tailscale.com/docs/aperture) that intercepts LLM requests referencing named services (Spotify, YouTube Music, Amazon Music, Google Photos/Contacts/Calendar/Gmail/Drive, Immich, Paperless-ngx, YNAB, Quicken, OwnTracks, Signal, Timeline, Bluesky, CalDAV/CardDAV), serves the user's data from a type-centric library, and enforces **data grants** with OAuth-based authentication.

> Looking for the pitch rather than the manual? The marketing site is at <https://fudomunro.github.io/aegis/> ([`site/`](site/)).

## How it works

Aegis runs as two components:

1. **Pre-request hook** — Aperture calls Aegis before forwarding requests to LLM providers. Aegis detects service references and injects instructions that point the agent at Aegis's unified library (`/v1/library/{kind}`) instead of external APIs.
2. **Unified library + content transport** — Agents call `GET /v1/library/{kind}` for the user's data (playlists, tracks, photos, messages, …). A narrow `/v1/services/{id}/...` transport remains only for content the library cannot store (photo bytes, full message bodies, playlist items) and local import.

```
Agent → Aperture → Aegis (hook: inject library URLs) → LLM
Agent → Aegis (/v1/library, content transport) → provider APIs
```

## Prerequisites

- **Go 1.27.1** (matches `go.mod`)
- **Tailscale** with an [Aperture gateway](https://tailscale.com/docs/aperture/get-started)
- **OAuth app credentials** for each service you want to connect (see [Connecting services](docs/connect/))

## Quick start

```bash
# Clone and build
git clone https://github.com/fudomunro/aegis.git
cd aegis
go build -o aegis.

# Local state lives under one data directory:
# macOS: ~/Library/Application Support/Aegis/
# Linux: ${XDG_DATA_HOME:-~/.local/share}/aegis/
# Override anytime with -data-dir /path or AEGIS_DATA_DIR.
mkdir -p ~/Library/Application\ Support/Aegis # or the Linux path above
cp aegis.example.json ~/Library/Application\ Support/Aegis/aegis.json
# Edit aegis.json with your OAuth client IDs and secrets

# Run (recommended: tailnet identity via Tailscale Serve or WhoIs)
./aegis \
 -addr:8080 \
 -base-url https://aegis.your-tailnet.ts.net \
 -auth-mode tailnet \
 -allowed-logins you@example.com \
 -token-pass your-encryption-passphrase
```

Then **connect one service** (for example [Spotify](docs/connect/spotify.md)) and call the library:

```bash
curl -s https://aegis.your-tailnet.ts.net/v1/library/playlists
```

You should see neutral playlist objects with `sources[]` provenance. Vocabulary (service vs connector, data grant, canonical home, content transport) is defined in [`docs/glossary.md`](docs/glossary.md).

## Documentation

| Doc | What it covers |
|-----|----------------|
| [docs/connect/](docs/connect/) | Per-service OAuth and import guides |
| [docs/api.md](docs/api.md) | Agent-facing HTTP surface (library, hook, transport) |
| [docs/configuration.md](docs/configuration.md) | Config schema, data grants, flags, token encryption |
| [docs/observability.md](docs/observability.md) | Health, metrics, audit log |
| [SECURITY.md](SECURITY.md) | Tailnet auth model, ACLs, operational guidance |
| [docs/threat-model.md](docs/threat-model.md) | Assets, actors, threats, residual risks |
| [docs/glossary.md](docs/glossary.md) | Canonical product vocabulary |
| [docs/caldav-carddav.md](docs/caldav-carddav.md) | Nextcloud/iCloud/Fastmail DAV setup |
| [docs/configuring-aperture.md](docs/configuring-aperture.md) | Aperture operator guide |
| [docs/user-defined-types.md](docs/user-defined-types.md) | Schema packs / user-defined types |
| [docs/adding-data-types.md](docs/adding-data-types.md) | Checklist for new library data types |
| [docs/development.md](docs/development.md) | Tests, project structure, sample data |
| [CHANGELOG.md](CHANGELOG.md) | History and design decisions |

## License

[BSD 3-Clause](LICENSE). Same family of license Tailscale uses for much of its open source work.
