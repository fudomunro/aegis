# API reference

Agent-facing HTTP surface. Connection and import guides: [`docs/connect/`](connect/).
Configuration: [`docs/configuration.md`](configuration.md).

## Pre-request hook

```
POST /v1/pre-request
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Receives Aperture's `pre_request` payload. Returns one of:
- `{"action": "allow"}` — no service matched, or the LLM request already carries the Aegis instructions
- `{"action": "modify", "request_body": {...}}` — LLM request with injected proxy instructions (Aperture requires the `request_body` field)
- `{"action": "block", "message": "..."}` — request rejected (403)

Instruction injection understands three provider wire shapes:

| Wire shape | How Aegis detects it | Where instructions go |
|------------|----------------------|------------------------|
| OpenAI chat completions | top-level `messages` | prepended `{"role":"system"}` message |
| Anthropic Messages | top-level `system` and/or `metadata` provider/model (`anthropic`, `claude`) | top-level `system` (string or content blocks) |
| OpenAI Responses | top-level `input` (without `messages`) | prepended system item in `input` (or `instructions` when `input` is absent) |

Injection is idempotent: if the same instruction block is already present, Aegis
returns `allow` and leaves the body unchanged. Unrecognized body shapes fail open
(`allow`) and are counted in hook health as `unrecognized_body_shape`.

When the user asks about playlists without naming a service (e.g. "what playlists do I have?"), Aegis injects instructions for the unified library endpoint so agents can answer across every connected service. The same applies to liked songs ("what songs have I liked?") via `/v1/library/tracks`.

## Unified library

```
GET /v1/library/playlists
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Returns the user's playlists from every connected music service in Aegis's **neutral format**, regardless of whether they live on Spotify, YouTube Music, Amazon Music, etc.

```json
{
 "playlists": [
 {
 "name": "Today's Top Hits",
 "description": "The hottest tracks right now.",
 "owner": "Spotify",
 "track_count": 50,
 "sources": [
 {
 "service": "spotify",
 "service_name": "Spotify",
 "id": "37i9dQZF1DXcBWIGoYBM5M",
 "url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
 "extra": { "uri": "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", "public": true }
 }
 ]
 },
 {
 "name": "Road Trip",
 "track_count": 15,
 "sources": [
 {
 "service": "spotify",
 "service_name": "Spotify",
 "id": "sp-roadtrip",
 "url": "https://open.spotify.com/playlist/sp-roadtrip"
 },
 {
 "service": "youtube-music",
 "service_name": "YouTube Music",
 "id": "PLxyz...",
 "url": "https://www.youtube.com/playlist?list=PLxyz...",
 "extra": { "published_at": "2024-01-15T00:00:00Z" }
 }
 ]
 }
 ],
 "total": 2,
 "synced": true,
 "by_service": {
 "spotify": { "status": "ok", "count": 2 },
 "youtube-music": { "status": "ok", "count": 1 },
 "amazon-music": { "status": "unauthorized", "error": "not authorized — connect at /v1/auth/amazon-music/login" }
 }
}
```

Neutral playlist fields (user-facing):

| Field | Description |
|-------|-------------|
| `name` | Playlist title |
| `description` | Playlist description, if any |
| `owner` | Owner display name, when known |
| `track_count` | Item count (max across synced sources) |
| `sources[]` | Service-local copies: `service`, `service_name`, `id`, `url`, `canonical`, `copy`, `extra` |
| `extra` | Other service-specific fields |

## Cross-service sync (v1)

The library endpoint treats **same-named playlists** on different services as one user-facing playlist: Aegis merges their `sources` and sets `"synced": true`. It does **not** copy tracks between services yet.

Tracks carry unified `artists` / `album` objects. Cross-service matching prefers a durable **`entity_id`** when a service provides one or Aegis resolves one (track **ISRC** or MusicBrainz **recording** MBID, album **UPC** or **release-group** MBID, artist **MBID**); otherwise it falls back to **text** (`NormalizeIdentityKey`) (`match: "name"`). MusicBrainz lookups are budgeted and cached; failures keep name matching.

**Playlists** have no catalog IDs. Same-name playlists still merge (`match: "name"`), but users can confirm identity explicitly:

```
GET /v1/library/links
POST /v1/library/links {"kind":"playlist","sources":[{"service":"spotify","id":"…"},{"service":"youtube-music","id":"…"}]}
DELETE /v1/library/links/{id}
```

Linked playlists merge even when names differ (`match: "link"`). Links persist in `entity_links.json` under the [data directory](configuration.md) (`-link-file` overrides).

**Merge review:** `GET /v1/library/review` lists multi-source entities joined by **name only** (no shared ISRC/UPC/MBID/link) so they can be checked or explicitly linked.

## Migration from name-key merge

1. Library responses now include `match` (`isrc` / `upc` / `mbid` / `link` / `name`) and `entity_id` when known.
2. Name-only joins still happen; they are marked `match: "name"` and appear in `/v1/library/review`.
3. Prefer durable IDs for identity; treat name merges as provisional until reviewed or linked.
4. Clients that only compared display names should prefer `entity_id` + `sources[]` instead.

Tracked in [](https://github.com/fudomunro/aegis/issues/4).

Service-level playlist lists (`GET /v1/services/{id}/me/playlists`) are **not** agent API. Per-service status values in library `by_service` remain: `ok`, `unauthorized`, `error`, `skipped`.

## Library source overview

Aggregate library responses enrich each `by_service` entry so clients can render a **source overview** — relative-sized boxes for every source of that data type (admin Library does this at the top of each kind):

| Field | Description |
|-------|-------------|
| `name` | Human-readable service label |
| `count` | Items returned (or archive inventory size for mail source rows) |
| `status` / `truncated` / `error` | Fetch health (unchanged) |
| `last_synced_at` | When Aegis last ingested or fetched this source, if known |
| `canonical` | Configured home copy for this kind (`canonical_for`,) |
| `role` | `"canonical"` / `"mirror"` when the kind has a configured home |
| `parent` | Optional nest key (e.g. `google-takeout` under `google-gmail` for messages) |

For messages, when the local mail archive knows both, `by_service` also includes separate `google-gmail` and `google-takeout` rows with archive inventory counts and per-source `last_synced_at`, so the overview can show Gmail and Google Takeout side by side (with Takeout nested under Gmail). The `mail-archive` fetch target remains for compatibility.

## Pagination and filters

Every `GET /v1/library/{kind}` (built-in and user-defined) shares one agent-facing page window so large libraries stay inside an LLM context:

| Request | Meaning |
|---------|---------|
| `limit` | Page size. Default **100**, max **500**. Out-of-range values clamp. |
| `cursor` | Opaque continue token from the previous response. Omit for the first page. Invalid cursor → `400`. |

| Response field | Meaning |
|----------------|---------|
| `total` | Full **filtered** count for this request (not the page length). |
| `next_cursor` | Pass as the next `cursor`. **Omitted when the walk is complete.** |
| `truncated` | Upstream page-cap incompleteness (see below) — **not** “more pages in this response.” |
| `synced` / `by_service` | Multi-source merge flag and per-service fetch status (unchanged). |

Time filters are always **`after` / `before`** (inclusive lower / exclusive upper). Kind-specific filters stay kind-specific (`q`, `tag`, `account`, `currency`, `locality`, `kind`, …). Mail **`from` / `to` are sender/recipient addresses only** — never time bounds. `/v1/mail/search` accepts the same `limit` + `cursor` (and still accepts `offset` for the admin UI).

Walk the full set by repeating the request with `next_cursor` until it is omitted. Do not stop at the first page and do not treat a short page as “that’s everything” when `next_cursor` is present.

## Sync completeness

Aegis aims to surface a user’s **full** library, not a prefix. Two layers matter:

1. **Pagination safety valve** — connectors and proxy auto-pagination follow up to `libraryPageCap` pages (currently 200, about 10k items at page size 50 / 20k at 100). That cap exists only to stop a looping upstream; it is not a product limit. When a connector hits it with more pages still upstream, it sets `by_service[service].truncated = true`, and the library response also sets a top-level `"truncated": true`. Proxy auto-pagination responses set `"truncated": true` in the combined payload the same way.

2. **Provider / API gaps** — some datasets are incomplete for reasons Aegis cannot fix by paginating harder:

| Gap | Why | How it is reported |
|-----|-----|--------------------|
| **Google Photos full library** | Library API reads require Google app verification; unverified apps get 403. Aegis uses the Picker API instead — only the user’s last selection. | Empty library until a pick; system instructions tell agents to run the picker flow. Not a `truncated` flag — the library is intentionally a selection, not a full dump. |
| **YouTube Music albums** | No public saved-albums API. | `/v1/library/albums` simply has no YouTube source. |
| **Amazon Music** | No public developer REST API; playlist pagination may return an unfollowed `nextToken`. | When `nextToken` is present and not followed, `truncated` is set rather than claiming completeness. |
| **Playlist track bodies** | Library playlist entries carry `track_count` and metadata, not every track object. | `track_count` is authoritative; fetch items via content transport `GET /v1/services/{id}/playlists/{playlistId}/items`. |
| **Cross-service entity resolution** | Tracks merge on ISRC when present; name+artist is the fallback. Artist MBIDs / playlist links not yet implemented. | Partial — [](https://github.com/fudomunro/aegis/issues/4). |

**Always check `truncated` before answering “is this everything?”** Library system instructions tell agents to do the same. Admin Library badges turn orange and say `truncated` when a service hit its page cap.

## Unified liked tracks

```
GET /v1/library/tracks
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Returns the user's liked/saved tracks from every connected music service in Aegis's neutral `Track` format. Connectors opt in by implementing `LikedTrackProvider` (`ListUserLikedTracks`). Permission key: `tracks` (also covers proxy paths `/me/tracks` and `/me/liked`).

```json
{
 "tracks": [
 {
 "name": "Blinding Lights",
 "artists": [{ "name": "The Weeknd", "sources": [{ "service": "spotify", "name": "The Weeknd" }] }],
 "album": { "name": "After Hours", "sources": [{ "service": "spotify", "id": "al1", "name": "After Hours" }] },
 "sources": [
 { "service": "spotify", "service_name": "Spotify", "id": "t1", "url": "https://open.spotify.com/track/t1" },
 { "service": "youtube-music", "service_name": "YouTube Music", "id": "vid1", "url": "https://www.youtube.com/watch?v=vid1" }
 ]
 }
 ],
 "total": 1,
 "synced": true,
 "next_cursor": "MTAw",
 "by_service": {
 "spotify": { "status": "ok", "count": 120 },
 "youtube-music": { "status": "ok", "count": 45 }
 }
}
```

`total` is the full filtered count; `next_cursor` (when present) continues the page walk — see [Pagination and filters](api.md). When any service hits its **upstream** page cap, the response also sets `"truncated": true` and that service’s `by_service` entry sets `"truncated": true`. That flag is independent of `next_cursor`.

Cross-service merge prefers shared **ISRC** (`entity_id`, `match: "isrc"`), then **title + primary artist** via `NormalizeIdentityKey` (`match: "name"`). Spotify maps `/me/tracks` (saved library) and supplies ISRC when present; YouTube Music maps the channel liked-videos playlist, keeps only music-shaped items (category Music or a `" - Topic"` catalog channel), and strips YouTube title decoration so music videos can name-match catalog tracks ([#43](https://github.com/fudomunro/aegis/issues/43), [#4](https://github.com/fudomunro/aegis/issues/4)).

## Unified albums and artists

```
GET /v1/library/albums
GET /v1/library/artists
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Saved albums and followed/top artists follow the same library pattern. Connectors opt in via `AlbumProvider` (`ListUserSavedAlbums`) and `ArtistProvider` (`ListUserFollowedArtists`). Permission keys: `albums` and `artists`.

- Spotify albums: `GET /me/albums`; artists: `GET /me/following?type=artist`
- YouTube Music artists: channel subscriptions (`GET /subscriptions?mine=true`). No public saved-albums API — albums remain Spotify-only until one is available.
- Permission key `artists` also gates the underlying proxy paths `/me/following` and `/me/subscriptions` so `artists: none` blocks both the library endpoint and the raw proxy.

## Contact as a data type

`Contact` is Aegis's neutral representation of a **person or other entity**
who appears in personal data — someone in a photo, an email correspondent, a
playlist collaborator, etc. Future data types (mail, photos, calendar) can
embed `Contact` references the same way tracks embed `Artist`/`Album`.

Neutral contact fields (user-facing):

| Field | Description |
|-------|-------------|
| `name` | Preferred display name |
| `emails[]` | Known addresses (lowercased for stable matching) |
| `phones[]` | Known numbers in digit form (`+` kept when present) |
| `handles[]` | Usernames / social IDs / service accounts |
| `organization` | Company, team, or group, when known |
| `sources[]` | Service-local copies: `service`, `service_name`, `id`, `name`, `emails`, `phones` |

Cross-service matching prefers the strongest shared identifier:

1. **Email** — normalized (case-insensitive)
2. **Phone** — digits only, optional leading `+`
3. **Handle** — lowercased, leading `@` stripped
4. **Name** — `NormalizeIdentityKey`, used **only** when a contact has no
 email/phone/handle, so two different people who share a display name do
 not merge

Contacts that share any of those keys are merged into one entity with multiple
`sources[]` (union-find, so A~email B and B~phone C form one contact).

```
GET /v1/library/contacts
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Returns the user's contacts from every connected contact-capable service.
Connectors opt in by implementing `ContactProvider` (`ListUserContacts`).
Permission key: `contacts` (paths `/contacts/*`, `/me/contacts`, and `/people/*`
on the service proxy).

```json
{
 "contacts": [
 {
 "name": "Ada Lovelace",
 "emails": ["ada@example.com"],
 "phones": ["+15551234567"],
 "organization": "Analytical Engines",
 "sources": [
 {
 "service": "google",
 "service_name": "Google Contacts",
 "id": "people/c123",
 "name": "Ada Lovelace",
 "emails": ["ada@example.com"]
 }
 ]
 }
 ],
 "total": 1,
 "synced": true,
 "by_service": {
 "google": { "status": "ok", "count": 1 }
 }
}
```

## Photos as a data type

`Photo` is Aegis's neutral representation of a photo (or video still).
Service identity lives in `sources[]`, same as playlists and contacts.

**Aegis never stores photo bytes.** The authoritative copy stays on a
non-Aegis service marked `canonical` (see
[Canonical sources](configuration.md) /
[](https://github.com/fudomunro/aegis/issues/16)).
`blob_url` points at the retrievable image when the source exposes one; Google
Photos `baseUrl` values expire in about an hour, so agents must not treat them
as permanent links.

Neutral photo fields (user-facing):

| Field | Description |
|-------|-------------|
| `taken_at` | Capture time (RFC3339), when known |
| `width` / `height` | Pixel dimensions |
| `mime_type` | Media type (e.g. `image/jpeg`) |
| `filename` | Original filename or service display name |
| `caption` | Description, when present |
| `people[]` | Known people as `Contact` values; merged with contacts-library identity matching |
| `sources[]` | Service-local copies: `service`, `id`, `url`, `canonical`, `copy`, `blob_url`, `thumbnail_url` |

Cross-service merge uses a **content hash** when a provider supplies one
(`extra.content_hash`), otherwise **taken_at + dimensions**. Filename alone is
never an identity — two different `IMG_0001.jpg` files must not merge.

```
GET /v1/library/photos
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Returns the user's photo metadata from every connected photo-capable service.
Connectors opt in by implementing `PhotoProvider` (`ListUserPhotos`).
Permission key: `photos` (paths `/photos/*`, `/mediaItems/*`, `/picker/*`, and
`/me/photos` on the service proxy).

```json
{
 "photos": [
 {
 "taken_at": "2023-07-04T18:30:00Z",
 "width": 4032,
 "height": 3024,
 "mime_type": "image/jpeg",
 "filename": "beach.jpg",
 "sources": [
 {
 "service": "google-photos",
 "service_name": "Google Photos",
 "id": "AGxyz...",
 "canonical": true,
 "blob_url": "https://lh3.googleusercontent.com/...",
 "thumbnail_url": "https://lh3.googleusercontent.com/..."
 }
 ]
 }
 ],
 "total": 1,
 "synced": true,
 "by_service": {
 "google-photos": { "status": "ok", "count": 1 }
 }
}
```

## Calendar events as a data type

`CalendarEvent` is Aegis's neutral representation of a calendar entry
(meeting, appointment, all-day block). Service identity lives in `sources[]`,
same as playlists, contacts, and photos. **Aegis does not store calendar
content** — the authoritative copy stays on a non-Aegis service marked
`canonical`. Initial provider: **Google Calendar**
([](https://github.com/fudomunro/aegis/issues/65)).

Neutral calendar-event fields (user-facing):

| Field | Description |
|-------|-------------|
| `title` | Event summary/title |
| `description` | Event notes/body, when present |
| `start_at` / `end_at` | RFC3339 times (all-day uses midnight UTC of that date) |
| `all_day` | Date-bounded events without a clock time |
| `location` | Free-form place, room, or meeting link |
| `status` | Provider status when known (`confirmed`, `tentative`, `cancelled`) |
| `calendar_id` / `calendar_name` | Service-local calendar this entry belongs to |
| `organizer` | Organizer as a `Contact` value |
| `attendees[]` | Invitees as `Contact` values; merged with contacts-library identity matching |
| `entity_id` | Durable id when present (Google `iCalUID`) |
| `match` | Cross-service identity: `ical_uid` or `title_start` (empty on single-source) |
| `sources[]` | Service-local copies: `service`, `id`, `calendar_id`, `url`, `status`, `canonical` |

Cross-service merge prefers **iCalUID** when a provider supplies it
(`match=ical_uid`); otherwise **title + start time + all-day flag**
(`match=title_start`). Two different meetings at the same time must not merge.

```
GET /v1/library/events
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Returns the user's calendar events from every connected calendar-capable
service. Connectors opt in by implementing `CalendarEventProvider`
(`ListCalendarEvents`). Permission key: `events` (paths `/events/*`,
`/me/events`, `/calendars/*`, `/me/calendars`, and `/calendarList*` on the
service proxy).

```json
{
 "events": [
 {
 "title": "Standup",
 "description": "Daily team sync",
 "start_at": "2024-06-01T09:00:00Z",
 "end_at": "2024-06-01T09:30:00Z",
 "location": "Room 1",
 "status": "confirmed",
 "calendar_id": "primary",
 "calendar_name": "Work",
 "entity_id": "standup@google.com",
 "organizer": {
 "name": "Ada Lovelace",
 "emails": ["ada@example.com"]
 },
 "attendees": [
 {
 "name": "Grace",
 "emails": ["grace@example.com"]
 }
 ],
 "sources": [
 {
 "service": "google-calendar",
 "service_name": "Google Calendar",
 "id": "ev1",
 "calendar_id": "primary",
 "url": "https://calendar.google.com/event?eid=...",
 "canonical": true
 }
 ]
 }
 ],
 "total": 1,
 "synced": true,
 "by_service": {
 "google-calendar": { "status": "ok", "count": 1 }
 }
}
```

Organizer/attendee `Contact` values use the same identity matching as
`/v1/library/contacts` and `Photo.people[]` (email → phone → handle → name).

## Messages as a data type

`Message` is Aegis's neutral representation of a **text communication
between people** — either written by the user or written to the user by
another person. Typical sources are email (Gmail), SMS/WhatsApp when a named
connector exists, and social posts/replies the user authored or that form a
conversation with them.

**What is a message**

- Person-to-person correspondence: email, chat/SMS, replies addressed to the user, and (for social) writing the user themselves posted.
- One logical conversation may have several `sources[]` when the same item exists on more than one service.

**What is not a message**

- App or service **notifications** (likes, follows, shipping updates, marketing blasts, system alerts, digest mail).
- Automated receipts and transactional mail that nobody "wrote" to the user as a person.

Today some Gmail/Google Takeout mail still matches that notification shape
(newsletters, alerts, receipts). Connectors should prefer real correspondence
where they can tell the difference; do not add new notification-like items to
this type. Longer term, operators should be able to reclassify notification
items under a dedicated **Notifications** data type (not implemented yet)
rather than growing `messages`.

Service identity lives in `sources[]`, same as playlists, contacts, photos,
and calendar events. **Aegis does not store mailboxes or message bodies as
the home copy** — the authoritative content stays on a non-Aegis service
marked `canonical`. Library listings carry subject/snippet
metadata; agents fetch full body through the service proxy.
Initial provider: **Gmail**
([](https://github.com/fudomunro/aegis/issues/60)); social posts and
replies via **Bluesky** (`channel: "social"`, `direction: "outbound"` for
authored items).

Neutral message fields (user-facing):

| Field | Description |
|-------|-------------|
| `channel` | `email`, `sms`, `whatsapp`, `social`, or `signal` |
| `subject` | Email subject or chat topic |
| `snippet` | Short service-supplied body preview |
| `body` | Full body when a detail proxy call includes it (usually empty in library lists) |
| `from` / `to[]` / `cc[]` | Participants as `Contact` values |
| `sent_at` / `received_at` | RFC3339 times |
| `direction` | `inbound`, `outbound`, or `unknown` |
| `read` | True when not marked unread on the source |
| `thread_id` | Service-local conversation id |
| `entity_id` | Durable id when present (RFC 5322 Message-ID) |
| `match` | Cross-service identity: `message_id` or `subject_from_time` |
| `labels[]` | Service-local labels (`INBOX`, `SENT`, `UNREAD`, …) |
| `sources[]` | Service-local copies: `service`, `id`, `thread_id`, `url`, `mailbox`, `canonical`, `copy` |

Cross-service merge prefers **RFC Message-ID** when a provider supplies it
(`match=message_id`); otherwise **subject + from + sent_at**
(`match=subject_from_time`). Replies that share a subject but different
Message-IDs or send times stay distinct.

**Gmail and Google Takeout are sources of one mail collection**
([](https://github.com/fudomunro/aegis/issues/139)). Live Gmail,
Gmail archive sync, and Takeout import all feed the same identity space:
either ingest order is safe, a Message-ID match attaches another `sources[]`
entry instead of creating a second message, and `/v1/library/messages` plus
`/v1/mail/search` present the merged collection.

```
GET /v1/library/messages
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Returns the user's mail collection from every message-capable service and,
when the local mail archive is enabled, archive rows (including Takeout).
Connectors opt in by implementing `MessageProvider` (`ListMessages`).
Permission key: `messages` (paths `/messages/*`, `/me/messages`, `/threads/*`,
`/me/threads`, `/users/me/messages`, and `/gmail*` on the service proxy).

Participant `Contact` values use the same identity matching as
`/v1/library/contacts` and calendar attendees (email → phone → handle → name).

## Transactions as a data type

```
GET /v1/library/transactions
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

Returns the user's financial transactions from every connected source.
Connectors opt in by implementing `TransactionProvider` (`ListUserTransactions`).
Permission key: `transactions` (paths `/budgets*`, `/accounts*`, `/transactions*`,
`/categories*`, `/payees*`, `/quicken*`, `/me/transactions` on the service proxy).

Query filters: `after`, `before`, `account`, `q`, `currency`.

Amounts are **signed decimal strings** (negative = outflow) so agents never do
float money math on JSON numbers. Account fields are coarse references only
(name, type, last 4) — never full account or card numbers.

Cross-service merge:

1. Durable provider IDs first (YNAB `import_id`, Quicken FITID) → `match: "provider_id"`
2. Otherwise amount + posted day + normalized merchant → `match: "amount_date_merchant"`
3. An adjacent-day (±1) pass catches the same card charge visible through two
 providers under different local IDs

`summary` always includes `summary` rollups (`total_amount`, `by_category`,
`by_month`). When the permission is `summary`, `transactions[]`
is omitted entirely.

Example:

```json
{
 "transactions": [
 {
 "posted_at": "2024-03-05",
 "amount": "-45.67",
 "currency": "USD",
 "description": "AMAZON.COM",
 "merchant": "Amazon",
 "category": ["Shopping"],
 "status": "posted",
 "account": {"name": "Everyday Checking", "type": "checking", "last4": "4421"},
 "entity_id": "AQB-20240305-0001",
 "sources": [{"service": "ynab", "service_name": "YNAB", "id": "ynab-tx-1"}]
 }
 ],
 "total": 1,
 "summary": {
 "count": 1,
 "currency": "USD",
 "total_amount": "-45.67",
 "by_category": {"Shopping": "-45.67"},
 "by_month": {"2024-03": "-45.67"}
 }
}
```

`category` is a **list** so a YNAB split can name every category on the line
item without splitting the amount. A Costco run tagged Groceries + Household
Goods becomes one transaction, `amount: "-200.00"`, `category: ["Groceries",
"Household Goods"]`. Summary `by_category` credits the full amount to each
listed category (so the transaction shows up in grocery and household views);
`total_amount` stays the true net. For exact per-category dollars, dig into
YNAB — Aegis does not invent split amounts.

## Location history (places)

`GET /v1/library/places` returns **visits and trips** (time windows + place),
not a firehose of GPS points. Query filters: `after`, `before`, `locality`,
`kind` (`visit` | `trip`).

```json
{
 "visits": [{
 "start_at": "2023-07-04T16:00:00Z",
 "end_at": "2023-07-04T21:00:00Z",
 "place": {"name": "Halifax Harbour", "locality": "Halifax", "country": "Canada"},
 "kind": "visit",
 "confidence": "high",
 "sources": [{"service": "google-timeline", "service_name": "Google Timeline", "id": "tl-1"}]
 }],
 "total": 1
}
```

**Data-grant ladder (default `none` — opt-in).** See [Data grants](configuration.md).
Pre-request injection never advertises `places` until a service permission
names `city`, `read`, or `precise`. Coordinates require `precise`.

### Photos ↔ places join

Photos join to visits by `taken_at` inside the visit window
(`PhotosTakenDuring` in `places.go`). Sample fixtures include a harbour
evening visit with a fireworks photo taken during it — see
`TestPhotosPlacesJoinDemo`. Aegis does not store photo bytes; the join is
metadata only.

## Documents as a data type

`GET /v1/library/documents` returns life-admin paperwork — receipts,
warranties, leases, insurance policies, tax letters, medical letters, and
mail attachments — as neutral **Document** metadata. Files stay on the source
service (Paperless-ngx, Google Docs/Drive, and Gmail attachments on the same
`google-gmail` source as the parent message; Nextcloud remains a candidate).
Aegis never stores document bytes and never runs OCR.

```json
{
 "documents": [{
 "title": "Apartment lease 2024",
 "mime_type": "application/pdf",
 "created_at": "2024-01-15T12:00:00Z",
 "modified_at": "2024-01-16T09:00:00Z",
 "correspondent": "Harbour Property Management",
 "document_type": "lease",
 "tags": ["housing", "legal"],
 "excerpt": "This lease agreement is entered into…",
 "people": [{"name": "Harbour Property Management"}],
 "entity_id": "sha256:…",
 "sources": [{
 "service": "paperless",
 "service_name": "Paperless-ngx",
 "id": "11",
 "url": "https://paperless.example.com/documents/11/details",
 "canonical": true,
 "blob_url": "https://paperless.example.com/api/documents/11/download"
 }]
 }],
 "total": 1
}
```

Query filters: `q`, `tag`, `after`, `before`.

**Metadata vs text.** List rows never carry full text.

| Access | List | Text endpoint |
|--------|------|---------------|
| `metadata` | title, dates, tags, correspondent, document_type, people[], sources[] — **no excerpt** | denied |
| `read` / `read-write` | same + short `excerpt` | `GET /v1/library/documents/{service}/{id}/text` |

The text endpoint is a separate gate so `metadata` (or “read the list”) never
implies reading content. Prefer `canonical` / `blob_url` on `sources[]` for
the real file. Out of scope: upload/edit, Aegis-side OCR or file storage, and
per-tag allow/deny (design spike).

## Content transport (`/v1/services/{id}/...`)

```
GET/POST /v1/services/{service_id}/{path}
Authorization: Bearer <api-key> # or tailnet identity in auto/tailnet mode
```

After this is **not** a general service API. Library (`/v1/library/{kind}`) is the only data surface. `/v1/services` stays open only for user-data content the library cannot include or for ingesting a local export:

| Allowed | Why |
|---------|-----|
| `/assets/{id}/original`, `/assets/{id}/thumbnail`, Bluesky `getBlob` | Photo/asset bytes (Aegis does not store media) |
| `/messages/{id}`, `/threads/{id}` | Full message bodies (library lists carry snippets) |
| `/messages/{id}/attachments`, `/messages/{id}/attachments/{attachmentId}` | Mail attachment metadata and bytes (library documents carry title/mime only) |
| `/playlists/{id}`, `/playlists/{id}/items` | Playlist items (library entries carry `track_count` only) |
| `/documents/{id}/download`, `/documents/{id}/preview` | Document file bytes (library lists carry metadata + short excerpt) |
| `/files/{id}/export` | Google Docs HTML/plain-text export (library text endpoint is plain text) |
| `/picker/*` | Google Photos user pick → library |
| `/import`, `/import/status` | Local export ingest (Signal, Quicken) |

Provider-shaped lists (`/me/...`), playback, catalog search, profiles, capabilities, and generic OAuth pass-through return **404**. Callers authenticate to Aegis; they never see upstream OAuth tokens.
- `X-Aegis-Cache: hit` or `miss` on connector content GETs
- GET responses cached per-service; POST not cached

## Auth endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/auth/{id}/login` | GET | Start OAuth flow |
| `/v1/auth/{id}/callback` | GET | OAuth callback (redirect target) |
| `/v1/auth/status` | GET | Token status for all services |
| `/v1/auth/{id}` | DELETE | Revoke stored token |

## Admin dashboard

All `/v1/admin` routes — including the HTML shell — require the same auth as
other protected APIs. The shell is markup-only (no secrets or live
data), but is not advertised to unauthenticated peers. OAuth callback pages
under `/v1/auth/` remain public so browsers can finish the redirect.

```
GET /v1/admin — HTML dashboard (shell only; no secrets; auth required)
GET /v1/admin/summary — redacted services, token status, cache stats
GET /v1/admin/flow — data-flow topology (nodes, edges, libraries) + per-agent access detail
GET /v1/admin/hook-health — Aperture hook health + expected config snippet
POST /v1/admin/hook-health/check — validate a pasted Aperture policy/hooks JSON
GET /v1/admin/cache — in-memory cache inventory
POST /v1/admin/cache/clear — clear the response cache
GET /v1/admin/library/{kind} — admin Library tab preview (; not counted as agent Home access)
GET /v1/admin/permissions — type-centric data grants editor model: agent level + relevant apps/sync per data type
GET /v1/admin/data-grants — agent data-grant map (primary,)
PUT /v1/admin/data-grants — replace data grants {"types":{"tracks":{"default":"read","providers":{"spotify":"none"}}}}
GET /v1/admin/services/{id}/grant-limits — service override map + effective_permissions + available/writable types
PUT /v1/admin/services/{id}/grant-limits — replace the service grant_limits (tighten-only vs data grants; writes data_grants.json overlay, not aegis.json)
GET /v1/admin/immich/setup — Immich connection status + checks
POST /v1/admin/immich/test — probe Immich endpoint/API key (key not stored from this call)
POST /v1/admin/immich/config — save Immich endpoint/API key/canonical claim (service_connections.json + encrypted token store)
POST /v1/admin/immich/check — re-run the live acceptance checklist
POST /v1/admin/immich/tailnet/config — save Tailscale Admin API key/host (encrypted token store; key never echoed)
POST /v1/admin/immich/tailnet/check — live-check VIP Service setup steps
POST /v1/admin/immich/tailnet/apply — fix only the named steps that still need work
```

Public brand assets (no auth;) are served from the process root:

```
GET /favicon.ico, /favicon.svg, /favicon-16.png, /favicon-32.png
GET /apple-touch-icon.png
GET /logo.svg
```

`/` loads in the browser and fetches the JSON APIs. On a tailnet with
Serve identity the page works with no extra setup. In API-key mode, enter the
key when prompted (kept only in that tab’s `sessionStorage`). Friendly SPA
paths are durable: `https://your-aegis/`, `/library`, `/services`,
and `/advanced` each open that section and survive a refresh. Legacy
`/v1/admin` still serves the same shell.

Tabs: **Home** (default, `/`), **Services** (`/services`), **Library**
(`/library`), and **Advanced** (`/advanced`).
Connection management lives on **Services** — Connect/Reconnect, permissions,
and empty-state help when nothing is authorized yet. There is no separate
setup wizard. Aperture setup and hook health live under **Advanced**.

The **Library** tab loads datasets via `/v1/admin/library/{kind}` so browsing
the admin UI does not inflate Home access counts. Agents still use
`/v1/library/{kind}`; those requests are the ones the Home map counts.

The **Home** tab (default) is a live SVG overview of how data moves. Aperture
is drawn as a container around Aegis; Aegis nests the token vault and
response cache and lists every data type available on this instance (unified
library kinds plus proxy-only types). Agents connect via the hook and
proxy/library APIs; edges show OAuth reads, library fan-in, and blocked
services. Service nodes are colored by status (connected, needs OAuth, expired,
blocked). Photo services note that bytes stay upstream. Use it as a quick
“what is this thing doing?” picture; the Services tab remains the place to
edit permissions.

The **Advanced** tab consolidates operational detail: Aperture setup and
hook health, unified library dataset cards, recent access
samples, the response cache inventory (with clear), and runtime/auth endpoint
info.

On the **Services** tab, shared **Data grants by type** instructions sit above
the filter. The data-grant editor lists each data type with its **agent**
level (editable) and any other accessors that apply to that type (third-party
apps, sync pipelines) as read-only chips — only when relevant to the type. Agent form controls (Available / Off always; **Editable** only
when agents can truly
change that data through Aegis — not mirror backups or picker sessions).
Each service card keeps its own live permissions form under the title; every
change saves immediately to `data_grants.json` (the admin overlay). Bootstrap
defaults remain in `aegis.json`; the overlay wins for any service the admin
UI has saved. Keys not shown in the form are preserved on save. Data-type rows
use the Home map icons.

Each service card leads with **name** on the title line and **connection
status** on the same line to the right — badge, time left,
Connect/Reconnect, and **Pick photos…** (when supported) sit inline with no
line breaks between them. **Connect** / **Reconnect** appear only when the
service is not connected. Everything else (data flow rows, description, OAuth
config, cache TTL, paths) is collapsed under an **Advanced** disclosure.

Photo connectors that expose a picker session create operation (Google Photos)
also show **Pick photos…** on the service card: it creates a session, opens
Google’s picker in a new tab, polls until the user finishes selecting, then
refreshes the photos library view. (Session create is a POST — the service’s
`photos` permission must allow write.)

Admin APIs use the same auth as the rest of Aegis. Secrets never appear in
admin responses: client secrets, access tokens, and refresh tokens are omitted;
only presence flags, scopes, and expiry status are shown.

