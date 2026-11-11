package shared

// Search shapes exchanged between the kinds pipeline (which builds and
// queries rows) and the search engine behind it. The row format is
// engine-internal; these are the seam. Metadata only — never media bytes.

// SearchDocument is one merged library item flattened for the search index.
// Payload carries the neutral item JSON so search hits can be returned
// without a live provider fetch.
type SearchDocument struct {
	// ItemID is the stable identity of the merged item within (kind, dataset).
	ItemID string
	// Title is the display title (TitleField value).
	Title string
	// Body is the concatenated indexed field text.
	Body string
	// Payload is the marshaled neutral item used to hydrate search results.
	Payload []byte
	// Services are the source service IDs of the item, used to drop hits the
	// request is not allowed to read.
	Services []string
}

// SearchQuery is one full-text lookup over a kind.
type SearchQuery struct {
	Kind    string
	Dataset string
	Q       string
	Limit   int
	Offset  int
}

// SearchResult is the page of matching documents plus the full hit count.
type SearchResult struct {
	Docs  []SearchDocument
	Total int
}
