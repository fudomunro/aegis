# IMAP Mail

Any IMAP server (RFC 3501) as the `imap-mail` mail source — the
service-neutral transport for mail that has no provider API. Dovecot, Cyrus,
iCloud, Fastmail, a work server: point Aegis at the mailbox and the local
archive does the rest.

1. Add a `imap-mail` service to `aegis.json` (or set it from the admin
 service connections overlay):

```json
{
  "id": "imap-mail",
  "name": "IMAP Mail",
  "action": "inject",
  "endpoint": "imaps://mail.example.com:993?folder=INBOX",
  "credentials": {
    "username": "alice@example.com",
    "password": "$ENV:IMAP_PASSWORD"
  },
  "canonical_for": ["messages"]
}
```

2. Authorize nothing — IMAP authenticates with the username/password pair
 in `credentials` (secret: they land in the encrypted token store, never in
 `service_connections.json`; `$ENV:` indirection works like other secrets).
3. Run an ingest: `POST /v1/admin/mail/sync {"service": "imap-mail"}`.

**Endpoint form.** `<scheme>://<host>[:<port>][?tls=…][&folder=…]`:

| Piece | Meaning |
|-------|---------|
| `imaps://` | TLS from the first byte (default port 993) |
| `imap://` | STARTTLS upgrade after the greeting (default port 993; set `?tls=none` for a plaintext link) |
| `folder=` | IMAP mailbox to walk; defaults to `INBOX` |
| `credentials.username` / `credentials.password` | the login pair — both required |

**What one run does.** Connect, `SELECT folder`, `UID SEARCH` from the
durable cursor, then `UID FETCH` each new message's flags and raw RFC 822
bytes (`BODY.PEEK[]` — walking the archive never marks mail read on the
server). Each message is mapped to a neutral Message (subject, from/to/cc,
sent_at, entity_id = RFC Message-ID) and stored in the local archive with
its payload. Rows are canonical (the live mailbox home) and merge with
Gmail / Outlook / Takeout copies on Message-ID.

**Cursor and flags.** The durable position is the highest processed UID;
UIDVALIDITY is stamped beside it, so a mailbox rebuild (UID renumbering)
resets the walk instead of silently skipping mail. `\Seen` maps to `read`;
the system flags map to labels (`SEEN`, `ANSWERED`, `FLAGGED`, `DRAFT`,
`DELETED`). Direction comes from the folder name (`INBOX` → inbound, a
`Sent` folder → outbound, anything else stays `unknown`).

Agent data is the unified library:

```
GET /me/messages
```

Library lists carry snippets only — message bodies stay on the IMAP server
beyond the compressed archive copy.

**Local mail archive.** `imap-mail` is a mail-ingest source
([`docs/mail-ingest.md`](../mail-ingest.md)): `POST /v1/admin/mail/sync`
walks the mailbox one bounded batch at a time (`max_messages` caps the
hydrations, default 200, max 1000), fail-open per unparseable message, and
stops without skipping ahead when the server errors mid-walk so the next
run resumes at the gap.

Out of scope in this phase: sending or replying, IDLE push (the walk is
poll-based), folder-tree sync beyond the one configured mailbox, and
XOAUTH2 for Google/Microsoft IMAP endpoints.
