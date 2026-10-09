package main

import (
	"context"
	"net/http"
	"time"
)

// SampleCalendarConnector serves synthetic calendar events for demos and
// query testing (issue #77 / #131). It never talks to a provider — register
// it for a service with endpoint "sample://calendar" so free/busy demos work
// without live Google OAuth.
type SampleCalendarConnector struct{}

// sampleCalendarServiceID is the config connector/service id for fixtures.
const sampleCalendarServiceID = "sample-calendar"

const sampleCalendarServiceName = "Sample Calendar"

// sampleCalendarEndpoint marks a service as fixture-backed.
const sampleCalendarEndpoint = "sample://calendar"

func NewSampleCalendarConnector() *SampleCalendarConnector {
	return &SampleCalendarConnector{}
}

func (c *SampleCalendarConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "events",
				Description: "Synthetic calendar events for demos (no upstream provider).",
				Operations: []Operation{
					{
						Name:        "list_events",
						Description: "List synthetic events as neutral CalendarEvent values. Permission key: events.",
						Method:      "GET",
						Path:        "/me/events",
						Response: ResponseSpec{
							Description: "Neutral events with near-term busy blocks for free/busy demos.",
							Fields:      []string{"events", "total"},
						},
					},
				},
			},
		},
	}
}

func (c *SampleCalendarConnector) SystemInstructions(baseURL string) string {
	return "Sample Calendar is synthetic fixture data (issue #77). Use Aegis library endpoints for events/freebusy; there is no upstream provider."
}

func (c *SampleCalendarConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	events := sampleCalendarEvents()
	writeJSON(w, CalendarEventList{
		Events: events,
		Total:  len(events),
	})
}

// ListCalendarEvents implements CalendarEventProvider with deterministic
// synthetic events near "now" so free/busy demos show busy blocks today.
func (c *SampleCalendarConnector) ListCalendarEvents(_ context.Context) ([]CalendarEvent, bool, error) {
	return sampleCalendarEvents(), false, nil
}

// sampleCalendarEvents returns fictional events anchored a few days from now.
// Cancelled rows exist to prove free/busy omits them (issue #128).
func sampleCalendarEvents() []CalendarEvent {
	base := time.Now().UTC().Truncate(24 * time.Hour)
	day := func(offset int, hour, minute, durMin int) (start, end string) {
		s := base.AddDate(0, 0, offset).Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
		e := s.Add(time.Duration(durMin) * time.Minute)
		return s.Format(time.RFC3339), e.Format(time.RFC3339)
	}
	s1, e1 := day(1, 10, 0, 90)
	s2, e2 := day(1, 15, 30, 60)
	s3, e3 := day(2, 9, 0, 120)
	s4, e4 := day(3, 0, 0, 0) // all-day handled below
	_ = s4
	_ = e4
	s5, e5 := day(4, 13, 0, 45)

	allDayStart := base.AddDate(0, 0, 3).Format(time.RFC3339)
	allDayEnd := base.AddDate(0, 0, 4).Format(time.RFC3339)

	full := newCalendarEvent(sampleCalendarServiceID, sampleCalendarServiceName, "s-ev-1", "Project kickoff", s1, e1, false)
	full.Description = "Bring the roadmap draft"
	full.Location = "Room 4B"
	full.Attendees = []Contact{{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}}}
	full.EntityID = "kickoff@synthetic.example"

	oneOnOne := newCalendarEvent(sampleCalendarServiceID, sampleCalendarServiceName, "s-ev-2", "1:1 with Grace", s2, e2, false)
	oneOnOne.EntityID = "oneonone@synthetic.example"

	workshop := newCalendarEvent(sampleCalendarServiceID, sampleCalendarServiceName, "s-ev-3", "Design workshop", s3, e3, false)
	workshop.Location = "Studio B"
	workshop.EntityID = "workshop@synthetic.example"

	allday := newCalendarEvent(sampleCalendarServiceID, sampleCalendarServiceName, "s-ev-4", "Offsite", allDayStart, allDayEnd, true)
	allday.EntityID = "offsite@synthetic.example"

	cancelled := newCalendarEvent(sampleCalendarServiceID, sampleCalendarServiceName, "s-ev-5", "Cancelled standup", s5, e5, false)
	cancelled.Status = "cancelled"
	cancelled.EntityID = "standup@synthetic.example"

	return []CalendarEvent{full, oneOnOne, workshop, allday, cancelled}
}

// isSampleCalendarService reports whether a service endpoint is fixture-backed.
func isSampleCalendarService(svc *Service) bool {
	if svc == nil {
		return false
	}
	return svc.Endpoint == sampleCalendarEndpoint || svc.ID == sampleCalendarServiceID
}
