# YNAB

Live budget data from the [YNAB API](https://api.ynab.com). Auth is a Personal
Access Token on the service (or a stored OAuth token):

```json
{
 "id": "ynab",
 "name": "YNAB",
 "endpoint": "https://api.ynab.com/v1",
 "headers": {"Authorization": "Bearer <your-ynab-personal-access-token>"},
 "grant_limits": {"transactions": "read"}
}
```

The connector lists budgets → accounts → transactions and maps YNAB milliunits
to signed decimal strings (`-45670` → `"-45.67"`). Payee becomes `merchant`;
parent + split category names become `category[]` (YNAB's placeholder
`"Split"` is dropped). Splits contribute **categories only** — the parent
amount is not redistributed. `import_id` becomes `entity_id` when present.
Read-only — no budget mutations are advertised.

Agent data is the unified library:

```
GET /v1/library/transactions
```

YNAB budget/account/transaction proxy paths are not agent API.

