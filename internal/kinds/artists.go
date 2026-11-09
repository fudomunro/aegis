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
		def:      def,
		provides: providerProvides[ArtistProvider],
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
