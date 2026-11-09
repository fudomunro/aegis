package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

type artistsKind struct {
	providerKind
	h       Host
	e       *Engine
	resolve func(context.Context, []types.Artist)
}

func newArtistsKind(h Host, e *Engine, d Deps) Kind {
	k := &artistsKind{h: h, e: e, resolve: d.ResolveArtists}
	def := *BuiltinKindByID("artists")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[types.ArtistProvider],
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
	Aggregate(k.h, k.e, w, r, Spec[types.Artist]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.Artist], []string) {
			return Collect[types.Artist, types.ArtistProvider](k.h, r, kind.PermType,
				func(p types.ArtistProvider) func(context.Context) ([]types.Artist, bool, error) {
					return p.ListUserFollowedArtists
				})
		},
		ServiceOf: types.ArtistServiceName,
		SortKey:   func(a types.Artist) string { return types.NormalizeIdentityKey(a.Name) },
		Merge:     types.MergeArtistLibrary,
		Enrich: func(ctx context.Context, artists []types.Artist) {
			if k.resolve != nil {
				k.resolve(ctx, artists)
			}
		},
		Encode: func(items []types.Artist, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.ArtistList{Artists: pageItems, LibraryListMeta: types.MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
	})
}
