package search

import (
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

func newTestIndex(t *testing.T) *Index {
	t.Helper()
	x, err := Open(filepath.Join(t.TempDir(), "search-index.db"))
	if err != nil {
		t.Fatalf("open search index: %v", err)
	}
	t.Cleanup(func() { x.Close() })
	return x
}

func searchDoc(id, title, body string, services ...string) shared.SearchDocument {
	return shared.SearchDocument{
		ItemID:   id,
		Title:    title,
		Body:     body,
		Payload:  []byte(`{"name":"` + title + `"}`),
		Services: services,
	}
}

func TestSearchIndex_UpsertAndSearch(t *testing.T) {
	x := newTestIndex(t)
	if err := x.Upsert("tracks", "", []shared.SearchDocument{
		searchDoc("t1", "Purple Rain", "Purple Rain Prince", "spotify"),
		searchDoc("t2", "Raining Blood", "Raining Blood Slayer", "spotify"),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	res, err := x.Search(shared.SearchQuery{Kind: "tracks", Q: "rain"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("total = %d, want 2: %+v", res.Total, res.Docs)
	}
	res, err = x.Search(shared.SearchQuery{Kind: "tracks", Q: "prince"})
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
	if err := x.Upsert("tracks", "", []shared.SearchDocument{
		searchDoc("t1", "Purple Rain", "Purple Rain The Artist Formerly Known As Prince", "spotify"),
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	res, err = x.Search(shared.SearchQuery{Kind: "tracks", Q: "artist"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("after re-upsert total = %d: %+v", res.Total, res.Docs)
	}
}

func TestSearchIndex_DatasetPartitions(t *testing.T) {
	x := newTestIndex(t)
	if err := x.Upsert("tracks", "shire", []shared.SearchDocument{
		searchDoc("t1", "Hobbiton", "Shared token shire"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.Upsert("tracks", "gondor", []shared.SearchDocument{
		searchDoc("t2", "Minas Tirith", "Shared token gondor"),
	}); err != nil {
		t.Fatal(err)
	}

	all, err := x.Search(shared.SearchQuery{Kind: "tracks", Q: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 {
		t.Fatalf("unscoped total = %d", all.Total)
	}
	shire, err := x.Search(shared.SearchQuery{Kind: "tracks", Dataset: "shire", Q: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if shire.Total != 1 || shire.Docs[0].ItemID != "t1" {
		t.Fatalf("shire partition leaked: %+v", shire)
	}
	gondor, err := x.Search(shared.SearchQuery{Kind: "tracks", Dataset: "gondor", Q: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if gondor.Total != 1 || gondor.Docs[0].ItemID != "t2" {
		t.Fatalf("gondor partition leaked: %+v", gondor)
	}
}

func TestSearchIndex_CountAndEmptyQuery(t *testing.T) {
	x := newTestIndex(t)
	n, err := x.Count("tracks", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
	if err := x.Upsert("tracks", "", []shared.SearchDocument{searchDoc("t1", "One", "One")}); err != nil {
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
	if _, err := x.Search(shared.SearchQuery{Kind: "tracks", Q: ""}); err != nil {
		t.Fatalf("empty query: %v", err)
	}
}

func TestSearchIndex_HostileFTSInput(t *testing.T) {
	x := newTestIndex(t)
	if err := x.Upsert("tracks", "", []shared.SearchDocument{
		searchDoc("t1", "NEAR(a b)", "OR AND NOT * ^ : ( ) { } \"quoted\""),
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`NEAR(a b)`, `title:foo`, `"unbalanced`, `a OR b`, `{x} ^y`,
		`*"`, `::::`, `(){} `,
	} {
		if _, err := x.Search(shared.SearchQuery{Kind: "tracks", Q: q}); err != nil {
			t.Fatalf("hostile q=%q: %v", q, err)
		}
	}
}

func TestSearchIndex_PaginationCap(t *testing.T) {
	x := newTestIndex(t)
	docs := make([]shared.SearchDocument, 0, 5)
	for i := range 5 {
		docs = append(docs, searchDoc("t"+string(rune('a'+i)), "Shared title", "Shared title"))
	}
	if err := x.Upsert("tracks", "", docs); err != nil {
		t.Fatal(err)
	}
	res, err := x.Search(shared.SearchQuery{Kind: "tracks", Q: "shared", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 || len(res.Docs) != 2 {
		t.Fatalf("page 1: total=%d len=%d", res.Total, len(res.Docs))
	}
	res2, err := x.Search(shared.SearchQuery{Kind: "tracks", Q: "shared", Limit: 2, Offset: 2})
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
	x, err := Open("")
	if err != nil || x != nil {
		t.Fatalf("empty path must disable: %v %v", x, err)
	}
	var nilIdx *Index
	if nilIdx.Enabled() {
		t.Fatal("nil index must not be enabled")
	}
	if err := nilIdx.Upsert("tracks", "", nil); err == nil {
		t.Fatal("upsert on disabled index must error")
	}
	if _, err := nilIdx.Search(shared.SearchQuery{Kind: "tracks"}); err == nil {
		t.Fatal("search on disabled index must error")
	}
}

func TestFtsMatchExpr_StripsOperators(t *testing.T) {
	match, _ := FTSMatchExpr(`rain "snow" title:foo NEAR(a b)`)
	// Last token is prefix-matched; operator characters never survive.
	if match != `"rain" AND "snow" AND "title" AND "foo" AND "NEAR" AND "a" AND "b"*` {
		t.Fatalf("match = %q", match)
	}
	if match, _ := FTSMatchExpr(`   `); match != "" {
		t.Fatalf("blank match = %q", match)
	}
}

func TestSearchIndex_CountsByKind(t *testing.T) {
	x := newTestIndex(t)
	if _, err := x.CountsByKind(); err != nil {
		t.Fatalf("empty counts: %v", err)
	}
	if err := x.Upsert("tracks", "", []shared.SearchDocument{
		searchDoc("t1", "One", "One"),
		searchDoc("t2", "Two", "Two"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.Upsert("contacts", "shire", []shared.SearchDocument{
		searchDoc("c1", "Ada", "Ada"),
	}); err != nil {
		t.Fatal(err)
	}
	counts, err := x.CountsByKind()
	if err != nil {
		t.Fatal(err)
	}
	if counts["tracks"] != 2 || counts["contacts"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	if _, err := x.CountsByKind(); err != nil {
		t.Fatal(err)
	}
	var nilIdx *Index
	if _, err := nilIdx.CountsByKind(); err == nil {
		t.Fatal("disabled index must error")
	}
}

func TestSearchIndex_SyncDropsRefutedRows(t *testing.T) {
	x := newTestIndex(t)
	if err := x.Upsert("contacts", "", []shared.SearchDocument{
		searchDoc("t1", "Ada", "Ada Lovelace", "google"),
		searchDoc("t2", "Grace", "Grace Hopper", "google"),
	}); err != nil {
		t.Fatal(err)
	}
	// A complete fetch that no longer returns t2 refutes it.
	if err := x.Sync("contacts", "", []shared.SearchDocument{
		searchDoc("t1", "Ada", "Ada Lovelace", "google"),
	}, []string{"google"}); err != nil {
		t.Fatal(err)
	}
	res, err := x.Search(shared.SearchQuery{Kind: "contacts", Q: "grace"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 {
		t.Fatalf("refuted row still matches: %+v", res.Docs)
	}
	if res, err = x.Search(shared.SearchQuery{Kind: "contacts", Q: "ada"}); err != nil || res.Total != 1 {
		t.Fatalf("survivor: %+v err=%v", res, err)
	}
}

func TestSearchIndex_SyncKeepsRowsFromIncompleteServices(t *testing.T) {
	x := newTestIndex(t)
	if err := x.Upsert("contacts", "", []shared.SearchDocument{
		searchDoc("t1", "Ada", "Ada Lovelace", "google"),
		searchDoc("t2", "Secret", "Secret Person", "outlook"),
	}); err != nil {
		t.Fatal(err)
	}
	// Only google completed; outlook's row must survive the fetch.
	if err := x.Sync("contacts", "", []shared.SearchDocument{
		searchDoc("t1", "Ada", "Ada Lovelace", "google"),
	}, []string{"google"}); err != nil {
		t.Fatal(err)
	}
	if res, err := x.Search(shared.SearchQuery{Kind: "contacts", Q: "secret"}); err != nil || res.Total != 1 {
		t.Fatalf("row from incomplete service dropped: %+v err=%v", res, err)
	}
}

func TestSearchIndex_SyncWithoutCompletionDeletesNothing(t *testing.T) {
	x := newTestIndex(t)
	if err := x.Upsert("contacts", "", []shared.SearchDocument{
		searchDoc("t1", "Ada", "Ada Lovelace", "google"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.Sync("contacts", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := x.Count("contacts", ""); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

func TestSearchIndex_SyncPreservesUnknownMembership(t *testing.T) {
	x := newTestIndex(t)
	if err := x.Upsert("contacts", "", []shared.SearchDocument{
		searchDoc("t1", "Ada", "Ada Lovelace", "google", "outlook"),
	}); err != nil {
		t.Fatal(err)
	}
	// google still returns the item but outlook is degraded: the vouch from
	// outlook must stay so a later google-only fetch cannot refute the row.
	if err := x.Sync("contacts", "", []shared.SearchDocument{
		searchDoc("t1", "Ada", "Ada Lovelace updated", "google"),
	}, []string{"google"}); err != nil {
		t.Fatal(err)
	}
	if err := x.Sync("contacts", "", nil, []string{"google"}); err != nil {
		t.Fatal(err)
	}
	res, err := x.Search(shared.SearchQuery{Kind: "contacts", Q: "ada"})
	if err != nil || res.Total != 1 {
		t.Fatalf("row with unknown vouch was refuted: %+v err=%v", res, err)
	}
	if got := res.Docs[0].Services; len(got) != 2 {
		t.Fatalf("services = %v, want google+outlook", got)
	}
}
