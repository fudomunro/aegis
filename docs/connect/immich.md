# Immich (local / external canonical)

Immich is a self-hosted photo library (open-source “Google Photos on your own
hardware”; quick start at `https://docs.immich.app/overview/quick-start/` —
Aegis ships a ready starter `.env` in `docs/immich-starter.env` so first-time
compose works without guessing values). Aegis treats it as a **foreign/local
canonical** source: metadata is listed through Aegis; **bytes stay on
Immich**. Auth is an API key in service headers — no OAuth app. Use Immich when
you want the photo archive on hardware you control; skip it if Google Photos is
enough.

```json
{
 "id": "immich",
 "name": "Immich",
 "action": "inject",
 "endpoint": "https://immich.your-tailnet.ts.net/api",
 "headers": { "X-API-Key": "$ENV:IMMICH_API_KEY" },
 "cache_ttl": "5m",
 "canonical_for": ["photos"],
 "grant_limits": { "default": "read", "photos": "read" }
}
```

`endpoint` must end in `/api`. Portable forms also work: same machine
`http://localhost:2283/api`, or MagicDNS + port
`http://photos.home.ts.net:2283/api`. For a **friendly tailnet HTTPS name**
(no `:2283`, real certs), host Immich as a Tailscale **VIP Service**
(`svc:immich`) — Admin → Services → Immich → Setup guide → **Tailnet URL**
emits the copyable walk-through (issue [#124](https://github.com/fudomunro/aegis/issues/124)).
Serve is tailnet-only; never Funnel.

**Tailscale Admin API automation (optional).** Paste a Tailscale access token
(Keys page; scopes `services` + `devices:core`) in the same wizard step. Aegis
stores it in the encrypted token store (`-token-pass`; presence-only in admin
APIs, never echoed back), **checks each walkthrough step** against `api.tailscale.com`, and
offers a **Fix** action only for steps that still need work:

| Step | Check | Auto-fix |
|------|-------|----------|
| `acl_tag` | `tagOwners` has the host tag | yes — adds `tag → ["autogroup:admin"]` (prerequisite for the next three) |
| `create_service` | `svc:immich` exists | yes — `PUT …/services/svc:immich` |
| `tag_host` | selected host device carries the tag | yes — `POST /device/{id}/tags` (preserves existing tags) |
| `serve` | probes the VIP/Immich `/api/server/ping` | no (mapping is host-local; probe turns green when it answers) |
| `approve` | service host approved | yes — `POST …/device/{id}/approved` |
| `connect` | Aegis Immich endpoint | yes — saves the VIP `…/api` endpoint |

Apply is explicit (`POST /v1/admin/immich/tailnet/apply`) and re-checks first so
already-OK steps are skipped. `create_service` / `tag_host` / `approve` first
ensure the host tag is declared in `tagOwners` (Tailscale rejects undeclared
tags). Aegis never mutates tailnet topology from the pre-request hook.

Only **one** non-blocked service may claim `canonical_for: ["photos"]`. If
Immich is the home copy, remove that key from `google-photos` (or omit it).

The connector implements `PhotoProvider` via Immich `POST /search/metadata`
(with EXIF). Library photos get `sources[].canonical: true` and
`blob_url` / `thumbnail_url` pointing at the **Aegis proxy**
(`/v1/services/immich/assets/{id}/original`), so agents never need the Immich
API key. Proxy paths under `/assets/*` and `/search/metadata` are classified
as the `photos` permission.

```
GET /v1/library/photos
GET /v1/services/immich/assets/{id}/original # content transport only
GET /v1/services/immich/assets/{id}/thumbnail
```

Asset search (`POST /search/metadata`) is not agent API — use
`/v1/library/photos`. This is the reference pattern for other local/foreign
stores: configure `endpoint` + static `headers`, implement a library provider,
mark `canonical_for`, and never cache content bytes in Aegis.

**Photo source discipline:** pre-request injection for photo
questions always includes `/v1/library/photos` and a hard rule to use only
Aegis URLs — not Photos.app / `Photos Library.photoslibrary` / Photo Booth /
`~/Pictures`, not Immich at `:2283`, and not Immich Docker/Postgres. Keep
photo service `aliases` broad enough (`photo`, `picture`, `photos`) that
singular prompts still match.

**Admin UI setup (preferred for operators):** **Admin → Services → Immich —
local canonical photos**. Enter the Immich endpoint + API key, optionally
claim `canonical_for: ["photos"]`, then **Save to Aegis** / **Run checks**.
The setup guide includes a **Tailnet URL** step (VIP Service commands for a
stable `https://…ts.net` Immich name). Saves write `service_connections.json`
under the data directory (never rewrites `aegis.json`); the API key is
presence-only in admin APIs. Backing endpoints: `GET /v1/admin/immich/setup`,
`POST /v1/admin/immich/test`, `POST /v1/admin/immich/config`,
`POST /v1/admin/immich/check`.

Live acceptance checklist is documented in
[docs/immich-live-testing.md](../immich-live-testing.md).

