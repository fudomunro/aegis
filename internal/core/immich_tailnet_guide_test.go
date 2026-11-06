package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Immich VIP-Service wizard (issue #124) still lives in core
// (tailscale_admin.go) and keeps its own routes until Phase 3 of issue #265
// folds it into the onboarding seam. These pin its copyable walkthrough.

func TestImmichTailnetGuide_VIPService(t *testing.T) {
	g := immichTailnetGuide("https://aegis.example.ts.net", "")
	if g == nil {
		t.Fatal("guide is nil")
	}
	if g.ServiceName != "svc:immich" {
		t.Fatalf("service name = %q", g.ServiceName)
	}
	if g.TailnetDNS != "example.ts.net" {
		t.Fatalf("tailnet dns = %q", g.TailnetDNS)
	}
	if g.SuggestedEndpoint != "https://immich.example.ts.net/api" {
		t.Fatalf("suggested endpoint = %q", g.SuggestedEndpoint)
	}
	if !strings.Contains(g.Why, "VIP") || strings.Contains(strings.ToLower(g.Why), "funnel") && !strings.Contains(g.Why, "never Funnel") {
		// Why must mention VIP and the no-Funnel rule.
		if !strings.Contains(g.Why, "never Funnel") {
			t.Fatalf("why = %q", g.Why)
		}
	}
	if len(g.Requirements) < 3 {
		t.Fatalf("requirements = %v", g.Requirements)
	}
	joined := ""
	for _, st := range g.Steps {
		if st.ID == "" || st.Title == "" {
			t.Fatalf("bad step %+v", st)
		}
		joined += st.Title + "\n" + st.Command + "\n"
	}
	for _, want := range []string{
		"tagOwners",
		"svc:immich",
		"/services/svc:immich",
		"tailscale serve --service svc:immich",
		"/device/$DEVICE_ID/tags",
		"/approved",
		"https://immich.example.ts.net/api",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("steps missing %q in:\n%s", want, joined)
		}
	}
	if g.Commands == "" || !strings.Contains(g.Commands, "Create the VIP Service") {
		t.Fatalf("commands block incomplete: %q", g.Commands)
	}
	// Configured endpoint wins as the connect target once set.
	g2 := immichTailnetGuide("https://aegis.example.ts.net", "http://photos.home.ts.net:2283/api")
	if g2.SuggestedEndpoint != "http://photos.home.ts.net:2283/api" {
		t.Fatalf("configured endpoint ignored: %q", g2.SuggestedEndpoint)
	}
}

func TestImmichTailnetStatus_IncludesGuide(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.ts.net")
	w := httptest.NewRecorder()
	srv.handleImmichTailnetRoutes(w, httptest.NewRequest(http.MethodGet, "/v1/admin/immich/tailnet/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var status ImmichTailnetStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Tailnet == nil {
		t.Fatal("expected tailnet guide")
	}
	if status.Tailnet.ServiceName != "svc:immich" {
		t.Fatalf("service = %q", status.Tailnet.ServiceName)
	}
}
