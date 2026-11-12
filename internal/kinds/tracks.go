package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/shared"
)

type tracksKind struct {
	providerKind
	h       Host
	e       *Engine
	resolve func(context.Context, []Track)
	mark    func([]Track, string)
	copies  func([]Track)
}

func newTracksKind(h Host, e *Engine, d Deps) Kind {
	k := &tracksKind{h: h, e: e, resolve: d.ResolveTracks}
	k.mark, k.copies = Stamps(h, "tracks", forEachTrackSource)
	def := *BuiltinKindByID("tracks")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[LikedTrackProvider],
		writeCapable: providerProvides[LikedTrackWriteProvider],
		converge: tracksConvergePlan(h),
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

func (k *tracksKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[Track]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Track], []string) {
			return Collect[Track, LikedTrackProvider](k.h, r, kind.PermType,
				func(p LikedTrackProvider) func(context.Context) ([]Track, bool, error) {
					return p.ListUserLikedTracks
				})
		},
		ServiceOf: TrackServiceName,
		SortKey:   TrackIdentityKey,
		Merge:     MergeTracksSync,
		Enrich: func(ctx context.Context, tracks []Track) {
			if k.resolve != nil {
				k.resolve(ctx, tracks)
			}
		},
		Encode: func(items []Track, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return TrackList{Tracks: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func forEachTrackSource(t *Track, f func(SourceMut)) {
	for i := range t.Sources {
		src := &t.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *CopyInfo) { src.Copy = info },
		})
	}
}

// tracksConvergePlan is the tracks kind's converge plan (issue #382):
// create-only saves of the liked/saved list, one cursor page at a time.
// Saving is create-or-update and never un-likes — a member's own likes
// survive untouched.
func tracksConvergePlan(h Host) *ConvergePlan {
	return &ConvergePlan{
		Shape:   ConvergeSaves,
		RowName: "liked/saved tracks",
		TargetReady: func(conn any) bool {
			return providerProvides[LikedTrackPageProvider](conn)
		},
		LoadMember: func(ctx context.Context, member, _ string) (MemberState, error) {
			src, err := OpFor[LikedTrackPageProvider](h, member, "converge target")
			if err != nil {
				return MemberState{}, err
			}
			m := NewTrackMatcher(nil)
			st := MemberState{held: m}
			cursor := ""
			for i := 0; i < shared.LibraryPageCap; i++ {
				page, err := src.ListUserLikedTracksPage(ctx, cursor, 100)
				if err != nil {
					return MemberState{}, err
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
		ReadSource: func(ctx context.Context, hub, _, cursor string, limit int) (SourcePage, error) {
			src, err := OpFor[LikedTrackPageProvider](h, hub, "converge source")
			if err != nil {
				return SourcePage{}, err
			}
			page, err := src.ListUserLikedTracksPage(ctx, cursor, limit)
			if err != nil {
				return SourcePage{}, err
			}
			return SourcePage{Items: batchOf(page.Tracks), Next: page.Next, HubTotal: page.Total}, nil
		},
		FilterHeld: trackFilterHeld,
		Place: func(ctx context.Context, service, _, _ string, items Batch) (PlaceResult, error) {
			tracks := batchItems[Track](items)
			if len(tracks) == 0 {
				return PlaceResult{}, nil
			}
			dst, err := OpFor[LikedTrackWriteProvider](h, service, "converge target")
			if err != nil {
				return PlaceResult{}, err
			}
			res, err := dst.SaveLikedTracks(ctx, tracks)
			return PlaceResult{Copied: res.Copied, Skips: res.Skips}, err
		},
		MemberExtras: func(ctx context.Context, hub, member, _, _ string) (int, func(context.Context) error, error) {
			memberTracks, err := likedTracksFor(ctx, h, member, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubTracks, err := likedTracksFor(ctx, h, hub, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			count, adopt := extrasOf(hubTracks, memberTracks, NewTrackMatcher(hubTracks).Has,
				func(ctx context.Context, extras []Track) error {
					dst, err := OpFor[LikedTrackWriteProvider](h, hub, "conflict target")
					if err != nil {
						return err
					}
					_, err = dst.SaveLikedTracks(ctx, extras)
					return err
				})
			return count, adopt, nil
		},
	}
}

// trackFilterHeld drops the tracks a member state already holds (the
// identity ladder of issue #366) — shared by the tracks and playlists
// plans. A member state with no matcher holds nothing (first placement).
func trackFilterHeld(state MemberState, items Batch) (Batch, int) {
	m, _ := state.held.(*TrackMatcher)
	if m == nil {
		return items, 0
	}
	return filterHeld(items, m.Has)
}

// likedTracksFor pages one service's full liked/saved list.
func likedTracksFor(ctx context.Context, h Host, service, what string) ([]Track, error) {
	src, err := OpFor[LikedTrackPageProvider](h, service, what)
	if err != nil {
		return nil, err
	}
	var out []Track
	cursor := ""
	for i := 0; i < shared.LibraryPageCap; i++ {
		page, err := src.ListUserLikedTracksPage(ctx, cursor, 100)
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
