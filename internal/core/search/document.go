package search

// SearchDocument is one merged library item flattened for the search index.
// Payload carries the neutral item JSON so search hits can be returned
// without a live provider fetch. Metadata only — never media bytes.
//
// The row format is engine-internal: rows are built by the kinds package
// (BuildSearchDocument) from the kind's SearchSpec, and this package only
// stores and queries them.
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
