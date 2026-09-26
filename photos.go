package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// PhotoProvider is implemented by connectors that can return the user's photos
// in Aegis's neutral format. The aggregate library endpoint uses this so
// agents can ask for "my photos" once and receive every service. Each returned
// Photo should carry one PhotoSource for that service. Connectors must not
// download or cache photo bytes — only metadata and service-provided URLs.
// https://github.com/fudomunro/aegis/issues/19
type PhotoProvider interface {
	// ListUserPhotos fetches the user's photo metadata from the upstream
	// service and maps them into neutral Photo values (service identity in
	// Sources; bytes stay on the service). Return ErrUnauthorized when no
	// valid token is available. truncated is true when the page cap was hit
	// with more items still upstream.
	ListUserPhotos(ctx context.Context) (photos []Photo, truncated bool, err error)
}

// photoServiceName returns the first source service name for stable pre-sort.
func photoServiceName(p Photo) string {
	if len(p.Sources) > 0 {
		return p.Sources[0].ServiceName
	}
	return ""
}

// photoContentKey is the preferred merge identity: a content hash when the
// provider supplied one, otherwise capture time + dimensions. Filename alone
// is never used — two "IMG_0001.jpg" files from different days must not merge.
func photoContentKey(p Photo) string {
	if h := strings.TrimSpace(photoExtraString(p.Extra, "content_hash")); h != "" {
		return "hash:" + strings.ToLower(h)
	}
	for _, src := range p.Sources {
		if h := strings.TrimSpace(photoExtraString(src.Extra, "content_hash")); h != "" {
			return "hash:" + strings.ToLower(h)
		}
	}
	taken := normalizePhotoTakenAt(p.TakenAt)
	if taken == "" {
		return ""
	}
	if p.Width == 0 && p.Height == 0 {
		return "time:" + taken
	}
	return fmt.Sprintf("time:%s|dim:%dx%d", taken, p.Width, p.Height)
}

// normalizePhotoTakenAt parses RFC3339 (or RFC3339Nano) and re-emits it at
// second precision in UTC so the same capture matches across services.
func normalizePhotoTakenAt(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format(time.RFC3339)
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
func photoIdentityKey(p Photo) string {
	if k := photoContentKey(p); k != "" {
		return k
	}
	if len(p.Sources) > 0 && p.Sources[0].Service != "" && p.Sources[0].ID != "" {
		return "src:" + p.Sources[0].Service + ":" + p.Sources[0].ID
	}
	return "anon:" + photoServiceName(p) + ":" + p.Filename + ":" + p.Caption
}

func mergePhotoInto(dst *Photo, src Photo) {
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
func mergePhotoPeople(dst, src []Contact) []Contact {
	if len(src) == 0 {
		return dst
	}
	if len(dst) == 0 {
		out := make([]Contact, len(src))
		copy(out, src)
		return out
	}
	return MergeContacts(append(append([]Contact(nil), dst...), src...))
}

// MergePhotos groups photos that share a content identity (hash, or taken_at
// plus dimensions) into one user-facing photo with multiple Sources. Photos
// without a content key are kept distinct via service:id. Canonical flags from
// any merged source are preserved so agents still know where the bytes live.
func MergePhotos(in []Photo) []Photo {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		p Photo
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
		cp.Sources = append([]PhotoSource(nil), p.Sources...)
		if len(p.People) > 0 {
			cp.People = append([]Contact(nil), p.People...)
		}
		byKey[key] = &slot{p: cp}
		order = append(order, key)
	}

	out := make([]Photo, 0, len(order))
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

// newPhoto builds a single-source neutral photo. canonical should be true
// when this service holds the authoritative bytes (the usual case for photos).
func newPhoto(service, serviceName, id, filename, mimeType, takenAt string, width, height int) Photo {
	src := PhotoSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}
	return Photo{
		TakenAt:  takenAt,
		Width:    width,
		Height:   height,
		MimeType: mimeType,
		Filename: filename,
		Sources:  []PhotoSource{src},
	}
}

// HandleLibraryPhotos returns the user's photos from every connected
// photo-capable service: GET /v1/library/photos
func (s *Server) HandleLibraryPhotos(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("photos")
	handleLibraryAggregate(s, w, r, libraryKindSpec[Photo]{
		kind: kind,
		targets: func(s *Server) ([]namedLister[Photo], []string) {
			return collectProviders[Photo, PhotoProvider](s, kind.PermType,
				func(p PhotoProvider) func(context.Context) ([]Photo, bool, error) {
					return p.ListUserPhotos
				})
		},
		serviceOf: photoServiceName,
		sortKey:   photoIdentityKey,
		merge:     MergePhotos,
		encode: func(merged []Photo, bySvc map[string]LibraryServiceStatus) any {
			return PhotoList{Photos: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampPhotoCanonical,
		stampCopies:   s.stampPhotoCopies,
	})
}

// hasPhotoProviders reports whether any registered connector can list photos.
func (s *Server) hasPhotoProviders() bool {
	return LibraryKindByID("photos").HasProviders(s)
}

// photosLibraryReadable reports whether any service allows photo reads.
func (s *Server) photosLibraryReadable() bool {
	return LibraryKindByID("photos").Readable(s)
}

// libraryPhotosSystemInstructions tells agents about the photos endpoint.
func (s *Server) libraryPhotosSystemInstructions() string {
	return s.libraryInstructionsFor("photos")
}

// mentionsPhotos detects photo-oriented questions that don't name a service.
func mentionsPhotos(msg string) bool {
	return LibraryKindByID("photos").Mentions(msg)
}
