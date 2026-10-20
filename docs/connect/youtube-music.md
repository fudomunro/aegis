# YouTube Music (Google)

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create an OAuth 2.0 Client ID (Web application)
3. Add redirect URIs for every Google surface you use:
 - `https://your-aegis/v1/auth/youtube-music/callback`
 - `https://your-aegis/v1/auth/google-photos/callback`
 - `https://your-aegis/v1/auth/google/callback`
4. Enable the APIs you need (YouTube Data API v3, People API, Photos Picker API)
5. Put the **same** client ID/secret in the shared `connections.google` block
 (see [Provider connections](../configuration.md))

