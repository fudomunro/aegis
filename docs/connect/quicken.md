# Quicken files (QIF / QFX / OFX)

Historical Quicken exports are a **read-only import location** — parse once into
the local store, then agents query them like any other transaction source.
Statements and PDFs are never stored ( / product rule: Aegis does not
store media).

```bash
./aegis -import-quicken /path/to/export.qif # or a directory of.qif/.qfx/.ofx
## or keep the store next to other local state:
./aegis... -quicken-store "$DATA_DIR/quicken.db"
```

| Endpoint | Purpose |
|----------|---------|
| `GET /v1/library/transactions` | Neutral transactions from the import store (source `quicken`) |
| `GET /v1/services/quicken/import/status` | Row count |
| `POST /v1/services/quicken/import` | `{"path": "/path/to/file-or-dir"}` |

Quicken list paths under `/v1/services/quicken/transactions` are not agent API — use the library.

Re-import skips rows already stored (by FITID / stable hash). CLI:

```bash
./aegis -quicken-store quicken.db -import-quicken ~/Downloads/Quicken
```

FITIDs become `entity_id` so the same bank import shows up once when YNAB also
has it (or when the same export is imported twice under different filenames).

**Plaid / SimpleFIN / Actual Budget:** not registered yet. The
`TransactionProvider` interface is the plug-in point.

