package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/core/search"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

func searchTestServer(t *testing.T, contacts ...kinds.Contact) *Server {
	t.Helper()
	cfg := &Config{Services: []Service{{ID: "google", Name: "Google", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"google": &fakeContactProvider{contacts: contacts},
	}
	return srv
}

func searchRequest(t *testing.T, srv *Server, handler http.HandlerFunc, path string) (*httptest.ResponseRecorder, kinds.ContactList) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(handler)(w, req)
	var resp kinds.ContactList
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	return w, resp
}

func TestLibrarySearch_QueryHitsIndexedItems(t *testing.T) {
	srv := searchTestServer(t,
		kinds.Contact{
			Name:    "Ada Lovelace",
			Emails:  []string{"ada@example.com"},
			Sources: []kinds.ContactSource{{Service: "google", ServiceName: "Google", ID: "g1"}},
		},
		kinds.Contact{
			Name:    "Grace Hopper",
			Emails:  []string{"grace@example.com"},
			Sources: []kinds.ContactSource{{Service: "google", ServiceName: "Google", ID: "g2"}},
		},
	)

	// A bare list fetch materializes the index (write-behind, off the
	// response path — wait for it before asserting index state).
	w, _ := searchRequest(t, srv, srv.libraryHandler("contacts"), "/v1/library/contacts")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	srv.waitIndex()
	n, err := srv.search.Count("contacts", "")
	if err != nil || n != 2 {
		t.Fatalf("index count = %d err=%v, want 2", n, err)
	}

	w, resp := searchRequest(t, srv, srv.libraryHandler("contacts"), "/v1/library/contacts?q=grace")
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 1 || resp.Contacts[0].Name != "Grace Hopper" {
		t.Fatalf("q=grace = %+v", resp.Contacts)
	}

	// Multi-token queries AND together.
	w, resp = searchRequest(t, srv, srv.libraryHandler("contacts"), "/v1/library/contacts?q=grace+lovelace")
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 0 {
		t.Fatalf("q=grace+lovelace must match nothing: %+v", resp.Contacts)
	}
}

func TestLibrarySearch_ColdIndexBootstrapsFromFetch(t *testing.T) {
	srv := searchTestServer(t, kinds.Contact{
		Name:    "Katherine Johnson",
		Emails:  []string{"kj@example.com"},
		Sources: []kinds.ContactSource{{Service: "google", ServiceName: "Google", ID: "g1"}},
	})

	// First-ever query with an empty index: one live fetch materializes it.
	w, resp := searchRequest(t, srv, srv.libraryHandler("contacts"), "/v1/library/contacts?q=katherine")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 1 || resp.Contacts[0].Name != "Katherine Johnson" {
		t.Fatalf("cold search = %+v", resp.Contacts)
	}
	if n, _ := srv.search.Count("contacts", ""); n != 1 {
		t.Fatalf("index not bootstrapped: count=%d", n)
	}
}

func TestLibrarySearch_DisabledIndex(t *testing.T) {
	srv := searchTestServer(t, kinds.Contact{
		Name:    "Ada Lovelace",
		Sources: []kinds.ContactSource{{Service: "google", ID: "g1"}},
	})
	srv.search = nil
	w, _ := searchRequest(t, srv, srv.libraryHandler("contacts"), "/v1/library/contacts?q=ada")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestLibrarySearch_UnsupportedKindRejectsQ(t *testing.T) {
	srv := searchTestServer(t)
	req := httptest.NewRequest("GET", "/v1/library/contacts?q=x", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	kinds.Aggregate(srv, srv.libraryEngine(), w, req, kinds.Spec[kinds.Contact]{
		Kind:      kinds.LibraryKind{ID: "notes", Label: "notes"},
		ServiceOf: func(kinds.Contact) string { return "" },
		SortKey:   func(kinds.Contact) string { return "" },
		Merge:     func(in []kinds.Contact) []kinds.Contact { return in },
		Encode: func(items []kinds.Contact, page kinds.PageRequest, bySvc map[string]kinds.LibraryServiceStatus) any {
			return kinds.ContactList{Contacts: items}
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestLibrarySearch_DatasetPartitionIsolated(t *testing.T) {
	srv := searchTestServer(t, kinds.Contact{
		Name:    "Frodo Baggins",
		Sources: []kinds.ContactSource{{Service: "google", ID: "g1"}},
	})
	shire := &scopes.Scope{ID: "shire", Dataset: "shire", Grants: readAllGrants()}
	gondor := &scopes.Scope{ID: "gondor", Dataset: "gondor", Grants: readAllGrants()}

	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req = scopes.WithDataScope(req, shire)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("contacts"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("shire list status = %d body=%s", w.Code, w.Body.String())
	}
	srv.waitIndex()

	req = scopes.WithDataScope(httptest.NewRequest("GET", "/v1/library/contacts?q=frodo", nil), shire)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("contacts"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("shire search status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Fatalf("shire search = %+v", resp.Contacts)
	}

	// Seed the gondor partition so its query is warm and cannot bootstrap
	// from the scope-unaware fake provider.
	if err := srv.search.Upsert("contacts", "gondor", []shared.SearchDocument{{
		ItemID:   "g1",
		Title:    "Gondor Contact",
		Body:     "Gondor Contact",
		Payload:  []byte(`{"name":"Gondor Contact","sources":[{"service":"google","id":"x"}]}`),
		Services: []string{"google"},
	}}); err != nil {
		t.Fatal(err)
	}
	req = scopes.WithDataScope(httptest.NewRequest("GET", "/v1/library/contacts?q=frodo", nil), gondor)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("contacts"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("gondor search status = %d body=%s", w.Code, w.Body.String())
	}
	resp = kinds.ContactList{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 0 {
		t.Fatalf("gondor search leaked shire contact: %+v", resp.Contacts)
	}
}

func TestLibrarySearch_DropsDeniedServiceHits(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google", Name: "Google", Action: "inject"},
		{ID: "outlook", Name: "Outlook", Action: "block"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"google": &fakeContactProvider{contacts: []kinds.Contact{{
			Name:    "Ada Lovelace",
			Sources: []kinds.ContactSource{{Service: "google", ID: "g1"}},
		}}},
		"outlook": &fakeContactProvider{contacts: []kinds.Contact{{
			Name:    "Secret Person",
			Sources: []kinds.ContactSource{{Service: "outlook", ID: "o1"}},
		}}},
	}

	// Index both rows via a fetch that includes only readable services, then
	// write the denied one behind the scenes to prove query-time filtering.
	w, _ := searchRequest(t, srv, srv.libraryHandler("contacts"), "/v1/library/contacts")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	srv.waitIndex()
	if err := srv.search.Upsert("contacts", "", []shared.SearchDocument{{
		ItemID:   "secret",
		Title:    "Secret Person",
		Body:     "Secret Person",
		Payload:  []byte(`{"name":"Secret Person","sources":[{"service":"outlook","id":"o1"}]}`),
		Services: []string{"outlook"},
	}}); err != nil {
		t.Fatal(err)
	}

	w, resp := searchRequest(t, srv, srv.libraryHandler("contacts"), "/v1/library/contacts?q=secret")
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 0 {
		t.Fatalf("denied service leaked into search: %+v", resp.Contacts)
	}
	w, resp = searchRequest(t, srv, srv.libraryHandler("contacts"), "/v1/library/contacts?q=ada")
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 1 {
		t.Fatalf("readable contact missing: %+v", resp.Contacts)
	}
}

func TestLibrarySearch_MetadataRouteStripsExcerpt(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"documents/metadata": {Default: shared.AccessRead}},
		Services: []Service{{
			ID:     "paperless",
			Name:   "Paperless-ngx",
			Action: "inject",
		}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []kinds.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Medical letter", "2024-01-01T00:00:00Z", "sensitive excerpt"),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents/metadata?q=medical", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/documents/metadata"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.DocumentList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Fatalf("want 1 hit: %+v", resp.Documents)
	}
	if resp.Documents[0].Excerpt != "" {
		t.Fatalf("metadata search leaked excerpt: %q", resp.Documents[0].Excerpt)
	}
}

func TestLibrarySearch_InstructionsMentionQ(t *testing.T) {
	kind := *LibraryKindByID("contacts")
	text := kind.Instructions("https://aegis.example.com")
	if !strings.Contains(text, "?q=") {
		t.Fatalf("searchable kind instructions missing q=: %s", text)
	}
	if strings.Contains(text, "issue #") {
		t.Fatalf("agent strings must not cite issues: %s", text)
	}
}

func TestHandleAdminSearchStatus(t *testing.T) {
	srv := searchTestServer(t, kinds.Contact{
		Name:    "Ada Lovelace",
		Sources: []kinds.ContactSource{{Service: "google", ID: "g1"}},
	})
	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("contacts"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	srv.waitIndex()

	req = httptest.NewRequest("GET", "/v1/admin/search/status", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var st search.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if !st.Enabled {
		t.Fatal("index must report enabled")
	}
	if st.Path == "" {
		t.Fatal("status must include the index path")
	}
	var contacts, tracks *search.KindStatus
	for i := range st.Kinds {
		switch st.Kinds[i].ID {
		case "contacts":
			contacts = &st.Kinds[i]
		case "tracks":
			tracks = &st.Kinds[i]
		}
	}
	if contacts == nil || !contacts.Searchable || contacts.Docs != 1 {
		t.Fatalf("contacts status = %+v", contacts)
	}
	if tracks == nil || !tracks.Searchable || tracks.Docs != 0 {
		t.Fatalf("tracks status = %+v (searchable but cold)", tracks)
	}
	if st.Total != 1 {
		t.Fatalf("total = %d, want 1", st.Total)
	}

	// Disabled index still reports kind searchability (q= is documented as
	// unsupported at runtime, not undeclared).
	srv.search = nil
	req = httptest.NewRequest("GET", "/v1/admin/search/status", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSearchStatus)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disabled status = %d body=%s", w.Code, w.Body.String())
	}
	st = search.Status{}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Enabled {
		t.Fatal("index must report disabled")
	}
	if st.Total != 0 {
		t.Fatalf("disabled total = %d", st.Total)
	}
}

// documentsQuery runs GET path as bearer and returns the document hits.
func documentsQuery(t *testing.T, srv *Server, bearer, path string) []kinds.Document {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("documents"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("documents status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.DocumentList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.Documents
}

// eventsQuery runs GET path as bearer and returns the calendar-event hits.
func eventsQuery(t *testing.T, srv *Server, bearer, path string) []kinds.CalendarEvent {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("events status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.Events
}

func TestLibrarySearch_FreeBusyRouteKeepsOwnerRows(t *testing.T) {
	// The freebusy route narrows only its response (issue #348): search
	// index rows stay at owner-tier detail.
	cfg := &Config{
		typePolicies: policy.DataGrants{
			"events":   {Default: shared.AccessRead},
			"freebusy": {Default: shared.AccessRead},
		},
		Services: []Service{{
			ID:     "google-calendar",
			Name:   "Google Calendar",
			Action: "inject",
		}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{events: freeBusyTestEvents()},
	}

	// A first query materializes the index at owner tier.
	before := eventsQuery(t, srv, "test-key", "/v1/library/events?q=appointment")
	if len(before) != 1 || before[0].Title != "Doctor appointment" {
		t.Fatalf("owner baseline = %+v", before)
	}
	beforeDocs, err := srv.search.Count("events", "")
	if err != nil {
		t.Fatal(err)
	}

	// A freebusy-route listing must not rewrite those rows.
	req := httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/freebusy"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("freebusy list status = %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Doctor") {
		t.Fatalf("freebusy route leaked title: %s", w.Body.String())
	}

	srv.cache.Clear()
	after := eventsQuery(t, srv, "test-key", "/v1/library/events?q=appointment")
	if len(after) != 1 || after[0].Title != "Doctor appointment" {
		t.Fatalf("owner ?q= changed after freebusy fetch: before=%+v after=%+v", before, after)
	}
	afterDocs, err := srv.search.Count("events", "")
	if err != nil {
		t.Fatal(err)
	}
	if afterDocs != beforeDocs {
		t.Fatalf("index rows changed: before=%d after=%d", beforeDocs, afterDocs)
	}
}

func TestLibrarySearch_RedactedAppFetchKeepsOwnerDocumentRows(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"documents": {Default: shared.AccessRead}},
		Services: []Service{{
			ID:     "paperless",
			Name:   "Paperless-ngx",
			Action: "inject",
		}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []kinds.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Medical letter", "2024-01-01T00:00:00Z", "sensitive excerpt"),
		}},
	}
	store := testAppStore(t)
	srv.apps = store
	_, appToken := createTestApp(t, store, "reader", shared.GrantMap{"documents/metadata": shared.AccessRead})

	before := documentsQuery(t, srv, "test-key", "/v1/library/documents?q=medical")
	if len(before) != 1 || before[0].Excerpt != "sensitive excerpt" {
		t.Fatalf("owner baseline = %+v", before)
	}

	// A metadata-scoped app sees titles only; its fetch must not demote the
	// stored row to that view.
	req := httptest.NewRequest("GET", "/v1/library/documents/metadata", nil)
	req.Header.Set("Authorization", "Bearer "+appToken)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/documents/metadata"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("app list status = %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sensitive") {
		t.Fatalf("metadata app leaked excerpt: %s", w.Body.String())
	}

	srv.cache.Clear()
	after := documentsQuery(t, srv, "test-key", "/v1/library/documents?q=medical")
	if len(after) != 1 || after[0].Excerpt != "sensitive excerpt" {
		t.Fatalf("owner ?q= returned a reduced payload: before=%+v after=%+v", before, after)
	}
}

func TestLibrarySearch_CompleteFetchDropsDeletedItems(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID: "paperless", Name: "Paperless-ngx", Action: "inject",
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	provider := &fakeDocumentProvider{documents: []kinds.Document{
		sampleDoc("paperless", "Paperless-ngx", "1", "Medical letter", "2024-01-01T00:00:00Z", "one"),
		sampleDoc("paperless", "Paperless-ngx", "2", "Lease agreement", "2024-02-01T00:00:00Z", "two"),
	}}
	srv.connectors = map[string]connectors.ServiceConnector{"paperless": provider}

	if docs := documentsQuery(t, srv, "test-key", "/v1/library/documents"); len(docs) != 2 {
		t.Fatalf("list = %+v", docs)
	}
	srv.waitIndex()

	// The lease is deleted upstream; the next complete list fetch must drop
	// its row instead of upserting around it.
	provider.documents = provider.documents[:1]
	srv.cache.Clear()
	if docs := documentsQuery(t, srv, "test-key", "/v1/library/documents"); len(docs) != 1 {
		t.Fatalf("list after delete = %+v", docs)
	}
	srv.waitIndex()

	srv.cache.Clear()
	if hits := documentsQuery(t, srv, "test-key", "/v1/library/documents?q=lease"); len(hits) != 0 {
		t.Fatalf("deleted item still matches q=: %+v", hits)
	}
	srv.cache.Clear()
	if hits := documentsQuery(t, srv, "test-key", "/v1/library/documents?q=medical"); len(hits) != 1 {
		t.Fatalf("survivor missing from q=: %+v", hits)
	}
}

func TestLibrarySearch_DegradedOrTruncatedFetchKeepsIndexRows(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID: "paperless", Name: "Paperless-ngx", Action: "inject",
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	provider := &fakeDocumentProvider{documents: []kinds.Document{
		sampleDoc("paperless", "Paperless-ngx", "1", "Medical letter", "2024-01-01T00:00:00Z", "one"),
		sampleDoc("paperless", "Paperless-ngx", "2", "Lease agreement", "2024-02-01T00:00:00Z", "two"),
	}}
	srv.connectors = map[string]connectors.ServiceConnector{"paperless": provider}

	if docs := documentsQuery(t, srv, "test-key", "/v1/library/documents"); len(docs) != 2 {
		t.Fatalf("list = %+v", docs)
	}
	srv.waitIndex()

	// A failing provider tells us nothing about deletions: rows stay.
	provider.err = errors.New("upstream boom")
	srv.cache.Clear()
	req := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("documents"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("degraded list status = %d body=%s", w.Code, w.Body.String())
	}
	srv.waitIndex()
	if n, _ := srv.search.Count("documents", ""); n != 2 {
		t.Fatalf("degraded fetch deleted rows: count=%d", n)
	}
	srv.cache.Clear()
	if hits := documentsQuery(t, srv, "test-key", "/v1/library/documents?q=lease"); len(hits) != 1 {
		t.Fatalf("degraded fetch dropped a hit: %+v", hits)
	}

	// A truncated fetch is incomplete for the same reason: rows stay.
	provider.err = nil
	provider.truncated = true
	provider.documents = provider.documents[:1]
	srv.cache.Clear()
	if docs := documentsQuery(t, srv, "test-key", "/v1/library/documents"); len(docs) != 1 {
		t.Fatalf("truncated list = %+v", docs)
	}
	srv.waitIndex()
	if n, _ := srv.search.Count("documents", ""); n != 2 {
		t.Fatalf("truncated fetch deleted rows: count=%d", n)
	}
	srv.cache.Clear()
	if hits := documentsQuery(t, srv, "test-key", "/v1/library/documents?q=lease"); len(hits) != 1 {
		t.Fatalf("truncated fetch dropped a hit: %+v", hits)
	}
}
