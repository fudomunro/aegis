# POP3 Mail

Any POP3 server (RFC 1939) as the `pop3-mail` mail source — the
service-neutral transport for legacy or download-style mailboxes. POP3 is
read-only here: messages are fetched and archived, never deleted from the
server.

1. Add a `pop3-mail` service to `aegis.json` (or set it from the admin
 service connections overlay):

```json
{
  "id": "pop3-mail",
  "name": "POP3 Mail",
  "action": "inject",
  "endpoint": "pop3s://pop.example.com:995",
  "credentials": {
    "username": "alice@example.com",
    "password": "$ENV:POP3_PASSWORD"
  },
  "canonical_for": ["messages"]
}
```

2. Authorize nothing — POP3 authenticates with the username/password pair
 in `credentials` (secret: they land in the encrypted token store, never in
 `service_connections.json`; `$ENV:` indirection works like other secrets).
3. Run an ingest: `POST /v1/admin/mail/sync {"service": "pop3-mail"}`.

**Endpoint form.** `<scheme>://<host>[:<port>][?tls=…]`:

| Piece | Meaning |
|-------|---------|
| `pop3s://` | TLS from the first byte (default port 995) |
| `pop3://` | `STLS` upgrade after the greeting (default port 995; set `?tls=none` for a plaintext link) |
| `credentials.username` / `credentials.password` | the login pair — both required |

**What one run does.** Connect, `UIDL` to list every message's stable
unique id, then `RETR` each message the archive does not already hold.
Each message is mapped to a neutral Message (subject, from/to/cc, sent_at,
entity_id = RFC Message-ID) and stored in the local archive with its raw
RFC 822 bytes. Rows are canonical (the live mailbox home) and merge with
Gmail / Outlook / Takeout copies on Message-ID.

**Cursor and dedupe.** POP3 has no flags and no folders: every row is
recorded as read (nothing marks it unread) in the single `POP3` mailbox,
and direction is `inbound` (the protocol's delivered-mail spool). Messages
are addressed by UIDL — RFC 1939's stable unique id — so renumbering after
a server-side delete cannot duplicate or drop rows. The durable cursor is
the last processed UIDL (a resume hint: if the server no longer lists it,
the walk starts from the top and the archive's per-UIDL probe absorbs the
overlap without re-downloading).

Agent data is the unified library:

```
GET /v1/library/messages
```

Library lists carry snippets only — beyond the compressed archive copy,
message bodies stay on the POP3 server.

**Local mail archive.** `pop3-mail` is a mail-ingest source
([`docs/mail-ingest.md`](../mail-ingest.md)): `POST /v1/admin/mail/sync`
walks the mailbox one bounded batch at a time (`max_messages` caps the
downloads, default 200, max 1000), fail-open per unparseable message, and
stops without skipping ahead when the server errors mid-walk so the next
run resumes at the gap. No `DELE` is ever sent — the walk is read-only.

Out of scope in this phase: sending, deleting mail from the server, and
TOP-based preview-only ingest.
