# Photo share to Bluesky

```
POST /v1/library/photos/share
Authorization: Bearer <api-key>
Content-Type: application/json

{
 "source_service": "immich",
 "source_id": "asset-uuid",
 "caption": "optional post text",
 "alt": "optional image alt text"
}
```

Aegis fetches the original bytes from the canonical photo source **for this
request only** (nothing is cached or stored), hashes them, uploads the image
to Bluesky, and creates an `app.bsky.feed.post` with an images embed. Response:

```json
{
 "target_service": "bluesky",
 "source_service": "immich",
 "source_id": "asset-uuid",
 "post_uri": "at://did:plc:…/app.bsky.feed.post/…",
 "post_url": "https://bsky.app/profile/you.bsky.social/post/…",
 "claim": {
 "type": "sha256+public-post",
 "sha256": "…",
 "mime_type": "image/jpeg",
 "byte_size": 123456,
 "target_service": "bluesky",
 "post_uri": "at://…",
 "note": "PoC ownership claim for …"
 }
}
```

The share is also recorded in the copy ledger (`provenance: "share"`), so
`GET /v1/library/copies` and `sources[].copy` can surface the breadcrumb.

**Limits (PoC):** Bluesky image embeds cap at 1 MB. The claim is a content
hash plus a public post locator — not a cryptographic proof (no signature or
trusted timestamp). Auth is a Bluesky **app password** + handle in service
headers (`X-Bluesky-Handle`, `X-Bluesky-App-Password`); Aegis owns the
credential and exchanges it for a session. Permissions: source needs
`photos` read, target needs `photos` write.

The connector also **syncs library metadata** (bytes stay on Bluesky):

- `GET /v1/library/photos` includes the user's Bluesky image posts
 (`sources[].blob_url` is the Aegis `getBlob` proxy — not a permanent CDN link).
- `GET /v1/library/messages` includes **only the user's own posts and
 replies** (`channel: "social"`, `direction: "outbound"`). Reposts, likes,
 follows, and other people's content are excluded.
- `GET /v1/library/contacts` includes the user's Bluesky **following list**
 as contacts (handle + display name + DID).
- Proxy reads: `GET /v1/services/bluesky/xrpc/com.atproto.sync.getBlob`,
 `app.bsky.feed.getAuthorFeed`, `app.bsky.graph.getFollows`.

