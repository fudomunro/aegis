package kinds

// Test-only helpers, moved out of production sources so they stop shipping
// in the binary. They were unreachable from any non-test call site.

// BuiltinKindPaths returns every built-in agent path in registry order.
func BuiltinKindPaths() []string {
	out := make([]string, 0, len(BuiltinLibraryKinds))
	for _, k := range BuiltinLibraryKinds {
		out = append(out, k.Path)
	}
	return out
}

// ReservedKindKeys returns permission / path-classification keys owned
// by built-in types: every BuiltinLibraryKinds ID plus non-kind proxy keys.
func ReservedKindKeys() map[string]bool {
	out := make(map[string]bool, len(BuiltinLibraryKinds)+len(nonKindKeys))
	for _, k := range BuiltinLibraryKinds {
		out[k.ID] = true
	}
	for id := range nonKindKeys {
		out[id] = true
	}
	return out
}
