# Microsoft Contacts

Microsoft 365 / Outlook.com address book over Microsoft Graph, as the `microsoft-contacts` surface.

1. Go to [Microsoft Entra admin center → App registrations](https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade)
2. New registration (single tenant, or "Accounts in any organizational directory and personal Microsoft accounts" for work + personal)
3. Add `https://your-aegis/v1/auth/microsoft-contacts/callback` as a Web redirect URI
 (register the `microsoft-calendar` callback too when using both surfaces)
4. Under **Certificates & secrets**, create a client secret and copy it
 (or leave it empty — the connection uses PKCE, so a public client works)
5. Under **API permissions**, add Microsoft Graph delegated permissions:
 `Contacts.Read`, `offline_access` (add `Contacts.ReadWrite` to sync **to**
 this service; `openid` and `profile` are optional)
6. Copy credentials into `aegis.json` (see `aegis.example.json`): one
 top-level `microsoft` connection with `"pkce": true`, and the surface's
 `oauth_scopes: ["Contacts.Read"]`
7. Authorize at `/v1/auth/microsoft-contacts/login` (shares the Microsoft
 OAuth connection with Calendar)

**Tenant admin consent.** Many work tenants block third-party apps. When
admin consent is missing the library surface reports `unauthorized` and
nothing else breaks (fail open) — ask your tenant admin to consent to the
app, or use a personal Microsoft account. Replace `common` in the
`auth_url` / `token_url` with your tenant id to restrict the login to one
tenant.

The connector lists contacts via `GET /me/contacts` and maps them to neutral
Contact values (name, emails, phones, organization) so the unified library
can merge them with Google Contacts and the other sources on the usual
identity ladder (email → phone → handle → name). Incremental sync uses
`GET /me/contacts/delta`: the resume token is recorded as the copy-ledger
watermark, deleted records the feed reports are dropped (convergence never
deletes), and expired tokens (410) restart the feed.

Agent data is the unified library:

```
GET /v1/library/contacts
```

Contacts sync through convergence: the group's **canonical home** fans out
to the other write-capable members, one-way fan-out only — contacts that
exist only on a member survive untouched. A dry run
(`payload: {"dry_run": true}` on a `converge` job) reports matched / created /
conflicting without writing.

Contact content stays on Microsoft Graph (non-Aegis canonical). To sync
**to** Microsoft (place contacts on it), add `Contacts.ReadWrite` to the
connection's scope union and re-authorize.
