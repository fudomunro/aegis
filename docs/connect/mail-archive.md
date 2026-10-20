# Local mail archive (`/v1/mail`)

For full-history agent queries, Aegis can keep a **local compressed
archive** (SQLite FTS5 + gzip provider payloads). This is a query mirror, not
a new home copy — Gmail remains canonical for the mailbox. The
archive is another **source of the user mail collection**: Gmail archive sync
and Google Takeout import both land here, merge on RFC Message-ID into
multi-source `sources[]`, and feed `/v1/library/messages`.
Optimize for Aegis/agents: search and fetch by id without paging Gmail every turn.

```bash
./aegis... -mail-archive mail-archive.db # default under <data-dir>; empty disables
```

| Endpoint | Purpose |
|----------|---------|
| `GET /v1/mail/search` | FTS + filters: `q`, `from`/`to` (addresses), `after`/`before`, `label`, `limit`, `cursor` (or `offset`), `sort=asc\|desc`, `include_body` |
| `GET /v1/mail/messages/{service}/{id}` | Neutral `Message` with `body` from the archive |
| `GET /v1/mail/messages/{service}/{id}/raw` | Decompressed provider payload JSON |
| `GET /v1/mail/status` | Message counts, last sync time/error, page cursors |
| `POST /v1/mail/sync` | Ingest a bounded batch; body `{"service","max_messages","query","reset_cursor"}` |

Sync is **resumable**: each call stores up to `max_messages` (default 200) and
persists the Gmail page token. Repeat until `"done": true`. Default query is
`in:anywhere` so spam/trash are included. Permission key: `messages`.

The **Advanced** tab in `/v1/admin` exposes the same mechanism: archive counts,
per-service page cursors, **Sync mail** (runs batches until complete or the
client batch cap), and **Restart sync**. Admin routes
`GET /v1/admin/mail/status` and `POST /v1/admin/mail/sync` skip the agent
messages gate so the operator can maintain the archive even when agents are
restricted.

```bash
## First fill (continue until done) — admin credential
curl -X POST "$AEGIS/v1/mail/sync" -H "Authorization: Bearer $ADMIN_KEY" \
 -d '{"max_messages":200}'

## Search history — agent or admin
curl "$AEGIS/v1/mail/search?q=invoice%20from:ada&after=2024-01-01&include_body=1" \
 -H "Authorization: Bearer $KEY"
```

`mail-archive.db` is local state (like `tokens.json`) and lives under the
[data directory](configuration.md) — do not commit it.

#### Google Takeout (read-only sync location)

A historical [Google Takeout](https://takeout.google.com) mail export is a
**read-only sync location** alongside live Gmail — not a write target and not
the canonical home. Point Aegis at an `.mbox` file, a directory of mbox
files, or a Takeout `.zip`. Takeout and Gmail are **two sources of one
collection**: re-import skips only copies this source already
stored; a Message-ID that Gmail already has **attaches** a `google-takeout`
source instead of skipping. Re-sync of the same export is cheap (by RFC
Message-ID per source) so you can run it as often as you like.

```
POST /v1/admin/mail/import
{"path": "/Users/you/Downloads/Takeout", "max_messages": 5000}
```

- `max_messages: 0` + `path` only saves the location
- Repeat with `max_messages > 0` until `"done": true` (already-present IDs from **this** source count as `skipped`)
- A durable byte cursor resumes mid-export so each batch continues instead of
 re-reading the whole mbox. `"reset_cursor": true` restarts.
- Admin **Advanced → Google Takeout** exposes the same controls
- Messages are stored as service `google-takeout` with `TAKEOUT` + mbox labels

```bash
curl -X POST "$AEGIS/v1/admin/mail/import" \
 -H "Authorization: Bearer $ADMIN_KEY" -H "Content-Type: application/json" \
 -d '{"path":"/Users/you/Downloads/Takeout","max_messages":5000}'
```

**SMS / WhatsApp:** the neutral `channel` field and library kind are ready,
but no connector is registered for them yet. Personal SMS history has no
simple OAuth surface; WhatsApp consumer history is not available through a
Aegis-compatible named API. Follow-ups can plug in connectors that
implement `MessageProvider` without changing the library contract.

