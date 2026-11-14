# LinkedIn import

LinkedIn has no public connections API. Connections arrive as a
**file-based import** (like mail Takeout / Signal / Quicken), not OAuth:

```bash
## LinkedIn → Settings & Privacy → Data Privacy → Get a copy of your data
## → select Connections (or the full archive). Wait for the download email.
./aegis -import-linkedin ~/Downloads/linkedin-export.zip

## Or the CSVs extracted from that archive:
./aegis -import-linkedin ~/Downloads/Connections.csv
./aegis -import-linkedin ~/Downloads/linkedin-exports/
```

Or use **Advanced → LinkedIn import** (`/v1/admin/linkedin/import`).

| Endpoint | Purpose |
|----------|---------|
| `GET /v1/library/contacts` | Imported LinkedIn connections (source service `linkedin`) |
| `GET /v1/services/linkedin/import/status` | Imported connection count |
| `POST /v1/services/linkedin/import` | `{"path": "/path/to/export"}` |

Accepted inputs: the data-export `.zip` (Aegis finds `Connections.csv` and
`Contacts.csv` inside), those CSVs directly, or a directory of `.csv` files.
The parser skips the prose preamble LinkedIn writes above the header row and
recognizes the documented columns (`First Name`, `Last Name`, `URL`,
`Email Address`, `Company`, `Position`, `Connected On`) plus the richer
`Contacts.csv` equivalents (`Title`, `Email 1-Value`, `Phone 1-Value`, …).

Mapping: name → `Contact.Name`, email → `emails`, the profile public id
(`janedoe` in `https://www.linkedin.com/in/janedoe`) → `handles`, company →
`organization`. The profile URL, position, and connection date stay on
`sources[].extra`. Re-import skips rows already stored (keyed by profile
public id). Contact metadata only — profile photos and other media are
never stored. The import source is service `linkedin` and feeds
`/v1/library/contacts`, where identity matching can fold connections into
the same people as Google Contacts or CardDAV.

A minimal sample lives at `examples/linkedin-connections-sample.csv`.
