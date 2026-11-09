package kinds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// stubKind is a minimal Kind for registry-construction tests.
type stubKind struct {
	id, pattern string
}

func (s stubKind) Def() LibraryKind { return LibraryKind{ID: s.id, Path: s.pattern} }

func (s stubKind) Routes() []Route {
	return []Route{{Pattern: s.pattern, Handler: func(http.ResponseWriter, *http.Request) {}}}
}

func (s stubKind) Provides(any) bool      { return false }
func (s stubKind) HasProviders(Host) bool { return true }
func (s stubKind) Readable(Host) bool     { return true }

func TestMergeUserDefined_RejectsShadowing(t *testing.T) {
	builtin := []Kind{
		stubKind{id: "documents", pattern: "/v1/library/documents"},
		stubKind{id: "text", pattern: "/v1/library/documents/{service}/{id}/text"},
	}
	udts := []Kind{
		// ID collision with a built-in.
		stubKind{id: "documents", pattern: "/v1/library/other-documents"},
		// Pattern conflict with a built-in subroute (unequal patterns —
		// only the ServeMux registration check catches this).
		stubKind{id: "doc-text-clone", pattern: "/v1/library/documents/{a}/{b}/text"},
		// Clean pack.
		stubKind{id: "safe-udt", pattern: "/v1/library/safe-udt"},
	}
	merged, skipped := MergeUserDefined(builtin, udts)

	if len(skipped) != 2 || skipped[0] != "documents" || skipped[1] != "doc-text-clone" {
		t.Fatalf("skipped = %v, want [documents doc-text-clone]", skipped)
	}
	if len(merged) != 3 {
		t.Fatalf("merged = %d kinds, want 3", len(merged))
	}
	found := false
	for _, k := range merged {
		if k.Def().ID == "safe-udt" {
			found = true
		}
		if k.Def().ID == "doc-text-clone" {
			t.Fatal("shadowing UDT must not merge")
		}
	}
	if !found {
		t.Fatal("non-colliding UDT not merged")
	}
}

func TestMergeUserDefined_RejectsReservedIDs(t *testing.T) {
	merged, skipped := MergeUserDefined(nil, []Kind{
		stubKind{id: "tracks", pattern: "/v1/library/whatever"},
	})
	if len(merged) != 0 || len(skipped) != 1 {
		t.Fatalf("merged = %v skipped = %v, want only the reserved id skipped", merged, skipped)
	}
}

// listUserDefined fetches every source for one schema and returns the raw
// pre-merge rows (the aggregate pipeline owns merge + encode).
func listUserDefined(ctx context.Context, schema UserDefinedTypeSchema, baseDir string) (items []UserDefinedItem, truncated bool, err error) {
	srcs := schema.Sources
	if len(srcs) == 0 {
		return nil, false, fmt.Errorf("no sources configured")
	}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	errs := make([]error, 0, len(srcs))
	all := make([]UserDefinedItem, 0, len(srcs)*8)
	for _, src := range srcs {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A bad source must not fail the whole list (fail-open).
			items, trunc, ferr := fetchUserDefinedSource(ctx, schema, src, baseDir)
			mu.Lock()
			defer mu.Unlock()
			if ferr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", src.ID, ferr))
				return
			}
			if trunc {
				truncated = true
			}
			all = append(all, items...)
		}()
	}
	wg.Wait()
	// Fail-open: return partial rows even when some sources erred. Only
	// return an error when every source failed and nothing was collected.
	if len(all) == 0 && len(errs) > 0 {
		return nil, truncated, errors.Join(errs...)
	}
	return all, truncated, nil
}

func TestDottedPath(t *testing.T) {
	doc := map[string]any{
		"items": []any{
			map[string]any{"name": "a", "meta": map[string]any{"tag": "x"}},
		},
	}
	if got := dottedPath(doc, "items.0.name"); got != "a" {
		t.Fatalf("items.0.name = %v", got)
	}
	if got := dottedPath(doc, "items.0.meta.tag"); got != "x" {
		t.Fatalf("items.0.meta.tag = %v", got)
	}
	if got := dottedPath(doc, "items.0.missing"); got != nil {
		t.Fatalf("items.0.missing = %v", got)
	}
	if got := dottedPath(doc, "items.9.name"); got != nil {
		t.Fatalf("items.9.name = %v", got)
	}
	if got := dottedPath(doc, ""); got == nil {
		t.Fatal("empty path should return the document")
	}
}

func TestMapUserDefinedDocument_HTTPSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{
					"id":         "n1",
					"properties": map[string]any{"Name": "The Dispossessed", "Author": "Le Guin"},
				},
			},
		})
	}))
	defer srv.Close()
	schema := UserDefinedTypeSchema{
		ID:   "books",
		Path: "/v1/library/books",
		Fields: map[string]UserDefinedField{
			"title":  {Type: "string"},
			"author": {Type: "string"},
		},
		Identity: UserDefinedIdentity{Fields: []string{"title", "author"}},
		Sources: []UserDefinedSourceSpec{{
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
	items, truncated, err := fetchUserDefinedSource(context.Background(), schema, schema.Sources[0], "")
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
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"items":[{"title":"Only Good","author":"A"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	schema := UserDefinedTypeSchema{
		ID:     "notes",
		Fields: map[string]UserDefinedField{"title": {}, "author": {}},
		Identity: UserDefinedIdentity{
			Fields: []string{"title", "author"},
		},
		Sources: []UserDefinedSourceSpec{
			{ID: "good", Kind: "file_json", Path: "good.json", ItemsPath: "items", FieldMap: map[string]string{"title": "title", "author": "author"}},
			{ID: "bad", Kind: "file_json", Path: "missing.json", ItemsPath: "items", FieldMap: map[string]string{"title": "title"}},
		},
	}
	if err := schema.Normalize(); err != nil {
		t.Fatal(err)
	}
	items, _, err := listUserDefined(context.Background(), schema, dir)
	if err != nil {
		t.Fatalf("fail-open list should succeed with partial rows, got %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
}
