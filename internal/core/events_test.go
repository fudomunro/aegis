package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

type fakeCalendarEventProvider struct {
	events    []kinds.CalendarEvent
	err       error
	truncated bool
}

func (f *fakeCalendarEventProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakeCalendarEventProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeCalendarEventProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeCalendarEventProvider) ListCalendarEvents(context.Context) ([]kinds.CalendarEvent, bool, error) {
	return f.events, f.truncated, f.err
}

func TestHandleLibraryEvents_AggregatesServices(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-calendar", Name: "Google Calendar", Action: "inject"},
		{ID: "outlook-calendar", Name: "Outlook Calendar", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{events: []kinds.CalendarEvent{
			kinds.NewCalendarEvent("google-calendar", "Google Calendar", "g1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false),
			kinds.NewCalendarEvent("google-calendar", "Google Calendar", "g2", "Offsite", "2024-06-10T00:00:00Z", "2024-06-11T00:00:00Z", true),
		}},
		"outlook-calendar": &fakeCalendarEventProvider{events: []kinds.CalendarEvent{
			kinds.NewCalendarEvent("outlook-calendar", "Outlook Calendar", "o1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.CalendarEventList
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
	var standup *kinds.CalendarEvent
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
		"google-calendar": &fakeCalendarEventProvider{events: []kinds.CalendarEvent{
			kinds.NewCalendarEvent("google-calendar", "Google Calendar", "g1", "Standup", "2024-06-01T09:00:00Z", "", false),
		}},
	}
	// Connector fixture already sets Canonical; stamp should keep it true.
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.CalendarEventList
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
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryEvents_DeniedByPermission(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"events": {Default: policy.AccessNone}},
		Services: []Service{{
			ID:     "google-calendar",
			Name:   "Google Calendar",
			Action: "inject",
		}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{events: []kinds.CalendarEvent{
			kinds.NewCalendarEvent("google-calendar", "Google Calendar", "g1", "Standup", "2024-06-01T09:00:00Z", "", false),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryEvents_UnauthorizedService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "google-calendar", Name: "Google Calendar", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-calendar": &fakeCalendarEventProvider{err: kinds.ErrUnauthorized},
	}
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	st := resp.ByService["google-calendar"]
	if st.Status != kinds.LibraryStatusUnauthorized {
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
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["google-calendar"].Status != kinds.LibraryStatusError {
		t.Fatalf("by_service = %+v", resp.ByService)
	}
}

func TestHandleLibraryEvents_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("POST", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
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
		"/v1/library/freebusy",
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
	items := []kinds.CalendarEvent{
		{Sources: []kinds.CalendarEventSource{{Service: "google-calendar"}}},
		{Sources: []kinds.CalendarEventSource{{Service: "other-cal"}}},
	}
	mark, _ := kinds.Stamps(srv, "events", forEachCalendarEventSource)
	mark(items, "google-calendar")
	mark(items, "other-cal")
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
