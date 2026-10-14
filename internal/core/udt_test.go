package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
	reg, loaded := LoadUserDefinedTypes(dir, nil)
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
	if !types.IsOptInDataType("reading-list") {
		t.Error("reading-list should be marked opt-in")
	}
	t.Cleanup(func() { types.UnmarkOptInDataType("reading-list") })

	items, truncated, err := listUserDefined(context.Background(), schema, reg.DirFor("reading-list"), nil)
	if err != nil {
		t.Fatalf("listUserDefined: %v", err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}
	merged := types.MergeUserDefinedItems(schema, items)
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
	types.UnmarkOptInDataType("garden-plants")
	reg, loaded := LoadUserDefinedTypes(dir, nil)
	if len(loaded) != 1 {
		t.Fatalf("loaded %d schemas, want 1: %+v", len(loaded), loaded)
	}
	if _, ok := reg.Get("garden-plants"); !ok {
		t.Fatal("garden-plants missing")
	}
	if _, ok := reg.Get("tracks"); ok {
		t.Fatal("built-in id must not be replaced")
	}
	if !types.IsOptInDataType("garden-plants") {
		t.Error("garden-plants should be opt-in")
	}
	t.Cleanup(func() { types.UnmarkOptInDataType("garden-plants") })
}

func TestDottedPath(t *testing.T) {
	doc := map[string]any{
		"items": []any{
			map[string]any{"name": "a", "meta": map[string]any{"tag": "x"}},
		},
	}
	if got := dottedPath(doc, "items.0.meta.tag"); got != "x" {
		t.Errorf("got %v", got)
	}
	if got := dottedPath(doc, "items.0.missing"); got != nil {
		t.Errorf("got %v", got)
	}
	if got := dottedPath(doc, "items.9.name"); got != nil {
		t.Errorf("got %v", got)
	}
	if got := dottedPath(doc, ""); got == nil {
		t.Error("empty path should return root")
	}
}

func TestMapUserDefinedDocument_HTTPSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{
					"id": "n1",
					"properties": map[string]any{
						"Name":   "The Dispossessed",
						"Author": "Le Guin",
					},
				},
			},
		})
	}))
	defer srv.Close()

	schema := types.UserDefinedTypeSchema{
		ID:    "work-notes",
		Label: "work notes",
		Fields: map[string]types.UserDefinedField{
			"title":  {Type: "string"},
			"author": {Type: "string"},
		},
		Identity: types.UserDefinedIdentity{Fields: []string{"title", "author"}},
		Sources: []types.UserDefinedSourceSpec{{
			ID:        "wiki",
			Name:      "Wiki",
			Kind:      "http_json",
			Endpoint:  srv.URL,
			ItemsPath: "results",
			IDField:   "id",
			FieldMap: map[string]string{
				"title":  "properties.Name",
				"author": "properties.Author",
			},
			URLTemplate: "https://wiki.example/{id}",
		}},
	}
	if err := schema.Normalize(); err != nil {
		t.Fatal(err)
	}
	items, truncated, err := fetchUserDefinedSource(context.Background(), schema, schema.Sources[0], "", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(items) != 1 {
		t.Fatalf("items=%d truncated=%v", len(items), truncated)
	}
	got := items[0]
	if got.Name != "The Dispossessed" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Fields["author"] != "Le Guin" {
		t.Errorf("Fields = %+v", got.Fields)
	}
	if len(got.Sources) != 1 || got.Sources[0].ID != "n1" || got.Sources[0].URL != "https://wiki.example/n1" {
		t.Errorf("Sources = %+v", got.Sources)
	}
}

func TestListUserDefined_FailOpenPartial(t *testing.T) {
	dir := t.TempDir()
	writeSchemaFile(t, dir, "good.json", `{"items":[{"title":"Only Good","author":"A"}]}`)
	schema := types.UserDefinedTypeSchema{
		ID:     "notes",
		Fields: map[string]types.UserDefinedField{"title": {}, "author": {}},
		Identity: types.UserDefinedIdentity{
			Fields: []string{"title", "author"},
		},
		Sources: []types.UserDefinedSourceSpec{
			{ID: "good", Kind: "file_json", Path: "good.json", ItemsPath: "items", FieldMap: map[string]string{"title": "title", "author": "author"}},
			{ID: "bad", Kind: "file_json", Path: "missing.json", ItemsPath: "items", FieldMap: map[string]string{"title": "title"}},
		},
	}
	if err := schema.Normalize(); err != nil {
		t.Fatal(err)
	}
	items, _, err := listUserDefined(context.Background(), schema, dir, nil)
	if err != nil {
		t.Fatalf("fail-open list should succeed with partial rows, got %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
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
		"default_access": "read",
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
	types.UnmarkOptInDataType("reading-list")
	t.Cleanup(func() { types.UnmarkOptInDataType("reading-list") })

	reg, loaded := LoadUserDefinedTypes(dir, nil)
	if len(loaded) != 1 {
		t.Fatalf("loaded=%d", len(loaded))
	}
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udts = reg
	srv.registerUserDefinedKinds()

	schema, _ := reg.Get("reading-list")
	handler := srv.HandleLibraryUserDefined(schema)
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
	types.UnmarkOptInDataType("secret-log")
	t.Cleanup(func() { types.UnmarkOptInDataType("secret-log") })

	reg, _ := LoadUserDefinedTypes(dir, nil)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udts = reg
	srv.registerUserDefinedKinds()
	schema, _ := reg.Get("secret-log")
	handler := srv.HandleLibraryUserDefined(schema)
	req := httptest.NewRequest(http.MethodGet, "/v1/library/secret-log", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want deny for opt-in kind", w.Code, w.Body.String())
	}
}
