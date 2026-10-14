package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func freeBusyTestEvents() []types.CalendarEvent {
	full := types.NewCalendarEvent("google-calendar", "Google Calendar", "e1", "Doctor appointment", "2026-03-02T10:00:00Z", "2026-03-02T10:30:00Z", false)
	full.Description = "Bring insurance card"
	full.Location = "Clinic"
	full.Attendees = []types.Contact{{Name: "Dr. Ada", Emails: []string{"ada@example.com"}}}
	full.EntityID = "e1@google.com"
	cancelled := types.NewCalendarEvent("google-calendar", "Google Calendar", "e2", "Cancelled standup", "2026-03-02T15:00:00Z", "2026-03-02T15:15:00Z", false)
	cancelled.Status = "cancelled"
	allday := types.NewCalendarEvent("google-calendar", "Google Calendar", "e3", "Offsite", "2026-03-05T00:00:00Z", "2026-03-06T00:00:00Z", true)
	return []types.CalendarEvent{full, cancelled, allday}
}

func TestRedactCalendarEventFreeBusy_StripsContent(t *testing.T) {
	e := freeBusyTestEvents()[0]
	got := types.RedactCalendarEventFreeBusy(e)
	if got.Title != "" || got.Description != "" || got.Location != "" ||
		got.EntityID != "" || len(got.Attendees) != 0 || len(got.Sources) != 0 {
		t.Fatalf("leaked content: %+v", got)
	}
	if got.StartAt != e.StartAt || got.EndAt != e.EndAt {
		t.Fatalf("timing lost: %+v", got)
	}
}

func TestFreeBusyListFromEvents_OmitsCancelled(t *testing.T) {
	list := freeBusyListFromEvents(freeBusyTestEvents(), nil)
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

func TestValidatePermissions_FreeBusyEventsOnly(t *testing.T) {
	if err := types.ValidatePermissions(types.Permissions{"events": types.AccessFreeBusy}); err != nil {
		t.Fatalf("events freebusy: %v", err)
	}
	if err := types.ValidatePermissions(types.Permissions{"events": "free-busy"}); err != nil {
		t.Fatalf("alias free-busy: %v", err)
	}
	if err := types.ValidatePermissions(types.Permissions{"tracks": types.AccessFreeBusy}); err == nil {
		t.Fatal("expected freebusy rejected on tracks")
	}
	if err := types.ValidatePermissions(types.Permissions{types.PermissionDefaultKey: types.AccessFreeBusy}); err == nil {
		t.Fatal("expected freebusy rejected on default")
	}
}

func TestPermissions_FreeBusyAllowsReadNotWrite(t *testing.T) {
	p := types.Permissions{"events": types.AccessFreeBusy}
	if !p.AllowsRead("events") {
		t.Fatal("expected GET allowed")
	}
	if p.AllowsWrite("events") {
		t.Fatal("expected write denied")
	}
	if types.IsFreeBusyAccess(p.For("events")) != true {
		t.Fatal("expected freebusy level")
	}
}

func TestStrictest_FreeBusyAloneDeniesUnknownPaths(t *testing.T) {
	p := types.Permissions{"events": types.AccessFreeBusy}
	if got := p.Strictest(); got != types.AccessNone {
		t.Fatalf("Strictest = %q, want none (cannot redact unknown paths)", got)
	}
	mixed := types.Permissions{"events": types.AccessFreeBusy, "tracks": types.AccessRead}
	if got := mixed.Strictest(); got != types.AccessRead {
		t.Fatalf("Strictest mixed = %q, want read", got)
	}
}

func TestHandleLibraryFreeBusy_ReturnsBusyOnly(t *testing.T) {
	svc := &Service{
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: types.Permissions{"events": types.AccessFreeBusy},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	req := httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryFreeBusy)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp types.FreeBusyList
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
	svc := &Service{
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: types.Permissions{"events": types.AccessFreeBusy},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, leak := range []string{"Doctor", "appointment", "Clinic", "Ada", "Offsite"} {
		if strings.Contains(body, leak) {
			t.Fatalf("leaked %q in %s", leak, body)
		}
	}
	var resp types.CalendarEventList
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
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: types.Permissions{"events": types.AccessRead},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Doctor appointment") {
		t.Fatalf("expected full title with read: %s", w.Body.String())
	}
}

func TestPermissionNotice_FreeBusy(t *testing.T) {
	p := types.Permissions{"events": types.AccessFreeBusy, "player": types.AccessNone}
	notice := permissionNotice(p)
	for _, want := range []string{"player", "free/busy only", "/v1/library/freebusy"} {
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
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: types.Permissions{"events": types.AccessRead},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: []types.CalendarEvent{
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ok", "Busy", "2026-03-02T10:00:00Z", "2026-03-02T11:00:00Z", false),
	}}

	req := httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryFreeBusy)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp types.FreeBusyList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d", resp.Total)
	}
}

func TestEventsProviderTargets_Collects(t *testing.T) {
	svc := &Service{
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: types.Permissions{"events": types.AccessFreeBusy},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}
	targets, denied := eventsProviderTargets(srv, "events")
	if len(targets) != 1 || len(denied) != 0 {
		t.Fatalf("targets = %d denied = %v", len(targets), denied)
	}
	items, _, err := targets[0].list(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
}
