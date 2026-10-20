# Google Docs / Google Drive

Google Docs are documents too. The `google-drive` connector lists native
Google Docs (`application/vnd.google-apps.document`) via Drive `files.list`
and pulls content with `files.export` as **HTML or plain text** — markdown
conversion is deferred. Files stay on Drive (non-Aegis canonical).

```json
{
 "id": "google-drive",
 "name": "Google Drive",
 "endpoint": "https://www.googleapis.com/drive/v3",
 "connection": "google",
 "oauth_scopes": ["https://www.googleapis.com/auth/drive.readonly"],
 "grant_limits": {"documents": "read"}
}
```

Shares the Google connection with Contacts/Calendar/Gmail/Photos.
`drive.readonly` covers both listing and export (a narrower content scope can
replace it later). Watch OAuth app verification limits, same class as Photos.

| What | Format |
|------|--------|
| `GET /v1/library/documents/{service}/{id}/text` | `text/plain` export |
| `GET /v1/services/google-drive/files/{id}/export?mimeType=text/html` | HTML (structure-preserving intermediate for a later markdown pass) |

