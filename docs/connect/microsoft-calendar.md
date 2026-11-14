# Microsoft Calendar

Microsoft 365 / Outlook.com calendar over Microsoft Graph, as the `microsoft-calendar` surface.

1. Go to [Microsoft Entra admin center → App registrations](https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade)
2. New registration (single tenant, or "Accounts in any organizational directory and personal Microsoft accounts" for work + personal)
3. Add `https://your-aegis/v1/auth/microsoft-calendar/callback` as a Web redirect URI
 (register the `microsoft-contacts` callback too when using both surfaces)
4. Under **Certificates & secrets**, create a client secret and copy it
 (or leave it empty — the connection uses PKCE, so a public client works)
5. Under **API permissions**, add Microsoft Graph delegated permissions:
 `Calendars.Read`, `offline_access` (add `Calendars.ReadWrite` to sync **to**
 this service; `openid` and `profile` are optional)
6. Copy credentials into `aegis.json` (see `aegis.example.json`): one
 top-level `microsoft` connection with `"pkce": true`, and the surface's
 `oauth_scopes: ["Calendars.Read"]`
7. Authorize at `/v1/auth/microsoft-calendar/login` (shares the Microsoft
 OAuth connection with Contacts)

**Tenant admin consent.** Many work tenants block third-party apps. When
admin consent is missing the library surface reports `unauthorized` and
nothing else breaks (fail open) — ask your tenant admin to consent to the
app, or use a personal Microsoft account. Replace `common` in the
`auth_url` / `token_url` with your tenant id to restrict the login to one
tenant.

The connector lists events via `GET /me/calendarView` in one window
(**30 days back + 90 days ahead**, like Google Calendar) with
`Prefer: outlook.timezone="UTC"`; proxy calls may pass explicit `timeMin` /
`timeMax`. Recurring meetings arrive expanded; `iCalUId` is the durable
cross-service id, so Microsoft and Google events merge on iCalUID. Incremental
sync uses `GET /me/calendarView/delta`: the resume token is recorded as the
copy-ledger watermark and expired tokens (410) restart the feed.

Agent data is the unified library:

```
GET /v1/library/events
GET /v1/library/freebusy
```

Events sync through convergence like every other kind: the group's
**canonical home** fans out to the other write-capable members (Microsoft
Calendar and Google Calendar both place and read events). A member can
declare the **free/busy projection** on its sync relationship
(`"projection": "freebusy"` via `POST /v1/admin/services/{id}/sync`) — copies
placed on it are busy blocks only (times, no title, description, attendees,
or location), which is the "keep my work calendar's busy blocks on my personal
calendar" case. A dry run (`payload: {"dry_run": true}` on a `converge` job)
reports matched / created / conflicting without writing.

Event content stays on Microsoft Graph (non-Aegis canonical). To sync **to**
Microsoft (place events on it), add `Calendars.ReadWrite` to the connection's
scope union and re-authorize.
