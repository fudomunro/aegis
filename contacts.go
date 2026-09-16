package main

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

// ContactProvider is implemented by connectors that can return the user's
// contacts in Guardian's neutral format. The aggregate library endpoint uses
// this so agents can ask for "my contacts" once and receive every service.
// Each returned Contact should carry one ContactSource for that service.
// https://github.com/fudomunro/aperture-guardian/issues/12
type ContactProvider interface {
	// ListUserContacts fetches the user's contacts from the upstream service
	// and maps them into neutral Contact values (service identity in Sources).
	// Return ErrUnauthorized when no valid token is available.
	// truncated is true when the page cap was hit with more items still upstream.
	ListUserContacts(ctx context.Context) (contacts []Contact, truncated bool, err error)
}

// NormalizeContactEmail lowercases and trims an address for identity matching.
func NormalizeContactEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormalizeContactPhone reduces a phone number to a comparable digit form.
// A leading "+" is preserved; punctuation and spaces are stripped.
func NormalizeContactPhone(phone string) string {
	s := strings.TrimSpace(phone)
	if s == "" {
		return ""
	}
	plus := strings.HasPrefix(s, "+")
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if digits == "" {
		return ""
	}
	if plus {
		return "+" + digits
	}
	return digits
}

// NormalizeContactHandle lowercases and trims a non-email handle.
func NormalizeContactHandle(handle string) string {
	s := strings.ToLower(strings.TrimSpace(handle))
	s = strings.TrimPrefix(s, "@")
	return s
}

// ContactIdentityKeys returns ordered merge keys for a contact: email first
// (strongest), then phone, then handle. Display name is used only when the
// contact has no stronger identifier, so two different "Alex Kim" addresses
// do not merge just because the names match. Empty keys are omitted. The
// first key is the contact's primary identity.
func ContactIdentityKeys(c Contact) []string {
	var keys []string
	seen := map[string]bool{}
	add := func(prefix, val string) {
		if val == "" {
			return
		}
		k := prefix + ":" + val
		if seen[k] {
			return
		}
		seen[k] = true
		keys = append(keys, k)
	}
	for _, e := range c.Emails {
		add("email", NormalizeContactEmail(e))
	}
	for _, p := range c.Phones {
		add("phone", NormalizeContactPhone(p))
	}
	for _, h := range c.Handles {
		add("handle", NormalizeContactHandle(h))
	}
	if len(keys) == 0 {
		if name := NormalizeIdentityKey(c.Name); name != "" {
			add("name", name)
		}
	}
	return keys
}

// primaryContactKey is the first identity key, or "" when the contact has none.
func primaryContactKey(c Contact) string {
	keys := ContactIdentityKeys(c)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// newContact builds a unified contact from one service spelling.
func newContact(service, serviceName, id, name string, emails, phones []string) Contact {
	emails = normalizeEmailList(emails)
	phones = normalizePhoneList(phones)
	name = strings.TrimSpace(name)
	if name == "" && len(emails) == 0 && len(phones) == 0 {
		return Contact{}
	}
	src := ContactSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Name:        name,
		Emails:      emails,
		Phones:      phones,
	}
	return Contact{
		Name:    name,
		Emails:  emails,
		Phones:  phones,
		Sources: []ContactSource{src},
	}
}

func normalizeEmailList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		n := NormalizeContactEmail(e)
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

func normalizePhoneList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		n := NormalizeContactPhone(p)
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

func normalizeHandleList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, h := range in {
		n := NormalizeContactHandle(h)
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
func mergeContactInto(dst *Contact, src Contact) {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Organization == "" {
		dst.Organization = src.Organization
	}
	dst.Emails = normalizeEmailList(unionStrings(dst.Emails, src.Emails))
	dst.Phones = normalizePhoneList(unionStrings(dst.Phones, src.Phones))
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
func MergeContacts(in []Contact) []Contact {
	if len(in) == 0 {
		return nil
	}

	// Drop contacts with no identity at all; keep them as distinct unnamed
	// entries only if they have a source ID to stand on.
	prepared := make([]Contact, 0, len(in))
	for _, c := range in {
		if primaryContactKey(c) == "" && len(c.Sources) > 0 {
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
		if primaryContactKey(c) == "" {
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
	var find func(int) int
	find = func(x int) int {
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
		for _, key := range ContactIdentityKeys(c) {
			if j, ok := keyOwner[key]; ok {
				union(i, j)
			} else {
				keyOwner[key] = i
			}
		}
	}

	type slot struct {
		c Contact
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
		cp.Sources = append([]ContactSource(nil), c.Sources...)
		byRoot[r] = &slot{c: cp}
		order = append(order, r)
	}

	out := make([]Contact, 0, len(order))
	for _, r := range order {
		e := byRoot[r]
		sort.SliceStable(e.c.Sources, func(i, j int) bool {
			if e.c.Sources[i].ServiceName != e.c.Sources[j].ServiceName {
				return e.c.Sources[i].ServiceName < e.c.Sources[j].ServiceName
			}
			return e.c.Sources[i].Service < e.c.Sources[j].Service
		})
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
		return primaryContactKey(out[i]) < primaryContactKey(out[j])
	})
	return out
}

// HandleLibraryContacts returns the user's contacts from every connected
// service: GET /v1/library/contacts
func (s *Server) HandleLibraryContacts(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("contacts")
	handleLibraryAggregate(s, w, r, libraryKindSpec[Contact]{
		kind: kind,
		targets: func(s *Server) ([]namedLister[Contact], []string) {
			return collectProviders[Contact, ContactProvider](s, kind.PermType,
				func(p ContactProvider) func(context.Context) ([]Contact, bool, error) {
					return p.ListUserContacts
				})
		},
		serviceOf: contactServiceName,
		sortKey:   primaryContactKey,
		merge:     MergeContacts,
		encode: func(merged []Contact, bySvc map[string]LibraryServiceStatus) any {
			return ContactList{Contacts: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
	})
}

func contactServiceName(c Contact) string {
	if len(c.Sources) > 0 {
		return c.Sources[0].ServiceName
	}
	return ""
}

// hasContactProviders reports whether any registered connector can list contacts.
func (s *Server) hasContactProviders() bool {
	return LibraryKindByID("contacts").HasProviders(s)
}

// contactsLibraryReadable reports whether any service allows contact reads.
func (s *Server) contactsLibraryReadable() bool {
	return LibraryKindByID("contacts").Readable(s)
}

// libraryContactsSystemInstructions tells agents about the contacts endpoint.
func (s *Server) libraryContactsSystemInstructions() string {
	return LibraryKindByID("contacts").Instructions(s.baseURL)
}

// mentionsContacts detects contact-oriented questions that don't name a service.
func mentionsContacts(msg string) bool {
	return LibraryKindByID("contacts").Mentions(msg)
}
