package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

type photosKind struct {
	providerKind
	h      Host
	e      *Engine
	mark   func([]types.Photo, string)
	copies func([]types.Photo)
}

func newPhotosKind(h Host, e *Engine, d Deps) Kind {
	k := &photosKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "photos", forEachPhotoSource)
	def := *types.BuiltinKindByID("photos")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[types.PhotoProvider],
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

func (k *photosKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[types.Photo]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.Photo], []string) {
			return Collect[types.Photo, types.PhotoProvider](k.h, r, kind.PermType,
				func(p types.PhotoProvider) func(context.Context) ([]types.Photo, bool, error) {
					return p.ListUserPhotos
				})
		},
		ServiceOf: photoServiceName,
		SortKey:   types.PhotoIdentityKey,
		Merge:     types.MergePhotos,
		Encode: func(items []types.Photo, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.PhotoList{Photos: pageItems, LibraryListMeta: types.MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func photoServiceName(p types.Photo) string {
	return types.PhotoServiceName(p)
}

func forEachPhotoSource(item *types.Photo, f func(SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}
