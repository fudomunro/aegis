package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/udk"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// writeSchemaFile drops a schema fixture on disk. The udk package tests keep
// their own copy.
func writeSchemaFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUserDefinedKinds_FromExamplePack(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "user-kinds")
	reg, loaded := udk.LoadUserDefinedKinds(dir, "", nil)
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
	srv.udks = reg
	srv.config.SeedDataGrant("reading-list", policy.DataGrant{Default: shared.AccessRead})
	req := httptest.NewRequest(http.MethodGet, "/v1/library/reading-list", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/v1/library/reading-list"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var list kinds.UserDefinedList
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
		case kinds.UserDefinedMatchEntityID:
			sawEntity = true
			if len(item.Sources) != 2 {
				t.Errorf("entity row sources = %d, want 2", len(item.Sources))
			}
		case kinds.UserDefinedMatchName:
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

func TestServer_UDKRouteAndAggregate(t *testing.T) {
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

	reg, loaded := udk.LoadUserDefinedKinds(dir, "", nil)
	if len(loaded) != 1 {
		t.Fatalf("loaded=%d", len(loaded))
	}
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udks = reg
	srv.library().RegisterUserDefinedKinds()
	// Packs cannot self-enable; an admin data grant must name the kind.
	srv.config.SeedDataGrant("reading-list", policy.DataGrant{Default: shared.AccessRead})

	schema, _ := reg.Get("reading-list")
	handler := srv.library().RouteHandler(schema.Path)
	req := httptest.NewRequest(http.MethodGet, "/v1/library/reading-list", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var list kinds.UserDefinedList
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

	handlers := srv.library().RouteHandlers()
	if _, ok := handlers["/v1/library/reading-list"]; !ok {
		t.Error("UDK route not registered in libraryRouteHandlers")
	}
	kinds := srv.library().AllLibraryKinds()
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

func TestServer_UDKDeniedWhenNotEnabled(t *testing.T) {
	dir := t.TempDir()
	writeSchemaFile(t, dir, "schema.json", `{
		"id": "secret-log",
		"fields": {"title": {"type": "string"}},
		"identity": {"fields": ["title"]},
		"sources": [{"id": "s", "kind": "file_json", "path": "x.json", "field_map": {"title": "t"}}]
	}`)
	writeSchemaFile(t, dir, "x.json", `{"t": "hi"}`)

	reg, _ := udk.LoadUserDefinedKinds(dir, "", nil)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udks = reg
	srv.library().RegisterUserDefinedKinds()
	schema, _ := reg.Get("secret-log")
	handler := srv.library().RouteHandler(schema.Path)
	req := httptest.NewRequest(http.MethodGet, "/v1/library/secret-log", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want deny for opt-in kind", w.Code, w.Body.String())
	}
}
