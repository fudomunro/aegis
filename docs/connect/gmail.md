# Gmail

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

Agent data is the unified library; content transport serves full bodies only:

```
GET /v1/library/messages
GET /v1/services/google-gmail/messages/{id} # ?format=full includes decoded body
GET /v1/services/google-gmail/threads/{id}
```

Message lists (`/me/messages`) are not agent API — use the library.
This phase is **read-only**. Capabilities do not advertise agent write ops
(send/delete), so the admin permissions form will not offer Editable for
`messages` and mutations stay blocked
([](https://github.com/fudomunro/aegis/issues/61)).
Message bodies stay on Gmail as the **canonical** home copy.

