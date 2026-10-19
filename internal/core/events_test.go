package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

type fakeCalendarEventProvider struct {
	events    []types.CalendarEvent
	err       error
	truncated bool
}

func (f *fakeCalendarEventProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakeCalendarEventProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeCalendarEventProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeCalendarEventProvider) ListCalendarEvents(context.Context) ([]types.CalendarEvent, bool, error) {
	return f.events, f.truncated, f.err
}

func TestCalendarEventIdentityKey_PrefersICalUID(t *testing.T) {
	e := types.NewCalendarEvent("google-calendar", "Google Calendar", "id1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	e.EntityID = "uid@google.com"
	if got := calendarEventIdentityKey(e); got != "ical:uid@google.com" {
		t.Fatalf("key = %q", got)
	}
}

func TestCalendarEventIdentityKey_TitleStartFallback(t *testing.T) {
	e := types.NewCalendarEvent("google-calendar", "Google Calendar", "id1", "  Standup  ", "2024-06-01T09:00:00+00:00", "2024-06-01T09:30:00Z", false)
	if got := calendarEventIdentityKey(e); got != "title:standup|start:2024-06-01T09:00:00Z|allday:0" {
		t.Fatalf("key = %q", got)
	}
}

func TestMergeCalendarEvents_ByICalUIDAndTitleStart(t *testing.T) {
	a := types.NewCalendarEvent("google-calendar", "Google Calendar", "a1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	a.EntityID = "shared@ical"
	b := types.NewCalendarEvent("outlook-calendar", "Outlook Calendar", "b1", "Different title", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	b.EntityID = "shared@ical"
	c := types.NewCalendarEvent("google-calendar", "Google Calendar", "c1", "Planning", "2024-06-02T14:00:00Z", "2024-06-02T15:00:00Z", false)
	d := types.NewCalendarEvent("outlook-calendar", "Outlook Calendar", "d1", "Planning", "2024-06-02T14:00:00+00:00", "2024-06-02T15:00:00Z", false)

	merged := MergeCalendarEvents([]types.CalendarEvent{a, b, c, d})
	if len(merged) != 2 {
		t.Fatalf("merged = %d events: %+v", len(merged), merged)
	}
	// Sorted by start: Standup first, Planning second.
	if merged[0].Title != "Standup" || len(merged[0].Sources) != 2 || merged[0].Match != "ical_uid" {
		t.Fatalf("first = %+v", merged[0])
	}
	if merged[1].Title != "Planning" || len(merged[1].Sources) != 2 || merged[1].Match != "title_start" {
		t.Fatalf("second = %+v", merged[1])
	}
}

func TestMergeCalendarEvents_DistinctWhenDifferentStart(t *testing.T) {
	a := types.NewCalendarEvent("google-calendar", "Google Calendar", "a", "Meeting", "2024-06-01T09:00:00Z", "", false)
	b := types.NewCalendarEvent("google-calendar", "Google Calendar", "b", "Meeting", "2024-06-01T10:00:00Z", "", false)
	merged := MergeCalendarEvents([]types.CalendarEvent{a, b})
	if len(merged) != 2 {
		t.Fatalf("merged = %d, want 2", len(merged))
	}
}

func TestMergeCalendarEvents_MergesAttendeesAsContacts(t *testing.T) {
	a := types.NewCalendarEvent("google-calendar", "Google Calendar", "a", "Allhands", "2024-06-01T09:00:00Z", "", false)
	a.EntityID = "allhands@ical"
	a.Attendees = []types.Contact{{Name: "Ada", Emails: []string{"Ada@Example.com"}}}
	b := types.NewCalendarEvent("outlook-calendar", "Outlook Calendar", "b", "Allhands", "2024-06-01T09:00:00Z", "", false)
	b.EntityID = "allhands@ical"
	b.Attendees = []types.Contact{{Name: "Ada L", Emails: []string{"ada@example.com"}, Phones: []string{"+1 555 0100"}}}
	org := types.Contact{Name: "Grace", Emails: []string{"grace@example.com"}}
	b.Organizer = &org

	merged := MergeCalendarEvents([]types.CalendarEvent{a, b})
	if len(merged) != 1 {
		t.Fatalf("merged = %+v", merged)
	}
	if len(merged[0].Attendees) != 1 {
		t.Fatalf("attendees = %+v", merged[0].Attendees)
	}
	if len(merged[0].Attendees[0].Emails) != 1 || merged[0].Attendees[0].Emails[0] != "ada@example.com" {
		t.Fatalf("merged attendee = %+v", merged[0].Attendees[0])
	}
	if merged[0].Organizer == nil || merged[0].Organizer.Name != "Grace" {
		t.Fatalf("organizer = %+v", merged[0].Organizer)
	}
}

func TestHandleLibraryEvents_AggregatesServices(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-calendar", Name: "Google Calendar", Action: "inject"},
		{ID: "outlook-calendar", Name: "Outlook Calendar", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{events: []types.CalendarEvent{
			types.NewCalendarEvent("google-calendar", "Google Calendar", "g1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false),
			types.NewCalendarEvent("google-calendar", "Google Calendar", "g2", "Offsite", "2024-06-10T00:00:00Z", "2024-06-11T00:00:00Z", true),
		}},
		"outlook-calendar": &fakeCalendarEventProvider{events: []types.CalendarEvent{
			types.NewCalendarEvent("outlook-calendar", "Outlook Calendar", "o1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d events=%+v", resp.Total, resp.Events)
	}
	if resp.ByService["google-calendar"].Count != 2 || resp.ByService["outlook-calendar"].Count != 1 {
		t.Fatalf("by_service = %+v", resp.ByService)
	}
	// Standup should merge across services.
	var standup *types.CalendarEvent
	for i := range resp.Events {
		if resp.Events[i].Title == "Standup" {
			standup = &resp.Events[i]
		}
	}
	if standup == nil || len(standup.Sources) != 2 || standup.Match != "title_start" {
		t.Fatalf("standup = %+v", standup)
	}
}

func TestHandleLibraryEvents_CanonicalStamp(t *testing.T) {
	cfg := &Config{Services: []Service{
		{
			ID:           "google-calendar",
			Name:         "Google Calendar",
			Action:       "inject",
			CanonicalFor: []string{"events"},
		},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{events: []types.CalendarEvent{
			types.NewCalendarEvent("google-calendar", "Google Calendar", "g1", "Standup", "2024-06-01T09:00:00Z", "", false),
		}},
	}
	// Connector fixture already sets Canonical; stamp should keep it true.
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Events) != 1 || len(resp.Events[0].Sources) != 1 || !resp.Events[0].Sources[0].Canonical {
		t.Fatalf("events = %+v", resp.Events)
	}
}

func TestHandleLibraryEvents_NoProviders(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryEvents_DeniedByPermission(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		GrantLimits: types.Permissions{"events": types.AccessNone},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{events: []types.CalendarEvent{
			types.NewCalendarEvent("google-calendar", "Google Calendar", "g1", "Standup", "2024-06-01T09:00:00Z", "", false),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryEvents_UnauthorizedService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "google-calendar", Name: "Google Calendar", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{err: types.ErrUnauthorized},
	}
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	st := resp.ByService["google-calendar"]
	if st.Status != types.LibraryStatusUnauthorized {
		t.Fatalf("status = %+v", st)
	}
}

func TestHandleLibraryEvents_ErrorService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "google-calendar", Name: "Google Calendar", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{err: errors.New("boom")},
	}
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["google-calendar"].Status != types.LibraryStatusError {
		t.Fatalf("by_service = %+v", resp.ByService)
	}
}

func TestHandleLibraryEvents_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("POST", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestLibraryKindEvents_Registered(t *testing.T) {
	k := LibraryKindByID("events")
	if k == nil {
		t.Fatal("events library kind not registered")
	}
	if k.Path != "/v1/library/events" || k.PermType != "events" {
		t.Fatalf("kind = %+v", k)
	}
	if !k.Mentions("what is on my calendar?") {
		t.Fatal("expected calendar keyword match")
	}
	handlers := (&Server{}).libraryRouteHandlers()
	if _, ok := handlers["/v1/library/events"]; !ok {
		t.Fatal("events route not registered")
	}
}

func TestLibraryEventsInstructions_MentionEndpoint(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	instr := srv.libraryInstructionsFor("events")
	for _, want := range []string{
		"/v1/library/events",
		"calendar events",
		"canonical",
	} {
		if !strings.Contains(instr, want) {
			t.Fatalf("missing %q in instructions:\n%s", want, instr)
		}
	}
}

func TestStampCalendarEventCanonical_OnlyWhenConfigured(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-calendar", Name: "Google Calendar", Action: "inject", CanonicalFor: []string{"events"}},
		{ID: "other-cal", Name: "Other", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	items := []types.CalendarEvent{
		{Sources: []types.CalendarEventSource{{Service: "google-calendar"}}},
		{Sources: []types.CalendarEventSource{{Service: "other-cal"}}},
	}
	srv.stampCalendarEventCanonical(items, "google-calendar")
	srv.stampCalendarEventCanonical(items, "other-cal")
	if !items[0].Sources[0].Canonical {
		t.Fatal("google-calendar should be canonical when configured")
	}
	if items[1].Sources[0].Canonical {
		t.Fatal("other-cal should not be canonical without config")
	}
	if items[0].CanonicalService() != "google-calendar" {
		t.Fatalf("CanonicalService = %q", items[0].CanonicalService())
	}
}

func TestGoogleCalendarDefaultWindow_CoversPastAndFuture(t *testing.T) {
	minS, maxS := connectors.GoogleCalendarDefaultWindow()
	minT, err := time.Parse(time.RFC3339, minS)
	if err != nil {
		t.Fatalf("timeMin parse: %v", err)
	}
	maxT, err := time.Parse(time.RFC3339, maxS)
	if err != nil {
		t.Fatalf("timeMax parse: %v", err)
	}
	if !minT.Before(time.Now().UTC()) {
		t.Fatalf("timeMin %v should be in the past", minT)
	}
	if !maxT.After(time.Now().UTC()) {
		t.Fatalf("timeMax %v should be in the future", maxT)
	}
}
