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
	"github.com/fudomunro/aegis/internal/types"
)

type fakeDocumentProvider struct {
	documents []types.Document
	text      string
	textErr   error
	err       error
	truncated bool
}

func (f *fakeDocumentProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakeDocumentProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeDocumentProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeDocumentProvider) ListUserDocuments(context.Context) ([]types.Document, bool, error) {
	return f.documents, f.truncated, f.err
}

func (f *fakeDocumentProvider) FetchDocumentText(context.Context, string) (string, bool, error) {
	return f.text, false, f.textErr
}

func sampleDoc(service, name, id, title, created, excerpt string) types.Document {
	return types.Document{
		Title:     title,
		CreatedAt: created,
		Excerpt:   excerpt,
		Sources: []types.DocumentSource{{
			Service:     service,
			ServiceName: name,
			ID:          id,
			Canonical:   true,
		}},
	}
}

func TestDocumentIdentityKey_PrefersDurableIDs(t *testing.T) {
	a := types.Document{EntityID: "SHA256:ABC", Title: "Lease", CreatedAt: "2024-01-01T00:00:00Z"}
	b := types.Document{EntityID: "sha256:abc", Title: "Lease (scan)", CreatedAt: "2024-01-02T00:00:00Z"}
	if documentIdentityKey(a) != documentIdentityKey(b) {
		t.Fatalf("checksum entity IDs should match case-insensitively: %q vs %q",
			documentIdentityKey(a), documentIdentityKey(b))
	}

	c := types.Document{
		Title: "Warranty", CreatedAt: "2023-06-01T00:00:00Z",
		Sources: []types.DocumentSource{{Service: "paperless", ID: "1", Extra: map[string]any{"archive_serial": "42"}}},
	}
	d := types.Document{
		Title: "Warranty (other)", CreatedAt: "2023-06-02T00:00:00Z",
		Sources: []types.DocumentSource{{Service: "paperless-archive", ID: "9", Extra: map[string]any{"archive_serial": "42"}}},
	}
	if documentIdentityKey(c) != documentIdentityKey(d) {
		t.Fatalf("archive serial should match: %q vs %q", documentIdentityKey(c), documentIdentityKey(d))
	}

	// Same title + created merges; different created must not.
	e := sampleDoc("paperless", "Paperless", "1", "Insurance", "2024-03-01T00:00:00Z", "")
	f := sampleDoc("drive", "Drive", "2", "Insurance", "2024-03-01T00:00:00Z", "")
	if documentIdentityKey(e) != documentIdentityKey(f) {
		t.Fatal("title+created should match")
	}
	g := sampleDoc("paperless", "Paperless", "3", "Insurance", "2021-03-01T00:00:00Z", "")
	if documentIdentityKey(e) == documentIdentityKey(g) {
		t.Fatal("different created dates must not share a title key")
	}

	// No identity falls back to service:id.
	h := sampleDoc("paperless", "Paperless", "x1", "", "", "")
	i := sampleDoc("paperless", "Paperless", "x2", "", "", "")
	if documentIdentityKey(h) == documentIdentityKey(i) {
		t.Fatal("documents with no identity must stay distinct via service:id")
	}
}

func TestMergeDocuments_CombinesSourcesAndRecordsMatch(t *testing.T) {
	a := types.Document{
		Title: "Lease", CreatedAt: "2024-01-15T00:00:00Z", Excerpt: "full excerpt",
		Tags: []string{"housing"}, EntityID: "sha256:same",
		Sources: []types.DocumentSource{{Service: "paperless", ServiceName: "Paperless-ngx", ID: "1", Canonical: true}},
	}
	b := types.Document{
		Title: "Lease scan", CreatedAt: "2024-01-15T00:00:00Z", Excerpt: "other",
		Tags: []string{"legal"}, EntityID: "sha256:same",
		Sources: []types.DocumentSource{{Service: "drive", ServiceName: "Google Drive", ID: "2"}},
	}
	merged := MergeDocuments([]types.Document{a, b})
	if len(merged) != 1 {
		t.Fatalf("got %d documents, want 1: %+v", len(merged), merged)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("want 2 sources, got %+v", merged[0].Sources)
	}
	if !merged[0].Sources[0].Canonical {
		t.Fatalf("canonical source should sort first: %+v", merged[0].Sources)
	}
	if merged[0].Match == "" {
		t.Fatal("multi-source merge should record match")
	}
	if merged[0].Excerpt != "full excerpt" {
		t.Fatalf("excerpt = %q, want first non-empty", merged[0].Excerpt)
	}
	if len(merged[0].Tags) != 2 {
		t.Fatalf("tags should union: %+v", merged[0].Tags)
	}
}

func TestMergeDocuments_NearMissDoesNotMerge(t *testing.T) {
	old := sampleDoc("paperless", "Paperless-ngx", "1", "Laptop warranty", "2021-06-01T00:00:00Z", "")
	fresh := sampleDoc("paperless", "Paperless-ngx", "2", "Laptop warranty", "2023-06-01T00:00:00Z", "")
	merged := MergeDocuments([]types.Document{old, fresh})
	if len(merged) != 2 {
		t.Fatalf("same title different dates must not merge: %+v", merged)
	}
}

func TestMergeDocuments_Empty(t *testing.T) {
	if MergeDocuments(nil) != nil {
		t.Fatal("MergeDocuments(nil) should be nil")
	}
}

func TestDocumentFilters_Match(t *testing.T) {
	doc := types.Document{
		Title:         "Apartment lease 2024",
		Correspondent: "Harbour Property",
		DocumentType:  "lease",
		Tags:          []string{"housing", "legal"},
		Excerpt:       "twelve month term",
		CreatedAt:     "2024-01-15T12:00:00Z",
	}
	if !(DocumentFilters{}).matches(doc) {
		t.Fatal("empty filters should match")
	}
	if !(DocumentFilters{Q: "lease"}).matches(doc) {
		t.Fatal("q=lease should match title")
	}
	if (DocumentFilters{Q: "spaceship"}).matches(doc) {
		t.Fatal("q=spaceship should not match")
	}
	if !(DocumentFilters{Tag: "housing"}).matches(doc) {
		t.Fatal("tag=housing should match")
	}
	if (DocumentFilters{Tag: "medical"}).matches(doc) {
		t.Fatal("tag=medical should not match")
	}
	if !(DocumentFilters{After: "2024-01-01", Before: "2024-02-01"}).matches(doc) {
		t.Fatal("date window should match")
	}
	if (DocumentFilters{Before: "2024-01-01"}).matches(doc) {
		t.Fatal("created before window should not match")
	}
}

func TestRedactDocumentMetadata_StripsExcerpt(t *testing.T) {
	d := types.Document{Title: "Lease", Excerpt: "secret text", Tags: []string{"a"}}
	got := RedactDocumentMetadata(d)
	if got.Excerpt != "" {
		t.Fatalf("excerpt = %q, want empty", got.Excerpt)
	}
	if got.Title != "Lease" || len(got.Tags) != 1 {
		t.Fatalf("metadata fields should survive: %+v", got)
	}
	if got := redactDocumentsForLevel([]types.Document{d}, types.AccessMetadata); got[0].Excerpt != "" {
		t.Fatal("metadata level should strip excerpt")
	}
	if got := redactDocumentsForLevel([]types.Document{d}, types.AccessRead); got[0].Excerpt != "secret text" {
		t.Fatal("read level should keep excerpt")
	}
	if got := redactDocumentsForLevel([]types.Document{d}, types.AccessNone); got != nil {
		t.Fatal("none should return no items")
	}
}

func TestHandleLibraryDocuments_AggregatesAndFilters(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "paperless", Name: "Paperless-ngx", Action: "inject"},
		{ID: "drive", Name: "Google Drive", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []types.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Apartment lease 2024", "2024-01-15T12:00:00Z", "lease excerpt"),
			sampleDoc("paperless", "Paperless-ngx", "2", "Tax letter 2023", "2024-04-12T00:00:00Z", "tax excerpt"),
		}},
		"drive": &fakeDocumentProvider{documents: []types.Document{
			sampleDoc("drive", "Google Drive", "9", "Laptop warranty", "2023-06-01T00:00:00Z", "warranty excerpt"),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents?q=lease", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryDocuments)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.DocumentList
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
		GrantLimits: types.Permissions{"documents": types.AccessMetadata},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []types.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Medical letter", "2024-01-01T00:00:00Z", "sensitive excerpt"),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryDocuments)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.DocumentList
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
		GrantLimits: types.Permissions{"documents": types.AccessNone},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{documents: []types.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Lease", "2024-01-01T00:00:00Z", ""),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryDocuments)(w, req)
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
		"paperless": &fakeDocumentProvider{documents: []types.Document{
			sampleDoc("paperless", "Paperless-ngx", "1", "Lease", "2024-01-01T00:00:00Z", ""),
		}},
		"drive": &fakeDocumentProvider{err: types.ErrUnauthorized},
	}
	req := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryDocuments)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.DocumentList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ByService["drive"].Status != types.LibraryStatusUnauthorized {
		t.Fatalf("drive status = %+v", resp.ByService["drive"])
	}

	srv.connectors["drive"] = &fakeDocumentProvider{err: errors.New("boom")}
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/v1/library/documents", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	srv.AuthMiddleware(srv.HandleLibraryDocuments)(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w2.Code, w2.Body.String())
	}
	var resp2 types.DocumentList
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp2.ByService["drive"].Status != types.LibraryStatusError {
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
	srv.AuthMiddleware(srv.HandleLibraryDocuments)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestHandleLibraryDocumentText_RequiresReadNotMetadata(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "paperless",
		Name:        "Paperless-ngx",
		Action:      "inject",
		GrantLimits: types.Permissions{"documents": types.AccessMetadata},
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
	srv.AuthMiddleware(srv.HandleLibraryDocumentText)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("metadata access must not read text: status = %d body=%s", w.Code, w.Body.String())
	}

	cfg.Services[0].GrantLimits = types.Permissions{"documents": types.AccessRead}
	srv2 := testServerWithBase(cfg, "https://aegis.example.com")
	srv2.connectors = map[string]connectors.ServiceConnector{
		"paperless": &fakeDocumentProvider{text: "full lease text"},
	}
	req2 := httptest.NewRequest("GET", "/v1/library/documents/paperless/1/text", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	req2.SetPathValue("service", "paperless")
	req2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	srv2.AuthMiddleware(srv2.HandleLibraryDocumentText)(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("read access should fetch text: status = %d body=%s", w2.Code, w2.Body.String())
	}
	var got types.DocumentText
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
		"paperless": &fakeDocumentProvider{textErr: types.ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/documents/nope/1/text", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.SetPathValue("service", "nope")
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryDocumentText)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown service status = %d", w.Code)
	}

	req2 := httptest.NewRequest("GET", "/v1/library/documents/paperless/1/text", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	req2.SetPathValue("service", "paperless")
	req2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryDocumentText)(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d body=%s", w2.Code, w2.Body.String())
	}

	srv.connectors["paperless"] = &fakeDocumentProvider{textErr: errors.New("boom")}
	req3 := httptest.NewRequest("GET", "/v1/library/documents/paperless/1/text", nil)
	req3.Header.Set("Authorization", "Bearer test-key")
	req3.SetPathValue("service", "paperless")
	req3.SetPathValue("id", "1")
	w3 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryDocumentText)(w3, req3)
	if w3.Code != http.StatusBadGateway {
		t.Fatalf("upstream error status = %d", w3.Code)
	}
}

func TestValidatePermissions_MetadataDocumentsOnly(t *testing.T) {
	if err := types.ValidatePermissions(types.Permissions{"documents": types.AccessMetadata}); err != nil {
		t.Fatalf("documents metadata should validate: %v", err)
	}
	if err := types.ValidatePermissions(types.Permissions{"tracks": types.AccessMetadata}); err == nil {
		t.Fatal("metadata must be rejected on tracks")
	}
	if err := types.ValidatePermissions(types.Permissions{"default": types.AccessMetadata}); err == nil {
		t.Fatal("metadata must be rejected on default")
	}
	if !types.IsMetadataAccess(types.AccessMetadata) || types.IsMetadataAccess(types.AccessRead) {
		t.Fatal("IsMetadataAccess")
	}
	if types.DocumentsLevelForResponse(types.AccessFull) != types.AccessRead {
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
	rt := kindRuntime["documents"]
	if rt.HasProviders == nil || rt.Readable == nil {
		t.Fatal("documents kindRuntime incomplete")
	}
}

func TestDataTypeForPath_Documents(t *testing.T) {
	cases := map[string]string{
		"/me/documents":           "documents",
		"/documents/11":           "documents",
		"/documents/11/download":  "documents",
		"/api/documents/11":       "documents",
		"/correspondents/3":       "documents",
		"/document_types/4":       "documents",
		"/api/tags/7":             "documents",
		"/files":                  "documents",
		"/files/gdoc-1":           "documents",
		"/files/gdoc-1/export":    "documents",
		"/drive/files/gdoc-1":     "documents",
	}
	for p, want := range cases {
		if got := types.DataTypeForPath(p); got != want {
			t.Fatalf("DataTypeForPath(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestSampleDocuments_MergePairs(t *testing.T) {
	lib := GenerateSampleLibrary()
	if len(lib.Documents) == 0 {
		t.Fatal("sample library must include documents")
	}
	merged := MergeDocuments(lib.Documents)
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
