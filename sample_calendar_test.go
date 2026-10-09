package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSampleCalendarEvents_NearTermBusy(t *testing.T) {
	events := sampleCalendarEvents()
	if len(events) < 4 {
		t.Fatalf("events = %d", len(events))
	}
	list := freeBusyListFromEvents(events, nil)
	if list.Total < 3 {
		t.Fatalf("busy = %d (cancelled should drop)", list.Total)
	}
	for _, b := range list.Busy {
		if b.StartAt == "" {
			t.Fatalf("empty block: %+v", b)
		}
	}
}

func TestSampleCalendarConnector_LibraryFreeBusy(t *testing.T) {
	svc := &Service{
		ID:          sampleCalendarServiceID,
		Name:        sampleCalendarServiceName,
		Action:      "inject",
		Endpoint:    sampleCalendarEndpoint,
		Permissions: Permissions{"events": AccessRead},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors[sampleCalendarServiceID] = NewSampleCalendarConnector()

	req := httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryFreeBusy)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp FreeBusyList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total < 3 {
		t.Fatalf("busy total = %d body = %s", resp.Total, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "kickoff") || strings.Contains(w.Body.String(), "Project") {
		t.Fatalf("freebusy leaked titles: %s", w.Body.String())
	}
}
