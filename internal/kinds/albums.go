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
