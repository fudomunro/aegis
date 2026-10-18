package core

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestUserDefinedHint_ListsSortedFieldsAndCanonical(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "notion", Name: "Notion Export", Action: "inject", CanonicalFor: []string{"reading-list"}},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	schema := types.UserDefinedTypeSchema{
		ID:            "reading-list",
		Label:         "reading list",
		Path:          "/v1/library/reading-list",
		Usage:         "books the user is reading",
		Summary:       "entries with title and author",
		SyncRule:      "match on title+author",
		ExampleQuestion: "what books am I reading?",
		Fields: map[string]types.UserDefinedField{
			"title":  {Type: "string", Required: true, Description: "book title"},
			"author": {Type: "string"},
			"pages":  {Type: "number", Description: "page count"},
		},
	}

	hint := srv.userDefinedHint(schema)
	for _, want := range []string{
		"/v1/library/reading-list",
		"Notion Export",
		"reading list",
		"Schema-declared fields",
		"title (book title, required)",
		"author (string)",
		"pages (page count)",
	} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint missing %q:\n%s", want, hint)
		}
	}
	// Sorted for prompt-cache stability: author before pages before title.
	ai := strings.Index(hint, "author (")
	pi := strings.Index(hint, "pages (")
	ti := strings.Index(hint, "title (")
	if !(ai < pi && pi < ti) {
		t.Fatalf("fields not sorted: author=%d pages=%d title=%d", ai, pi, ti)
	}
}
