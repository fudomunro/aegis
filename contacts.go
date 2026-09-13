package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
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
	ListUserContacts(ctx context.Context) ([]Contact, error)
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
// unions identifier lists and sources, and keeps the first non-empty extra.
func mergeContactInto(dst *Contact, src Contact) {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Organization == "" {
		dst.Organization = src.Organization
	}
	dst.Emails = unionStrings(dst.Emails, src.Emails)
	dst.Phones = unionStrings(dst.Phones, src.Phones)
	dst.Handles = unionStrings(dst.Handles, src.Handles)
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
// service in one neutral list: GET /v1/library/contacts
func (s *Server) HandleLibraryContacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	logger := withPeerLog(slog.With("handler", "library_contacts"), r)

	cacheKey := "library:contacts"
	if cached, ok := s.cache.Get(cacheKey); ok {
		logger.Debug("cache hit")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Guardian-Cache", "hit")
		w.Write([]byte(cached))
		return
	}

	type serviceTarget struct {
		id       string
		provider ContactProvider
	}

	var targets []serviceTarget
	var deniedContacts []string
	for id, connector := range s.connectors {
		svc := s.config.GetService(id)
		if svc == nil || svc.Action == "block" {
			continue
		}
		provider, ok := connector.(ContactProvider)
		if !ok {
			continue
		}
		if !svc.Permissions.AllowsRead("contacts") {
			deniedContacts = append(deniedContacts, id)
			logger.Info("library contacts denied by permission", "service", id,
				"permission", svc.Permissions.For("contacts"))
			continue
		}
		targets = append(targets, serviceTarget{id: id, provider: provider})
	}

	if len(targets) == 0 {
		if len(deniedContacts) > 0 {
			writeJSONError(w, http.StatusForbidden, "permission denied: agent access to contacts is not allowed")
			return
		}
		writeJSONError(w, http.StatusNotFound, "no contact-capable services configured")
		return
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		all   []Contact
		bySvc = make(map[string]LibraryServiceStatus, len(targets))
	)

	ctx := r.Context()
	for _, t := range targets {
		wg.Add(1)
		go func(t serviceTarget) {
			defer wg.Done()
			contacts, err := t.provider.ListUserContacts(ctx)
			var status LibraryServiceStatus
			switch {
			case err == nil:
				status = LibraryServiceStatus{Status: LibraryStatusOK, Count: len(contacts)}
				mu.Lock()
				all = append(all, contacts...)
				mu.Unlock()
			case errors.Is(err, ErrUnauthorized):
				logger.Info("library contacts unauthorized", "service", t.id)
				status = LibraryServiceStatus{Status: LibraryStatusUnauthorized, Error: "not authorized — connect at /v1/auth/" + t.id + "/login"}
			default:
				logger.Warn("library contacts failed", "service", t.id, "error", err)
				status = LibraryServiceStatus{Status: LibraryStatusError, Error: "failed to fetch contacts"}
			}
			mu.Lock()
			bySvc[t.id] = status
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	sort.SliceStable(all, func(i, j int) bool {
		si, sj := "", ""
		if len(all[i].Sources) > 0 {
			si = all[i].Sources[0].ServiceName
		}
		if len(all[j].Sources) > 0 {
			sj = all[j].Sources[0].ServiceName
		}
		if si != sj {
			return si < sj
		}
		return primaryContactKey(all[i]) < primaryContactKey(all[j])
	})

	merged := MergeContacts(all)

	resp := ContactList{
		Contacts:  merged,
		Total:     len(merged),
		Synced:    true,
		ByService: bySvc,
	}
	if resp.Contacts == nil {
		resp.Contacts = []Contact{}
	}

	result, err := json.Marshal(resp)
	if err != nil {
		logger.Error("failed to marshal library contacts", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build contact list")
		return
	}

	// Short TTL: aggregate spans multiple APIs and should stay fresh.
	s.cache.Set(cacheKey, string(result), 60*time.Second)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Guardian-Cache", "miss")
	w.Write(result)
}

// hasContactProviders reports whether any registered connector can list
// neutral contacts for the library endpoint.
func (s *Server) hasContactProviders() bool {
	for _, connector := range s.connectors {
		if _, ok := connector.(ContactProvider); ok {
			return true
		}
	}
	return false
}

// contactsLibraryReadable reports whether at least one configured service
// allows read access to contacts (used to decide whether to inject library hints).
func (s *Server) contactsLibraryReadable() bool {
	for _, svc := range s.config.Services {
		if svc.Action == "block" {
			continue
		}
		if svc.Permissions.AllowsRead("contacts") {
			return true
		}
	}
	return false
}

// libraryContactsSystemInstructions tells agents about the cross-service
// contacts endpoint.
func (s *Server) libraryContactsSystemInstructions() string {
	root := strings.TrimRight(s.baseURL, "/")
	return fmt.Sprintf(
		"Unified contacts library (use when the user asks about people, contacts, or their address book):\n"+
			"GET %s/v1/library/contacts — all user contacts from every connected service in one "+
			"neutral list (name, emails, phones, handles, organization, sources[]).\n"+
			"Contacts that share an email, phone, handle, or name are synced into one entry with "+
			"multiple sources[]. Prefer this endpoint for questions like \"who is in my contacts?\" "+
			"so the answer includes every connected contact-capable service.",
		root,
	)
}

// mentionsContacts detects contact-oriented questions that don't name a service.
func mentionsContacts(msg string) bool {
	lower := strings.ToLower(msg)
	for _, keyword := range []string{
		"my contacts",
		"address book",
		"contact list",
		"contacts list",
		"who is in my contacts",
		"people i know",
	} {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}
