package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

type fakeDocumentProvider struct {
	documents []kinds.Document
	text      string
	textErr   error
	err       error
	truncated bool
}

func (f *fakeDocumentProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakeDocumentProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeDocumentProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeDocumentProvider) ListUserDocuments(context.Context) ([]kinds.Document, bool, error) {
	return f.documents, f.truncated, f.err
}

func (f *fakeDocumentProvider) FetchDocumentText(context.Context, string) (string, bool, error) {
	return f.text, false, f.textErr
}

func sampleDoc(service, name, id, title, created, excerpt string) kinds.Document {
	return kinds.Document{
		Title:     title,
		CreatedAt: created,
		Excerpt:   excerpt,
		Sources: []kinds.DocumentSource{{
			Service:     service,
			ServiceName: name,
			ID:          id,
			Canonical:   true,
		}},
	}
}

func TestHandleLibraryDocuments_AggregatesAndFilters(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "paperless", Name: "Paperless-ngx", Action: "inject"},
		{ID: "drive", Name: "Google Drive", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []kinds.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Apartment lease 2024", "2024-01-15T12:00:00Z", "lease excerpt"),
			sampleDoc("paperless", "Paperless-ngx", "2", "Tax letter 2023", "2024-04-12T00:00:00Z", "tax excerpt"),
		}},
		"drive": &fakeDocumentProvider{documents: []kinds.Document{
			sampleDoc("drive", "Google Drive", "9", "Laptop warranty", "2023-06-01T00:00:00Z", "warranty excerpt"),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents?q=lease", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("documents"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.DocumentList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1 (q=lease): %+v", resp.Total, resp.Documents)
	}
	if !strings.Contains(strings.ToLower(resp.Documents[0].Title), "lease") {
		t.Fatalf("unexpected filtered doc: %+v", resp.Documents[0])
	}
	if resp.Documents[0].Excerpt == "" {
		t.Fatal("read-level response should keep excerpt")
	}
}

func TestHandleLibraryDocuments_MetadataStripsExcerpt(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "paperless",
		Name:        "Paperless-ngx",
		Action:      "inject",
		GrantLimits: policy.Permissions{"documents": policy.AccessMetadata},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []kinds.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Medical letter", "2024-01-01T00:00:00Z", "sensitive excerpt"),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("documents"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.DocumentList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if resp.Documents[0].Excerpt != "" {
		t.Fatalf("metadata access leaked excerpt: %q", resp.Documents[0].Excerpt)
	}
	if resp.Documents[0].Title != "Medical letter" {
		t.Fatalf("title should survive metadata access: %+v", resp.Documents[0])
	}
}

func TestHandleLibraryDocuments_DeniedByPermission(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "paperless",
		Name:        "Paperless-ngx",
		Action:      "inject",
		GrantLimits: policy.Permissions{"documents": policy.AccessNone},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []kinds.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Lease", "2024-01-01T00:00:00Z", ""),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("documents"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryDocuments_UnauthorizedAndError(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "paperless", Name: "Paperless-ngx", Action: "inject"},
		{ID: "drive", Name: "Google Drive", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []kinds.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Lease", "2024-01-01T00:00:00Z", ""),
		}},
		"drive": &fakeDocumentProvider{err: kinds.ErrUnauthorized},
	}
	req := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("documents"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.DocumentList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ByService["drive"].Status != kinds.LibraryStatusUnauthorized {
		t.Fatalf("drive status = %+v", resp.ByService["drive"])
	}

	srv.connectors["drive"] = &fakeDocumentProvider{err: errors.New("boom")}
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	srv.AuthMiddleware(srv.libraryHandler("documents"))(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w2.Code, w2.Body.String())
	}
	var resp2 kinds.DocumentList
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp2.ByService["drive"].Status != kinds.LibraryStatusError {
		t.Fatalf("drive status = %+v", resp2.ByService["drive"])
	}
}

func TestHandleLibraryDocuments_MethodNotAllowed(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "paperless", Name: "Paperless-ngx", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"paperless": &fakeDocumentProvider{}}
	req := httptest.NewRequest("POST", "/v1/library/documents", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("documents"))(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestHandleLibraryDocumentText_RequiresReadNotMetadata(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "paperless",
		Name:        "Paperless-ngx",
		Action:      "inject",
		GrantLimits: policy.Permissions{"documents": policy.AccessMetadata},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{text: "full lease text"},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents/paperless/1/text", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.SetPathValue("service", "paperless")
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/documents/{service}/{id}/text"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("metadata access must not read text: status = %d body=%s", w.Code, w.Body.String())
	}

	cfg.Services[0].GrantLimits = policy.Permissions{"documents": policy.AccessRead}
	srv2 := testServerWithBase(cfg, "https://aegis.example.com")
	srv2.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{text: "full lease text"},
	}
	req2 := httptest.NewRequest("GET", "/v1/library/documents/paperless/1/text", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	req2.SetPathValue("service", "paperless")
	req2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	srv2.AuthMiddleware(srv2.routeHandler("/v1/library/documents/{service}/{id}/text"))(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("read access should fetch text: status = %d body=%s", w2.Code, w2.Body.String())
	}
	var got kinds.DocumentText
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Text != "full lease text" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestHandleLibraryDocumentText_UnknownServiceAndErrors(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "paperless", Name: "Paperless-ngx", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{textErr: kinds.ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents/nope/1/text", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.SetPathValue("service", "nope")
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/documents/{service}/{id}/text"))(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown service status = %d", w.Code)
	}

	req2 := httptest.NewRequest("GET", "/v1/library/documents/paperless/1/text", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	req2.SetPathValue("service", "paperless")
	req2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/documents/{service}/{id}/text"))(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d body=%s", w2.Code, w2.Body.String())
	}

	srv.connectors["paperless"] = &fakeDocumentProvider{textErr: errors.New("boom")}
	req3 := httptest.NewRequest("GET", "/v1/library/documents/paperless/1/text", nil)
	req3.Header.Set("Authorization", "Bearer test-key")
	req3.SetPathValue("service", "paperless")
	req3.SetPathValue("id", "1")
	w3 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/documents/{service}/{id}/text"))(w3, req3)
	if w3.Code != http.StatusBadGateway {
		t.Fatalf("upstream error status = %d", w3.Code)
	}
}

func TestValidatePermissions_MetadataDocumentsOnly(t *testing.T) {
	if err := policy.ValidatePermissions(policy.Permissions{"documents": policy.AccessMetadata}); err != nil {
		t.Fatalf("documents metadata should validate: %v", err)
	}
	if err := policy.ValidatePermissions(policy.Permissions{"tracks": policy.AccessMetadata}); err == nil {
		t.Fatal("metadata must be rejected on tracks")
	}
	if err := policy.ValidatePermissions(policy.Permissions{"default": policy.AccessMetadata}); err == nil {
		t.Fatal("metadata must be rejected on default")
	}
	if !policy.IsMetadataAccess(policy.AccessMetadata) || policy.IsMetadataAccess(policy.AccessRead) {
		t.Fatal("IsMetadataAccess")
	}
	if policy.DocumentsLevelForResponse(policy.AccessFull) != policy.AccessRead {
		t.Fatal("full should respond at read (excerpt ok)")
	}
}

func TestLibraryKindDocuments_Registered(t *testing.T) {
	k := LibraryKindByID("documents")
	if k == nil {
		t.Fatal("documents kind missing")
	}
	if k.PermType != "documents" || k.Path != "/v1/library/documents" {
		t.Fatalf("kind = %+v", k)
	}
	if !k.Mentions("where is my lease?") {
		t.Fatal("keywords should catch lease questions")
	}
	if !k.Mentions("find my warranty") || !k.Mentions("my receipts") {
		t.Fatal("keywords should catch paperwork questions")
	}
	if _, ok := testServerWithBase(&Config{}, "https://aegis.example.com").registryKind("documents"); !ok {
		t.Fatal("documents kind missing from kinds.Builtin()")
	}
}

func TestSampleDocuments_MergePairs(t *testing.T) {
	lib := GenerateSampleLibrary()
	if len(lib.Documents) == 0 {
		t.Fatal("sample library must include documents")
	}
	merged := kinds.MergeDocuments(lib.Documents)
	byTitle := map[string]int{}
	for _, d := range merged {
		byTitle[d.Title]++
	}
	// Shared checksum lease pair collapses to one entry.
	leaseCount := 0
	for _, d := range merged {
		if strings.Contains(strings.ToLower(d.Title), "lease") && d.CreatedAt == "2024-01-15T12:00:00Z" {
			leaseCount++
			if len(d.Sources) != 2 {
				t.Fatalf("lease should merge to 2 sources: %+v", d.Sources)
			}
			if d.Match == "" {
				t.Fatal("merged lease should record match")
			}
		}
	}
	if leaseCount != 1 {
		t.Fatalf("lease merge count = %d, want 1", leaseCount)
	}
	// Near-miss warranties stay distinct.
	warrantyCount := 0
	for _, d := range merged {
		if d.Title == "Laptop warranty" {
			warrantyCount++
		}
	}
	if warrantyCount != 2 {
		t.Fatalf("warranty near-miss count = %d, want 2", warrantyCount)
	}
}
