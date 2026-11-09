package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

// vipTestHost wires a testHost against the mock Tailscale API and an offline
// Immich /server/ping so no probe ever dials a real VIP name.
func vipTestHost(t *testing.T, mock *mockTailscaleAdmin) (*testHost, func()) {
	t.Helper()
	h := startMockTailscale(t, mock)
	immich := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "ping") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pong":true,"immich":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	h.setService(ImmichServiceID, immich.URL)
	h.services[ImmichServiceID].Headers = map[string]string{"X-API-Key": "test"}
	h.secrets[shared.SetupSecretTailscaleAPIKey] = "tskey-api-test"
	h.settings[shared.SetupFlowTailscale] = map[string]string{
		"host_device_id": "n1",
		"host_tag":       immichDefaultHostTag,
	}
	return h, immich.Close
}

func vipState(t *testing.T, h *testHost) shared.OnboardingGuideState {
	t.Helper()
	state := NewImmichConnector(h).immichTailnetState(context.Background())
	return state
}

func stepByID(t *testing.T, state shared.OnboardingGuideState, id string) shared.OnboardingGuideStepState {
	t.Helper()
	for _, st := range state.Steps {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("step %q missing from %+v", id, state.Steps)
	return shared.OnboardingGuideStepState{}
}

// TestImmichTailnetGuide pins the copyable VIP walkthrough (issue #124).
func TestImmichTailnetGuide(t *testing.T) {
	g := immichTailnetGuide("https://aegis.example.ts.net", "")
	if g.Title != "Friendly tailnet URL (VIP Service)" {
		t.Fatalf("title = %q", g.Title)
	}
	if !strings.Contains(g.Body, "VIP Service") || !strings.Contains(g.Body, "never Funnel") {
		t.Fatalf("body = %q", g.Body)
	}
	if len(g.Requirements) < 3 {
		t.Fatalf("requirements = %v", g.Requirements)
	}
	if len(g.Steps) != 6 {
		t.Fatalf("steps = %d", len(g.Steps))
	}
	joined := g.Command
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
	if !strings.Contains(g.Command, "Create the VIP Service") {
		t.Fatalf("commands block incomplete: %q", g.Command)
	}
	// A configured endpoint wins as the connect target once set.
	g2 := immichTailnetGuide("https://aegis.example.ts.net", "http://photos.home.ts.net:2283/api")
	last := g2.Steps[len(g2.Steps)-1]
	if !strings.Contains(last.Command, "http://photos.home.ts.net:2283/api") {
		t.Fatalf("configured endpoint ignored: %q", last.Command)
	}
}

// TestImmichTailnetSteps_CheckOnlyOffersFixWhereNeeded pins the live check
// semantics: only broken steps are marked as needing a fix.
func TestImmichTailnetSteps_CheckOnlyOffersFixWhereNeeded(t *testing.T) {
	mock := newMockTailscaleAdmin()
	mock.policy.TagOwners = map[string][]string{"tag:immich-host": {"autogroup:admin"}}
	mock.devices = []TailscaleDevice{
		{ID: "n1", Hostname: "photos-box", Tags: []string{"tag:servers"}},
	}
	h, done := vipTestHost(t, mock)
	defer done()
	c := NewImmichConnector(h)

	res, updates := c.immichTailnetApply(context.Background(),
		[]string{"create_service", "tag_host", "approve", "acl_tag", "serve"}, "n1", immichDefaultHostTag)
	if res.Summary() == "" {
		t.Fatalf("expected a summary, got %+v", res)
	}

	applied := map[string]bool{}
	for _, id := range res.Applied {
		applied[id] = true
	}
	if !applied["create_service"] || !applied["tag_host"] {
		t.Fatalf("applied = %v details=%v", res.Applied, res.Details)
	}
	skipped := map[string]bool{}
	for _, id := range res.Skipped {
		skipped[id] = true
	}
	// acl_tag already OK → skipped, not applied.
	if !skipped["acl_tag"] {
		t.Fatalf("acl_tag should be skipped (already OK): %+v", res)
	}
	// serve is host-local and not auto-fixable.
	if !skipped["serve"] {
		t.Fatalf("serve should be skipped as not fixable: %+v", res)
	}
	// The connect fix would have contributed a connection save; nothing here
	// asked for it.
	if len(updates) != 0 {
		t.Fatalf("unexpected updates: %+v", updates)
	}

	// A second apply of the same steps skips everything already OK.
	res2, _ := c.immichTailnetApply(context.Background(),
		[]string{"create_service", "tag_host"}, "n1", immichDefaultHostTag)
	if len(res2.Applied) != 0 {
		t.Fatalf("second apply should skip everything: %+v", res2)
	}
	if len(res2.Skipped) < 2 {
		t.Fatalf("second apply skipped = %v", res2.Skipped)
	}
}

// TestImmichTailnetApply_FixesMissingTagFirst pins the ordering rule:
// create/tag/approve all reject undeclared tags, so the tag is declared first.
func TestImmichTailnetApply_FixesMissingTagFirst(t *testing.T) {
	mock := newMockTailscaleAdmin()
	mock.devices = []TailscaleDevice{{ID: "n1", Hostname: "photos-box"}}
	h, done := vipTestHost(t, mock)
	defer done()
	c := NewImmichConnector(h)

	// acl_tag is NOT requested; the prerequisite fix must still run.
	_, _ = c.immichTailnetApply(context.Background(),
		[]string{"create_service"}, "n1", immichDefaultHostTag)

	pol, _, err := NewTailscaleAdminClient("tskey-api-test", "").LoadPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if owners := TagOwnersOf(pol, "tag:immich-host"); len(owners) == 0 {
		t.Fatalf("tag should have been declared as a prerequisite; owners=%v calls=%v", owners, mock.calls)
	}
	if _, ok := mock.services[ImmichVIPServiceName]; !ok {
		t.Fatalf("service should have been created; calls=%v", mock.calls)
	}
}

// TestImmichTailnetSteps_SkipAlreadyOK pins that already-satisfied steps are
// not re-fixed.
func TestImmichTailnetSteps_SkipAlreadyOK(t *testing.T) {
	mock := newMockTailscaleAdmin()
	mock.policy.TagOwners = map[string][]string{"tag:immich-host": {"autogroup:admin"}}
	mock.services[ImmichVIPServiceName] = TailscaleService{
		Name: ImmichVIPServiceName, Ports: []string{"tcp:443"},
	}
	mock.devices = []TailscaleDevice{{ID: "n1", Hostname: "photos-box", Tags: []string{"tag:immich-host"}}}
	mock.approved["n1"] = true
	h, done := vipTestHost(t, mock)
	defer done()
	c := NewImmichConnector(h)

	res, _ := c.immichTailnetApply(context.Background(),
		[]string{"acl_tag", "create_service", "tag_host", "approve"}, "n1", immichDefaultHostTag)
	if len(res.Applied) != 0 {
		t.Fatalf("nothing should have been applied: %+v", res)
	}
	if len(res.Skipped) < 4 {
		t.Fatalf("skipped = %v details=%v", res.Skipped, res.Details)
	}
}

// TestImmichTailnetWithoutKey pins the fail-open empty state.
func TestImmichTailnetWithoutKey(t *testing.T) {
	h := newTestHost("https://aegis.example.ts.net")
	h.setService(ImmichServiceID, "http://localhost:2283/api")
	state := NewImmichConnector(h).immichTailnetState(context.Background())
	if state.Checked {
		t.Fatal("should not report checked without a key")
	}
	if !strings.Contains(state.Error, "Tailscale API key not configured") {
		t.Fatalf("error = %q", state.Error)
	}
	// The declared steps still come through so the walkthrough renders.
	if len(state.Steps) != 6 {
		t.Fatalf("steps = %+v", state.Steps)
	}

	res, _ := NewImmichConnector(h).immichTailnetApply(context.Background(), []string{"create_service"}, "", "")
	if len(res.Failed) != 1 || !strings.Contains(strings.Join(res.Details, " "), "not configured") {
		t.Fatalf("apply without key = %+v", res)
	}
}

// TestImmichTailnetConnect_Update pins that the Aegis-side fix is a state
// request, never a direct write.
func TestImmichTailnetConnect_Update(t *testing.T) {
	mock := newMockTailscaleAdmin()
	h, done := vipTestHost(t, mock)
	defer done()
	c := NewImmichConnector(h)

	up, err := c.tsFixConnect()
	if err != nil {
		t.Fatal(err)
	}
	if up.Endpoint != "https://immich.example.ts.net/api" {
		t.Fatalf("endpoint = %q", up.Endpoint)
	}
	// The existing key and canonical claim are preserved.
	if up.SecretHeaders["X-API-Key"] != "test" {
		t.Fatalf("headers = %v", up.SecretHeaders)
	}
	if len(up.CanonicalFor) != 0 {
		t.Fatalf("claim = %v", up.CanonicalFor)
	}
}

// TestImmichTailnetServe_ProbeVip pins that a working host-local serve turns
// the serve step green without SSH.
func TestImmichTailnetServe_ProbeVip(t *testing.T) {
	mock := newMockTailscaleAdmin()
	h, done := vipTestHost(t, mock)
	defer done()
	state := vipState(t, h)
	st := stepByID(t, state, "serve")
	if !st.Checked || st.Fixable {
		t.Fatalf("serve step = %+v", st)
	}
	if !st.OK {
		t.Fatalf("serve should probe green: %+v", st)
	}
	if !strings.Contains(st.Detail, "reachable at") {
		t.Fatalf("detail = %q", st.Detail)
	}
}
