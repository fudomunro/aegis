package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

func searchTestServer(t *testing.T, contacts ...types.Contact) *Server {
	t.Helper()
	cfg := &Config{Services: []Service{{ID: "google", Name: "Google", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"google": &fakeContactProvider{contacts: contacts},
	}
	return srv
}

func searchRequest(t *testing.T, srv *Server, handler http.HandlerFunc, path string) (*httptest.ResponseRecorder, types.ContactList) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(handler)(w, req)
	var resp types.ContactList
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	return w, resp
}

func TestLibrarySearch_QueryHitsIndexedItems(t *testing.T) {
	srv := searchTestServer(t,
		types.Contact{
			Name:    "Ada Lovelace",
			Emails:  []string{"ada@example.com"},
			Sources: []types.ContactSource{{Service: "google", ServiceName: "Google", ID: "g1"}},
		},
		types.Contact{
			Name:    "Grace Hopper",
			Emails:  []string{"grace@example.com"},
			Sources: []types.ContactSource{{Service: "google", ServiceName: "Google", ID: "g2"}},
		},
	)

	// A bare list fetch materializes the index (write-behind).
	w, _ := searchRequest(t, srv, srv.HandleLibraryContacts, "/v1/library/contacts")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	n, err := srv.search.Count("contacts", "")
	if err != nil || n != 2 {
		t.Fatalf("index count = %d err=%v, want 2", n, err)
	}

	w, resp := searchRequest(t, srv, srv.HandleLibraryContacts, "/v1/library/contacts?q=grace")
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 1 || resp.Contacts[0].Name != "Grace Hopper" {
		t.Fatalf("q=grace = %+v", resp.Contacts)
	}

	// Multi-token queries AND together.
	w, resp = searchRequest(t, srv, srv.HandleLibraryContacts, "/v1/library/contacts?q=grace+lovelace")
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 0 {
		t.Fatalf("q=grace+lovelace must match nothing: %+v", resp.Contacts)
	}
}

func TestLibrarySearch_ColdIndexBootstrapsFromFetch(t *testing.T) {
	srv := searchTestServer(t, types.Contact{
		Name:    "Katherine Johnson",
		Emails:  []string{"kj@example.com"},
		Sources: []types.ContactSource{{Service: "google", ServiceName: "Google", ID: "g1"}},
	})

	// First-ever query with an empty index: one live fetch materializes it.
	w, resp := searchRequest(t, srv, srv.HandleLibraryContacts, "/v1/library/contacts?q=katherine")
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
	srv := searchTestServer(t, types.Contact{
		Name:    "Ada Lovelace",
		Sources: []types.ContactSource{{Service: "google", ID: "g1"}},
	})
	srv.search = nil
	w, _ := searchRequest(t, srv, srv.HandleLibraryContacts, "/v1/library/contacts?q=ada")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestLibrarySearch_UnsupportedKindRejectsQ(t *testing.T) {
	srv := searchTestServer(t)
	req := httptest.NewRequest("GET", "/v1/library/contacts?q=x", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	handleLibraryAggregate(srv, w, req, libraryKindSpec[types.Contact]{
		kind:      types.LibraryKind{ID: "notes", Label: "notes"},
		serviceOf: func(types.Contact) string { return "" },
		sortKey:   func(types.Contact) string { return "" },
		merge:     func(in []types.Contact) []types.Contact { return in },
		encode: func(items []types.Contact, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			return types.ContactList{Contacts: items}
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestLibrarySearch_DatasetPartitionIsolated(t *testing.T) {
	srv := searchTestServer(t, types.Contact{
		Name:    "Frodo Baggins",
		Sources: []types.ContactSource{{Service: "google", ID: "g1"}},
	})
	shire := &DataScope{ID: "shire", Dataset: "shire"}
	gondor := &DataScope{ID: "gondor", Dataset: "gondor"}

	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req = withDataScope(req, shire)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("shire list status = %d body=%s", w.Code, w.Body.String())
	}

	req = withDataScope(httptest.NewRequest("GET", "/v1/library/contacts?q=frodo", nil), shire)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("shire search status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Fatalf("shire search = %+v", resp.Contacts)
	}

	// Seed the gondor partition so its query is warm and cannot bootstrap
	// from the scope-unaware fake provider.
	if err := srv.search.Upsert("contacts", "gondor", []types.SearchDocument{{
		ItemID:   "g1",
		Title:    "Gondor Contact",
		Body:     "Gondor Contact",
		Payload:  []byte(`{"name":"Gondor Contact","sources":[{"service":"google","id":"x"}]}`),
		Services: []string{"google"},
	}}); err != nil {
		t.Fatal(err)
	}
	req = withDataScope(httptest.NewRequest("GET", "/v1/library/contacts?q=frodo", nil), gondor)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("gondor search status = %d body=%s", w.Code, w.Body.String())
	}
	resp = types.ContactList{}
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
		{ID: "outlook", Name: "Outlook", Action: "inject",
			GrantLimits: types.Permissions{"contacts": types.AccessNone}},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"google": &fakeContactProvider{contacts: []types.Contact{{
			Name:    "Ada Lovelace",
			Sources: []types.ContactSource{{Service: "google", ID: "g1"}},
		}}},
		"outlook": &fakeContactProvider{contacts: []types.Contact{{
			Name:    "Secret Person",
			Sources: []types.ContactSource{{Service: "outlook", ID: "o1"}},
		}}},
	}

	// Index both rows via a fetch that includes only readable services, then
	// write the denied one behind the scenes to prove query-time filtering.
	w, _ := searchRequest(t, srv, srv.HandleLibraryContacts, "/v1/library/contacts")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	if err := srv.search.Upsert("contacts", "", []types.SearchDocument{{
		ItemID:   "secret",
		Title:    "Secret Person",
		Body:     "Secret Person",
		Payload:  []byte(`{"name":"Secret Person","sources":[{"service":"outlook","id":"o1"}]}`),
		Services: []string{"outlook"},
	}}); err != nil {
		t.Fatal(err)
	}

	w, resp := searchRequest(t, srv, srv.HandleLibraryContacts, "/v1/library/contacts?q=secret")
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 0 {
		t.Fatalf("denied service leaked into search: %+v", resp.Contacts)
	}
	w, resp = searchRequest(t, srv, srv.HandleLibraryContacts, "/v1/library/contacts?q=ada")
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Total != 1 {
		t.Fatalf("readable contact missing: %+v", resp.Contacts)
	}
}

func TestLibrarySearch_MetadataLevelStripsExcerpt(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "paperless",
		Name:        "Paperless-ngx",
		Action:      "inject",
		GrantLimits: types.Permissions{"documents": types.AccessMetadata},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []types.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Medical letter", "2024-01-01T00:00:00Z", "sensitive excerpt"),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents?q=medical", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryDocuments)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.DocumentList
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
	srv := searchTestServer(t, types.Contact{
		Name:    "Ada Lovelace",
		Sources: []types.ContactSource{{Service: "google", ID: "g1"}},
	})
	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/v1/admin/search/status", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var st SearchIndexStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if !st.Enabled {
		t.Fatal("index must report enabled")
	}
	if st.Path == "" {
		t.Fatal("status must include the index path")
	}
	var contacts, tracks *SearchIndexKindStatus
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
	st = SearchIndexStatus{}
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
