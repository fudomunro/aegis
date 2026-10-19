package types

import (
	"testing"
)

func sampleReadingListSchema(t *testing.T) UserDefinedTypeSchema {
	t.Helper()
	s := UserDefinedTypeSchema{
		Version: "1",
		ID:      "reading-list",
		Label:   "reading list",
		Usage:   "books or articles they want to read",
		ExampleQuestion: "what's on my reading list?",
		Keywords:        []string{"reading list", "want to read"},
		Fields: map[string]UserDefinedField{
			"id":     {Type: "string"},
			"title":  {Type: "string", Required: true},
			"author": {Type: "string"},
			"status": {Type: "string"},
		},
		Identity: UserDefinedIdentity{
			Fields:        []string{"title", "author"},
			EntityIDField: "id",
		},
		Sources: []UserDefinedSourceSpec{{
			ID:        "notion-reading",
			Name:      "Notion",
			Kind:      "file_json",
			Path:      "sample/books.json",
			ItemsPath: "items",
			FieldMap: map[string]string{
				"id":     "id",
				"title":  "properties.Name",
				"author": "properties.Author",
				"status": "properties.Status",
			},
			IDField: "id",
		}},
	}
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return s
}

func TestUserDefinedTypeSchema_NormalizeDefaults(t *testing.T) {
	s := UserDefinedTypeSchema{
		ID:     "garden-plants",
		Label:  "garden plants",
		Fields: map[string]UserDefinedField{"name": {Type: "string"}},
	}
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if s.PermType != "garden-plants" {
		t.Errorf("PermType = %q, want garden-plants", s.PermType)
	}
	if s.Path != "/v1/library/garden-plants" {
		t.Errorf("Path = %q", s.Path)
	}
	if s.DefaultAccess != AccessNone {
		t.Errorf("DefaultAccess = %q, want none (UDTs are opt-in)", s.DefaultAccess)
	}
	if len(s.Identity.Fields) != 1 || s.Identity.Fields[0] != "name" {
		t.Errorf("Identity.Fields = %v, want [name]", s.Identity.Fields)
	}
}

func TestUserDefinedTypeSchema_NormalizeRejects(t *testing.T) {
	cases := []struct {
		name   string
		schema UserDefinedTypeSchema
	}{
		{"no id", UserDefinedTypeSchema{Fields: map[string]UserDefinedField{"a": {}}}},
		{"slash id", UserDefinedTypeSchema{ID: "a/b", Fields: map[string]UserDefinedField{"a": {}}}},
		{"no fields", UserDefinedTypeSchema{ID: "x"}},
		{"bad version", UserDefinedTypeSchema{Version: "9", ID: "x", Fields: map[string]UserDefinedField{"a": {}}}},
		{"unknown identity field", UserDefinedTypeSchema{
			ID:     "x",
			Fields: map[string]UserDefinedField{"title": {}},
			Identity: UserDefinedIdentity{
				Fields: []string{"missing"},
			},
		}},
		{"source missing path", UserDefinedTypeSchema{
			ID:     "x",
			Fields: map[string]UserDefinedField{"title": {}},
			Sources: []UserDefinedSourceSpec{{
				ID:       "s",
				Kind:     "file_json",
				FieldMap: map[string]string{"title": "t"},
			}},
		}},
		{"source unknown field map", UserDefinedTypeSchema{
			ID:     "x",
			Fields: map[string]UserDefinedField{"title": {}},
			Sources: []UserDefinedSourceSpec{{
				ID:       "s",
				Kind:     "file_json",
				Path:     "p.json",
				FieldMap: map[string]string{"nope": "t"},
			}},
		}},
		{"bad source kind", UserDefinedTypeSchema{
			ID:     "x",
			Fields: map[string]UserDefinedField{"title": {}},
			Sources: []UserDefinedSourceSpec{{
				ID:       "s",
				Kind:     "sql",
				FieldMap: map[string]string{"title": "t"},
			}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.schema
			if err := s.Normalize(); err == nil {
				t.Fatal("Normalize succeeded, want error")
			}
		})
	}
}

func TestUserDefinedTypeSchema_LibraryKind(t *testing.T) {
	s := sampleReadingListSchema(t)
	k := s.LibraryKind()
	if k.ID != "reading-list" || k.Path != "/v1/library/reading-list" {
		t.Fatalf("kind = %+v", k)
	}
	if !k.Mentions("what's on my reading list?") {
		t.Error("Mentions failed for example keywords")
	}
	if k.CacheKey() != "library:reading-list" {
		t.Errorf("CacheKey = %q", k.CacheKey())
	}
}

func TestMergeUserDefinedItems_EntityIDPreferred(t *testing.T) {
	s := sampleReadingListSchema(t)
	items := []UserDefinedItem{
		{
			Name:     "The Dispossessed",
			EntityID: "isbn:9780061054884",
			Fields:   map[string]any{"title": "The Dispossessed", "author": "Le Guin"},
			Sources:  []UserDefinedSource{{Service: "notion", ID: "a1"}},
		},
		{
			// Different spelling of author, same durable id → one row.
			Fields:  map[string]any{"id": "isbn:9780061054884", "title": "The Dispossessed", "author": "Ursula K. Le Guin"},
			Sources: []UserDefinedSource{{Service: "goodreads", ID: "g9"}},
		},
	}
	merged := MergeUserDefinedItems(s, items)
	if len(merged) != 1 {
		t.Fatalf("merged %d items, want 1: %+v", len(merged), merged)
	}
	got := merged[0]
	if got.Match != UserDefinedMatchEntityID {
		t.Errorf("Match = %q, want entity_id", got.Match)
	}
	if len(got.Sources) != 2 {
		t.Errorf("Sources = %+v, want 2", got.Sources)
	}
	if got.Fields["author"] != "Le Guin" {
		t.Errorf("author = %v, want first non-empty Le Guin", got.Fields["author"])
	}
}

func TestMergeUserDefinedItems_NameFallback(t *testing.T) {
	s := sampleReadingListSchema(t)
	items := []UserDefinedItem{
		{
			Fields:  map[string]any{"title": "The Left Hand of Darkness", "author": "Le Guin"},
			Sources: []UserDefinedSource{{Service: "notion", ID: "a1"}},
		},
		{
			Fields:  map[string]any{"title": "  the left hand of darkness ", "author": "le guin"},
			Sources: []UserDefinedSource{{Service: "csv-export", ID: "row2"}},
		},
	}
	merged := MergeUserDefinedItems(s, items)
	if len(merged) != 1 {
		t.Fatalf("merged %d items, want 1: %+v", len(merged), merged)
	}
	if merged[0].Match != UserDefinedMatchName {
		t.Errorf("Match = %q, want name", merged[0].Match)
	}
	if len(merged[0].Sources) != 2 {
		t.Errorf("Sources = %+v", merged[0].Sources)
	}
}

func TestMergeUserDefinedItems_SingleSourceNoMatchLabel(t *testing.T) {
	s := sampleReadingListSchema(t)
	merged := MergeUserDefinedItems(s, []UserDefinedItem{{
		Fields:  map[string]any{"title": "Parable of the Sower"},
		Sources: []UserDefinedSource{{Service: "notion", ID: "a1"}},
	}})
	if len(merged) != 1 {
		t.Fatalf("len=%d", len(merged))
	}
	if merged[0].Match != "" {
		t.Errorf("Match = %q, want empty on single-source", merged[0].Match)
	}
	if merged[0].Name != "Parable of the Sower" {
		t.Errorf("Name = %q", merged[0].Name)
	}
}

func TestEncodeUserDefinedList(t *testing.T) {
	s := sampleReadingListSchema(t)
	by := map[string]LibraryServiceStatus{
		"notion-reading": {Status: LibraryStatusOK, Count: 1},
		"broken":         {Status: LibraryStatusError, Truncated: true},
	}
	list := EncodeUserDefinedList(s, []UserDefinedItem{{Name: "x"}}, PageRequest{Limit: MaxPageLimit}, by)
	if list.Total != 1 || !list.Truncated || !list.Synced || list.SchemaID != "reading-list" {
		t.Fatalf("list = %+v", list)
	}
	if list.Fields["title"].Type != "string" {
		t.Errorf("Fields not copied: %+v", list.Fields)
	}
}

func TestUserDefinedTypeSchema_RejectsHijacking(t *testing.T) {
	cases := []struct {
		name   string
		schema UserDefinedTypeSchema
	}{
		{"built-in perm_type", UserDefinedTypeSchema{
			ID:       "my-list",
			PermType: "playlists",
			Fields:   map[string]UserDefinedField{"title": {}},
		}},
		{"built-in data type id", UserDefinedTypeSchema{
			ID:     "tracks",
			Fields: map[string]UserDefinedField{"title": {}},
		}},
		{"reserved path id", UserDefinedTypeSchema{
			ID:     "links",
			Fields: map[string]UserDefinedField{"title": {}},
		}},
		{"path collision", UserDefinedTypeSchema{
			ID:     "my-list",
			Path:   "/v1/library/links",
			Fields: map[string]UserDefinedField{"title": {}},
		}},
		{"non-library path", UserDefinedTypeSchema{
			ID:     "my-list",
			Path:   "/v1/mail/",
			Fields: map[string]UserDefinedField{"title": {}},
		}},
		{"bad id charset", UserDefinedTypeSchema{
			ID:     "My_List",
			Fields: map[string]UserDefinedField{"title": {}},
		}},
		{"forbidden header", UserDefinedTypeSchema{
			ID:     "my-list",
			Fields: map[string]UserDefinedField{"title": {}},
			Sources: []UserDefinedSourceSpec{{
				ID:       "s",
				Kind:     "http_json",
				Endpoint: "https://example.com",
				Headers:  map[string]string{"Authorization": "Bearer x"},
				FieldMap: map[string]string{"title": "t"},
			}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.schema
			if err := s.Normalize(); err == nil {
				t.Fatalf("Normalize(%+v) succeeded, want error", s)
			}
		})
	}
}

func TestUserDefinedTypeSchema_RejectsPathOverride(t *testing.T) {
	s := UserDefinedTypeSchema{
		ID:     "reading-list",
		Path:   "/v1/library/tracks",
		Fields: map[string]UserDefinedField{"title": {}},
	}
	if err := s.Normalize(); err == nil {
		t.Fatal("path override to built-in route must fail")
	}
}
