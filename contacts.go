package main

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

func normalizeHandleList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, h := range in {
		n := types.NormalizeContactHandle(h)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// unionStrings appends values from b that are not already in a.
func unionStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a)+len(b))
	for _, v := range a {
		seen[v] = true
	}
	out := a
	for _, v := range b {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// mergeContactInto folds src into dst (shared identity). Fills empty fields,
// unions identifier lists (normalized so photo.people[] and library contacts
// share one spelling) and sources, and keeps the first non-empty extra.
func mergeContactInto(dst *types.Contact, src types.Contact) {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Organization == "" {
		dst.Organization = src.Organization
	}
	dst.Emails = types.NormalizeEmailList(unionStrings(dst.Emails, src.Emails))
	dst.Phones = types.NormalizePhoneList(unionStrings(dst.Phones, src.Phones))
	dst.Handles = normalizeHandleList(unionStrings(dst.Handles, src.Handles))
	dst.Sources = append(dst.Sources, src.Sources...)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

// MergeContacts groups contacts that share any identity key (email, phone,
// handle, or normalized name) into one user-facing contact with multiple
// Sources. Uses union-find so A~email and B~email and C~name(B) form one
// entity. Order follows first appearance; sources are sorted by service name.
func MergeContacts(in []types.Contact) []types.Contact {
	if len(in) == 0 {
		return nil
	}

	// Drop contacts with no identity at all; keep them as distinct unnamed
	// entries only if they have a source ID to stand on.
	prepared := make([]types.Contact, 0, len(in))
	for _, c := range in {
		if types.PrimaryContactKey(c) == "" && len(c.Sources) > 0 {
			// Anonymous service-local entry: identity by service:id.
			s := c.Sources[0]
			key := "\x00" + s.Service + ":" + s.ID
			c.Name = strings.TrimSpace(c.Name)
			// Encode as a synthetic name-only key via a handle-like field so
			// it cannot collide with real people.
			if len(c.Handles) == 0 {
				c.Handles = []string{key}
			}
		}
		if types.PrimaryContactKey(c) == "" {
			continue
		}
		prepared = append(prepared, c)
	}
	if len(prepared) == 0 {
		return nil
	}

	parent := make([]int, len(prepared))
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			// Prefer lower index as root for stable first-appearance order.
			if rb < ra {
				ra, rb = rb, ra
			}
			parent[rb] = ra
		}
	}

	keyOwner := map[string]int{}
	for i, c := range prepared {
		for _, key := range types.ContactIdentityKeys(c) {
			if j, ok := keyOwner[key]; ok {
				union(i, j)
			} else {
				keyOwner[key] = i
			}
		}
	}

	type slot struct {
		c types.Contact
	}
	order := []int{}
	byRoot := map[int]*slot{}
	for i, c := range prepared {
		r := find(i)
		if cur, ok := byRoot[r]; ok {
			mergeContactInto(&cur.c, c)
			continue
		}
		cp := c
		cp.Emails = append([]string(nil), c.Emails...)
		cp.Phones = append([]string(nil), c.Phones...)
		cp.Handles = append([]string(nil), c.Handles...)
		cp.Sources = append([]types.ContactSource(nil), c.Sources...)
		byRoot[r] = &slot{c: cp}
		order = append(order, r)
	}

	out := make([]types.Contact, 0, len(order))
	for _, r := range order {
		e := byRoot[r]
		types.SortContactSourcesCanonicalFirst(e.c.Sources)
		out = append(out, e.c)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ni, nj := out[i].Name, out[j].Name
		if ni != nj {
			if ni == "" {
				return false
			}
			if nj == "" {
				return true
			}
			return ni < nj
		}
		return types.PrimaryContactKey(out[i]) < types.PrimaryContactKey(out[j])
	})
	return out
}

// HandleLibraryContacts returns the user's contacts from every connected
// service: GET /v1/library/contacts
func (s *Server) HandleLibraryContacts(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("contacts")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Contact]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Contact], []string) {
			return collectProviders[types.Contact, types.ContactProvider](s, kind.PermType,
				func(p types.ContactProvider) func(context.Context) ([]types.Contact, bool, error) {
					return p.ListUserContacts
				})
		},
		serviceOf: contactServiceName,
		sortKey:   types.PrimaryContactKey,
		merge:     MergeContacts,
		encode: func(merged []types.Contact, bySvc map[string]types.LibraryServiceStatus) any {
			return types.ContactList{Contacts: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampContactCanonical,
		stampCopies:   s.stampContactCopies,
	})
}

func contactServiceName(c types.Contact) string {
	if len(c.Sources) > 0 {
		return c.Sources[0].ServiceName
	}
	return ""
}
