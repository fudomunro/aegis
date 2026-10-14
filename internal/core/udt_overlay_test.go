package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestUserTypesOverlay_ApplyFileSourcePaths(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	_ = os.WriteFile(a, []byte(`{}`), 0o600)
	_ = os.WriteFile(b, []byte(`{}`), 0o600)

	schema := types.UserDefinedTypeSchema{
		ID:     "reading-list",
		Fields: map[string]types.UserDefinedField{"title": {}},
		Sources: []types.UserDefinedSourceSpec{{
			ID:       "sample-export",
			Kind:     "file_json",
			Path:     "sample/books.json",
			FieldMap: map[string]string{"title": "title"},
		}},
	}
	ov := UDTKindOverlay{
		FileSources: map[string]UDTFileSourceOverlay{
			"sample-export": {Paths: []string{a, b}},
		},
	}
	got := applyUserTypesOverlay(schema, ov)
	if len(got.Sources) != 2 {
		t.Fatalf("sources = %+v", got.Sources)
	}
	if got.Sources[0].Path != a || got.Sources[1].Path != b {
		t.Errorf("paths = %q %q", got.Sources[0].Path, got.Sources[1].Path)
	}
	if got.Sources[0].FieldMap["title"] != "title" {
		t.Error("field_map should be preserved from schema source")
	}
}

func TestUserTypesOverlay_ExtraFile(t *testing.T) {
	schema := types.UserDefinedTypeSchema{
		ID: "notes",
		Fields: map[string]types.UserDefinedField{
			"title":  {},
			"author": {},
		},
		Sources: []types.UserDefinedSourceSpec{{
			ID:       "s",
			Kind:     "file_json",
			Path:     "x.json",
			FieldMap: map[string]string{"title": "t"},
		}},
	}
	ov := UDTKindOverlay{
		ExtraFiles: []UDTExtraFile{{
			ID:   "my-export",
			Path: "/tmp/export.json",
		}},
	}
	got := applyUserTypesOverlay(schema, ov)
	if len(got.Sources) != 2 {
		t.Fatalf("sources = %+v", got.Sources)
	}
	extra := got.Sources[1]
	if extra.ID != "my-export" || extra.Kind != "file_json" {
		t.Fatalf("extra = %+v", extra)
	}
	if extra.FieldMap["title"] != "title" || extra.FieldMap["author"] != "author" {
		t.Errorf("name-match field map = %+v", extra.FieldMap)
	}
}

func TestLoadSaveUserTypesOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user_types.json")
	ov, err := loadUserTypesOverlay(path)
	if err != nil || len(ov.Kinds) != 0 {
		t.Fatalf("empty overlay: %v %+v", err, ov)
	}
	ov.Kinds["reading-list"] = UDTKindOverlay{
		FileSources: map[string]UDTFileSourceOverlay{
			"sample-export": {Paths: []string{"/tmp/books.json"}},
		},
	}
	if err := saveUserTypesOverlay(path, ov); err != nil {
		t.Fatal(err)
	}
	back, err := loadUserTypesOverlay(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Kinds["reading-list"].FileSources["sample-export"].Paths[0] != "/tmp/books.json" {
		t.Fatalf("roundtrip = %+v", back)
	}
}

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
		"default_access": "read",
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
	reg, loaded := LoadUserDefinedTypes(pack, overlayPath, nil)
	if len(loaded) != 1 {
		t.Fatalf("loaded=%d", len(loaded))
	}
	types.UnmarkOptInDataType("reading-list")
	t.Cleanup(func() { types.UnmarkOptInDataType("reading-list") })

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
	types.MarkOptInDataType("reading-list")
	handler := srv.HandleLibraryUserDefined(schema)
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
	reg, _ := LoadUserDefinedTypes(pack, overlayPath, nil)
	types.UnmarkOptInDataType("notes")
	t.Cleanup(func() { types.UnmarkOptInDataType("notes") })
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
