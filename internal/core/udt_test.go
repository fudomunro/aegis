package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/types"
)

func writeSchemaFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUserDefinedTypes_FromExamplePack(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "user-types")
	reg, loaded := LoadUserDefinedTypes(dir, "", nil)
	if len(loaded) == 0 {
		t.Fatal("expected the reading-list example pack to load")
	}
	schema, ok := reg.Get("reading-list")
	if !ok {
		t.Fatalf("reading-list not registered: %v", loaded)
	}
	if schema.Path != "/v1/library/reading-list" {
		t.Errorf("path = %q", schema.Path)
	}

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udts = reg
	srv.config.SeedDataGrant("reading-list", policy.DataGrant{Default: policy.AccessRead})
	req := httptest.NewRequest(http.MethodGet, "/v1/library/reading-list", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/reading-list"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var list types.UserDefinedList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	merged := list.Items
	if len(merged) != 3 {
		t.Fatalf("merged %d items, want 3 (ISBN pair, name pair, singleton): %+v", len(merged), merged)
	}
	// Find the name-merged row.
	var sawEntity, sawName bool
	for _, item := range merged {
		switch item.Match {
		case types.UserDefinedMatchEntityID:
			sawEntity = true
			if len(item.Sources) != 2 {
				t.Errorf("entity row sources = %d, want 2", len(item.Sources))
			}
		case types.UserDefinedMatchName:
			sawName = true
			if len(item.Sources) != 2 {
				t.Errorf("name row sources = %d, want 2", len(item.Sources))
			}
		}
	}
	if !sawEntity || !sawName {
		t.Errorf("match labels: entity=%v name=%v in %+v", sawEntity, sawName, merged)
	}
}

func TestLoadUserDefinedTypes_DefaultAccessCanonicalized(t *testing.T) {
	dir := t.TempDir()
	write := func(name, schema string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(schema), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("synonym.json", `{"id": "syn-1", "label": "syn one", "default_access": "rw", "fields": {"name": {"type": "string"}}}`)
	write("omitted.json", `{"id": "syn-2", "label": "syn two", "fields": {"name": {"type": "string"}}}`)
	reg, loaded := LoadUserDefinedTypes(dir, "", nil)
	if len(loaded) != 2 {
		t.Fatalf("loaded %d schemas, want 2", len(loaded))
	}
	if s, _ := reg.Get("syn-1"); s.DefaultAccess != "read-write" {
		t.Errorf("synonym DefaultAccess = %q, want read-write", s.DefaultAccess)
	}
	if s, _ := reg.Get("syn-2"); s.DefaultAccess != "none" {
		t.Errorf("omitted DefaultAccess = %q, want none", s.DefaultAccess)
	}
}

func TestLoadUserDefinedTypes_SkipsInvalidAndCollisions(t *testing.T) {
	dir := t.TempDir()
	writeSchemaFile(t, dir, "bad.json", "{not json")
	writeSchemaFile(t, dir, "no-fields.json", `{"id":"x","label":"x"}`)
	writeSchemaFile(t, dir, "collides.json", `{
		"id": "tracks",
		"label": "tracks",
		"fields": {"title": {"type": "string"}},
		"identity": {"fields": ["title"]}
	}`)
	writeSchemaFile(t, dir, "ok.json", `{
		"id": "garden-plants",
		"label": "garden plants",
		"fields": {"name": {"type": "string", "required": true}},
		"identity": {"fields": ["name"]},
		"sources": [{"id": "s", "kind": "file_json", "path": "plants.json", "field_map": {"name": "name"}}]
	}`)
	reg, loaded := LoadUserDefinedTypes(dir, "", nil)
	if len(loaded) != 1 {
		t.Fatalf("loaded %d schemas, want 1: %+v", len(loaded), loaded)
	}
	if _, ok := reg.Get("garden-plants"); !ok {
		t.Fatal("garden-plants missing")
	}
	if _, ok := reg.Get("tracks"); ok {
		t.Fatal("built-in id must not be replaced")
	}
}

func TestServer_UDTRouteAndAggregate(t *testing.T) {
	dir := t.TempDir()
	writeSchemaFile(t, dir, "books.json", `{"items":[
		{"row_id":"r1","id":"1","title":"Dune","author":"Herbert","status":"want"},
		{"row_id":"r2","id":"2","title":"Dune","author":"Herbert","status":"reading"}
	]}`)
	writeSchemaFile(t, dir, "schema.json", `{
		"id": "reading-list",
		"label": "reading list",
		"fields": {
			"id": {"type": "string"},
			"title": {"type": "string"},
			"author": {"type": "string"},
			"status": {"type": "string"}
		},
		"identity": {"fields": ["title", "author"], "entity_id_field": "id"},
		"sources": [{
			"id": "sample-export",
			"kind": "file_json",
			"path": "books.json",
			"items_path": "items",
			"id_field": "row_id",
			"field_map": {"id": "id", "title": "title", "author": "author", "status": "status"}
		}]
	}`)

	reg, loaded := LoadUserDefinedTypes(dir, "", nil)
	if len(loaded) != 1 {
		t.Fatalf("loaded=%d", len(loaded))
	}
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udts = reg
	srv.registerUserDefinedKinds()
	// Packs cannot self-enable; an admin data grant must name the kind.
	srv.config.SeedDataGrant("reading-list", policy.DataGrant{Default: policy.AccessRead})

	schema, _ := reg.Get("reading-list")
	handler := srv.routeHandler(schema.Path)
	req := httptest.NewRequest(http.MethodGet, "/v1/library/reading-list", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var list types.UserDefinedList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Same identity (title+author) but different entity ids → 2 rows in this
	// schema (entity_id is preferred when present).
	if list.Total != 2 {
		t.Fatalf("total=%d items=%+v", list.Total, list.Items)
	}
	if list.SchemaID != "reading-list" {
		t.Errorf("schema_id = %q", list.SchemaID)
	}

	handlers := srv.libraryRouteHandlers()
	if _, ok := handlers["/v1/library/reading-list"]; !ok {
		t.Error("UDT route not registered in libraryRouteHandlers")
	}
	kinds := srv.allLibraryKinds()
	found := false
	for _, k := range kinds {
		if k.ID == "reading-list" {
			found = true
		}
	}
	if !found {
		t.Error("allLibraryKinds missing reading-list")
	}
}

func TestServer_UDTDeniedWhenNotEnabled(t *testing.T) {
	dir := t.TempDir()
	writeSchemaFile(t, dir, "schema.json", `{
		"id": "secret-log",
		"fields": {"title": {"type": "string"}},
		"identity": {"fields": ["title"]},
		"sources": [{"id": "s", "kind": "file_json", "path": "x.json", "field_map": {"title": "t"}}]
	}`)
	writeSchemaFile(t, dir, "x.json", `{"t": "hi"}`)

	reg, _ := LoadUserDefinedTypes(dir, "", nil)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udts = reg
	srv.registerUserDefinedKinds()
	schema, _ := reg.Get("secret-log")
	handler := srv.routeHandler(schema.Path)
	req := httptest.NewRequest(http.MethodGet, "/v1/library/secret-log", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want deny for opt-in kind", w.Code, w.Body.String())
	}
}
