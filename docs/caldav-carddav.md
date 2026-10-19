# CalDAV / CardDAV (issue #146)

Aegis talks to CalDAV and CardDAV with one adapter family so a single
self-hosted (or hosted) server can become the home copy for **contacts** and
**events** — the same shape Immich has for photos.

Supported servers: **Nextcloud**, **iCloud** (app-specific password),
**Fastmail**, **Radicale**, **Baïkal**, and anything else that speaks RFC 4791
/ RFC 6352.

| Service ID | Data type | Provider interface | Notes |
|------------|-----------|--------------------|-------|
| `dav-contacts` | `contacts` | `ContactProvider` | vCard 3/4 → neutral `Contact` |
| `dav-calendar` | `events` | `CalendarEventProvider` | iCalendar `VEVENT` → neutral `CalendarEvent`, RRULE expanded in the window |

Aegis never stores address-book or calendar content. Discovery walks
`.well-known/carddav` / `.well-known/caldav` → principal → home-set.

## Credentials

Static Basic-auth credentials, stored like the Immich API key (encrypted at
rest in `service_connections.json` when saved via admin; never returned by
admin/API):

```json
"headers": {
  "X-DAV-Username": "alice",
  "X-DAV-Password": "app-specific-password"
}
```

An `Authorization: Basic …` header is also accepted and decoded.

## Nextcloud

1. Create an app password: **Settings → Security → Devices & sessions →
   Create new app password** (or an account password on a trusted tailnet).
2. Point both services at the Nextcloud origin (not a collection path):

```json
{
  "id": "dav-contacts",
  "endpoint": "https://cloud.example.com",
  "headers": {
    "X-DAV-Username": "alice",
    "X-DAV-Password": "$ENV:DAV_APP_PASSWORD"
  },
  "canonical_for": ["contacts"]
}
```

```json
{
  "id": "dav-calendar",
  "endpoint": "https://cloud.example.com",
  "headers": {
    "X-DAV-Username": "alice",
    "X-DAV-Password": "$ENV:DAV_APP_PASSWORD"
  },
  "canonical_for": ["events"]
}
```

Discovery resolves `https://cloud.example.com/.well-known/carddav` and
`…/caldav`, then the addressbook/calendar home-set. Shared and delegated
calendars are out of scope for this phase.

### Canonical-for example (Nextcloud home, Google mirrored)

When Nextcloud is the home copy and Google stays as a mirrored backup, claim
the kinds on DAV and remove them from Google:

```json
{ "id": "dav-contacts",  "canonical_for": ["contacts"], "...": "..." },
{ "id": "dav-calendar",  "canonical_for": ["events"],   "...": "..." }
```

Then mirror (dry-run first):

```bash
curl -X POST http://localhost:8080/v1/library/mirror \
  -H "Authorization: Bearer $AEGIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"kind":"contacts","source_service":"dav-contacts","target_service":"google","dry_run":true}'

curl -X POST http://localhost:8080/v1/library/mirror \
  -H "Authorization: Bearer $AEGIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"kind":"events","source_service":"dav-calendar","target_service":"google-calendar","dry_run":true}'
```

Drop `dry_run` to write. Writes use `If-Match` / etags so a newer remote edit
is **never** overwritten — the mirror reports it in `conflicts[]` instead of
picking a winner. Per-item etag watermarks land in the copy ledger.

## iCloud

iCloud Contacts and Calendar speak CalDAV/CardDAV with an **app-specific
password** (appleid.apple.com → Sign-In and Security → App-Specific
Passwords). Your Apple ID password will not work.

| Setting | Value |
|---------|-------|
| Endpoint | `https://contacts.icloud.com` (contacts) / `https://caldav.icloud.com` (calendar) |
| Username | Apple ID email |
| Password | App-specific password |

iCloud quirks (best-effort, documented rather than papered over):

- Principal discovery is slower than Nextcloud; first list may take a few
  seconds while `.well-known` redirects settle.
- Rate limits are tighter than self-hosted servers. Aegis caps a single
  mirror at 200 items (`mirrorTrackCap`) and can be re-run for the rest.
- Some iCloud collections only answer `calendar-query` / `addressbook-query`
  REPORT; Aegis falls back to PROPFIND + GET when REPORT is refused.

iCloud is **best-effort** in this phase, not an officially supported target.

## Free/busy

Mirrored and listed CalDAV events feed `GET /v1/library/freebusy` (issue
#128) exactly like Google Calendar. A freebusy-only data grant never sees
titles, attendees, or locations.

## Tests

Connector tests run against an in-process `httptest` DAV mock covering
`.well-known` redirect, PROPFIND multistatus, REPORT (addressbook-query /
calendar-query), GET, and PUT with `If-Match` conflict (412).
