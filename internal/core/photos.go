package core

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

// photoServiceName returns the first source service name for stable pre-sort.
func photoServiceName(p types.Photo) string {
	return types.PhotoServiceName(p)
}

// HandleLibraryPhotos returns the user's photos from every connected
// photo-capable service: GET /v1/library/photos
func (s *Server) HandleLibraryPhotos(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("photos")
	mark, copies := kinds.Stamps(s, "photos", forEachPhotoSource)
	kinds.Aggregate(s, s.libraryEngine(), w, r, kinds.Spec[types.Photo]{
		Kind: kind,
		Targets: func(r *http.Request) ([]kinds.NamedLister[types.Photo], []string) {
			return kinds.Collect[types.Photo, types.PhotoProvider](s, r, kind.PermType,
				func(p types.PhotoProvider) func(context.Context) ([]types.Photo, bool, error) {
					return p.ListUserPhotos
				})
		},
		ServiceOf: photoServiceName,
		SortKey:   types.PhotoIdentityKey,
		Merge:     types.MergePhotos,
		Encode: func(items []types.Photo, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.PhotoList{Photos: pageItems, LibraryListMeta: types.MetaForPage(pr, true, kinds.AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: mark,
		StampCopies:   copies,
	})
}

func forEachPhotoSource(item *types.Photo, f func(kinds.SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(kinds.SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}
