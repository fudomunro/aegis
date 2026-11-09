package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

type albumsKind struct {
	providerKind
	h       Host
	e       *Engine
	resolve func(context.Context, []types.Album)
}

func newAlbumsKind(h Host, e *Engine, d Deps) Kind {
	k := &albumsKind{h: h, e: e, resolve: d.ResolveAlbums}
	def := *types.BuiltinKindByID("albums")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[types.AlbumProvider],
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
	Aggregate(k.h, k.e, w, r, Spec[types.Album]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.Album], []string) {
			return Collect[types.Album, types.AlbumProvider](k.h, r, kind.PermType,
				func(p types.AlbumProvider) func(context.Context) ([]types.Album, bool, error) {
					return p.ListUserSavedAlbums
				})
		},
		ServiceOf: types.AlbumServiceName,
		SortKey:   types.AlbumIdentityKey,
		Merge:     types.MergeAlbumsSync,
		Enrich: func(ctx context.Context, albums []types.Album) {
			if k.resolve != nil {
				k.resolve(ctx, albums)
			}
		},
		Encode: func(items []types.Album, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.AlbumList{Albums: pageItems, LibraryListMeta: types.MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
	})
}
