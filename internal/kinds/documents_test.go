package kinds

import (
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/types"
)

func TestDocumentFilters_Match(t *testing.T) {
	doc := types.Document{
		Title:         "Apartment lease 2024",
		Correspondent: "Harbour Property",
		DocumentType:  "lease",
		Tags:          []string{"housing", "legal"},
		Excerpt:       "twelve month term",
		CreatedAt:     "2024-01-15T12:00:00Z",
	}
	if !(documentFilters{}).matches(doc) {
		t.Fatal("empty filters should match")
	}
	if !(documentFilters{Q: "lease"}).matches(doc) {
		t.Fatal("q=lease should match title")
	}
	if (documentFilters{Q: "spaceship"}).matches(doc) {
		t.Fatal("q=spaceship should not match")
	}
	if !(documentFilters{Tag: "housing"}).matches(doc) {
		t.Fatal("tag=housing should match")
	}
	if (documentFilters{Tag: "medical"}).matches(doc) {
		t.Fatal("tag=medical should not match")
	}
	if !(documentFilters{After: "2024-01-01", Before: "2024-02-01"}).matches(doc) {
		t.Fatal("date window should match")
	}
	if (documentFilters{Before: "2024-01-01"}).matches(doc) {
		t.Fatal("created before window should not match")
	}
}

func TestRedactDocumentMetadata_StripsExcerpt(t *testing.T) {
	d := types.Document{Title: "Lease", Excerpt: "secret text", Tags: []string{"a"}}
	got := redactDocumentMetadata(d)
	if got.Excerpt != "" {
		t.Fatalf("excerpt = %q, want empty", got.Excerpt)
	}
	if got.Title != "Lease" || len(got.Tags) != 1 {
		t.Fatalf("metadata fields should survive: %+v", got)
	}
	if got := redactDocumentsForLevel([]types.Document{d}, policy.AccessMetadata); got[0].Excerpt != "" {
		t.Fatal("metadata level should strip excerpt")
	}
	if got := redactDocumentsForLevel([]types.Document{d}, policy.AccessRead); got[0].Excerpt != "secret text" {
		t.Fatal("read level should keep excerpt")
	}
	if got := redactDocumentsForLevel([]types.Document{d}, policy.AccessNone); got != nil {
		t.Fatal("none should return no items")
	}
}
