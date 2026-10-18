package core

import "testing"

func TestCalendarEventExtraString(t *testing.T) {
	if got := calendarEventExtraString(nil, "ical_uid"); got != "" {
		t.Fatalf("nil map = %q", got)
	}
	extra := map[string]any{
		"ical_uid": "abc-123",
		"count":    3,
		"blank":    nil,
	}
	if got := calendarEventExtraString(extra, "ical_uid"); got != "abc-123" {
		t.Fatalf("string = %q", got)
	}
	if got := calendarEventExtraString(extra, "missing"); got != "" {
		t.Fatalf("missing = %q", got)
	}
	if got := calendarEventExtraString(extra, "count"); got != "" {
		t.Fatalf("non-string = %q", got)
	}
	if got := calendarEventExtraString(extra, "blank"); got != "" {
		t.Fatalf("nil value = %q", got)
	}
}
