package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
)

func writeDataScopes(t *testing.T, scopes []DataScope) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data_scopes.json")
	raw, err := json.Marshal(dataScopesFile{Version: dataScopesFileVersion, DataScopes: scopes})
	if err != nil {
		t.Fatalf("marshal scopes: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write scopes: %v", err)
	}
	return path
}

func TestNewDataScopeStore_MissingFile(t *testing.T) {
	s, err := NewDataScopeStore(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := s.FindByAPIKey("nope"); got != nil {
		t.Fatalf("expected nil scope, got %#v", got)
	}
}

func TestNewDataScopeStore_LoadAndResolve(t *testing.T) {
	path := writeDataScopes(t, []DataScope{
		{ID: "shire", Name: "Shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY", ReadOnly: true},
		{ID: "gondor", Dataset: "gondor", APIKey: "YOUR_GONDOR_KEY"},
		{ID: "off", Dataset: "x", APIKey: "YOUR_OFF_KEY", Disabled: true},
	})
	s, err := NewDataScopeStore(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sc := s.FindByAPIKey("YOUR_SHIRE_KEY")
	if sc == nil || sc.ID != "shire" || sc.Dataset != "shire" || !sc.ReadOnly {
		t.Fatalf("shire scope = %#v", sc)
	}
	if sc.APIKey != "" {
		t.Fatalf("plaintext key retained in memory: %q", sc.APIKey)
	}
	if s.FindByAPIKey("YOUR_OFF_KEY") != nil {
		t.Fatal("disabled scope must not authenticate")
	}
	if s.FindByAPIKey("wrong") != nil {
		t.Fatal("unknown key must not authenticate")
	}
	if s.Get("gondor") == nil || s.Get("missing") != nil {
		t.Fatal("Get by id failed")
	}
	if len(s.All()) != 3 {
		t.Fatalf("All() = %d scopes", len(s.All()))
	}
}

func TestServiceVisibleToScope(t *testing.T) {
	shared := &Service{ID: "spotify"}
	partitioned := &Service{ID: "sample-library-shire", Dataset: "shire"}
	owner := (*DataScope)(nil)
	shire := &DataScope{ID: "shire", Dataset: "shire"}
	gondor := &DataScope{ID: "gondor", Dataset: "gondor"}

	if !serviceVisibleToScope(shared, owner) || !serviceVisibleToScope(shared, shire) {
		t.Fatal("shared service must be visible to every scope")
	}
	if !serviceVisibleToScope(partitioned, owner) {
		t.Fatal("owner scope sees every service")
	}
	if !serviceVisibleToScope(partitioned, shire) {
		t.Fatal("matching dataset must be visible")
	}
	if serviceVisibleToScope(partitioned, gondor) {
		t.Fatal("other dataset must be invisible")
	}
	if serviceVisibleToScope(nil, shire) {
		t.Fatal("nil service is not visible")
	}
}

func TestScopeCacheSuffix(t *testing.T) {
	if scopeCacheSuffix(nil) != "" || scopeCacheSuffix(&DataScope{ID: "x"}) != "" {
		t.Fatal("default partition must not change cache keys")
	}
	if got := scopeCacheSuffix(&DataScope{Dataset: "shire"}); got != "|ds:shire" {
		t.Fatalf("suffix = %q", got)
	}
}

// twoScopeServer builds a server with two dataset-partitioned sample library
// services and two matching data-scope API keys, plus a seeded mail archive.
func twoScopeServer(t *testing.T) *Server {
	t.Helper()
	cfg := &Config{Services: []Service{
		{ID: "sample-library-shire", Name: "Sample Shire", Action: "inject", Endpoint: SampleLibraryEndpoint, Dataset: "shire"},
		{ID: "sample-library-gondor", Name: "Sample Gondor", Action: "inject", Endpoint: SampleLibraryEndpoint, Dataset: "gondor"},
	}}
	scopes, err := NewDataScopeStore(writeDataScopes(t, []DataScope{
		{ID: "shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY"},
		{ID: "gondor", Dataset: "gondor", APIKey: "YOUR_GONDOR_KEY"},
	}))
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	archive, err := OpenMailArchive(filepath.Join(t.TempDir(), "mail.db"))
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
		cache:      NewCache(),
		connectors: make(map[string]connectors.ServiceConnector),
		scopes:     scopes,
		mail:       archive,
		links:      &EntityLinkStore{},
		metrics:    NewAccessMetrics(32),
	}
	srv.connectors["sample-library-shire"] = NewSampleLibraryConnector("sample-library-shire", "shire", GenerateSampleLibraryFor("shire"))
	srv.connectors["sample-library-gondor"] = NewSampleLibraryConnector("sample-library-gondor", "gondor", GenerateSampleLibraryFor("gondor"))
	return srv
}

func scopeRequest(key, path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Api-Key", key)
	return req
}

func TestAuthMiddleware_DataScopeKey(t *testing.T) {
	srv := twoScopeServer(t)
	var got *DataScope
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		got = DataScopeFrom(r)
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
	if DataScopeFrom(httptest.NewRequest(http.MethodGet, "/x", nil)) != nil {
		t.Fatal("bare request must have no scope")
	}
}

func TestAuthMiddleware_ReadOnlyScopeRejectsWrites(t *testing.T) {
	path := writeDataScopes(t, []DataScope{
		{ID: "demo", Dataset: "demo", APIKey: "YOUR_DEMO_KEY", ReadOnly: true},
	})
	scopes, err := NewDataScopeStore(path)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	srv := &Server{authMode: AuthModeAPIKey, scopes: scopes, config: &Config{}, cache: NewCache()}
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/mail/sync", nil)
	req.Header.Set("X-Api-Key", "YOUR_DEMO_KEY")
	h(w, req)
	if w.Code != http.StatusUnauthorized {
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
	// Find a gondor message id via search, then fetch it with the shire key.
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
	if len(parsed.Messages) == 0 || len(parsed.Messages[0].Sources) == 0 {
		t.Fatalf("no gondor messages: %s", search)
	}
	src := parsed.Messages[0].Sources[0]

	w := httptest.NewRecorder()
	req := scopeRequest("YOUR_SHIRE_KEY", "/v1/mail/messages/"+src.Service+"/"+src.ID)
	srv.AuthMiddleware(srv.handleMailRoutes)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-dataset fetch status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = scopeRequest("YOUR_GONDOR_KEY", "/v1/mail/messages/"+src.Service+"/"+src.ID)
	srv.AuthMiddleware(srv.handleMailRoutes)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("own-dataset fetch status = %d body=%s", w.Code, w.Body.String())
	}
}

func callLibrary(t *testing.T, srv *Server, key, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, scopeRequest(key, path))
	if w.Code != http.StatusOK {
		t.Fatalf("library %s status=%d body=%s", key, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func callMailSearch(t *testing.T, srv *Server, key string) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.handleMailRoutes)(w, scopeRequest(key, "/v1/mail/search"))
	if w.Code != http.StatusOK {
		t.Fatalf("mail search %s status=%d body=%s", key, w.Code, w.Body.String())
	}
	return w.Body.String()
}
