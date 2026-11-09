package udk

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

// writeSchemaFile drops a schema fixture on disk. The hub test helpers keep
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

func TestLoadUserDefinedKinds_DefaultAccessCanonicalized(t *testing.T) {
	dir := t.TempDir()
	write := func(name, schema string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(schema), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("synonym.json", `{"id": "syn-1", "label": "syn one", "default_access": "rw", "fields": {"name": {"type": "string"}}}`)
	write("omitted.json", `{"id": "syn-2", "label": "syn two", "fields": {"name": {"type": "string"}}}`)
	reg, loaded := LoadUserDefinedKinds(dir, "", nil)
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

func TestLoadUserDefinedKinds_SkipsInvalidAndCollisions(t *testing.T) {
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
	reg, loaded := LoadUserDefinedKinds(dir, "", nil)
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

func TestUserKindsOverlay_ApplyFileSourcePaths(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	_ = os.WriteFile(a, []byte(`{}`), 0o600)
	_ = os.WriteFile(b, []byte(`{}`), 0o600)

	schema := kinds.UserDefinedKindSchema{
		ID:     "reading-list",
		Fields: map[string]kinds.UserDefinedField{"title": {}},
		Sources: []kinds.UserDefinedSourceSpec{{
			ID:       "sample-export",
			Kind:     "file_json",
			Path:     "sample/books.json",
			FieldMap: map[string]string{"title": "title"},
		}},
	}
	ov := KindOverlay{
		FileSources: map[string]FileSourceOverlay{
			"sample-export": {Paths: []string{a, b}},
		},
	}
	got := applyUserKindsOverlay(schema, ov)
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

func TestUserKindsOverlay_ExtraFile(t *testing.T) {
	schema := kinds.UserDefinedKindSchema{
		ID: "notes",
		Fields: map[string]kinds.UserDefinedField{
			"title":  {},
			"author": {},
		},
		Sources: []kinds.UserDefinedSourceSpec{{
			ID:       "s",
			Kind:     "file_json",
			Path:     "x.json",
			FieldMap: map[string]string{"title": "t"},
		}},
	}
	ov := KindOverlay{
		ExtraFiles: []ExtraFile{{
			ID:   "my-export",
			Path: "/tmp/export.json",
		}},
	}
	got := applyUserKindsOverlay(schema, ov)
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

func TestLoadSaveUserKindsOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user_kinds.json")
	ov, err := LoadOverlay(path)
	if err != nil || len(ov.Kinds) != 0 {
		t.Fatalf("empty overlay: %v %+v", err, ov)
	}
	ov.Kinds["reading-list"] = KindOverlay{
		FileSources: map[string]FileSourceOverlay{
			"sample-export": {Paths: []string{"/tmp/books.json"}},
		},
	}
	if err := SaveOverlay(path, ov); err != nil {
		t.Fatal(err)
	}
	back, err := LoadOverlay(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Kinds["reading-list"].FileSources["sample-export"].Paths[0] != "/tmp/books.json" {
		t.Fatalf("roundtrip = %+v", back)
	}
}
