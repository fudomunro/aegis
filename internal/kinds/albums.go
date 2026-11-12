package kinds

import (
	"context"
	"net/http"
)

type albumsKind struct {
	providerKind
	h       Host
	e       *Engine
	resolve func(context.Context, []Album)
}

func newAlbumsKind(h Host, e *Engine, d Deps) Kind {
	k := &albumsKind{h: h, e: e, resolve: d.ResolveAlbums}
	def := *BuiltinKindByID("albums")
	k.providerKind = providerKind{
		def:          def,
		provides:     providerProvides[AlbumProvider],
		writeCapable: providerProvides[AlbumWriteProvider],
		converge:     albumsConvergePlan(h),
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

func (k *albumsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[Album]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Album], []string) {
			return Collect[Album, AlbumProvider](k.h, r, kind.PermType,
				func(p AlbumProvider) func(context.Context) ([]Album, bool, error) {
					return p.ListUserSavedAlbums
				})
		},
		ServiceOf: AlbumServiceName,
		SortKey:   AlbumIdentityKey,
		Merge:     MergeAlbumsSync,
		Enrich: func(ctx context.Context, albums []Album) {
			if k.resolve != nil {
				k.resolve(ctx, albums)
			}
		},
		Encode: func(items []Album, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return AlbumList{Albums: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
	})
}

// albumsConvergePlan is the albums kind's converge plan (issue #382):
// create-only saves of the whole saved-albums collection. Saving never
// un-saves — a member's own saves survive untouched.
func albumsConvergePlan(h Host) *ConvergePlan {
	return &ConvergePlan{
		Shape:   ConvergeSaves,
		RowName: "saved albums",
		TargetReady: func(conn any) bool {
			return providerProvides[AlbumProvider](conn)
		},
		LoadMember: func(ctx context.Context, member, _ string) (MemberState, error) {
			src, err := OpFor[AlbumProvider](h, member, "converge target")
			if err != nil {
				return MemberState{}, err
			}
			albums, _, err := src.ListUserSavedAlbums(ctx)
			if err != nil {
				return MemberState{}, err
			}
			return MemberState{Count: len(albums), held: NewAlbumMatcher(albums)}, nil
		},
		ReadSource: func(ctx context.Context, hub, _, _ string, _ int) (SourcePage, error) {
			src, err := OpFor[AlbumProvider](h, hub, "converge source")
			if err != nil {
				return SourcePage{}, err
			}
			albums, truncated, err := src.ListUserSavedAlbums(ctx)
			if err != nil {
				return SourcePage{}, err
			}
			return SourcePage{Items: batchOf(albums), HubTotal: len(albums), Truncated: truncated}, nil
		},
		FilterHeld: func(state MemberState, items Batch) (Batch, int) {
			m, _ := state.held.(*AlbumMatcher)
			if m == nil {
				return items, 0
			}
			return filterHeld(items, m.Has)
		},
		Place: func(ctx context.Context, service, _, _ string, items Batch) (PlaceResult, error) {
			albums := batchItems[Album](items)
			if len(albums) == 0 {
				return PlaceResult{}, nil
			}
			dst, err := OpFor[AlbumWriteProvider](h, service, "converge target")
			if err != nil {
				return PlaceResult{}, err
			}
			res, err := dst.SaveAlbums(ctx, albums)
			return PlaceResult{Copied: res.Copied, Skips: res.Skips}, err
		},
		MemberExtras: func(ctx context.Context, hub, member, _, _ string) (int, func(context.Context) error, error) {
			memberList, err := savedAlbumsFor(ctx, h, member, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubList, err := savedAlbumsFor(ctx, h, hub, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			count, adopt := extrasOf(hubList, memberList, NewAlbumMatcher(hubList).Has,
				func(ctx context.Context, extras []Album) error {
					dst, err := OpFor[AlbumWriteProvider](h, hub, "conflict target")
					if err != nil {
						return err
					}
					_, err = dst.SaveAlbums(ctx, extras)
					return err
				})
			return count, adopt, nil
		},
	}
}

// savedAlbumsFor reads one service's full saved-album list.
func savedAlbumsFor(ctx context.Context, h Host, service, what string) ([]Album, error) {
	src, err := OpFor[AlbumProvider](h, service, what)
	if err != nil {
		return nil, err
	}
	albums, _, err := src.ListUserSavedAlbums(ctx)
	return albums, err
}
