package kinds

import "testing"

func sampleDoc(service, name, id, title, created, excerpt string) Document {
	return Document{
		Title:     title,
		CreatedAt: created,
		Excerpt:   excerpt,
		Sources: []DocumentSource{{
			Service:     service,
			ServiceName: name,
			ID:          id,
			Canonical:   true,
		}},
	}
}

func TestDocumentIdentityKey_PrefersDurableIDs(t *testing.T) {
	a := Document{EntityID: "SHA256:ABC", Title: "Lease", CreatedAt: "2024-01-01T00:00:00Z"}
	b := Document{EntityID: "sha256:abc", Title: "Lease (scan)", CreatedAt: "2024-01-02T00:00:00Z"}
	if DocumentIdentityKey(a) != DocumentIdentityKey(b) {
		t.Fatalf("checksum entity IDs should match case-insensitively: %q vs %q",
			DocumentIdentityKey(a), DocumentIdentityKey(b))
	}

	c := Document{
		Title: "Warranty", CreatedAt: "2023-06-01T00:00:00Z",
		Sources: []DocumentSource{{Service: "paperless", ID: "1", Extra: map[string]any{"archive_serial": "42"}}},
	}
	d := Document{
		Title: "Warranty (other)", CreatedAt: "2023-06-02T00:00:00Z",
		Sources: []DocumentSource{{Service: "paperless-archive", ID: "9", Extra: map[string]any{"archive_serial": "42"}}},
	}
	if DocumentIdentityKey(c) != DocumentIdentityKey(d) {
		t.Fatalf("archive serial should match: %q vs %q", DocumentIdentityKey(c), DocumentIdentityKey(d))
	}

	// Same title + created merges; different created must not.
	e := sampleDoc("paperless", "Paperless", "1", "Insurance", "2024-03-01T00:00:00Z", "")
	f := sampleDoc("drive", "Drive", "2", "Insurance", "2024-03-01T00:00:00Z", "")
	if DocumentIdentityKey(e) != DocumentIdentityKey(f) {
		t.Fatal("title+created should match")
	}
	g := sampleDoc("paperless", "Paperless", "3", "Insurance", "2021-03-01T00:00:00Z", "")
	if DocumentIdentityKey(e) == DocumentIdentityKey(g) {
		t.Fatal("different created dates must not share a title key")
	}

	// No identity falls back to service:id.
	h := sampleDoc("paperless", "Paperless", "x1", "", "", "")
	i := sampleDoc("paperless", "Paperless", "x2", "", "", "")
	if DocumentIdentityKey(h) == DocumentIdentityKey(i) {
		t.Fatal("documents with no identity must stay distinct via service:id")
	}
}

func TestMergeDocuments_CombinesSourcesAndRecordsMatch(t *testing.T) {
	a := Document{
		Title: "Lease", CreatedAt: "2024-01-15T00:00:00Z", Excerpt: "full excerpt",
		Tags: []string{"housing"}, EntityID: "sha256:same",
		Sources: []DocumentSource{{Service: "paperless", ServiceName: "Paperless-ngx", ID: "1", Canonical: true}},
	}
	b := Document{
		Title: "Lease scan", CreatedAt: "2024-01-15T00:00:00Z", Excerpt: "other",
		Tags: []string{"legal"}, EntityID: "sha256:same",
		Sources: []DocumentSource{{Service: "drive", ServiceName: "Google Drive", ID: "2"}},
	}
	merged := MergeDocuments([]Document{a, b})
	if len(merged) != 1 {
		t.Fatalf("got %d documents, want 1: %+v", len(merged), merged)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("want 2 sources, got %+v", merged[0].Sources)
	}
	if !merged[0].Sources[0].Canonical {
		t.Fatalf("canonical source should sort first: %+v", merged[0].Sources)
	}
	if merged[0].Match == "" {
		t.Fatal("multi-source merge should record match")
	}
	if merged[0].Excerpt != "full excerpt" {
		t.Fatalf("excerpt = %q, want first non-empty", merged[0].Excerpt)
	}
	if len(merged[0].Tags) != 2 {
		t.Fatalf("tags should union: %+v", merged[0].Tags)
	}
}

func TestMergeDocuments_NearMissDoesNotMerge(t *testing.T) {
	old := sampleDoc("paperless", "Paperless-ngx", "1", "Laptop warranty", "2021-06-01T00:00:00Z", "")
	fresh := sampleDoc("paperless", "Paperless-ngx", "2", "Laptop warranty", "2023-06-01T00:00:00Z", "")
	merged := MergeDocuments([]Document{old, fresh})
	if len(merged) != 2 {
		t.Fatalf("same title different dates must not merge: %+v", merged)
	}
}

func TestMergeDocuments_Empty(t *testing.T) {
	if MergeDocuments(nil) != nil {
		t.Fatal("MergeDocuments(nil) should be nil")
	}
}
