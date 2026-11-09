package kinds

import (
	"sort"
	"strings"
)

// DocumentIdentityKey is the sort/merge key used by the library aggregate.
// Prefer durable content checksum, then archive serial, then title + created.
// Documents without any identity fall back to service:id so they stay distinct.
func DocumentIdentityKey(d Document) string {
	if id := strings.TrimSpace(d.EntityID); id != "" {
		return "entity:" + strings.ToLower(id)
	}
	for _, src := range d.Sources {
		if cs := strings.TrimSpace(ExtraString(src.Extra, "checksum")); cs != "" {
			return "checksum:" + strings.ToLower(cs)
		}
		if asn := strings.TrimSpace(ExtraString(src.Extra, "archive_serial")); asn != "" {
			return "asn:" + asn
		}
	}
	title := NormalizeIdentityKey(d.Title)
	created := strings.TrimSpace(d.CreatedAt)
	if title == "" || created == "" {
		if len(d.Sources) > 0 && d.Sources[0].Service != "" && d.Sources[0].ID != "" {
			return "src:" + d.Sources[0].Service + ":" + d.Sources[0].ID
		}
		return "anon:" + DocumentServiceName(d) + ":" + d.Title + ":" + d.CreatedAt
	}
	return "title:" + title + "|created:" + created
}

// documentMatchKind records how identity was established for multi-source merges.
func documentMatchKind(key string) string {
	switch {
	case strings.HasPrefix(key, "entity:"):
		// EntityID may be checksum or archive serial; both are durable IDs.
		return "entity_id"
	case strings.HasPrefix(key, "checksum:"):
		return "checksum"
	case strings.HasPrefix(key, "asn:"):
		return "archive_serial"
	case strings.HasPrefix(key, "title:"):
		return "title_created"
	default:
		return ""
	}
}

func mergeDocumentInto(dst *Document, src Document) {
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.MimeType == "" {
		dst.MimeType = src.MimeType
	}
	if dst.CreatedAt == "" {
		dst.CreatedAt = src.CreatedAt
	}
	if dst.ModifiedAt == "" {
		dst.ModifiedAt = src.ModifiedAt
	}
	if dst.Correspondent == "" {
		dst.Correspondent = src.Correspondent
	}
	if dst.DocumentType == "" {
		dst.DocumentType = src.DocumentType
	}
	if dst.Excerpt == "" {
		dst.Excerpt = src.Excerpt
	}
	if dst.EntityID == "" {
		dst.EntityID = src.EntityID
	}
	dst.Tags = unionDocumentStrings(dst.Tags, src.Tags)
	dst.People = MergeMessageContacts(dst.People, src.People)
	dst.Sources = append(dst.Sources, src.Sources...)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

func unionDocumentStrings(dst, src []string) []string {
	if len(src) == 0 {
		return dst
	}
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{dst, src} {
		for _, l := range list {
			l = strings.TrimSpace(l)
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// MergeDocuments groups documents that share a durable identity (checksum,
// archive serial, or entity id) or the same title + created into one
// user-facing entry with multiple Sources. Documents without an identity
// stay distinct via service:id.
func MergeDocuments(in []Document) []Document {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		d Document
	}
	var order []string
	byKey := map[string]*slot{}

	for _, doc := range in {
		key := DocumentIdentityKey(doc)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergeDocumentInto(&cur.d, doc)
			continue
		}
		cp := doc
		cp.Sources = append([]DocumentSource(nil), doc.Sources...)
		if len(doc.Tags) > 0 {
			cp.Tags = append([]string(nil), doc.Tags...)
		}
		if len(doc.People) > 0 {
			cp.People = append([]Contact(nil), doc.People...)
		}
		byKey[key] = &slot{d: cp}
		order = append(order, key)
	}

	out := make([]Document, 0, len(order))
	for _, key := range order {
		item := byKey[key]
		if len(item.d.Sources) > 1 {
			if mk := documentMatchKind(key); mk != "" {
				item.d.Match = mk
			}
		}
		sort.SliceStable(item.d.Sources, func(i, j int) bool {
			if item.d.Sources[i].Canonical != item.d.Sources[j].Canonical {
				return item.d.Sources[i].Canonical
			}
			if item.d.Sources[i].ServiceName != item.d.Sources[j].ServiceName {
				return item.d.Sources[i].ServiceName < item.d.Sources[j].ServiceName
			}
			return item.d.Sources[i].Service < item.d.Sources[j].Service
		})
		out = append(out, item.d)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := out[i].CreatedAt, out[j].CreatedAt
		if ci != cj {
			return ci > cj // newest first
		}
		ti, tj := out[i].Title, out[j].Title
		if ti != tj {
			return ti < tj
		}
		return DocumentIdentityKey(out[i]) < DocumentIdentityKey(out[j])
	})
	return out
}
