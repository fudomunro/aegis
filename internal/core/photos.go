package core

import (
	"context"
	"net/http"

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
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Photo]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Photo], []string) {
			return collectProviders[types.Photo, types.PhotoProvider](s, r, kind.PermType,
				func(p types.PhotoProvider) func(context.Context) ([]types.Photo, bool, error) {
					return p.ListUserPhotos
				})
		},
		serviceOf: photoServiceName,
		sortKey:   types.PhotoIdentityKey,
		merge:     types.MergePhotos,
		encode: func(items []types.Photo, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.PhotoList{Photos: pageItems, LibraryListMeta: types.MetaForPage(pr, true, anyServiceTruncated(bySvc), bySvc)}
		},
		markCanonical: s.stampPhotoCanonical,
		stampCopies:   s.stampPhotoCopies,
	})
}
