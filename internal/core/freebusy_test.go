package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
)

func freeBusyTestEvents() []kinds.CalendarEvent {
	full := kinds.NewCalendarEvent("google-calendar", "Google Calendar", "e1", "Doctor appointment", "2026-03-02T10:00:00Z", "2026-03-02T10:30:00Z", false)
	full.Description = "Bring insurance card"
	full.Location = "Clinic"
	full.Attendees = []kinds.Contact{{Name: "Dr. Ada", Emails: []string{"ada@example.com"}}}
	full.EntityID = "e1@google.com"
	cancelled := kinds.NewCalendarEvent("google-calendar", "Google Calendar", "e2", "Cancelled standup", "2026-03-02T15:00:00Z", "2026-03-02T15:15:00Z", false)
	cancelled.Status = "cancelled"
	allday := kinds.NewCalendarEvent("google-calendar", "Google Calendar", "e3", "Offsite", "2026-03-05T00:00:00Z", "2026-03-06T00:00:00Z", true)
	return []kinds.CalendarEvent{full, cancelled, allday}
}

func TestRedactCalendarEventFreeBusy_StripsContent(t *testing.T) {
	e := freeBusyTestEvents()[0]
	got := kinds.RedactCalendarEventFreeBusy(e)
	if got.Title != "" || got.Description != "" || got.Location != "" ||
		got.EntityID != "" || len(got.Attendees) != 0 || len(got.Sources) != 0 {
		t.Fatalf("leaked content: %+v", got)
	}
	if got.StartAt != e.StartAt || got.EndAt != e.EndAt {
		t.Fatalf("timing lost: %+v", got)
	}
}

func TestValidateGrantMap_FreeBusyEventsOnly(t *testing.T) {
	if err := policy.ValidateGrantMap(policy.GrantMap{"events": policy.AccessFreeBusy}); err != nil {
		t.Fatalf("events freebusy: %v", err)
	}
	if err := policy.ValidateGrantMap(policy.GrantMap{"events": "free-busy"}); err != nil {
		t.Fatalf("alias free-busy: %v", err)
	}
	if err := policy.ValidateGrantMap(policy.GrantMap{"tracks": policy.AccessFreeBusy}); err == nil {
		t.Fatal("expected freebusy rejected on tracks")
	}
	if err := policy.ValidateGrantMap(policy.GrantMap{policy.DefaultKey: policy.AccessFreeBusy}); err == nil {
		t.Fatal("expected freebusy rejected on default")
	}
}

func TestPermissions_FreeBusyAllowsReadNotWrite(t *testing.T) {
	p := policy.GrantMap{"events": policy.AccessFreeBusy}
	if !p.AllowsRead("events") {
		t.Fatal("expected GET allowed")
	}
	if p.AllowsWrite("events") {
		t.Fatal("expected write denied")
	}
	if policy.IsFreeBusyAccess(p.Level("events")) != true {
		t.Fatal("expected freebusy level")
	}
}

func TestHandleLibraryFreeBusy_ReturnsBusyOnly(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"events": {Default: policy.AccessFreeBusy}},
		Services:     []Service{{ID: "google-calendar", Name: "Google Calendar", Action: "inject"}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	req := httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/freebusy"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp kinds.FreeBusyList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d body = %s", resp.Total, w.Body.String())
	}
	for _, b := range resp.Busy {
		raw := w.Body.String()
		if strings.Contains(raw, "Doctor") || strings.Contains(raw, "appointment") || strings.Contains(raw, "Ada") {
			t.Fatalf("content leaked: %s", raw)
		}
		_ = b
	}
}

func TestHandleLibraryEvents_FreeBusyRedactsService(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"events": {Default: policy.AccessFreeBusy}},
		Services:     []Service{{ID: "google-calendar", Name: "Google Calendar", Action: "inject"}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, leak := range []string{"Doctor", "appointment", "Clinic", "Ada", "Offsite"} {
		if strings.Contains(body, leak) {
			t.Fatalf("leaked %q in %s", leak, body)
		}
	}
	var resp kinds.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Events) == 0 {
		t.Fatal("expected redacted events, got none")
	}
	for _, e := range resp.Events {
		if e.Title != "" || e.Location != "" || len(e.Attendees) != 0 {
			t.Fatalf("event not redacted: %+v", e)
		}
	}
}

func TestHandleLibraryEvents_FullReadKeepsTitles(t *testing.T) {
	svc := &Service{
		ID:     "google-calendar",
		Name:   "Google Calendar",
		Action: "inject",
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("events"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Doctor appointment") {
		t.Fatalf("expected full title with read: %s", w.Body.String())
	}
}

func TestPermissionNotice_FreeBusy(t *testing.T) {
	p := policy.GrantMap{"events": policy.AccessFreeBusy, "player": policy.AccessNone}
	notice := permissionNotice(p)
	// Deny-by-default map: the notice is an allow list (player is simply
	// outside it), with the freebusy caveat attached.
	for _, want := range []string{"only these kinds are available", "events", "free/busy only", "/v1/library/freebusy"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice missing %q: %s", want, notice)
		}
	}
}

func TestLibraryRouteHandlers_IncludesFreeBusy(t *testing.T) {
	handlers := (&Server{}).libraryRouteHandlers()
	if _, ok := handlers["/v1/library/freebusy"]; !ok {
		t.Fatal("freebusy route not registered")
	}
}

// Ensure freebusy lister still fans out when one provider errors (fail-open).
func TestHandleLibraryFreeBusy_FailOpenPartial(t *testing.T) {
	svc := &Service{
		ID:     "google-calendar",
		Name:   "Google Calendar",
		Action: "inject",
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: []kinds.CalendarEvent{
		kinds.NewCalendarEvent("google-calendar", "Google Calendar", "ok", "Busy", "2026-03-02T10:00:00Z", "2026-03-02T11:00:00Z", false),
	}}

	req := httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/freebusy"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp kinds.FreeBusyList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d", resp.Total)
	}
}

func TestEventsProviderTargets_Collects(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"events": {Default: policy.AccessFreeBusy}},
		Services:     []Service{{ID: "google-calendar", Name: "Google Calendar", Action: "inject"}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}
	targets, denied := kinds.Collect[kinds.CalendarEvent, kinds.CalendarEventProvider](srv,
		httptest.NewRequest(http.MethodGet, "/v1/library/freebusy", nil), "events",
		func(p kinds.CalendarEventProvider) func(context.Context) ([]kinds.CalendarEvent, bool, error) {
			return p.ListCalendarEvents
		})
	if len(targets) != 1 || len(denied) != 0 {
		t.Fatalf("targets = %d denied = %v", len(targets), denied)
	}
	items, _, err := targets[0].List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
}
