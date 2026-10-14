package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestGoogleCalendarConnector_ListCalendarEvents_MockAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/users/me/calendarList":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"id": "primary", "summary": "Work", "primary": true, "accessRole": "owner"},
					{"id": "family@example.com", "summary": "Family", "accessRole": "reader"},
					{"id": "busy@example.com", "summary": "Busy only", "accessRole": "freeBusyReader"},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/calendars/") && strings.HasSuffix(r.URL.Path, "/events"):
			q := r.URL.Query()
			if q.Get("singleEvents") != "true" || q.Get("timeMin") == "" || q.Get("timeMax") == "" {
				t.Errorf("missing event list params: %s", r.URL.RawQuery)
			}
			cal := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/calendars/"), "/events")
			if cal == "primary" {
				json.NewEncoder(w).Encode(map[string]any{
					"items": []map[string]any{
						{
							"id":       "ev1",
							"iCalUID":  "standup@google.com",
							"status":   "confirmed",
							"summary":  "Standup",
							"location": "Room 1",
							"start":    map[string]any{"dateTime": "2024-06-01T09:00:00Z"},
							"end":      map[string]any{"dateTime": "2024-06-01T09:30:00Z"},
							"organizer": map[string]any{
								"email":       "ada@example.com",
								"displayName": "Ada Lovelace",
							},
							"attendees": []map[string]any{
								{"email": "grace@example.com", "displayName": "Grace", "responseStatus": "accepted"},
							},
							"htmlLink": "https://calendar.google.com/event?eid=ev1",
						},
						{
							"id":      "ev2",
							"iCalUID": "offsite@google.com",
							"status":  "confirmed",
							"summary": "Offsite",
							"start":   map[string]any{"date": "2024-06-10"},
							"end":     map[string]any{"date": "2024-06-11"},
						},
					},
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID:       connectors.GoogleCalendarServiceID,
		Name:     connectors.GoogleCalendarServiceName,
		Action:   "inject",
		Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleCalendarServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGoogleCalendarConnector(srv)
	events, truncated, err := conn.ListCalendarEvents(context.Background())
	if err != nil {
		t.Fatalf("ListCalendarEvents: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (freeBusyReader skipped)", len(events))
	}
	ev := events[0]
	if ev.Title != "Standup" || ev.Location != "Room 1" || ev.AllDay {
		t.Fatalf("standup = %+v", ev)
	}
	if ev.StartAt != "2024-06-01T09:00:00Z" || ev.EndAt != "2024-06-01T09:30:00Z" {
		t.Fatalf("times = %q..%q", ev.StartAt, ev.EndAt)
	}
	if ev.EntityID != "standup@google.com" {
		t.Fatalf("entity = %q", ev.EntityID)
	}
	if len(ev.Sources) != 1 || ev.Sources[0].Service != connectors.GoogleCalendarServiceID {
		t.Fatalf("sources = %+v", ev.Sources)
	}
	if ev.Sources[0].ID != "ev1" || ev.Sources[0].URL == "" {
		t.Fatalf("source = %+v", ev.Sources[0])
	}
	if ev.Organizer == nil || len(ev.Organizer.Emails) != 1 || ev.Organizer.Emails[0] != "ada@example.com" {
		t.Fatalf("organizer = %+v", ev.Organizer)
	}
	if len(ev.Attendees) != 1 || ev.Attendees[0].Name != "Grace" {
		t.Fatalf("attendees = %+v", ev.Attendees)
	}
	offsite := events[1]
	if !offsite.AllDay || offsite.Title != "Offsite" || !strings.HasPrefix(offsite.StartAt, "2024-06-10") {
		t.Fatalf("offsite = %+v", offsite)
	}
}

func TestGoogleCalendarConnector_FreeBusyRedactsProxyList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/users/me/calendarList":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"id": "primary", "summary": "Work", "primary": true, "accessRole": "owner"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/events"):
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{
						"id":       "ev1",
						"summary":  "Therapy",
						"location": "Office",
						"start":    map[string]any{"dateTime": "2024-06-01T09:00:00Z"},
						"end":      map[string]any{"dateTime": "2024-06-01T09:30:00Z"},
						"attendees": []map[string]any{
							{"email": "doc@example.com", "displayName": "Doc"},
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID:          connectors.GoogleCalendarServiceID,
		Name:        connectors.GoogleCalendarServiceName,
		Action:      "inject",
		Endpoint:    upstream.URL,
		Permissions: types.Permissions{"events": types.AccessFreeBusy},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleCalendarServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleCalendarConnector(srv)
	if !conn.EventsFreeBusyOnly() {
		t.Fatal("expected freebusy-only")
	}
	req := httptest.NewRequest("GET", "/v1/services/google-calendar/me/events", nil)
	w := httptest.NewRecorder()
	conn.HandleListEvents(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, leak := range []string{"Therapy", "Office", "Doc", "doc@example.com"} {
		if strings.Contains(body, leak) {
			t.Fatalf("leaked %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "2024-06-01T09:00:00Z") {
		t.Fatalf("missing busy start: %s", body)
	}
}

func TestGoogleCalendarConnector_ListCalendarEvents_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: connectors.GoogleCalendarServiceID, Name: connectors.GoogleCalendarServiceName, Action: "inject",
	}}}, "https://aegis.example.com")
	conn := connectors.NewGoogleCalendarConnector(srv)
	_, _, err := conn.ListCalendarEvents(context.Background())
	if !errors.Is(err, types.ErrUnauthorized) {
		t.Fatalf("err = %v, want types.ErrUnauthorized", err)
	}
}

func TestGoogleCalendarConnector_ListCalendarEvents_PaginatesAndTruncates(t *testing.T) {
	var eventPages int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/users/me/calendarList" {
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "primary", "summary": "Work", "primary": true, "accessRole": "owner"}},
			})
			return
		}
		eventPages++
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":      fmt.Sprintf("e%d", eventPages),
					"iCalUID": fmt.Sprintf("uid%d@google.com", eventPages),
					"summary": "Meeting",
					"start":   map[string]any{"dateTime": "2024-06-01T09:00:00Z"},
					"end":     map[string]any{"dateTime": "2024-06-01T09:30:00Z"},
				},
			},
			"nextPageToken": "more",
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: connectors.GoogleCalendarServiceID, Name: connectors.GoogleCalendarServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleCalendarServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGoogleCalendarConnector(srv)
	events, truncated, err := conn.ListCalendarEvents(context.Background())
	if err != nil {
		t.Fatalf("ListCalendarEvents: %v", err)
	}
	if !truncated {
		t.Fatal("expected truncated after page cap")
	}
	if len(events) != connectors.LibraryPageCap {
		t.Fatalf("got %d events, want %d", len(events), connectors.LibraryPageCap)
	}
}

func TestGoogleCalendarConnector_HandleProxy_ListEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users/me/calendarList":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "primary", "summary": "Work", "primary": true}},
			})
		case "/calendars/primary/events":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{
						"id":      "ev9",
						"summary": "Focus",
						"start":   map[string]any{"dateTime": "2024-06-03T10:00:00Z"},
						"end":     map[string]any{"dateTime": "2024-06-03T11:00:00Z"},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: connectors.GoogleCalendarServiceID, Name: connectors.GoogleCalendarServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleCalendarServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleCalendarConnector(srv)

	req := httptest.NewRequest("GET", "/me/events", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/events")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.CalendarEventList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || resp.Events[0].Title != "Focus" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestGoogleCalendarConnector_HandleProxy_ListCalendars(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/me/calendarList" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "primary", "summary": "Work", "primary": true, "accessRole": "owner"},
				{"id": "family@example.com", "summary": "Family", "accessRole": "reader"},
			},
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: connectors.GoogleCalendarServiceID, Name: connectors.GoogleCalendarServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleCalendarServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleCalendarConnector(srv)

	req := httptest.NewRequest("GET", "/me/calendars", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/calendars")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Total     int              `json:"total"`
		Calendars []map[string]any `json:"calendars"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d body=%s", resp.Total, w.Body.String())
	}
}

func TestGoogleCalendarConnector_HandleProxy_GetEventByID(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "abc",
			"iCalUID": "abc@google.com",
			"summary": "1:1",
			"start":   map[string]any{"dateTime": "2024-06-04T15:00:00Z"},
			"end":     map[string]any{"dateTime": "2024-06-04T15:30:00Z"},
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: connectors.GoogleCalendarServiceID, Name: connectors.GoogleCalendarServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleCalendarServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleCalendarConnector(srv)

	req := httptest.NewRequest("GET", "/events/abc", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/events/abc")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if gotPath != "/calendars/primary/events/abc" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	var ev types.CalendarEvent
	if err := json.Unmarshal(w.Body.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Title != "1:1" || ev.EntityID != "abc@google.com" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestGoogleCalendarConnector_HandleProxy_GetEventFromCalendarPath(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "xyz",
			"summary": "Deep link",
			"start":   map[string]any{"date": "2024-07-01"},
			"end":     map[string]any{"date": "2024-07-02"},
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: connectors.GoogleCalendarServiceID, Name: connectors.GoogleCalendarServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleCalendarServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleCalendarConnector(srv)

	req := httptest.NewRequest("GET", "/calendars/family%40example.com/events/xyz", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/calendars/family%40example.com/events/xyz")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(gotPath, "/calendars/family@example.com/events/xyz") {
		t.Fatalf("upstream path = %q", gotPath)
	}
	var ev types.CalendarEvent
	if err := json.Unmarshal(w.Body.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ev.AllDay || ev.CalendarID != "family@example.com" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestGoogleCalendarConnector_HandleProxy_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: connectors.GoogleCalendarServiceID, Name: connectors.GoogleCalendarServiceName, Action: "inject",
	}}}, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleCalendarServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleCalendarConnector(srv)

	req := httptest.NewRequest("POST", "/me/events", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/events")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestGoogleCalendarConnector_HandleProxy_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: connectors.GoogleCalendarServiceID, Name: connectors.GoogleCalendarServiceName, Action: "inject",
	}}}, "https://aegis.example.com")
	conn := connectors.NewGoogleCalendarConnector(srv)
	req := httptest.NewRequest("GET", "/me/events", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/events")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestGoogleCalendarConnector_SystemInstructions_MentionLibrary(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewGoogleCalendarConnector(srv)
	instr := conn.SystemInstructions("https://aegis.example.com")
	for _, want := range []string{
		"/v1/library/events",
		"/me/events",
		"/me/calendars",
		"Google Calendar",
		"Aegis authenticates you",
	} {
		if !strings.Contains(instr, want) {
			t.Fatalf("missing %q in instructions:\n%s", want, instr)
		}
	}
}

func TestGoogleCalendarConnector_Capabilities_ReadOnlyEvents(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewGoogleCalendarConnector(srv)
	caps := conn.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "events" {
		t.Fatalf("caps = %+v", caps)
	}
	if types.DataTypeHasAgentWriteOp(caps, "events") {
		t.Fatal("calendar events must not advertise agent write ops in this phase")
	}
	if !types.DataTypeHasWriteOp(caps, "events") {
		// Still no mutating ops at all — proxy write is blocked by connector capability.
	}
	for _, op := range caps.DataTypes[0].Operations {
		if types.IsMutatingMethod(op.Method) {
			t.Fatalf("unexpected mutating op: %+v", op)
		}
	}
}

func TestCalendarEventPathParts(t *testing.T) {
	cases := []struct {
		path    string
		cal, ev string
		ok      bool
	}{
		{"/calendars/primary/events", "primary", "", true},
		{"/calendars/primary/events/abc", "primary", "abc", true},
		{"/calendars/family@example.com/events/xyz", "family@example.com", "xyz", true},
		{"/me/calendars/primary/events", "primary", "", true},
		{"/me/calendars/primary/events/abc", "primary", "abc", true},
		{"/events/abc", "", "", false},
		{"/me/events", "", "", false},
		{"/calendars/primary", "", "", false},
	}
	for _, tc := range cases {
		cal, ev, ok := connectors.CalendarEventPathParts(tc.path)
		if ok != tc.ok || cal != tc.cal || ev != tc.ev {
			t.Errorf("connectors.CalendarEventPathParts(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.path, cal, ev, ok, tc.cal, tc.ev, tc.ok)
		}
	}
}

func TestGoogleCalendarTimeToRFC3339(t *testing.T) {
	s, allDay := connectors.GoogleCalendarTimeToRFC3339(connectors.GoogleCalendarEventTime{DateTime: "2024-06-01T09:00:00-07:00"})
	if s != "2024-06-01T16:00:00Z" || allDay {
		t.Fatalf("dateTime = %q allDay=%v", s, allDay)
	}
	s, allDay = connectors.GoogleCalendarTimeToRFC3339(connectors.GoogleCalendarEventTime{Date: "2024-06-10"})
	if s != "2024-06-10T00:00:00Z" || !allDay {
		t.Fatalf("date = %q allDay=%v", s, allDay)
	}
	s, allDay = connectors.GoogleCalendarTimeToRFC3339(connectors.GoogleCalendarEventTime{})
	if s != "" || allDay {
		t.Fatalf("empty = %q allDay=%v", s, allDay)
	}
}
