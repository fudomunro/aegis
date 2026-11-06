package shared

import "testing"

func TestExtractNamesAndNested(t *testing.T) {
	obj := map[string]any{
		"artists": []any{
			map[string]any{"name": "A"},
			"skip",
			map[string]any{"no_name": true},
			map[string]any{"name": "B"},
		},
		"album": map[string]any{"name": "Al", "id": "1"},
	}
	names := ExtractNames(obj, "artists")
	if len(names) != 2 || names[0] != "A" || names[1] != "B" {
		t.Fatalf("names = %v", names)
	}
	if ExtractNames(obj, "missing") != nil {
		t.Fatal("missing key should be nil")
	}
	if got := ExtractNested(obj, "album", "name"); got != "Al" {
		t.Fatalf("nested = %v", got)
	}
	if got := ExtractNested(obj, "album", "id"); got != "1" {
		t.Fatalf("nested id = %v", got)
	}
	if got := ExtractNested(obj, "album", "missing"); got != nil {
		t.Fatalf("missing nested = %v", got)
	}
	if got := ExtractNested(map[string]any{}, "a", "b"); got != nil {
		t.Fatalf("empty = %v", got)
	}
}

func TestExtraOrNil(t *testing.T) {
	if ExtraOrNil(nil) != nil {
		t.Fatal("nil map should stay nil")
	}
	if ExtraOrNil(map[string]any{}) != nil {
		t.Fatal("empty map should be nil")
	}
	m := map[string]any{"uri": "x"}
	if ExtraOrNil(m)["uri"] != "x" {
		t.Fatal("non-empty map preserved")
	}
}

func TestAsInt(t *testing.T) {
	if n, ok := AsInt(float64(42)); !ok || n != 42 {
		t.Fatalf("float64 %v %v", n, ok)
	}
	if n, ok := AsInt(int(7)); !ok || n != 7 {
		t.Fatalf("int %v %v", n, ok)
	}
	if n, ok := AsInt(int64(8)); !ok || n != 8 {
		t.Fatalf("int64 %v %v", n, ok)
	}
	if _, ok := AsInt("nope"); ok {
		t.Fatal("string should not convert")
	}
	if _, ok := AsInt(nil); ok {
		t.Fatal("nil should not convert")
	}
}
