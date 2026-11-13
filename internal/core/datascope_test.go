package core

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/admin/scopes"
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/sampledata"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library/links"
	"github.com/fudomunro/aegis/internal/shared"
)

// readAllGrants is the migrated "share everything read-only" grant map
// (docs/external-access.md): packs that relied on "empty grants = full read"
// state it explicitly now that scope resolution is deny-by-default.
func readAllGrants() policy.DataGrants {
	return policy.DataGrants{shared.DefaultKey: {Default: shared.AccessRead}}
}

// scopeServer builds a server with two dataset-partitioned sample library
// services, data-scope API keys, and a seeded mail archive.
func scopeServer(t *testing.T, list []scopes.Scope) *Server {
	t.Helper()
	cfg := &Config{Services: []Service{
		{ID: "sample-library-shire", Name: "Sample Shire", Action: "inject", Endpoint: sampledata.SampleLibraryEndpoint, Dataset: "shire"},
		{ID: "sample-library-gondor", Name: "Sample Gondor", Action: "inject", Endpoint: sampledata.SampleLibraryEndpoint, Dataset: "gondor"},
	}}
	loaded, err := scopes.NewStore(writeDataScopes(t, list))
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	archive, err := mail.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatalf("mail: %v", err)
	}
	t.Cleanup(func() { archive.Close() })
	for _, rec := range GenerateSampleMailFor("shire", 3).Records {
		if err := archive.Upsert(rec); err != nil {
			t.Fatalf("seed shire mail: %v", err)
		}
	}
	for _, rec := range GenerateSampleMailFor("gondor", 3).Records {
		if err := archive.Upsert(rec); err != nil {
			t.Fatalf("seed gondor mail: %v", err)
		}
	}
	srv := &Server{
		apiKey:     "legacy-agent-key",
		authMode:   AuthModeAPIKey,
		config:     cfg,
		cache:      shared.NewCache(),
		mbCache:    shared.NewCache(),
		connectors: make(map[string]connectors.ServiceConnector),
		scopes:     loaded,
		mail:       archive,
		links:      &links.Store{},
		metrics:    metrics.NewAccessMetrics(32),
	}
	srv.connectors["sample-library-shire"] = sampledata.NewSampleLibraryConnector("sample-library-shire", "shire", sampledata.GenerateSampleLibraryFor("shire"))
	srv.connectors["sample-library-gondor"] = sampledata.NewSampleLibraryConnector("sample-library-gondor", "gondor", sampledata.GenerateSampleLibraryFor("gondor"))
	return srv
}

// twoScopeServer is the standard isolation fixture: one granted principal
// per dataset plus a no-grants principal on shire (deny-by-default control).
func twoScopeServer(t *testing.T) *Server {
	t.Helper()
	return scopeServer(t, []scopes.Scope{
		{ID: "shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY", Grants: readAllGrants()},
		{ID: "gondor", Dataset: "gondor", APIKey: "YOUR_GONDOR_KEY", Grants: readAllGrants()},
		{ID: "empty", Dataset: "shire", APIKey: "YOUR_EMPTY_KEY"},
	})
}

func scopeRequest(key, path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Api-Key", key)
	return req
}

func TestAuthMiddleware_DataScopeKey(t *testing.T) {
	srv := twoScopeServer(t)
	var got *scopes.Scope
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		got = scopes.From(r)
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	h(w, scopeRequest("YOUR_SHIRE_KEY", "/v1/library/contacts"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got == nil || got.ID != "shire" {
		t.Fatalf("scope = %#v", got)
	}
	if scopes.From(httptest.NewRequest(http.MethodGet, "/x", nil)) != nil {
		t.Fatal("bare request must have no scope")
	}
}

func TestAuthMiddleware_ReadOnlyScopeRejectsWrites(t *testing.T) {
	path := writeDataScopes(t, []scopes.Scope{
		{ID: "demo", Dataset: "demo", APIKey: "YOUR_DEMO_KEY", ReadOnly: true},
	})
	scopes, err := scopes.NewStore(path)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	srv := &Server{authMode: AuthModeAPIKey, scopes: scopes, config: &Config{}, cache: shared.NewCache(), mbCache: shared.NewCache()}
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/library/photos/share", nil)
	req.Header.Set("X-Api-Key", "YOUR_DEMO_KEY")
	h(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("write status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/pre-request", nil)
	req.Header.Set("X-Api-Key", "YOUR_DEMO_KEY")
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pre-request status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h(w, scopeRequest("YOUR_DEMO_KEY", "/v1/library/contacts"))
	if w.Code != http.StatusOK {
		t.Fatalf("read status = %d", w.Code)
	}
}

func TestLibraryContacts_DatasetIsolation(t *testing.T) {
	srv := twoScopeServer(t)

	shireBody := callLibrary(t, srv, "YOUR_SHIRE_KEY", "/v1/library/contacts")
	gondorBody := callLibrary(t, srv, "YOUR_GONDOR_KEY", "/v1/library/contacts")

	if !strings.Contains(shireBody, "shire") {
		t.Fatalf("shire body missing shire identities: %s", shireBody)
	}
	if strings.Contains(shireBody, "gondor") {
		t.Fatalf("shire body leaked gondor: %s", shireBody)
	}
	if !strings.Contains(gondorBody, "gondor") {
		t.Fatalf("gondor body missing gondor identities: %s", gondorBody)
	}
	if strings.Contains(gondorBody, "shire") {
		t.Fatalf("gondor body leaked shire: %s", gondorBody)
	}
}

func TestLibraryCache_DoesNotLeakAcrossScopes(t *testing.T) {
	srv := twoScopeServer(t)
	first := callLibrary(t, srv, "YOUR_SHIRE_KEY", "/v1/library/contacts")
	second := callLibrary(t, srv, "YOUR_GONDOR_KEY", "/v1/library/contacts")
	if strings.Contains(second, "shire") {
		t.Fatalf("cache leak into gondor: %s", second)
	}
	if first == second {
		t.Fatal("scopes produced identical cached bodies")
	}
}

func TestMailSearch_DatasetIsolation(t *testing.T) {
	srv := twoScopeServer(t)

	shire := callMailSearch(t, srv, "YOUR_SHIRE_KEY")
	gondor := callMailSearch(t, srv, "YOUR_GONDOR_KEY")

	if strings.Contains(shire, "gondor") {
		t.Fatalf("shire mail leaked gondor: %s", shire)
	}
	if strings.Contains(gondor, "shire") {
		t.Fatalf("gondor mail leaked shire: %s", gondor)
	}
	if !strings.Contains(shire, "shire") || !strings.Contains(gondor, "gondor") {
		t.Fatalf("missing own-partition mail\nshire=%s\ngondor=%s", shire, gondor)
	}
}

func TestMailMessage_CrossDatasetHidden(t *testing.T) {
	srv := twoScopeServer(t)
	// Find a gondor archive message via the collection, then fetch it with
	// the shire key. Archive rows are namespaced "gondor:…"; live sample
	// rows share one provider id space across datasets.
	search := callMailSearch(t, srv, "YOUR_GONDOR_KEY")
	var parsed struct {
		Messages []struct {
			Sources []struct {
				Service string `json:"service"`
				ID      string `json:"id"`
			} `json:"sources"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(search), &parsed); err != nil {
		t.Fatalf("parse search: %v", err)
	}
	var src struct {
		Service string `json:"service"`
		ID      string `json:"id"`
	}
	found := false
	for _, m := range parsed.Messages {
		for _, s := range m.Sources {
			if strings.HasPrefix(s.ID, "gondor:") {
				src.Service, src.ID = s.Service, s.ID
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatalf("no gondor archive message: %s", search)
	}

	w := callMailFetch(t, srv, "YOUR_SHIRE_KEY", src.Service, src.ID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-dataset fetch status = %d body=%s", w.Code, w.Body.String())
	}

	w = callMailFetch(t, srv, "YOUR_GONDOR_KEY", src.Service, src.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("own-dataset fetch status = %d body=%s", w.Code, w.Body.String())
	}
}

func callLibrary(t *testing.T, srv *Server, key, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("contacts"))(w, scopeRequest(key, path))
	if w.Code != http.StatusOK {
		t.Fatalf("library %s status=%d body=%s", key, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func callMailSearch(t *testing.T, srv *Server, key string) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("messages"))(w, scopeRequest(key, "/v1/library/messages"))
	if w.Code != http.StatusOK {
		t.Fatalf("mail search %s status=%d body=%s", key, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func callMailFetch(t *testing.T, srv *Server, key, service, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := scopeRequest(key, "/v1/library/messages/"+service+"/"+id)
	req.SetPathValue("service", service)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/v1/library/messages/{service}/{id}"))(w, req)
	return w
}

func TestAdminMiddleware_RejectsScopeKeys(t *testing.T) {
	scopes, err := scopes.NewStore(writeDataScopes(t, []scopes.Scope{
		{ID: "shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY"},
	}))
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	srv := &Server{
		authMode:    AuthModeTailnet,
		scopes:      scopes,
		adminAPIKey: "YOUR_ADMIN_KEY",
		config:      &Config{},
		cache:       shared.NewCache(),
	}
	called := false
	h := srv.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := scopeRequest("YOUR_SHIRE_KEY", "/v1/admin/summary")
	h(w, req)
	if called || w.Code != http.StatusForbidden {
		t.Fatalf("scope key on admin: called=%v status=%d", called, w.Code)
	}
}

func TestServiceProxy_HidesForeignDataset(t *testing.T) {
	srv := twoScopeServer(t)
	// Foreign service path must 404, not leak via 403/existence.
	w := httptest.NewRecorder()
	req := scopeRequest("YOUR_SHIRE_KEY", "/v1/services/sample-library-gondor/messages/x")
	srv.AuthMiddleware(srv.library().HandleServiceProxy)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign proxy status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestLibraryDocumentText_HidesForeignDataset(t *testing.T) {
	srv := twoScopeServer(t)
	// Own-dataset documents are reachable; foreign ids 404.
	shireDocs := sampledata.GenerateSampleLibraryFor("shire").Documents
	if len(shireDocs) == 0 || len(shireDocs[0].Sources) == 0 {
		t.Fatal("need sample documents")
	}
	ownID := shireDocs[0].Sources[0].ID
	w := httptest.NewRecorder()
	req := scopeRequest("YOUR_SHIRE_KEY", "/v1/library/documents/sample-library-shire/"+ownID+"/text")
	srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("service", "sample-library-shire")
		r.SetPathValue("id", ownID)
		srv.library().RouteHandler("/v1/library/documents/{service}/{id}/text")(w, r)
	})(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("own document text status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = scopeRequest("YOUR_SHIRE_KEY", "/v1/library/documents/sample-library-gondor/"+ownID+"/text")
	srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("service", "sample-library-gondor")
		r.SetPathValue("id", ownID)
		srv.library().RouteHandler("/v1/library/documents/{service}/{id}/text")(w, r)
	})(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign document text status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestPreRequest_FiltersForeignDatasetMatches(t *testing.T) {
	srv := twoScopeServer(t)
	srv.config.Services[0].Name = "Sample Shire Library"
	srv.config.Services[0].Aliases = []string{"shire library"}
	srv.config.Services[1].Name = "Sample Gondor Library"
	srv.config.Services[1].Aliases = []string{"gondor library"}
	srv.config.Services[1].Action = "block"

	payload := `{"user_message":"tell me about the gondor library","request_body":{}}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/pre-request", strings.NewReader(payload))
	req.Header.Set("X-Api-Key", "YOUR_SHIRE_KEY")
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pre-request status = %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "block") && strings.Contains(strings.ToLower(body), "gondor") {
		t.Fatalf("foreign blocked service leaked into pre-request: %s", body)
	}
}

func TestNewDataScopeStore_DuplicateAPIKey(t *testing.T) {
	path := writeDataScopes(t, []scopes.Scope{
		{ID: "a", Dataset: "a", APIKey: "YOUR_SHARED_KEY"},
		{ID: "b", Dataset: "b", APIKey: "YOUR_SHARED_KEY"},
	})
	if _, err := scopes.NewStore(path); err == nil {
		t.Fatal("duplicate api_key must fail load")
	}
}

func TestMailArchive_MigratesLegacySchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mail.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	legacy := `
CREATE TABLE mail_messages (
  id INTEGER PRIMARY KEY,
  service TEXT NOT NULL,
  service_msg_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  message_id TEXT NOT NULL DEFAULT '',
  mailbox TEXT NOT NULL DEFAULT '',
  labels_json TEXT NOT NULL DEFAULT '[]',
  subject TEXT NOT NULL DEFAULT '',
  snippet TEXT NOT NULL DEFAULT '',
  from_name TEXT NOT NULL DEFAULT '',
  from_email TEXT NOT NULL DEFAULT '',
  to_json TEXT NOT NULL DEFAULT '[]',
  cc_json TEXT NOT NULL DEFAULT '[]',
  sent_at TEXT NOT NULL DEFAULT '',
  received_at TEXT NOT NULL DEFAULT '',
  direction TEXT NOT NULL DEFAULT '',
  is_read INTEGER NOT NULL DEFAULT 1,
  body_text TEXT NOT NULL DEFAULT '',
  payload_gzip BLOB,
  updated_at TEXT NOT NULL DEFAULT '',
  UNIQUE(service, service_msg_id)
);
CREATE VIRTUAL TABLE mail_fts USING fts5(subject, from_email, participants, body_text);
CREATE TABLE mail_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	db.Close()

	archive, err := mail.Open(path)
	if err != nil {
		t.Fatalf("migrate open: %v", err)
	}
	defer archive.Close()
	if err := archive.Upsert(mail.Record{
		Service: "google-gmail", ID: "m1", Subject: "hello", Dataset: "shire",
	}); err != nil {
		t.Fatalf("upsert after migrate: %v", err)
	}
	resp, err := archive.Search(mail.SearchQuery{Limit: 10, WithBody: true, Dataset: "shire"})
	if err != nil || len(resp.Messages) != 1 {
		t.Fatalf("dataset rows after migrate: %d err=%v", len(resp.Messages), err)
	}
}

// TestScopeGrants_NoGrantsPrincipalSeesNothing extends the isolation contract
// with a principal holding no grants: deny-by-default resolves every type to
// none, so library, mail, and pre-request injection all come up empty.
func TestScopeGrants_NoGrantsPrincipalSeesNothing(t *testing.T) {
	srv := twoScopeServer(t)

	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("contacts"))(w, scopeRequest("YOUR_EMPTY_KEY", "/v1/library/contacts"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("library status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("messages"))(w, scopeRequest("YOUR_EMPTY_KEY", "/v1/library/messages"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("mail search status = %d body=%s", w.Code, w.Body.String())
	}

	payload := `{"user_message":"search my email archive for invoices and show my contacts","request_body":{}}`
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/pre-request", strings.NewReader(payload))
	req.Header.Set("X-Api-Key", "YOUR_EMPTY_KEY")
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pre-request status = %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "/v1/library") || strings.Contains(w.Body.String(), "/v1/mail") {
		t.Fatalf("no-grants principal was given data hints: %s", w.Body.String())
	}
}

// TestScopeGrants_TypeAllowList covers the deny-by-default rows of the
// resolution table at the HTTP seam: listed types answer, unlisted types 403.
func TestScopeGrants_TypeAllowList(t *testing.T) {
	srv := scopeServer(t, []scopes.Scope{
		{ID: "contacts-only", Dataset: "shire", APIKey: "YOUR_CONTACTS_KEY",
			Grants: policy.DataGrants{"contacts": {Default: shared.AccessRead}}},
	})

	body := callLibrary(t, srv, "YOUR_CONTACTS_KEY", "/v1/library/contacts")
	if !strings.Contains(body, "shire") {
		t.Fatalf("granted type missing: %s", body)
	}

	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("tracks"))(w, scopeRequest("YOUR_CONTACTS_KEY", "/v1/library/tracks"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unlisted type status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("messages"))(w, scopeRequest("YOUR_CONTACTS_KEY", "/v1/library/messages"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unlisted mail status = %d body=%s", w.Code, w.Body.String())
	}
}

// TestScopeGrants_ProviderOverrideTightens proves provider entries only narrow
// a stated type level: the overridden connector disappears from the aggregate
// while the other still answers.
// TestScopeGrants_RouteKeys: a grant map key is a route (issue #348). A
// freebusy-only scope reaches /v1/library/freebusy but never
// /v1/library/events, and the full-read scope's cached events page is not
// served to it.
func TestScopeGrants_RouteKeys(t *testing.T) {
	srv := scopeServer(t, []scopes.Scope{
		{ID: "shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY", Grants: readAllGrants()},
		{ID: "shire-busy", Dataset: "shire", APIKey: "YOUR_BUSY_KEY",
			Grants: policy.DataGrants{"freebusy": {Default: shared.AccessRead}}},
	})

	// Full-read scope first so its events page is cached.
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("events"))(w, scopeRequest("YOUR_SHIRE_KEY", "/v1/library/events"))
	if w.Code != http.StatusOK {
		t.Fatalf("events shire status = %d body=%s", w.Code, w.Body.String())
	}
	var list kinds.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("parse events: %v", err)
	}
	if len(list.Events) == 0 || list.Events[0].Title == "" {
		t.Fatalf("full-read events must keep titles: %+v", list.Events)
	}

	// The freebusy-only scope is denied the events route entirely.
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("events"))(w, scopeRequest("YOUR_BUSY_KEY", "/v1/library/events"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("events busy status = %d body=%s, want 403", w.Code, w.Body.String())
	}

	// …and gets availability from its own route.
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/v1/library/freebusy"))(w, scopeRequest("YOUR_BUSY_KEY", "/v1/library/freebusy"))
	if w.Code != http.StatusOK {
		t.Fatalf("freebusy busy status = %d body=%s", w.Code, w.Body.String())
	}
	var busy kinds.FreeBusyList
	if err := json.Unmarshal(w.Body.Bytes(), &busy); err != nil {
		t.Fatalf("parse freebusy: %v", err)
	}
	if len(busy.Busy) == 0 {
		t.Fatalf("expected busy intervals: %s", w.Body.String())
	}
}

// --- invite lifecycle (issue #253 phase 3) ---

func TestDataScopeStore_MigratesPlaintextToHashAtRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data_scopes.json")
	legacy := `{"version":1,"data_scopes":[{"id":"shire","dataset":"shire","api_key":"YOUR_SHIRE_KEY"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := scopes.NewStore(path)
	if err != nil {
		t.Fatalf("load legacy: %v", err)
	}
	sc := s.FindByAPIKey("YOUR_SHIRE_KEY")
	if sc == nil || sc.ID != "shire" {
		t.Fatalf("legacy key must resolve after migration: %#v", sc)
	}
	if sc.APIKeyHint != "YOUR_SHI" {
		t.Fatalf("hint = %q, want first 8 of the key", sc.APIKeyHint)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "YOUR_SHIRE_KEY") {
		t.Fatalf("migrated file still holds plaintext: %s", raw)
	}
	if !strings.Contains(string(raw), "api_key_hash") {
		t.Fatalf("migrated file missing hash: %s", raw)
	}

	// The migrated file (hash-only) must load and resolve without the plaintext.
	s2, err := scopes.NewStore(path)
	if err != nil {
		t.Fatalf("reload migrated: %v", err)
	}
	if s2.FindByAPIKey("YOUR_SHIRE_KEY") == nil {
		t.Fatal("key must resolve from hash-only file")
	}
}

func TestDataScopeStore_MintStoresHashOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data_scopes.json")
	s, err := scopes.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	view, token, err := s.Create(scopes.CreateRequest{
		ID:     "family",
		Name:   "Family",
		Grants: readAllGrants(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(token, "aeg_invite_") {
		t.Fatalf("token = %q, want invite prefix", token)
	}
	if view.APIKeyHint == "" || strings.Contains(view.APIKeyHint, token[len("aeg_invite_"):]) {
		t.Fatalf("hint must be a short non-secret prefix: %q", view.APIKeyHint)
	}
	if s.FindByAPIKey(token) == nil {
		t.Fatal("minted token must authenticate")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("store holds plaintext token: %s", raw)
	}
	if !strings.Contains(string(raw), "api_key_hash") || !strings.Contains(string(raw), view.APIKeyHint) {
		t.Fatalf("store missing hash or hint: %s", raw)
	}
}

func TestDataScopeStore_RotateInvalidatesOldToken(t *testing.T) {
	s, err := scopes.NewStore(filepath.Join(t.TempDir(), "data_scopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, oldToken, err := s.Create(scopes.CreateRequest{ID: "shire", Grants: readAllGrants()})
	if err != nil {
		t.Fatal(err)
	}
	view, newToken, err := s.Rotate("shire")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newToken == oldToken {
		t.Fatal("rotate must issue a new token")
	}
	if s.FindByAPIKey(oldToken) != nil {
		t.Fatal("old token must stop authenticating")
	}
	if s.FindByAPIKey(newToken) == nil {
		t.Fatal("new token must authenticate")
	}
	if view.APIKeyHint == "" {
		t.Fatal("rotate must refresh the hint")
	}
}

func TestDataScopeStore_UpdateDisableAndGrants(t *testing.T) {
	s, err := scopes.NewStore(filepath.Join(t.TempDir(), "data_scopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Create(scopes.CreateRequest{ID: "shire", Grants: readAllGrants()})
	if err != nil {
		t.Fatal(err)
	}

	view, err := s.Update("shire", scopes.UpdateRequest{Disabled: boolPtr(true)})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !view.Disabled {
		t.Fatal("view must show disabled")
	}
	if s.FindByAPIKey(token) != nil {
		t.Fatal("disabled scope must not authenticate")
	}

	grants := policy.DataGrants{"contacts": {Default: shared.AccessRead}}
	view, err = s.Update("shire", scopes.UpdateRequest{Disabled: boolPtr(false), Grants: &grants})
	if err != nil {
		t.Fatalf("update grants: %v", err)
	}
	if s.FindByAPIKey(token) == nil {
		t.Fatal("re-enabled scope must authenticate")
	}
	if got := view.Grants["contacts"].Default; got != shared.AccessRead {
		t.Fatalf("grants = %v, want contacts read", view.Grants)
	}
}

func TestDataScopeStore_CreateRejectsDuplicateAndBadID(t *testing.T) {
	s, err := scopes.NewStore(filepath.Join(t.TempDir(), "data_scopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(scopes.CreateRequest{ID: "bad id!"}); err == nil {
		t.Fatal("invalid id must be rejected")
	}
	if _, _, err := s.Create(scopes.CreateRequest{ID: "dup"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(scopes.CreateRequest{ID: "dup"}); err == nil {
		t.Fatal("duplicate id must be rejected")
	}
	if _, _, err := s.Rotate("missing"); err == nil {
		t.Fatal("rotating a missing scope must fail")
	}
}

func TestAdminDataScopes_CRUD(t *testing.T) {
	store, err := scopes.NewStore(filepath.Join(t.TempDir(), "data_scopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(&Config{Services: []Service{}}, "https://aegis.example.com")
	srv.scopes = store

	adminReq := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var reader *strings.Reader
		if body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("X-Api-Key", "test-key")
		w := httptest.NewRecorder()
		srv.AuthMiddleware(srv.admin().HandleAdminDataScopes)(w, req)
		return w
	}

	// Mint
	w := adminReq("POST", "/v1/admin/data-scopes",
		`{"id":"family","name":"Family","grants":{"default":{"default":"read"}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Scope       scopes.View `json:"scope"`
		InviteToken string      `json:"invite_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.InviteToken == "" || created.Scope.ID != "family" {
		t.Fatalf("create resp: %+v", created)
	}

	// List must never include the plaintext token.
	w = adminReq("GET", "/v1/admin/data-scopes", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "family") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), created.InviteToken) {
		t.Fatal("list must not include plaintext invite token")
	}

	// Disable via patch.
	w = adminReq("PATCH", "/v1/admin/data-scopes/family", `{"disabled":true}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"disabled":true`) {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}

	// Rotate.
	w = adminReq("POST", "/v1/admin/data-scopes/family/rotate", "")
	if w.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	var rotated struct {
		InviteToken string `json:"invite_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rotated); err != nil || rotated.InviteToken == "" {
		t.Fatalf("rotate resp: %s", w.Body.String())
	}
	if rotated.InviteToken == created.InviteToken {
		t.Fatal("rotate must issue a new token")
	}
}

func boolPtr(b bool) *bool { return &b }
