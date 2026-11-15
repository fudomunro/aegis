# Apple Music

Library playlists, tracks, albums, and artists from the
[Apple Music API](https://developer.apple.com/documentation/applemusicapi),
plus mirror/converge placement that matches on ISRC. Apple Music is not an
OAuth provider: Aegis signs a MusicKit **developer token** from your key and
sends your **Music User Token** beside it. Neither credential is ever shown
to agents.

## Setup

**An [Apple Developer Program](https://developer.apple.com/programs/)
membership is required** — the MusicKit key lives in App Store Connect and
cannot be created on a free account.

1. **Create the MusicKit key**: [App Store Connect → Users and Access →
   Integrations → MusicKit](https://appstoreconnect.apple.com/access/integrations/api)
   → generate a key, download the `.p8` file (offered once), and copy the
   **Key ID** and **Issuer ID** shown beside it.
2. **Get a Music User Token**: Apple issues it in a browser through MusicKit
   JS — there is no redirect-code flow. The admin setup panel
   (`/v1/admin/setup/apple-music`) carries a copyable MusicKit JS snippet:
   save it as a local HTML page, replace `PASTE_DEVELOPER_TOKEN` with a
   developer token, sign in with the user's Apple ID, and copy the token it
   prints.
3. **Save both halves** in the admin setup panel (or the service headers
   below). Aegis mints the developer token itself (ES256, one-hour
   lifetime, cached) from the `.p8`; nothing is pasted per request.

```json
{
  "id": "apple-music",
  "name": "Apple Music",
  "endpoint": "https://api.music.apple.com/v1",
  "headers": {
    "X-Apple-Music-Key-Id": "<music-kit-key-id>",
    "X-Apple-Music-Issuer-Id": "<apple-developer-issuer-id>",
    "X-Apple-Music-Private-Key": "<full .p8 file contents>",
    "Music-User-Token": "<music-user-token>"
  }
}
```

Header values are stored encrypted (`tokens.json`) and never echoed back.

## Token lifetime

The developer token is minted and cached by Aegis — no operator action. The
Music User Token is long-lived with no refresh step; the user can revoke it
from their Apple ID, and when Apple rejects it the surface reports
`unauthorized` until a new token is saved. There is nothing to re-authorize
in a browser unless that happens.

## What Aegis reads and writes

| Kind | Read | Write |
|------|------|-------|
| playlists | library playlists | create, append, rename |
| tracks | library songs | add to library (save) |
| albums | library albums | add to library (save) |
| artists | library artists | — (Apple Music has no follow graph) |

**Liked/saved tracks map to the library, not to "loved".** Apple Music can
*favorite* ("love") a song, but favorites are not enumerable through the
documented API, so a mirror could never read its own writes back. Aegis uses
library songs both ways: reading `GET /v1/library/tracks` and writing adds to
the library. Placement is create-only — a mirror never un-saves.

## Mirror and backup

Apple Music is a full mirror participant in both directions:

- **As a target**, placement matches **ISRC first** (`filter[isrc]` catalog
  lookup — exact), then a duration-guarded name search. A source that carries
  ISRCs (Spotify, Apple Music itself) places with far fewer skips than
  name-only matching.
- **As a source**, library songs carry their catalog id; Aegis batch-resolves
  those to ISRCs through the catalog so the other side can match durably
  instead of by name.

Playlists and liked/saved tracks copy through the queued mirror jobs in the
admin Data sync panel, and copies are stamped in the copy ledger so "is my
backup current?" answers from freshness checks without re-syncing.

## Known limits

- An Apple Developer Program membership is required to self-host.
- "Loved"/favorite songs can be written but not listed; Aegis stays on the
  library for read-your-write consistency (above).
- Library writes (playlist create, append, library add) are supported;
  Apple's documented rate limits apply and the mirror paces itself with
  bounded pages (200 tracks per step).
- Catalog search is used only internally for placement matching — it is not
  agent API.

Agent data is the unified library:

```
GET /v1/library/playlists
GET /v1/library/tracks
GET /v1/library/albums
GET /v1/library/artists
```

Full playlist contents ride the content transport
(`GET /v1/services/apple-music/playlists/{id}`).
