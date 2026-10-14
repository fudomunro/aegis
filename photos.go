package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// photoServiceName returns the first source service name for stable pre-sort.
func photoServiceName(p types.Photo) string {
	if len(p.Sources) > 0 {
		return p.Sources[0].ServiceName
	}
	return ""
}

// photoContentKey is the preferred merge identity: a content hash when the
// provider supplied one, otherwise capture time + dimensions. Filename alone
// is never used — two "IMG_0001.jpg" files from different days must not merge.
func photoContentKey(p types.Photo) string {
	if h := strings.TrimSpace(photoExtraString(p.Extra, "content_hash")); h != "" {
		return "hash:" + strings.ToLower(h)
	}
	for _, src := range p.Sources {
		if h := strings.TrimSpace(photoExtraString(src.Extra, "content_hash")); h != "" {
			return "hash:" + strings.ToLower(h)
		}
	}
	taken := types.NormalizePhotoTakenAt(p.TakenAt)
	if taken == "" {
		return ""
	}
	if p.Width == 0 && p.Height == 0 {
		return "time:" + taken
	}
	return fmt.Sprintf("time:%s|dim:%dx%d", taken, p.Width, p.Height)
}

func photoExtraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v, ok := extra[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// photoIdentityKey is the sort/merge key used by the library aggregate.
// Photos with no content identity fall back to service:id so they stay distinct.
func photoIdentityKey(p types.Photo) string {
	if k := photoContentKey(p); k != "" {
		return k
	}
	if len(p.Sources) > 0 && p.Sources[0].Service != "" && p.Sources[0].ID != "" {
		return "src:" + p.Sources[0].Service + ":" + p.Sources[0].ID
	}
	return "anon:" + photoServiceName(p) + ":" + p.Filename + ":" + p.Caption
}

func mergePhotoInto(dst *types.Photo, src types.Photo) {
	if dst.TakenAt == "" {
		dst.TakenAt = src.TakenAt
	}
	if dst.Width == 0 {
		dst.Width = src.Width
	}
	if dst.Height == 0 {
		dst.Height = src.Height
	}
	if dst.MimeType == "" {
		dst.MimeType = src.MimeType
	}
	if dst.Filename == "" {
		dst.Filename = src.Filename
	}
	if dst.Caption == "" {
		dst.Caption = src.Caption
	}
	dst.Sources = append(dst.Sources, src.Sources...)
	dst.People = mergePhotoPeople(dst.People, src.People)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

// mergePhotoPeople unions people across photo sources using the same contact
// identity matching as the contacts library (email → phone → handle → name).
// This is the first cross-type link: photo.people[] can resolve to a Google
// Contacts (or other) Contact entity.
func mergePhotoPeople(dst, src []types.Contact) []types.Contact {
	if len(src) == 0 {
		return dst
	}
	if len(dst) == 0 {
		out := make([]types.Contact, len(src))
		copy(out, src)
		return out
	}
	return MergeContacts(append(append([]types.Contact(nil), dst...), src...))
}

// MergePhotos groups photos that share a content identity (hash, or taken_at
// plus dimensions) into one user-facing photo with multiple Sources. Photos
// without a content key are kept distinct via service:id. Canonical flags from
// any merged source are preserved so agents still know where the bytes live.
func MergePhotos(in []types.Photo) []types.Photo {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		p types.Photo
	}
	var order []string
	byKey := map[string]*slot{}

	for _, p := range in {
		key := photoIdentityKey(p)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergePhotoInto(&cur.p, p)
			continue
		}
		cp := p
		cp.Sources = append([]types.PhotoSource(nil), p.Sources...)
		if len(p.People) > 0 {
			cp.People = append([]types.Contact(nil), p.People...)
		}
		byKey[key] = &slot{p: cp}
		order = append(order, key)
	}

	out := make([]types.Photo, 0, len(order))
	for _, key := range order {
		e := byKey[key]
		sort.SliceStable(e.p.Sources, func(i, j int) bool {
			// Prefer canonical sources first so blob location is obvious.
			if e.p.Sources[i].Canonical != e.p.Sources[j].Canonical {
				return e.p.Sources[i].Canonical
			}
			if e.p.Sources[i].ServiceName != e.p.Sources[j].ServiceName {
				return e.p.Sources[i].ServiceName < e.p.Sources[j].ServiceName
			}
			return e.p.Sources[i].Service < e.p.Sources[j].Service
		})
		out = append(out, e.p)
	}

	sort.SliceStable(out, func(i, j int) bool {
		// Newest captures first — photos are usually sought by recency.
		ti, tj := out[i].TakenAt, out[j].TakenAt
		if ti != tj {
			return ti > tj
		}
		return photoIdentityKey(out[i]) < photoIdentityKey(out[j])
	})
	return out
}

// HandleLibraryPhotos returns the user's photos from every connected
// photo-capable service: GET /v1/library/photos
func (s *Server) HandleLibraryPhotos(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("photos")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Photo]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Photo], []string) {
			return collectProviders[types.Photo, types.PhotoProvider](s, kind.PermType,
				func(p types.PhotoProvider) func(context.Context) ([]types.Photo, bool, error) {
					return p.ListUserPhotos
				})
		},
		serviceOf: photoServiceName,
		sortKey:   photoIdentityKey,
		merge:     MergePhotos,
		encode: func(merged []types.Photo, bySvc map[string]types.LibraryServiceStatus) any {
			return types.PhotoList{Photos: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampPhotoCanonical,
		stampCopies:   s.stampPhotoCopies,
	})
}
