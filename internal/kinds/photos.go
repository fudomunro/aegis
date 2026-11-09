package kinds

import (
	"context"
	"net/http"
)

type photosKind struct {
	providerKind
	h      Host
	e      *Engine
	mark   func([]Photo, string)
	copies func([]Photo)
}

func newPhotosKind(h Host, e *Engine, d Deps) Kind {
	k := &photosKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "photos", forEachPhotoSource)
	def := *BuiltinKindByID("photos")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[PhotoProvider],
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
	Aggregate(k.h, k.e, w, r, Spec[Photo]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Photo], []string) {
			return Collect[Photo, PhotoProvider](k.h, r, kind.PermType,
				func(p PhotoProvider) func(context.Context) ([]Photo, bool, error) {
					return p.ListUserPhotos
				})
		},
		ServiceOf: photoServiceName,
		SortKey:   PhotoIdentityKey,
		Merge:     MergePhotos,
		Encode: func(items []Photo, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return PhotoList{Photos: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func photoServiceName(p Photo) string {
	return PhotoServiceName(p)
}

func forEachPhotoSource(item *Photo, f func(SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *CopyInfo) { src.Copy = info },
		})
	}
}
