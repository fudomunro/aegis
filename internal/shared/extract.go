package shared

// ExtraOrNil returns m, or nil when m is empty — so empty provider "extra"
// blobs do not round-trip as `{}` in neutral models.
func ExtraOrNil(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// ExtractNames collects the "name" field of each object under obj[key].
func ExtractNames(obj map[string]any, key string) []string {
	arr, ok := obj[key].([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := m["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

// ExtractNested walks obj through the given keys and returns the final value.
func ExtractNested(obj map[string]any, keys ...string) any {
	current := obj
	for _, key := range keys[:len(keys)-1] {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current[keys[len(keys)-1]]
}

// AsInt coerces a decoded JSON number (int/int32/int64/float64) to int.
func AsInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
