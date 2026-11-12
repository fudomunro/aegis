package kinds

import (
	"context"
	"net/http"
)

type artistsKind struct {
	providerKind
	h       Host
	e       *Engine
	resolve func(context.Context, []Artist)
}

func newArtistsKind(h Host, e *Engine, d Deps) Kind {
	k := &artistsKind{h: h, e: e, resolve: d.ResolveArtists}
	def := *BuiltinKindByID("artists")
	k.providerKind = providerKind{
		def:          def,
		provides:     providerProvides[ArtistProvider],
		writeCapable: providerProvides[ArtistWriteProvider],
		converge:     artistsConvergePlan(h, def.ID),
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

func (k *artistsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[Artist]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Artist], []string) {
			return Collect[Artist, ArtistProvider](k.h, r, kind.PermType,
				func(p ArtistProvider) func(context.Context) ([]Artist, bool, error) {
					return p.ListUserFollowedArtists
				})
		},
		ServiceOf: ArtistServiceName,
		SortKey:   func(a Artist) string { return NormalizeIdentityKey(a.Name) },
		Merge:     MergeArtistLibrary,
		Enrich: func(ctx context.Context, artists []Artist) {
			if k.resolve != nil {
				k.resolve(ctx, artists)
			}
		},
		Encode: func(items []Artist, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return ArtistList{Artists: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
	})
}

// artistsConvergePlan is the artists kind's converge plan (issue #382):
// create-only saves of the followed-artist collection. Following never
// unfollows — a member's own follows survive untouched.
func artistsConvergePlan(h Host, kind string) *ConvergePlan {
	return &ConvergePlan{
		Shape:   ConvergeSaves,
		RowName: "followed artists",
		TargetReady: func(conn any) bool {
			return providerProvides[ArtistWriteProvider](conn) &&
				providerProvides[ArtistProvider](conn)
		},
		LoadMember: func(ctx context.Context, member, _ string) (MemberState, error) {
			src, err := OpFor[ArtistProvider](h, member, "converge target")
			if err != nil {
				return MemberState{}, err
			}
			artists, _, err := src.ListUserFollowedArtists(ctx)
			if err != nil {
				return MemberState{}, err
			}
			return MemberState{Count: len(artists), held: NewArtistMatcher(artists)}, nil
		},
		ReadSource: func(ctx context.Context, hub, _, _ string, _ int) (SourcePage, error) {
			src, err := OpFor[ArtistProvider](h, hub, "converge source")
			if err != nil {
				return SourcePage{}, err
			}
			artists, truncated, err := src.ListUserFollowedArtists(ctx)
			if err != nil {
				return SourcePage{}, err
			}
			return SourcePage{Items: batchOf(artists), HubTotal: len(artists), Truncated: truncated}, nil
		},
		FilterHeld: func(state MemberState, items Batch) (Batch, int) {
			m, _ := state.held.(*ArtistMatcher)
			if m == nil {
				return items, 0
			}
			artists := batchItems[Artist](items)
			kept := make([]Artist, 0, len(artists))
			held := 0
			for _, a := range artists {
				if m.Has(a) {
					held++
					continue
				}
				kept = append(kept, a)
			}
			return batchOf(kept), held
		},
		Place: func(ctx context.Context, service, _, _ string, items Batch) (PlaceResult, error) {
			artists := batchItems[Artist](items)
			if len(artists) == 0 {
				return PlaceResult{}, nil
			}
			dst, err := OpFor[ArtistWriteProvider](h, service, "converge target")
			if err != nil {
				return PlaceResult{}, err
			}
			res, err := dst.FollowArtists(ctx, artists)
			return PlaceResult{Copied: res.Copied, Skips: res.Skips}, err
		},
		MemberExtras: func(ctx context.Context, hub, member, _, _ string) (int, func(context.Context) error, error) {
			memberList, err := followedArtistsFor(ctx, h, member, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubList, err := followedArtistsFor(ctx, h, hub, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			count, adopt := extrasOf(hubList, memberList, NewArtistMatcher(hubList).Has,
				func(ctx context.Context, extras []Artist) error {
					dst, err := OpFor[ArtistWriteProvider](h, hub, "conflict target")
					if err != nil {
						return err
					}
					_, err = dst.FollowArtists(ctx, extras)
					return err
				})
			return count, adopt, nil
		},
	}
}

// followedArtistsFor reads one service's full followed-artist list.
func followedArtistsFor(ctx context.Context, h Host, service, what string) ([]Artist, error) {
	src, err := OpFor[ArtistProvider](h, service, what)
	if err != nil {
		return nil, err
	}
	artists, _, err := src.ListUserFollowedArtists(ctx)
	return artists, err
}
