# Sync identity readiness

Every library kind that takes part in a sync relationship needs an answer
to one question: **what identifies "the same item" across two services?**
A missed match duplicates the item on the next converge run; a false match
hides one. This note is the inventory (issue #367) — per-kind identity
stories and the verdict on whether the kind's sync group can converge
today.

The declaration is **kind-owned**: each registry entry carries a
`SyncIdentity` block (`internal/kinds/librarykinds.go`) with its primary
identity, its fallback, the readiness verdict, and what blocks it. A test
(`TestSyncIdentityDeclarations`) keeps the verdicts honest: a kind cannot
participate in sync unless its row says ready, and every "no sync yet" row
must name its gap.

## The matching ladder

Placement and existence filters match the way the merge layer does
(issue #366):

1. **Durable id first** — ISRC, then UPC/MBID where the provider exposes
   them (`EntityID` on the neutral model). Distinct durable ids never
   match, even when the names collide (different recordings / remixes).
2. **Duration-guarded name** — normalized title + primary artist, and only
   when the two lengths agree within five seconds (zero means unknown and
   does not block). A name hit of wildly different length is a different
   recording and is not placed.
3. Every placed item records **which identity matched** (`matched_by` on
   `PlaceRecord`), so matches and skips are explainable.

YouTube Music exposes no ISRC and no durations in search, so its name
fallback is title-checked rather than duration-guarded; MusicBrainz
enrichment supplies recording MBIDs where the provider has none.

## Inventory

| Kind | Primary identity | Fallback | Ready | What blocks it |
|------|------------------|----------|-------|----------------|
| playlists | user-confirmed playlist link | duration-guarded name (items) | yes | — (item reorders of already-placed items are not re-ordered on members) |
| contacts | vCard UID / email | phone → handle → name | yes | member-side creates are undetected until an entity freshness checker lands |
| events | iCal UID | title + start | yes | member-side creates are undetected until an entity freshness checker lands |
| tracks | ISRC / recording MBID | duration-guarded name | yes | — (YouTube Music copies match on the title key) |
| albums | UPC / MBID | title + primary artist | no sync yet | no write surface (no album placement) |
| artists | MBID | name | no sync yet | no write surface (no artist placement) |
| photos | — | — | no sync yet | no cross-service photo identity, no write surface; photo ownership is unsettled |
| messages | RFC Message-ID | subject + from + sent_at | no sync yet | read-only ingest by design — messages are never written back |
| transactions | provider id | amount + posted day + merchant | no sync yet | local import store only — read-only by design |
| places | provider id | days + rounded place key | no sync yet | local import store only — read-only by design |
| documents | content checksum | archive serial → title + created | no sync yet | read-only ingest by design — bytes stay at the source |

Read-only-by-design kinds (messages, transactions, places, documents) are
not gaps: their local stores are the point, and no provider write surface
is wanted. The rows still record what would identify their items, so the
answer is on file if that ever changes.

## Gaps worth closing

- Album and artist write placement (so those kinds have somewhere to
  converge) — [#375](https://github.com/fudomunro/aegis/issues/375).
- Photo identity and write surface (photo ownership is being settled
  separately) — [#376](https://github.com/fudomunro/aegis/issues/376).
- Entity freshness checker, so member-side *creates* of contacts and
  events are detected the way renames and edits are —
  [#377](https://github.com/fudomunro/aegis/issues/377).
- Playlist order reconciliation on members (today's convergence preserves
  order for what it places, but does not re-order already-placed items) —
  [#378](https://github.com/fudomunro/aegis/issues/378).
