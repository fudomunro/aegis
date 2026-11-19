# Aegis

A personal data service for [Tailscale Aperture](https://tailscale.com/docs/aperture) that intercepts LLM requests referencing named services (Spotify, YouTube Music, Amazon Music, Apple Music, Google Photos/Contacts/Calendar/Gmail/Drive, Immich, Paperless-ngx, YNAB, Quicken, OwnTracks, Signal, LinkedIn, Timeline, Bluesky, CalDAV/CardDAV, GitHub), serves the user's data from a kind-centric library, and enforces **data grants** with OAuth-based authentication.

> Looking for the pitch rather than the manual? The marketing site is at <https://fudomunro.github.io/aegis/> ([`site/`](site/)).

## How it works

Aegis runs as two components:

1. **Pre-request hook** — Aperture calls Aegis before forwarding requests to LLM providers. Aegis detects service references and injects instructions that point the agent at Aegis's unified library (`/me/{kind}`) instead of external APIs.
2. **Unified library + content transport** — Agents call `GET /me/{kind}` for the user's data (playlists, tracks, photos, messages, …). Add `?q=` to search a kind's fields instead of paging the whole list. A narrow `/v1/services/{id}/...` transport remains only for content the library cannot store (photo bytes, full message bodies, playlist items) and local import.

```
Agent → Aperture → Aegis (hook: inject library URLs) → LLM
Agent → Aegis (/me, content transport) → provider APIs
```

## Prerequisites

- **Go 1.27.1** (matches `go.mod`)
- **Tailscale** with an [Aperture gateway](https://tailscale.com/docs/aperture/get-started)
- **OAuth app credentials** for each service you want to connect (see [Connecting services](docs/connect/)). Protocol mail sources ([IMAP](docs/connect/imap.md) / [POP3](docs/connect/pop3.md)) need only a username and password — no OAuth app.

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
curl -s https://aegis.your-tailnet.ts.net/me/playlists
```

You should see neutral playlist objects with `sources[]` provenance. Vocabulary (service vs connector, data grant, canonical home, content transport) is defined in [`docs/glossary.md`](docs/glossary.md).

### Setup: registering a Microsoft 365 / Outlook app

Calendar, contacts, and mail from Microsoft 365 / Outlook.com come through one
Entra app registration ([entra.microsoft.com](https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade) →
App registrations → New registration). Register each surface's redirect URI
(`https://your-aegis/v1/auth/microsoft-calendar/callback`,
`.../microsoft-contacts/callback`, and/or `.../microsoft-mail/callback`), add
the Microsoft Graph delegated permissions `Calendars.Read`, `Contacts.Read`,
and/or `Mail.Read` plus `offline_access`, and copy the client id (and secret,
if any) into the top-level `microsoft` connection in `aegis.json` with
`"pkce": true`. Work tenants may require admin consent — without it the
surfaces report `unauthorized` and nothing else breaks. Full steps, sync
directions, and the free/busy projection are in
[`docs/connect/microsoft-calendar.md`](docs/connect/microsoft-calendar.md),
[`docs/connect/microsoft-contacts.md`](docs/connect/microsoft-contacts.md),
and [`docs/connect/microsoft-mail.md`](docs/connect/microsoft-mail.md).

### Setup: connecting Apple Music

Apple Music needs an [Apple Developer Program](https://developer.apple.com/programs/)
membership for the MusicKit key (App Store Connect → Users and Access →
Integrations → MusicKit) and a Music User Token issued in a browser through
MusicKit JS. Aegis signs the developer token itself and stores both
credentials encrypted; the admin setup panel walks through each step. Apple
Music joins the mirror/backup surface in both directions, matching on ISRC.
Full steps and the known limits (library vs "loved" tracks) are in
[`docs/connect/apple-music.md`](docs/connect/apple-music.md).

## Documentation

| Doc | What it covers |
|-----|----------------|
| [docs/connect/](docs/connect/) | Per-service connection guides: OAuth, protocol mail (IMAP/POP3), and import |
| [docs/api.md](docs/api.md) | Agent-facing HTTP surface (library, hook, transport) |
| [docs/configuration.md](docs/configuration.md) | Config schema, data grants, flags, token encryption |
| [docs/data-ownership.md](docs/data-ownership.md) | Per-kind data-ownership completeness feedback |
| [docs/observability.md](docs/observability.md) | Health, metrics, audit log |
| [SECURITY.md](SECURITY.md) | Auth model (tailnet and external access), ACLs, operational guidance |
| [docs/external-access.md](docs/external-access.md) | Publishing the API listener: split listeners, invite tokens, deny-by-default grants |
| [docs/threat-model.md](docs/threat-model.md) | Assets, actors, threats, residual risks |
| [docs/glossary.md](docs/glossary.md) | Canonical product vocabulary |
| [docs/caldav-carddav.md](docs/caldav-carddav.md) | Nextcloud/iCloud/Fastmail DAV setup |
| [docs/configuring-aperture.md](docs/configuring-aperture.md) | Aperture operator guide |
| [docs/user-defined-kinds.md](docs/user-defined-kinds.md) | Schema packs / user-defined kinds |
| [docs/adding-kinds.md](docs/adding-kinds.md) | Checklist for new library kinds |
| [docs/development.md](docs/development.md) | Tests, project structure, sample data |
| [CHANGELOG.md](CHANGELOG.md) | History and design decisions |

## License

[BSD 3-Clause](LICENSE). Same family of license Tailscale uses for much of its open source work.
