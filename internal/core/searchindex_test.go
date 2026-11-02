package core

import (
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func newTestSearchIndex(t *testing.T) *SearchIndex {
	t.Helper()
	x, err := OpenSearchIndex(filepath.Join(t.TempDir(), "search-index.db"))
	if err != nil {
		t.Fatalf("open search index: %v", err)
	}
	t.Cleanup(func() { x.Close() })
	return x
}

func searchDoc(id, title, body string, services ...string) types.SearchDocument {
	return types.SearchDocument{
		ItemID:   id,
		Title:    title,
		Body:     body,
		Payload:  []byte(`{"name":"` + title + `"}`),
		Services: services,
	}
}

func TestSearchIndex_UpsertAndSearch(t *testing.T) {
	x := newTestSearchIndex(t)
	if err := x.Upsert("tracks", "", []types.SearchDocument{
		searchDoc("t1", "Purple Rain", "Purple Rain Prince", "spotify"),
		searchDoc("t2", "Raining Blood", "Raining Blood Slayer", "spotify"),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	res, err := x.Search(SearchIndexQuery{Kind: "tracks", Q: "rain"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("total = %d, want 2: %+v", res.Total, res.Docs)
	}
	res, err = x.Search(SearchIndexQuery{Kind: "tracks", Q: "prince"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 1 || res.Docs[0].ItemID != "t1" {
		t.Fatalf("q=prince = %+v", res)
	}
	if string(res.Docs[0].Payload) == "" {
		t.Fatal("payload must round-trip")
	}

	// Re-upsert refreshes the FTS row rather than duplicating it.
	if err := x.Upsert("tracks", "", []types.SearchDocument{
		searchDoc("t1", "Purple Rain", "Purple Rain The Artist Formerly Known As Prince", "spotify"),
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	res, err = x.Search(SearchIndexQuery{Kind: "tracks", Q: "artist"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("after re-upsert total = %d: %+v", res.Total, res.Docs)
	}
}

func TestSearchIndex_DatasetPartitions(t *testing.T) {
	x := newTestSearchIndex(t)
	if err := x.Upsert("tracks", "shire", []types.SearchDocument{
		searchDoc("t1", "Hobbiton", "Shared token shire"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.Upsert("tracks", "gondor", []types.SearchDocument{
		searchDoc("t2", "Minas Tirith", "Shared token gondor"),
	}); err != nil {
		t.Fatal(err)
	}

	all, err := x.Search(SearchIndexQuery{Kind: "tracks", Q: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 {
		t.Fatalf("unscoped total = %d", all.Total)
	}
	shire, err := x.Search(SearchIndexQuery{Kind: "tracks", Dataset: "shire", Q: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if shire.Total != 1 || shire.Docs[0].ItemID != "t1" {
		t.Fatalf("shire partition leaked: %+v", shire)
	}
	gondor, err := x.Search(SearchIndexQuery{Kind: "tracks", Dataset: "gondor", Q: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if gondor.Total != 1 || gondor.Docs[0].ItemID != "t2" {
		t.Fatalf("gondor partition leaked: %+v", gondor)
	}
}

func TestSearchIndex_CountAndEmptyQuery(t *testing.T) {
	x := newTestSearchIndex(t)
	n, err := x.Count("tracks", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
	if err := x.Upsert("tracks", "", []types.SearchDocument{searchDoc("t1", "One", "One")}); err != nil {
		t.Fatal(err)
	}
	n, err = x.Count("tracks", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	// An empty match expression must not error the query.
	if _, err := x.Search(SearchIndexQuery{Kind: "tracks", Q: ""}); err != nil {
		t.Fatalf("empty query: %v", err)
	}
}

func TestSearchIndex_HostileFTSInput(t *testing.T) {
	x := newTestSearchIndex(t)
	if err := x.Upsert("tracks", "", []types.SearchDocument{
		searchDoc("t1", "NEAR(a b)", "OR AND NOT * ^ : ( ) { } \"quoted\""),
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`NEAR(a b)`, `title:foo`, `"unbalanced`, `a OR b`, `{x} ^y`,
		`*"`, `::::`, `(){} `,
	} {
		if _, err := x.Search(SearchIndexQuery{Kind: "tracks", Q: q}); err != nil {
			t.Fatalf("hostile q=%q: %v", q, err)
		}
	}
}

func TestSearchIndex_PaginationCap(t *testing.T) {
	x := newTestSearchIndex(t)
	docs := make([]types.SearchDocument, 0, 5)
	for i := range 5 {
		docs = append(docs, searchDoc("t"+string(rune('a'+i)), "Shared title", "Shared title"))
	}
	if err := x.Upsert("tracks", "", docs); err != nil {
		t.Fatal(err)
	}
	res, err := x.Search(SearchIndexQuery{Kind: "tracks", Q: "shared", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 || len(res.Docs) != 2 {
		t.Fatalf("page 1: total=%d len=%d", res.Total, len(res.Docs))
	}
	res2, err := x.Search(SearchIndexQuery{Kind: "tracks", Q: "shared", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Docs) != 2 {
		t.Fatalf("page 2 len=%d", len(res2.Docs))
	}
	if res.Docs[0].ItemID == res2.Docs[0].ItemID {
		t.Fatal("pages must not overlap")
	}
}

func TestSearchIndex_Disabled(t *testing.T) {
	x, err := OpenSearchIndex("")
	if err != nil || x != nil {
		t.Fatalf("empty path must disable: %v %v", x, err)
	}
	var nilIdx *SearchIndex
	if nilIdx.Enabled() {
		t.Fatal("nil index must not be enabled")
	}
	if err := nilIdx.Upsert("tracks", "", nil); err == nil {
		t.Fatal("upsert on disabled index must error")
	}
	if _, err := nilIdx.Search(SearchIndexQuery{Kind: "tracks"}); err == nil {
		t.Fatal("search on disabled index must error")
	}
}

func TestFtsMatchExpr_StripsOperators(t *testing.T) {
	match, _ := ftsMatchExpr(`rain "snow" title:foo NEAR(a b)`)
	// Last token is prefix-matched; operator characters never survive.
	if match != `"rain" AND "snow" AND "title" AND "foo" AND "NEAR" AND "a" AND "b"*` {
		t.Fatalf("match = %q", match)
	}
	if match, _ := ftsMatchExpr(`   `); match != "" {
		t.Fatalf("blank match = %q", match)
	}
}
