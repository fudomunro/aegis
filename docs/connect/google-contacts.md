# Google Contacts

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create an OAuth 2.0 Client ID (Web application)
3. Add `https://your-aegis/v1/auth/google/callback` as an authorized redirect URI
4. Enable the **People API**
5. Copy credentials into `aegis.json` (see `aegis.example.json`)

Scope: `https://www.googleapis.com/auth/contacts.readonly`. The connector lists
address-book connections via `GET /people/me/connections` and maps them to
neutral `Contact` values. Service-local IDs are People resource names
(`people/c123`).

Contacts are the first cross-type identity: `Photo.people[]` holds the same
`Contact` shape, so a person from this address book can be matched to someone
listed on a photo via email/phone/name. Google Photos itself does not currently
expose face/people labels through the public API, so photo `people[]` stays
empty until a provider supplies them — the merge path is ready.

