package types

// SearchSpec declares how one library kind is searched (issue #178). It is
// part of the type definition — builtin kinds declare it in the registry and
// user-defined packs declare it in the schema — so search stays pluggable
// instead of a per-handler afterthought.
type SearchSpec struct {
	// Fields lists neutral item JSON keys to full-text index. Values are
	// flattened to text (strings as-is; arrays and nested objects contribute
	// their display text, minus identity/transport keys).
	Fields []string
	// TitleField is the JSON key whose value titles a search hit. Defaults
	// to the first entry in Fields.
	TitleField string
}
