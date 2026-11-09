package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/udt"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestHandleAdminUDT_StatusBrowseAndSelect(t *testing.T) {
	pack := t.TempDir()
	kindDir := filepath.Join(pack, "reading-list")
	if err := os.MkdirAll(kindDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sample := filepath.Join(kindDir, "sample")
	if err := os.MkdirAll(sample, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sample, "books.json"), []byte(`{"items":[]}`), 0o600)
	_ = os.WriteFile(filepath.Join(kindDir, "schema.json"), []byte(`{
		"id": "reading-list",
		"label": "reading list",
		"fields": {"title": {"type": "string"}},
		"identity": {"fields": ["title"]},
		"sources": [{
			"id": "sample-export",
			"kind": "file_json",
			"path": "sample/books.json",
			"items_path": "items",
			"field_map": {"title": "title"}
		}]
	}`), 0o600)

	overlayPath := filepath.Join(t.TempDir(), "user_types.json")
	reg, loaded := udt.LoadUserDefinedTypes(pack, overlayPath, nil)
	if len(loaded) != 1 {
		t.Fatalf("loaded=%d", len(loaded))
	}

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udts = reg
	srv.registerUserDefinedKinds()

	// Status
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/udt/status", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminUDT)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", w.Code, w.Body.String())
	}
	var st udtAdminStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || len(st.Kinds) != 1 || len(st.Kinds[0].FileSources) != 1 {
		t.Fatalf("status = %+v", st)
	}

	// Browse a directory of JSON files
	browseDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(browseDir, "picked.json"), []byte(`{}`), 0o600)
	_ = os.WriteFile(filepath.Join(browseDir, "skip.txt"), []byte(`x`), 0o600)
	req = httptest.NewRequest(http.MethodGet, "/v1/admin/udt/browse?path="+browseDir, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminUDT)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("browse code=%d body=%s", w.Code, w.Body.String())
	}
	var br udtBrowseResult
	if err := json.Unmarshal(w.Body.Bytes(), &br); err != nil {
		t.Fatal(err)
	}
	if len(br.Entries) != 1 || br.Entries[0].Name != "picked.json" {
		t.Fatalf("entries = %+v", br.Entries)
	}

	// Select that file for the source
	picked := filepath.Join(browseDir, "picked.json")
	body, _ := json.Marshal(map[string]any{"paths": []string{picked}})
	req = httptest.NewRequest(http.MethodPut, "/v1/admin/udt/kinds/reading-list/sources/sample-export", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminUDT)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("select code=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Kinds[0].FileSources[0].Selected || st.Kinds[0].FileSources[0].Paths[0] != picked {
		t.Fatalf("selected = %+v", st.Kinds[0].FileSources[0])
	}

	// Overlay is applied on SchemaWithOverlay
	schema := reg.SchemaWithOverlay("reading-list")
	if schema.Sources[0].Path != picked {
		t.Fatalf("schema path = %q, want %q", schema.Sources[0].Path, picked)
	}

	// Library aggregate uses the selected file (empty items list still 200)
	shared.MarkKindDefaultNone("reading-list")
	srv.config.SeedDataGrant("reading-list", policy.DataGrant{Default: policy.AccessRead})
	handler := srv.routeHandler(schema.Path)
	req = httptest.NewRequest(http.MethodGet, "/v1/library/reading-list", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("library code=%d body=%s", w.Code, w.Body.String())
	}

	// Clear selection
	req = httptest.NewRequest(http.MethodDelete, "/v1/admin/udt/kinds/reading-list/sources/sample-export", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminUDT)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear code=%d body=%s", w.Code, w.Body.String())
	}
	schema = reg.SchemaWithOverlay("reading-list")
	if schema.Sources[0].Path != "sample/books.json" {
		t.Fatalf("after clear path = %q", schema.Sources[0].Path)
	}
}

func TestHandleAdminUDT_ExtraFile(t *testing.T) {
	pack := t.TempDir()
	_ = os.WriteFile(filepath.Join(pack, "schema.json"), []byte(`{
		"id": "notes",
		"fields": {"title": {"type": "string"}},
		"identity": {"fields": ["title"]},
		"sources": [{
			"id": "s",
			"kind": "file_json",
			"path": "notes.json",
			"field_map": {"title": "title"}
		}]
	}`), 0o600)
	_ = os.WriteFile(filepath.Join(pack, "notes.json"), []byte(`[{"title":"a"}]`), 0o600)

	overlayPath := filepath.Join(t.TempDir(), "user_types.json")
	reg, _ := udt.LoadUserDefinedTypes(pack, overlayPath, nil)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udts = reg

	extraPath := filepath.Join(t.TempDir(), "extra.json")
	_ = os.WriteFile(extraPath, []byte(`[{"title":"b"}]`), 0o600)
	body, _ := json.Marshal(map[string]any{
		"extra": map[string]any{"id": "my-extra", "path": extraPath},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/udt/kinds/notes/extra", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminUDT)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("extra code=%d body=%s", w.Code, w.Body.String())
	}
	schema := reg.SchemaWithOverlay("notes")
	if len(schema.Sources) != 2 || schema.Sources[1].ID != "my-extra" {
		t.Fatalf("sources = %+v", schema.Sources)
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/admin/udt/kinds/notes/extra/my-extra", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminUDT)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete extra code=%d", w.Code)
	}
	schema = reg.SchemaWithOverlay("notes")
	if len(schema.Sources) != 1 {
		t.Fatalf("after delete sources = %+v", schema.Sources)
	}
}
