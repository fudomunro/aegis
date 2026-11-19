# Microsoft Mail

Microsoft 365 / Outlook.com mail over Microsoft Graph, as the `microsoft-mail`
surface.

1. Go to [Microsoft Entra admin center → App registrations](https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade)
2. New registration (single tenant, or "Accounts in any organizational directory and personal Microsoft accounts" for work + personal)
3. Add `https://your-aegis/v1/auth/microsoft-mail/callback` as a Web redirect URI
 (register the `microsoft-calendar` / `microsoft-contacts` callbacks too when
 using those surfaces)
4. Under **Certificates & secrets**, create a client secret and copy it
 (or leave it empty — the connection uses PKCE, so a public client works)
5. Under **API permissions**, add Microsoft Graph delegated permissions:
 `Mail.Read`, `offline_access` (add `Mail.ReadWrite` only if a future write
 surface needs it; `openid` and `profile` are optional)
6. Copy credentials into `aegis.json` (see `aegis.example.json`): one
 top-level `microsoft` connection with `"pkce": true`, and the surface's
 `oauth_scopes: ["Mail.Read"]`
7. Authorize at `/v1/auth/microsoft-mail/login` (shares the Microsoft
 OAuth connection with Calendar and Contacts)

**Tenant admin consent.** Many work tenants block third-party apps. When
admin consent is missing the library surface reports `unauthorized` and
nothing else breaks (fail open) — ask your tenant admin to consent to the
app, or use a personal Microsoft account. Replace `common` in the
`auth_url` / `token_url` with your tenant id to restrict the login to one
tenant.

The connector lists mail via `GET /me/messages` and maps it to neutral
Message values (subject, snippet, from/to/cc, sent_at, direction, read,
thread_id, entity_id) so the unified library can merge it with Gmail and the
other sources on RFC Message-ID (`internetMessageId` is normalized into
`entity_id`). Mailbox and direction come from the folder map
(`GET /me/mailFolders`, cached); an unreadable folder list degrades those
fields to `unknown` without failing.

Agent data is the unified library:

```
GET /me/messages
GET /v1/services/microsoft-mail/messages/{id}   # full body when the snippet is not enough
```

Library lists carry snippets only — message bodies stay on Microsoft Graph.

**Local mail archive.** `microsoft-mail` is also a mail-ingest source
([`docs/mail-ingest.md`](../mail-ingest.md)): `POST /v1/admin/mail/sync`
pages the mailbox into the local archive with a durable continuation cursor
(Graph's `@odata.nextLink` query), fail-open per message, and back-off on
Graph throttling (HTTP 429, honoring `Retry-After`). Rows are canonical
(the live mailbox home) and merge with Gmail / Takeout copies on Message-ID.

Out of scope in this phase: sending or replying, attachments surfaced as
documents, and Graph `/delta` incremental ingest.
