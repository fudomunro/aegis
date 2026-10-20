# Google Calendar

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create or reuse an OAuth 2.0 Client ID (Web application)
3. Add `https://your-aegis/v1/auth/google-calendar/callback` as an authorized redirect URI
4. Enable the **Google Calendar API**
5. Copy credentials into `aegis.json` (see `aegis.example.json`) with scope
 `https://www.googleapis.com/auth/calendar.readonly`
6. Authorize at `/v1/auth/google-calendar/login` (shares the Google OAuth
 connection with YouTube Music, Photos, and Contacts; tokens are stored per
 service id)

The connector lists calendars via `GET /users/me/calendarList`, then events
via `GET /calendars/{id}/events` with `singleEvents=true` and
`orderBy=startTime`. The library window defaults to **30 days back + 90 days
ahead** so "my calendar" covers recent meetings and upcoming plans; proxy
calls may pass explicit `timeMin` / `timeMax`. Free/busy-only calendars are
skipped. A failure on one calendar is logged and skipped (fail-open) so other
calendars still appear.

Agent data is the unified library. Calendar list/proxy paths are
not agent API.

```
GET /v1/library/events
GET /v1/library/freebusy
```

This phase is **read-only**. Capabilities do not advertise agent write ops
(create/update/delete events), so the admin permissions form will not offer
Editable for `events` and mutations stay blocked
([](https://github.com/fudomunro/aegis/issues/61)).
Event content stays on Google Calendar.

