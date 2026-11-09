package kinds

import (
	"fmt"
	"sort"
	"strings"
)

// photoContentKey is the preferred merge identity: a content hash when the
// provider supplied one, otherwise capture time + dimensions. Filename alone
// is never used — two "IMG_0001.jpg" files from different days must not merge.
func photoContentKey(p Photo) string {
	if h := strings.TrimSpace(ExtraString(p.Extra, "content_hash")); h != "" {
		return "hash:" + strings.ToLower(h)
	}
	for _, src := range p.Sources {
		if h := strings.TrimSpace(ExtraString(src.Extra, "content_hash")); h != "" {
			return "hash:" + strings.ToLower(h)
		}
	}
	taken := NormalizePhotoTakenAt(p.TakenAt)
	if taken == "" {
		return ""
	}
	if p.Width == 0 && p.Height == 0 {
		return "time:" + taken
	}
	return fmt.Sprintf("time:%s|dim:%dx%d", taken, p.Width, p.Height)
}

// PhotoIdentityKey is the sort/merge key used by the library aggregate.
// Photos with no content identity fall back to service:id so they stay distinct.
func PhotoIdentityKey(p Photo) string {
	if k := photoContentKey(p); k != "" {
		return k
	}
	if len(p.Sources) > 0 && p.Sources[0].Service != "" && p.Sources[0].ID != "" {
		return "src:" + p.Sources[0].Service + ":" + p.Sources[0].ID
	}
	return "anon:" + PhotoServiceName(p) + ":" + p.Filename + ":" + p.Caption
}

// PhotoServiceName returns the display name of the first source, or "".
func PhotoServiceName(p Photo) string {
	if len(p.Sources) > 0 {
		return p.Sources[0].ServiceName
	}
	return ""
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
		key := PhotoIdentityKey(p)
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
		return PhotoIdentityKey(out[i]) < PhotoIdentityKey(out[j])
	})
	return out
}
