package kinds

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
)

type playlistsKind struct {
	providerKind
	h       Host
	e       *Engine
	linkKey func(Playlist) string
	mark    func([]Playlist, string)
	copies  func([]Playlist)
}

func newPlaylistsKind(h Host, e *Engine, d Deps) Kind {
	k := &playlistsKind{h: h, e: e, linkKey: d.PlaylistLinkKey}
	k.mark, k.copies = Stamps(h, "playlists", forEachPlaylistSource)
	def := *BuiltinKindByID("playlists")
	k.providerKind = providerKind{
		def:          def,
		provides:     providerProvides[PlaylistProvider],
		writeCapable: providerProvides[PlaylistItemWriteProvider],
		converge:     playlistsConvergePlan(h),
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

func (k *playlistsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[Playlist]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Playlist], []string) {
			return Collect[Playlist, PlaylistProvider](k.h, r, kind.PermType,
				func(p PlaylistProvider) func(context.Context) ([]Playlist, bool, error) {
					return p.ListUserPlaylists
				})
		},
		ServiceOf: PlaylistServiceName,
		SortKey:   func(p Playlist) string { return p.Name },
		Merge: func(in []Playlist) []Playlist {
			return MergePlaylistsSyncLinked(in, k.linkKey)
		},
		Encode: func(items []Playlist, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return PlaylistList{Playlists: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func forEachPlaylistSource(p *Playlist, f func(SourceMut)) {
	for i := range p.Sources {
		src := &p.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *CopyInfo) { src.Copy = info },
		})
	}
}

// playlistsConvergePlan is the playlists kind's converge plan (issue
// #382): the title-propagating shape — the canonical home's playlists fan
// out onto each member item page by item page, appending only what is
// missing and propagating renames (a member-side rename is a conflict,
// not an overwrite).
func playlistsConvergePlan(h Host) *ConvergePlan {
	return &ConvergePlan{
		Shape: ConvergePlaylists,
		TargetReady: func(conn any) bool {
			// Playlist convergence re-reads the member's items so a re-run
			// appends only what is missing. A member that cannot list its
			// items cannot converge idempotently.
			return providerProvides[PlaylistItemsProvider](conn)
		},
		ListSources: func(ctx context.Context, hub string) ([]SourceEntry, bool, error) {
			src, err := OpFor[PlaylistProvider](h, hub, "converge source")
			if err != nil {
				return nil, false, err
			}
			all, truncated, err := src.ListUserPlaylists(ctx)
			if err != nil {
				return nil, false, err
			}
			out := make([]SourceEntry, 0, len(all))
			for _, pl := range all {
				out = append(out, SourceEntry{ID: playlistSourceID(pl, hub), Title: pl.Name})
			}
			return out, truncated, nil
		},
		ReadSource: func(ctx context.Context, hub, entityID, cursor string, limit int) (SourcePage, error) {
			src, err := OpFor[PlaylistItemsProvider](h, hub, "converge source")
			if err != nil {
				return SourcePage{}, err
			}
			ref, page, err := src.ListPlaylistItems(ctx, entityID, cursor, limit)
			if err != nil {
				return SourcePage{}, err
			}
			return SourcePage{
				Items:    batchOf(page.Tracks),
				Next:     page.Next,
				Title:    ref.Name,
				HubTotal: ref.TrackCount,
			}, nil
		},
		LoadMember: func(ctx context.Context, member, memberID string) (MemberState, error) {
			src, err := OpFor[PlaylistItemsProvider](h, member, "converge target")
			if err != nil {
				return MemberState{}, err
			}
			m := NewTrackMatcher(nil)
			st := MemberState{held: m}
			cursor := ""
			for i := 0; i < shared.LibraryPageCap; i++ {
				ref, page, err := src.ListPlaylistItems(ctx, memberID, cursor, 100)
				if err != nil {
					return MemberState{}, err
				}
				if i == 0 {
					st.Title = ref.Name
				}
				for _, t := range page.Tracks {
					m.Add(t)
					st.Count++
				}
				if page.Next == "" {
					return st, nil
				}
				cursor = page.Next
			}
			return st, nil
		},
		FilterHeld: trackFilterHeld,
		Place: func(ctx context.Context, service, entityID, title string, items Batch) (PlaceResult, error) {
			dst, err := OpFor[PlaylistItemWriteProvider](h, service, "converge target")
			if err != nil {
				return PlaceResult{}, err
			}
			res, err := dst.PlacePlaylistItems(ctx, entityID, title, batchItems[Track](items))
			return PlaceResult{
				EntityID: res.PlaylistID,
				Copied:   res.Copied,
				Placed:   res.Placed,
				Skips:    res.Skips,
			}, err
		},
		MemberExtras: func(ctx context.Context, hub, member, hubID, memberID string) (int, func(context.Context) error, error) {
			memberTracks, err := playlistTracksFor(ctx, h, member, memberID, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubTracks, err := playlistTracksFor(ctx, h, hub, hubID, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubHeld := NewTrackMatcher(hubTracks)
			var extras []Track
			for _, t := range memberTracks {
				if !hubHeld.Has(t) {
					extras = append(extras, t)
				}
			}
			adopt := func(ctx context.Context) error {
				dst, err := OpFor[PlaylistItemWriteProvider](h, hub, "conflict target")
				if err != nil {
					return err
				}
				// The title write is deliberately empty — adoption never
				// renames the hub list.
				_, err = dst.PlacePlaylistItems(ctx, hubID, "", extras)
				return err
			}
			return len(extras), adopt, nil
		},
		TitleOps: &TitleOps{
			Read: func(ctx context.Context, service, id string) (string, error) {
				src, err := OpFor[PlaylistItemsProvider](h, service, "conflict source")
				if err != nil {
					return "", err
				}
				ref, _, err := src.ListPlaylistItems(ctx, id, "", 1)
				if err != nil {
					return "", err
				}
				return ref.Name, nil
			},
			Rename: func(ctx context.Context, service, id, name string) error {
				dst, err := OpFor[PlaylistItemWriteProvider](h, service, "conflict target")
				if err != nil {
					return err
				}
				_, err = dst.PlacePlaylistItems(ctx, id, name, nil)
				return err
			},
		},
		OrderOps: playlistOrderOps(h),
	}
}

// playlistSourceID is a playlist's service-local id on the canonical home.
func playlistSourceID(pl Playlist, hub string) string {
	for _, src := range pl.Sources {
		if src.Service == hub {
			return src.ID
		}
	}
	if len(pl.Sources) > 0 {
		return pl.Sources[0].ID
	}
	return ""
}

// playlistTracksFor pages one playlist's full item list.
func playlistTracksFor(ctx context.Context, h Host, service, playlistID, what string) ([]Track, error) {
	src, err := OpFor[PlaylistItemsProvider](h, service, what)
	if err != nil {
		return nil, err
	}
	var out []Track
	cursor := ""
	for i := 0; i < shared.LibraryPageCap; i++ {
		_, page, err := src.ListPlaylistItems(ctx, playlistID, cursor, 100)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Tracks...)
		if page.Next == "" {
			return out, nil
		}
		cursor = page.Next
	}
	return out, nil
}

// playlistOrderOps is the playlist shape's order propagation (issue
// #378): probe the member's sequence against the recorded watermark,
// rewrite the copy into the canonical order when the member has not
// moved, and settle an order conflict either way. The write is
// set-preserving and extras-staying: the canonical items fill the slots
// they already occupy on the copy, and items the copy holds beyond the
// canonical home keep their places — reordering never removes or moves
// anything else.
func playlistOrderOps(h Host) *OrderOps {
	return &OrderOps{
		Propagate: func(ctx context.Context, hub, member, hubID, memberID, recorded string) (OrderProbe, error) {
			hubTracks, err := playlistTracksFor(ctx, h, hub, hubID, "order source")
			if err != nil {
				return OrderProbe{}, err
			}
			memberTracks, err := playlistTracksFor(ctx, h, member, memberID, "order target")
			if err != nil {
				return OrderProbe{}, err
			}
			// The watermark is the canonical items' order as it stands on
			// the copy: a pure reorder of them diverges it, and a
			// member-only item moving among them does not (its place is
			// the member's own, and the items channel owns membership).
			live := matchedRefIDs(hubTracks, memberTracks, member)
			probe := OrderProbe{
				Watermark:      OrderWatermark(live),
				MemberOrder:    orderLabels(matchedTracks(hubTracks, memberTracks)),
				CanonicalOrder: orderLabels(hubTracks),
			}
			if orderMoved(recorded, probe.Watermark) {
				// A member-side reorder: left alone for the user's
				// accept/reject, and the watermark stays on the recorded
				// baseline so it keeps re-detecting until then.
				probe.Moved = true
				probe.Watermark = recorded
				return probe, nil
			}
			dst, err := OpFor[PlaylistOrderProvider](h, member, "order target")
			if err != nil {
				// Recognized, not implemented: order reconciliation is
				// simply off for this member. Nothing to write or stamp.
				probe.Watermark = recorded
				return probe, nil
			}
			target := orderRefIDs(hubTracks, memberTracks, member)
			if !equalSeq(target, trackRefIDs(memberTracks, member)) {
				if err := dst.SetPlaylistOrder(ctx, memberID, target); err != nil {
					return OrderProbe{}, err
				}
			}
			// Stamp the order this write leaves behind (the canonical
			// sequence), not the one it read — the next probe compares
			// the member against what we set.
			probe.Watermark = OrderWatermark(canonicalRefIDs(hubTracks, memberTracks, member))
			return probe, nil
		},
		Resolve: func(ctx context.Context, hub, member, hubID, memberID string, accept bool) (string, bool, error) {
			if hubID == "" || memberID == "" {
				return "", false, nil
			}
			hubTracks, err := playlistTracksFor(ctx, h, hub, hubID, "order source")
			if err != nil {
				return "", true, err
			}
			memberTracks, err := playlistTracksFor(ctx, h, member, memberID, "order target")
			if err != nil {
				return "", true, err
			}
			if accept {
				// The member's order becomes canonical: write it onto the
				// home (the fan-out follows on the next converge). The
				// member itself is unchanged — its watermark is refreshed
				// to the order it holds so the next converge does not
				// re-flag.
				dst, err := OpFor[PlaylistOrderProvider](h, hub, "order target")
				if err != nil {
					return "", true, err
				}
				target := orderRefIDs(memberTracks, hubTracks, hub)
				if !equalSeq(target, trackRefIDs(hubTracks, hub)) {
					if err := dst.SetPlaylistOrder(ctx, hubID, target); err != nil {
						return "", true, err
					}
				}
				return OrderWatermark(matchedRefIDs(hubTracks, memberTracks, member)), true, nil
			}
			dst, err := OpFor[PlaylistOrderProvider](h, member, "order target")
			if err != nil {
				return "", true, err
			}
			target := orderRefIDs(hubTracks, memberTracks, member)
			if !equalSeq(target, trackRefIDs(memberTracks, member)) {
				if err := dst.SetPlaylistOrder(ctx, memberID, target); err != nil {
					return "", true, err
				}
			}
			return OrderWatermark(canonicalRefIDs(hubTracks, memberTracks, member)), true, nil
		},
	}
}

// orderMoved reports a member-side reorder: the recorded watermark and
// the live one carry the same canonical-item set in a different sequence.
// No baseline (recorded empty) is not a move, and membership drift is the
// items channel's — only a pure reorder flags here.
func orderMoved(recorded, live string) bool {
	if recorded == "" || recorded == live {
		return false
	}
	rSeq, rSet, rOK := strings.Cut(recorded, ".")
	lSeq, lSet, lOK := strings.Cut(live, ".")
	return rOK && lOK && rSet != "" && rSet == lSet && rSeq != lSeq
}

// matchPrimary maps each pool item to the primary index it matches (the
// identity ladder's greedy match, in pool order), or -1 for a pool-only
// item.
func matchPrimary(primary, pool []Track) []int {
	out := make([]int, len(pool))
	for i := range out {
		out[i] = -1
	}
	for pi, p := range primary {
		for i, c := range pool {
			if out[i] == -1 && TracksMatch(p, c) {
				out[i] = pi
				break
			}
		}
	}
	return out
}

// matchedRefIDs returns the pool items primary also holds, in pool order
// — the primary's order as it stands in the pool.
func matchedRefIDs(primary, pool []Track, poolService string) []string {
	m := matchPrimary(primary, pool)
	out := make([]string, 0, len(pool))
	for i, c := range pool {
		if m[i] >= 0 {
			out = append(out, trackRefID(c, poolService))
		}
	}
	return out
}

// canonicalRefIDs returns the pool items primary also holds, in primary's
// order — the sequence a slot-fill write leaves behind. When the copy
// already stands in the canonical order it equals matchedRefIDs.
func canonicalRefIDs(primary, pool []Track, poolService string) []string {
	m := matchPrimary(primary, pool)
	out := make([]string, 0, len(pool))
	for pi := range primary {
		for i := range pool {
			if m[i] == pi {
				out = append(out, trackRefID(pool[i], poolService))
				break
			}
		}
	}
	return out
}

// matchedTracks returns the pool items primary also holds, in pool order.
func matchedTracks(primary, pool []Track) []Track {
	m := matchPrimary(primary, pool)
	out := make([]Track, 0, len(pool))
	for i, c := range pool {
		if m[i] >= 0 {
			out = append(out, c)
		}
	}
	return out
}

// trackRefIDs returns each track's service-local id in list order.
func trackRefIDs(tracks []Track, service string) []string {
	out := make([]string, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, trackRefID(t, service))
	}
	return out
}

// trackRefID reads one track's service-local id for one service.
func trackRefID(t Track, service string) string {
	for _, src := range t.Sources {
		if src.Service == service {
			return src.ID
		}
	}
	if len(t.Sources) > 0 {
		return t.Sources[0].ID
	}
	return ""
}

// orderRefIDs sequences pool to primary's order without disturbing the
// rest: the items primary holds take the slots they already occupy in
// pool, filled in primary's order, and the pool items primary does not
// hold keep their places. The result is exactly pool's items, once each —
// a reorder never changes membership and never moves anything else.
func orderRefIDs(primary, pool []Track, poolService string) []string {
	matched := matchPrimary(primary, pool)
	// The matched pool items in primary's order fill the matched slots.
	filling := make([]int, 0, len(pool))
	for pi := range primary {
		for i := range pool {
			if matched[i] == pi {
				filling = append(filling, i)
				break
			}
		}
	}
	out := make([]string, len(pool))
	f := 0
	for i := range pool {
		if matched[i] < 0 {
			out[i] = trackRefID(pool[i], poolService)
			continue
		}
		out[i] = trackRefID(pool[filling[f]], poolService)
		f++
	}
	return out
}

// equalSeq reports two id sequences are identical.
func equalSeq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// orderLabels renders a track sequence for the conflict row: names in
// order, capped so a long playlist stays readable.
func orderLabels(tracks []Track) string {
	const cap = 8
	names := make([]string, 0, len(tracks))
	for _, t := range tracks {
		names = append(names, t.Name)
	}
	if len(names) > cap {
		return strings.Join(names[:cap], " | ") + fmt.Sprintf(" | … +%d more", len(names)-cap)
	}
	return strings.Join(names, " | ")
}
