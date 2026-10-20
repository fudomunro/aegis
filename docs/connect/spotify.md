# Spotify

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

