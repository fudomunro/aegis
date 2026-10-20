# Signal import

Signal has no public chat API. Contacts and messages arrive as a
**file-based import** (like mail Takeout / Quicken), not OAuth:

```bash
## Android: Settings → Chats → Chat backups → Create backup, then decrypt
## with signalbackup-tools to a plaintext database.sqlite
./aegis -import-signal /path/to/database.sqlite

## Or a JSONL export (one object per line):
## {"type":"contact","id":"alice","name":"Alice","phones":["+15551234567"]}
## {"type":"message","id":"m1","body":"hello","sent_at":"2024-01-01T12:00:00Z",
## "direction":"inbound","from":{"name":"Alice","phones":["+15551234567"]}}
./aegis -import-signal ~/Downloads/signal-export.jsonl
```

Or use **Advanced → Signal import** (`/v1/admin/signal/import`).

| Endpoint | Purpose |
|----------|---------|
| `GET /v1/library/messages` | Imported Signal chats (source service `signal`) |
| `GET /v1/library/contacts` | Imported Signal contacts |
| `GET /v1/services/signal/import/status` | Contact + message counts |
| `POST /v1/services/signal/import` | `{"path": "/path/to/export"}` |

Signal list paths under `/v1/services/signal/messages` / `contacts` are not
agent API — use the library.

Accepted inputs: `.jsonl` / `.ndjson`, `.json` (bundle or array), and
plaintext `.db` / `.sqlite` dumps in either Signal **Desktop** schema
(`conversations` + `messages`) or Android / **signalbackup-tools** schema
(`recipient` + `message` + `thread`). Encrypted app databases are not
supported — decrypt or export first. Re-import skips rows already stored.
Attachments and media are never stored (text metadata only). The import
source is service `signal` with `channel: "signal"` and feeds both
`/v1/library/messages` and `/v1/library/contacts`.

A minimal JSONL sample lives at `examples/signal-export-sample.jsonl`.

