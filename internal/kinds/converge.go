package kinds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Converge plan (issue #382): the kind-owned declaration of how
// convergence and conflict resolution read, match, and place one kind —
// the write-side analogue of the read pipeline's Spec[T]. The sync engine
// stays a reconciliation policy over kinds: it schedules, chains, retries,
// and records; which provider to call with which list, what to name the
// copy row, and how to diff and adopt member extras is declared here.
// Plans reach connectors over the Host seam (OpFor), as the read side does.

// ErrConvergeUnsupported marks a converge-plan composition failure the
// engine treats as terminal: a service that cannot serve the plan's
// operation is misconfigured in a way retrying cannot fix. Converge-scoped
// — it classifies plan-op errors only.
var ErrConvergeUnsupported = errors.New("converge operation not supported")

// ConvergeShape names how one kind converges (issue #382). The engine
// iterates shapes, never kinds: create-only saves (whole-collection or
// cursor-paged — the cursor chain degenerates to one step when the source
// list fits in one batch), etag entities with If-Match placement, and the
// title-propagating playlist fan-out.
type ConvergeShape int

const (
	// ConvergeSaves is create-only placement of a collection (liked
	// tracks, saved albums, followed artists). Placement never removes;
	// member-only items survive untouched.
	ConvergeSaves ConvergeShape = iota
	// ConvergeEntities is etag-aware placement of a whole collection
	// (contacts, events): records carrying a member ref update with
	// If-Match semantics and a member-side edit surfaces as a conflict
	// instead of being overwritten.
	ConvergeEntities
	// ConvergePlaylists is the title-propagating fan-out: the canonical
	// home's playlists are chained onto each member item page by item
	// page, appending only what is missing and propagating renames.
	ConvergePlaylists
)

// ItemRef is one member-side record reference: the service-local id plus
// the etag watermark placement matches against (If-Match).
type ItemRef struct {
	ExternalID string
	ETag       string
}

// ItemStamp is one per-item copy watermark the engine records after a
// plan write (issue #382).
type ItemStamp struct {
	// EntityName keys the item's copy row (ItemEntityKey).
	EntityName string
	Service    string
	ExternalID string
	Name       string
	ETag       string
}

// Batch is one kind's opaque item batch in flight (issue #382) — typed
// inside the kind's plan closures, never inspected by the engine. The
// item unions the resolve engine used to carry live inside the closures
// instead.
type Batch struct {
	n      int
	items  any
	shrink func(int) Batch
}

// Len reports how many items the batch carries.
func (b Batch) Len() int { return b.n }

// Truncate trims the batch to n items.
func (b Batch) Truncate(n int) Batch {
	if n >= b.n || b.shrink == nil {
		return b
	}
	return b.shrink(n)
}

// batchOf builds a Batch over a typed slice (kind-side helper).
func batchOf[T any](items []T) Batch {
	return Batch{
		n:     len(items),
		items: items,
		shrink: func(n int) Batch {
			return batchOf(items[:n])
		},
	}
}

// batchItems extracts a batch's typed slice (kind-side helper).
func batchItems[T any](b Batch) []T {
	out, _ := b.items.([]T)
	return out
}

// MemberState is one kind's opaque view of what a member already holds
// (issue #382): the identity matcher behind FilterHeld plus the counts
// the stamps and extras check read. Only the owning plan interprets held.
type MemberState struct {
	// Count is how many items the member holds now.
	Count int
	// Title is the member copy's live title (the playlist shape's rename
	// detection reads it).
	Title string
	// held is the plan-private identity matcher FilterHeld reads.
	held any
}

// SourcePage is one batch of the canonical home's collection (issue #382).
type SourcePage struct {
	// Items is the batch to place.
	Items Batch
	// Next is the opaque cursor for the following page; "" ends the list.
	Next string
	// Title is the source entry's live title (per-entity shapes).
	Title string
	// HubTotal is the upstream item count for the extras check, when
	// known (pre-truncation).
	HubTotal int
	// Truncated is true when the list is known incomplete — the extras
	// check is then left to the freshness pass.
	Truncated bool
}

// SourceEntry is one fan-out source on the canonical home (the playlist
// shape's top-level collections).
type SourceEntry struct {
	// ID is the service-local id on the canonical home; "" skips the entry.
	ID string
	// Title is the entry's display name.
	Title string
}

// PlaceResult is the neutral result of a plan's Place operation.
type PlaceResult struct {
	// EntityID is the service-local id the placement landed on (the
	// create that placed a member playlist), when known.
	EntityID string
	// Copied is how many items were placed.
	Copied int
	// Placed carries per-item match records (which identity matched).
	Placed []PlaceRecord
	// Skips are items that could not be placed.
	Skips []PlaceSkip
	// Conflicts are items refused because the remote changed underfoot
	// (the entity shapes' If-Match refusals).
	Conflicts []PlaceConflict
}

// TitleOps is the title-propagating shape's read/rename pair (playlists).
type TitleOps struct {
	// Read returns a copy's current title.
	Read func(ctx context.Context, service, id string) (string, error)
	// Rename sets a copy's title (the append-path title write).
	Rename func(ctx context.Context, service, id, name string) error
}

// OrderOps is the playlist shape's order propagation (issue #378).
// Order is fidelity: the canonical home's item order fans out to its
// copies, a member-side reorder is a non-hub edit left for the user, and
// a reorder never changes membership (member-only items included).
type OrderOps struct {
	// Propagate probes the member's live order against the recorded
	// watermark and — when the member's own order has not moved —
	// rewrites the copy into the canonical order. A member-side reorder
	// (same items, different sequence) is reported as Moved and left
	// alone: the user accepts it (it becomes canonical) or rejects it
	// (the canonical order is restored) via Resolve.
	Propagate func(ctx context.Context, hub, member, hubID, memberID, recorded string) (OrderProbe, error)

	// Resolve applies one order-conflict decision: accept writes the
	// member's order onto the canonical home (the fan-out follows on the
	// next converge), reject restores the canonical order on the member.
	// found=false means a copy is gone (nothing to decide). The returned
	// watermark is what to stamp on the member's copy row.
	Resolve func(ctx context.Context, hub, member, hubID, memberID string, accept bool) (watermark string, found bool, err error)
}

// OrderProbe is one order-propagation attempt (issue #378).
type OrderProbe struct {
	// Watermark is the member copy's order marker to stamp: what this
	// write left there (unchanged when Moved, so the pending member-side
	// edit keeps re-detecting until it is resolved).
	Watermark string
	// Moved reports a member-side reorder — the write is left alone.
	Moved bool
	// MemberOrder / CanonicalOrder are the readable sequences for the
	// conflict row, in the order each side holds.
	MemberOrder    string
	CanonicalOrder string
}

// OrderWatermark is the order marker for a copy's item sequence
// (issue #378): two fingerprints packed as "<seq>.<set>" — the sequence
// as it stands and the same items sorted. A live marker with the same
// set fingerprint but a different sequence fingerprint is a member-side
// reorder (same items, different order); anything else is membership
// drift, which the items channel owns.
func OrderWatermark(ids []string) string {
	return shortFingerprint(ids) + "." + shortFingerprint(sortedCopy(ids))
}

// shortFingerprint hashes a sequence of item ids into a short marker.
func shortFingerprint(ids []string) string {
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// sortedCopy returns the ids in sorted order.
func sortedCopy(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// ConvergePlan is one kind's converge declaration (issue #382). Fields a
// shape does not use stay nil; the engine iterates plan shapes and never
// names a kind.
type ConvergePlan struct {
	// Shape is the convergence shape the engine iterates.
	Shape ConvergeShape

	// RowName is the collection copy row's display name — what the
	// freshness checks and admin read on the row ("liked/saved tracks").
	// The playlist shape names rows from the live title instead.
	RowName string

	// TargetReady reports whether a connector that is already
	// WriteCapable for this kind can also list back what it holds — the
	// read-back the skip filter and extras check need (a member that
	// cannot list its items cannot converge idempotently). Nil when the
	// shape has no read-back requirement (the entity shapes match inside
	// the write).
	TargetReady func(conn any) bool

	// LoadMember returns what a member already holds. memberID names a
	// member-side entity (the playlist shape); save shapes read the whole
	// collection. Nil for the entity shapes — they place everything and
	// match inside the write.
	LoadMember func(ctx context.Context, member, memberID string) (MemberState, error)

	// ReadSource reads one batch of the canonical home's collection —
	// cursor-paged or whole, entityID naming the entry being paged (the
	// playlist shape). Nil for the playlist shape, which fans out through
	// ListSources and then pages items.
	ReadSource func(ctx context.Context, hub, entityID, cursor string, limit int) (SourcePage, error)

	// ListSources (the playlist shape) lists the canonical home's
	// top-level collections to fan out, each with its service-local id.
	ListSources func(ctx context.Context, hub string) (srcs []SourceEntry, truncated bool, err error)

	// FilterHeld drops the batch's items the member state already holds,
	// returning what is left to place and how many were held back. Nil
	// for the entity shapes.
	FilterHeld func(state MemberState, items Batch) (kept Batch, held int)

	// AttachRefs (the entity shapes) rewrites the batch's member-side
	// refs (id + etag) on member from the watermark map — keyed by
	// ItemEntityKey — so placement takes its update path: re-runs update
	// in place instead of creating duplicates, and a member-side edit
	// surfaces as an If-Match conflict instead of being overwritten.
	AttachRefs func(items Batch, member string, refs map[string]ItemRef) Batch

	// Place writes a batch onto a service — the member during converge,
	// the canonical home during extras adoption. entityID and title name
	// the member-side entity for the playlist shape ("" keeps the title).
	Place func(ctx context.Context, service, entityID, title string, items Batch) (PlaceResult, error)

	// MemberExtras returns how many items the member holds beyond the
	// canonical home and a closure that adopts them onto hubID — the
	// accept/reject pair behind an items conflict. Rejections call it for
	// the count alone and never run adopt (convergence never deletes).
	MemberExtras func(ctx context.Context, hub, member, hubID, memberID string) (count int, adopt func(context.Context) error, err error)

	// TitleOps is the title-propagating shape's rename pair. Nil for
	// kinds whose copies carry no title to resolve.
	TitleOps *TitleOps

	// OrderOps is the order-propagating shape's probe/write pair
	// (issue #378). Nil for kinds whose copies carry no item order to
	// reconcile.
	OrderOps *OrderOps

	// RecordResolve applies one etag-record conflict decision: accept
	// writes the member's version to the canonical home, reject restores
	// the canonical version on the member (If-Match against the member's
	// live etag — the overwrite is the user's decision, not a silent
	// one). found=false means the member record is gone (nothing to
	// decide). The returned stamps are the per-item watermarks to record.
	RecordResolve func(ctx context.Context, hub, member, memberID, subject string, accept bool) (stamps []ItemStamp, found bool, err error)
}

// ItemEntityKey is the copy-ledger entity key for one per-item watermark
// row (issue #382): the convention RecordMirrorItemCopy stamps, the
// entity plans' AttachRefs read back, and the conflict store indexes.
func ItemEntityKey(kind, name string) string {
	return kind + ":" + strings.TrimSpace(name)
}

// withETag records an etag watermark on a source's Extra map.
func withETag(extra map[string]any, etag string) map[string]any {
	if etag == "" {
		return extra
	}
	if extra == nil {
		return map[string]any{"etag": etag}
	}
	extra["etag"] = etag
	return extra
}

// itemStampsFrom projects placement records onto per-item watermarks the
// engine stamps after a plan write.
func itemStampsFrom(placed []PlaceRecord, service string) []ItemStamp {
	if len(placed) == 0 {
		return nil
	}
	out := make([]ItemStamp, 0, len(placed))
	for _, rec := range placed {
		out = append(out, ItemStamp{
			EntityName: rec.Name,
			Service:    service,
			ExternalID: rec.ExternalID,
			Name:       rec.Name,
			ETag:       rec.ETag,
		})
	}
	return out
}

// extrasOf diffs the member's items against the canonical home's — the
// items a member holds beyond the hub (non-hub creates an accept adopts)
// — and returns the count plus the adoption closure. has indexes the hub
// list (the kind's identity matcher); place writes the extras onto the
// canonical home.
func extrasOf[T any](hubList, memberList []T, has func(T) bool, place func(context.Context, []T) error) (int, func(context.Context) error) {
	var extras []T
	for _, it := range memberList {
		if !has(it) {
			extras = append(extras, it)
		}
	}
	return len(extras), func(ctx context.Context) error {
		if len(extras) == 0 {
			return nil
		}
		return place(ctx, extras)
	}
}

// filterHeld drops the batch's items has already reports held (the kind's
// identity ladder) and returns what is left to place — the loop behind
// each save shape's FilterHeld.
func filterHeld[T any](items Batch, has func(T) bool) (Batch, int) {
	all := batchItems[T](items)
	kept := make([]T, 0, len(all))
	held := 0
	for _, it := range all {
		if has(it) {
			held++
			continue
		}
		kept = append(kept, it)
	}
	return batchOf(kept), held
}

// OpFor resolves a provider operation surface P for one service over the
// Host seam — the read side's Collect pattern targeted at a single
// service, used by converge plans. Failures wrap ErrConvergeUnsupported
// so the engine can tell a misconfigured group (terminal) from provider
// trouble (retryable).
func OpFor[P any](h Host, serviceID, what string) (P, error) {
	var zero P
	if h == nil {
		return zero, fmt.Errorf("%w: %s %q has no connector registry", ErrConvergeUnsupported, what, serviceID)
	}
	if conn, ok := h.Connectors()[serviceID].(P); ok {
		return conn, nil
	}
	return zero, fmt.Errorf("%w: %s %q is not supported yet", ErrConvergeUnsupported, what, serviceID)
}

// ConvergeKindIDs lists the built-in kinds that declare a converge plan —
// the schedule seed's kind list. Kind declarations are static data, so a
// nil-Host pass is safe (the builtinDecls rule).
func ConvergeKindIDs() []string {
	var out []string
	for _, k := range Builtin(nil, nil, Deps{}) {
		if k.Converge() != nil {
			out = append(out, k.Def().ID)
		}
	}
	return out
}
