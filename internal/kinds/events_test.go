package kinds

import (
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

// TestFreeBusyListFromEvents_OmitsCancelled pins the freebusy encode
// (issue #128): cancelled events never block time, all-day blocks survive.
func TestFreeBusyListFromEvents_OmitsCancelled(t *testing.T) {
	full := types.NewCalendarEvent("google-calendar", "Google Calendar", "e1", "Doctor appointment", "2026-03-02T10:00:00Z", "2026-03-02T10:30:00Z", false)
	cancelled := types.NewCalendarEvent("google-calendar", "Google Calendar", "e2", "Cancelled standup", "2026-03-02T15:00:00Z", "2026-03-02T15:15:00Z", false)
	cancelled.Status = "cancelled"
	allday := types.NewCalendarEvent("google-calendar", "Google Calendar", "e3", "Offsite", "2026-03-05T00:00:00Z", "2026-03-06T00:00:00Z", true)
	events := []types.CalendarEvent{full, cancelled, allday}

	list := freeBusyListFromEvents(events, types.PageRequest{Limit: types.MaxPageLimit}, nil)
	if list.Total != 2 || len(list.Busy) != 2 {
		t.Fatalf("total = %d busy = %+v", list.Total, list.Busy)
	}
	for _, b := range list.Busy {
		if b.StartAt == "" {
			t.Fatalf("empty block: %+v", b)
		}
	}
	if !list.Busy[1].AllDay {
		t.Fatalf("expected all-day block: %+v", list.Busy[1])
	}
}
