package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSampleCalendarConnector_Capabilities(t *testing.T) {
	caps := NewSampleCalendarConnector().Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "events" {
		t.Fatalf("caps = %+v", caps)
	}
	if len(caps.DataTypes[0].Operations) != 1 || caps.DataTypes[0].Operations[0].Path != "/me/events" {
		t.Fatalf("ops = %+v", caps.DataTypes[0].Operations)
	}
}

func TestSampleCalendarConnector_SystemInstructions(t *testing.T) {
	instr := NewSampleCalendarConnector().SystemInstructions("https://aegis.example.com")
	if !strings.Contains(instr, "library") || !strings.Contains(instr, "synthetic") {
		t.Fatalf("instr = %q", instr)
	}
}

func TestSampleCalendarConnector_ListCalendarEvents(t *testing.T) {
	events, truncated, err := NewSampleCalendarConnector().ListCalendarEvents(context.Background())
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if len(events) < 4 {
		t.Fatalf("events = %d", len(events))
	}
}

func TestSampleCalendarConnector_HandleProxy(t *testing.T) {
	c := NewSampleCalendarConnector()
	w := httptest.NewRecorder()
	c.HandleProxy(w, httptest.NewRequest("GET", "/v1/services/sample-calendar/me/events", nil), "/me/events")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var list struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total < 4 {
		t.Fatalf("total = %d body = %s", list.Total, w.Body.String())
	}

	w = httptest.NewRecorder()
	c.HandleProxy(w, httptest.NewRequest("POST", "/v1/services/sample-calendar/me/events", nil), "/me/events")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
}

func TestIsSampleCalendarService(t *testing.T) {
	if IsSampleCalendarService(nil) {
		t.Fatal("nil must be false")
	}
	if !IsSampleCalendarService(&ServiceInfo{ID: SampleCalendarServiceID}) {
		t.Fatal("id match")
	}
	if !IsSampleCalendarService(&ServiceInfo{ID: "other", Endpoint: SampleCalendarEndpoint}) {
		t.Fatal("endpoint match")
	}
	if IsSampleCalendarService(&ServiceInfo{ID: "spotify", Endpoint: "https://api.spotify.com/v1"}) {
		t.Fatal("unrelated service must be false")
	}
}
