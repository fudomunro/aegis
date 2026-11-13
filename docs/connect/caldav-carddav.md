# CalDAV / CardDAV (Nextcloud, iCloud, Fastmail)

One adapter family for contacts and calendars on open standards.
`dav-contacts` implements `ContactProvider` (vCard 3/4 → `Contact`);
`dav-calendar` implements `CalendarEventProvider` (iCalendar `VEVENT` →
`CalendarEvent`, with RRULE expansion in the requested window). Auth is HTTP
Basic username + app-specific password in static headers (Immich-style
credentials). Discovery walks `.well-known/carddav` / `caldav`. Aegis never
stores address-book or calendar content.

```json
{
  "id": "dav-contacts",
  "name": "DAV Contacts",
  "endpoint": "https://cloud.example.com",
  "headers": {
    "X-DAV-Username": "alice",
    "X-DAV-Password": "$ENV:DAV_APP_PASSWORD"
  },
  "canonical_for": ["contacts"],
}
```

```json
{
  "id": "dav-calendar",
  "name": "DAV Calendar",
  "endpoint": "https://cloud.example.com",
  "headers": {
    "X-DAV-Username": "alice",
    "X-DAV-Password": "$ENV:DAV_APP_PASSWORD"
  },
  "canonical_for": ["events"],
}
```

When Nextcloud (or another DAV server) is the home copy, claim
`canonical_for: ["contacts"]` / `["events"]` here and remove those keys from
Google. With both sources connected, convergence fans the kind out from the
canonical home to the other write-capable members: writes use `If-Match` /
etags so a newer remote edit is never overwritten — a member-side edit
surfaces as a conflict to accept or reject. Per-item etag watermarks land in
the copy ledger for incremental sync. Mirrored CalDAV
events feed `GET /v1/library/freebusy`.

**iCloud** needs an app-specific password (appleid.apple.com → App-Specific
Passwords); endpoint is `https://contacts.icloud.com` /
`https://caldav.icloud.com`. Best-effort in this phase — see
[`docs/caldav-carddav.md`](../caldav-carddav.md) for Nextcloud/iCloud setup
notes and the canonical-for example.

