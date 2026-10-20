# Connecting services

OAuth and import guides for each named service. After connecting one music
service, call `GET /v1/library/playlists` to confirm the library is live.

| Service | Guide |
|---------|-------|
| Spotify | [spotify.md](spotify.md) |
| YouTube Music | [youtube-music.md](youtube-music.md) |
| Amazon Music | [amazon-music.md](amazon-music.md) |
| Google (Photos, Contacts, Calendar, Gmail, Drive) | [google-contacts.md](google-contacts.md), [google-photos.md](google-photos.md), [google-calendar.md](google-calendar.md), [gmail.md](gmail.md), [google-drive.md](google-drive.md) |
| Immich | [immich.md](immich.md) |
| YNAB | [ynab.md](ynab.md) |
| Quicken import | [quicken.md](quicken.md) |
| Paperless-ngx | [paperless.md](paperless.md) |
| OwnTracks | [owntracks.md](owntracks.md) |
| Google Maps Timeline import | [google-timeline.md](google-timeline.md) |
| Bluesky (photo share) | [bluesky.md](bluesky.md) |
| CalDAV / CardDAV | [caldav-carddav.md](caldav-carddav.md) |
| Local mail archive / Takeout | [mail-archive.md](mail-archive.md) |
| Signal import | [signal.md](signal.md) |

Shared OAuth client setup (redirect URIs, `connections.google`) is under
[Configuration → Provider connections](../configuration.md).
