package types

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

// ReservedDataTypeKeys returns permission / path-classification keys owned
// by built-in types: every BuiltinLibraryKinds ID plus non-kind proxy keys.
func ReservedDataTypeKeys() map[string]bool {
	out := make(map[string]bool, len(BuiltinLibraryKinds)+len(nonKindDataTypeKeys))
	for _, k := range BuiltinLibraryKinds {
		out[k.ID] = true
	}
	for id := range nonKindDataTypeKeys {
		out[id] = true
	}
	return out
}
