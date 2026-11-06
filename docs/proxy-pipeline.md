# Connector proxy pipelines

> Design record for consolidating connector `HandleProxy` implementations
> (issue #275). It exists so the "why is this connector still hand-rolled?"
> question has one answer that outlives any single PR. Product-facing
> behavior lives in [`docs/api.md`](api.md).

Status: **landed** for the shared-pipeline candidates; the special cases
below are deliberate, not backlog.

## The two shared pipelines

Everything in `internal/connectors` that speaks to a provider over HTTP goes
through one of two helpers in [`proxy.go`](../internal/connectors/proxy.go).
They own the rules that must not drift between connectors: cache GET + 200
only, map upstream credential rejection to a reconnect message, fail open
when a response cannot be transformed, bound upstream calls with
`connectors.HTTPClient`.

**`RunProxy`** is the buffered JSON pipeline for connectors with a provider
API and a neutral model: token check → cache → upstream → credential /
rate-limit handling → optional page expansion → transform → cache-on-200.
Per-connector policy is expressed in `ProxyOpts`:

| Option | Why it exists |
|--------|---------------|
| `token` | Credential source is not always OAuth — API-key services resolve from service headers first. |
| `missingToken` / `rejectToken` | Providers without a login route need copy that says "set a key", not "re-authorize at /v1/auth/…". |
| `rejectForbidden` | API-key providers answer 403 for a bad key. OAuth providers answer 403 for insufficient scope, and that must stay visible — so this is opt-in. |
| `mapUpstream` / `expandPages` | Provider path quirks and auto-pagination. |
| `transform` | Raw upstream bytes → neutral model. Returning `nil` (or leaving the field nil) passes bytes through unchanged, because a JSON re-marshal would reject upstream error pages. |

**`StreamProxy`** is the streaming variant: forward with a bearer token and
copy the response back unchanged. No cache, no transform, no pagination. It
is for tails that must move bytes through — full bodies, exports, generic
provider paths — where a cache entry and a neutral model would be the wrong
shape. Upstream statuses pass through unmapped; the connector has already
resolved and named the credential before calling in.

Path classification is *not* re-done in either pipeline. The agent-facing
surface is gated once, in `types.IsAgentContentPath`, before any
`HandleProxy` runs (issue #119): `/v1/services` is content/ingest transport
only, so provider-shaped list mirrors never reach a connector at all.

## Inventory

| Connector | Shape | Notes |
|-----------|-------|-------|
| Spotify, YouTube Music, Amazon Music | `RunProxy` | The original callers. Neutral models + page expansion. |
| YNAB | `RunProxy` | Was a line-for-line copy of the pipeline. Transaction listings return the neutral `TransactionList`; everything else passes through. |
| OwnTracks | `RunProxy` | Was a line-for-line copy. Static-header credential, `rejectForbidden`, no transform — the visit model belongs to the library surface, not this proxy. |
| Google Calendar, Contacts, Gmail, Photos | `StreamProxy` tail | Typed routes (list/detail/picker) stay connector-owned; the generic tail they shared byte for byte is now one call. |
| Google Drive | hand-rolled | Export paths return HTML or plain text, and the connector picks the response `Content-Type` from the requested `mimeType`. That is a content-negotiation concern `StreamProxy` should not grow. |
| Immich | hand-rolled | Mixed JSON metadata and original/thumbnail/download bytes in one route, with `Content-Disposition` forwarding and an API-key auth application. Same shape as Paperless (below); consolidating the two is a reasonable follow-up behind a binary-capable option, but it is not a straight copy of either pipeline. |
| Paperless | hand-rolled | Same binary+JSON mix as Immich (`/download`, `/preview`, `/thumb`) with its own token header convention and error truncation. |
| Bluesky | hand-rolled | `getBlob` is binary, `getAuthorFeed` needs session auth and a feed-shaping step, and the remaining xrpc GETs are session-authenticated pass-through. Three shapes in one route table; no shared pipeline would be more than a dispatch. |
| Signal, Quicken, Timeline | local | No upstream. SQLite stores under the data directory; `HandleProxy` serves import status and imported lists. |
| Sample Calendar | local | In-memory fixture, library-only. |
| CalDAV, CardDAV | library-only | `HandleProxy` is an intentional 404 pointing at `/v1/library/{events,contacts}`. DAV transport lives in `dav.go` behind the library surface. |

## Deliberate behavior the shared pipeline adds

Migrating the straight copies is not a byte-for-byte no-op. Where the copies
had drifted, the shared rules win:

- **OwnTracks now reports upstream latency** (`Host.ObserveUpstream`), like
  every other OAuth/API connector. It previously skipped metrics.
- **OwnTracks now maps 429 to the shared rate-limit response** with
  `Retry-After`. It previously forwarded the raw body.
- **Both migrated connectors honor `ServiceConfig.CacheTTL`** for proxy GETs.
  They previously hardcoded 60s.

Everything else — cache key shape, cache-on-GET-200, response `Content-Type`,
credential failure copy — is preserved exactly.

## Dropped along the way

`core.aegisHTTPClient` and `connectors.HTTPClient` were duplicates of each
other (both 30s). The core copy is gone; `connectors.HTTPClient` is the one
client for upstream calls. The remaining `http.Client` values are
deliberately not that client: `immichHTTPClient` (8s) bounds onboarding
probes, `dav.go` and `identity.go` own redirect/auth behavior the shared
client does not have.
