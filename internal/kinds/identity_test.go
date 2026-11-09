package kinds

import "testing"

func TestExtraString(t *testing.T) {
	if got := ExtraString(nil, "ical_uid"); got != "" {
		t.Fatalf("nil map = %q", got)
	}
	extra := map[string]any{
		"ical_uid": "abc-123",
		"count":    3,
		"blank":    nil,
	}
	if got := ExtraString(extra, "ical_uid"); got != "abc-123" {
		t.Fatalf("string = %q", got)
	}
	if got := ExtraString(extra, "missing"); got != "" {
		t.Fatalf("missing = %q", got)
	}
	if got := ExtraString(extra, "count"); got != "" {
		t.Fatalf("non-string = %q", got)
	}
	if got := ExtraString(extra, "blank"); got != "" {
		t.Fatalf("nil value = %q", got)
	}
}
