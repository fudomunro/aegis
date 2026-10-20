# Google Photos (Picker API)

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

## User opens picker_uri in a browser and selects photos

GET /v1/services/google-photos/picker/sessions/{id}
 → poll until media_items_set is true

GET /v1/services/google-photos/picker/mediaItems?sessionId={id}
 → neutral Photo[] (also cached as the last pick for /v1/library/photos)
```

`/v1/library/photos` returns the **last completed Google Photos pick** (empty
until the user has picked once). Metadata only — Aegis still never downloads
or stores image bytes; `blob_url` values expire in about an hour.

Ownership / provenance proofs for photos are intentionally deferred:
[](https://github.com/fudomunro/aegis/issues/20). A **Bluesky share
PoC** is available so a published photo carries a lightweight claim (content
hash + public post locator) — see [Photo share to Bluesky](bluesky.md).

