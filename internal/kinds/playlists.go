package kinds

import (
	"context"
	"net/http"

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
		def:      def,
		provides: providerProvides[PlaylistProvider],
		writeCapable: providerProvides[PlaylistItemWriteProvider],
		converge: playlistsConvergePlan(h, def.ID),
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
func playlistsConvergePlan(h Host, kind string) *ConvergePlan {
	return &ConvergePlan{
		Shape: ConvergePlaylists,
		TargetReady: func(conn any) bool {
			// Playlist convergence re-reads the member's items so a re-run
			// appends only what is missing. A member that cannot list its
			// items cannot converge idempotently.
			return providerProvides[PlaylistItemWriteProvider](conn) &&
				providerProvides[PlaylistItemsProvider](conn)
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
			st := MemberState{held: NewTrackMatcher(nil)}
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
					st.held.(*TrackMatcher).Add(t)
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
