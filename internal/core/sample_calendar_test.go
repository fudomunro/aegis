package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"

	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSampleCalendarConnector_LibraryFreeBusy(t *testing.T) {
	svc := &Service{
		ID:          connectors.SampleCalendarServiceID,
		Name:        connectors.SampleCalendarServiceName,
		Action:      "inject",
		Endpoint:    connectors.SampleCalendarEndpoint,
		GrantLimits: policy.Permissions{"events": policy.AccessRead},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.connectors[connectors.SampleCalendarServiceID] = connectors.NewSampleCalendarConnector()

	req := httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.routeHandler("/v1/library/freebusy"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp kinds.FreeBusyList
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
