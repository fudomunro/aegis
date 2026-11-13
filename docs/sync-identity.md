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
   them (`EntityID` on the neutral model), and the vCard UID for contacts.
   For music, distinct durable ids never match, even when the names collide
   (different recordings / remixes). Contacts are the exception: the UID
   names the vCard *card*, not the person, and one person's cards may carry
   different UIDs — so a shared UID folds cards even when their emails
   drifted apart, and a shared email still folds cards that carry different
   UIDs.
2. **Duration-guarded name** — normalized title + primary artist, and only
   when the two lengths agree within five seconds (zero means unknown and
   does not block). A name hit of wildly different length is a different
   recording and is not placed. Contacts fall back to phone → handle →
   name, and the name is a weak key: it folds only cards with no email,
   phone, or handle, so two different "Alex Kim" addresses do not merge
   because the names match.
3. Every placed item records **which identity matched** (`matched_by` on
   `PlaceRecord`), so matches and skips are explainable.

One caveat sits outside the ladder: per-item copy watermarks key on the
display name (`ItemEntityKey`), not on the identity keys above — a rename
on the canonical home orphans the row, and the next run places a fresh copy
instead of updating in place.

YouTube Music exposes no ISRC and no durations in search, so its name
fallback is title-checked rather than duration-guarded; MusicBrainz
enrichment supplies recording MBIDs where the provider has none.

## Inventory

| Kind | Primary identity | Fallback | Ready | What blocks it |
|------|------------------|----------|-------|----------------|
| playlists | user-confirmed playlist link | duration-guarded name (items) | yes | — (a reorder moves only the canonical items into their slots — member-only items keep their places) |
| contacts | vCard UID / email | phone → handle → name | yes | — |
| events | iCal UID | title + start | yes | — |
| tracks | ISRC / recording MBID | duration-guarded name | yes | — (YouTube Music copies match on the title key) |
| albums | UPC / MBID | title + primary artist | yes | — (YouTube Music exposes no saved-albums library — album placement runs on providers that can save albums) |
| artists | MBID | name | yes | — (YouTube Music follows are channel subscriptions matched on the channel title key) |
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

- Photo identity and write surface (photo ownership is being settled
  separately) — [#376](https://github.com/fudomunro/aegis/issues/376).
