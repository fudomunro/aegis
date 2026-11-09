package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

// TestYNABOnboarding_Spec pins YNAB's declared setup contract (issue #265):
// the provider copy has exactly one owner — this spec — and the admin panel
// must render it without restating any of it.
func TestYNABOnboarding_Spec(t *testing.T) {
	spec := NewYNABConnector(newTestHost("https://aegis.example.com")).Onboarding()

	if spec.ServiceID != YNABServiceID || spec.DisplayName != YNABServiceName {
		t.Fatalf("service = %q / %q", spec.ServiceID, spec.DisplayName)
	}
	if spec.Title != "YNAB — budget transactions" {
		t.Fatalf("title = %q", spec.Title)
	}

	byID := map[string]shared.OnboardingField{}
	for _, f := range spec.Fields {
		byID[f.ID] = f
	}
	if f := byID["endpoint"]; f.Label != "API endpoint" || f.Kind != "url" || f.Placeholder != "https://api.ynab.com/v1" {
		t.Fatalf("endpoint field = %+v", f)
	}
	if f := byID["token"]; f.Label != "Personal Access Token" || f.Kind != "password" || !f.Secret {
		t.Fatalf("token field = %+v", f)
	}

	if len(spec.Guides) != 1 {
		t.Fatalf("guides = %+v", spec.Guides)
	}
	want := []string{
		"Live budget line items for `GET /v1/library/transactions`.",
		"Create a Personal Access Token in YNAB → Account Settings → Developer Settings.",
		"Stored in `service_connections.json` — never shown again after save.",
		"[YNAB API docs](https://api.ynab.com/)",
	}
	for _, phrase := range want {
		if !strings.Contains(spec.Guides[0].Body, phrase) {
			t.Fatalf("guide missing %q:\n%s", phrase, spec.Guides[0].Body)
		}
	}

	actions := map[string]shared.OnboardingAction{}
	for _, a := range spec.Actions {
		actions[a.ID] = a
	}
	for id, label := range map[string]string{
		"probe":     "Test",
		"save":      "Save connection",
		"save_test": "Save & test",
	} {
		a, ok := actions[id]
		if !ok {
			t.Fatalf("action %q missing", id)
		}
		if a.Label != label {
			t.Fatalf("action %q label = %q, want %q", id, a.Label, label)
		}
	}
	if actions["probe"].BusyLabel != "Testing YNAB API…" {
		t.Fatalf("probe busy label = %q", actions["probe"].BusyLabel)
	}
	if actions["save"].Placement != shared.ActionInHeader || actions["save_test"].Placement != shared.ActionInForm {
		t.Fatalf("placements = %+v / %+v", actions["save"].Placement, actions["save_test"].Placement)
	}

	// Connection metadata the runner persists on save.
	if spec.Connection.Name != YNABServiceName || spec.Connection.Action != "inject" || spec.Connection.CacheTTL != "5m" {
		t.Fatalf("connection = %+v", spec.Connection)
	}
	if len(spec.Connection.Aliases) != 3 {
		t.Fatalf("aliases = %v", spec.Connection.Aliases)
	}
}

// mockYNAB budgets upstream for probe paths (never call the real provider).
func mockYNAB(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"budgets":[{"id":"b1"}]}}`))
	}))
}

// TestYNABOnboarding_StatusSummary pins the one-line status copy.
func TestYNABOnboarding_StatusSummary(t *testing.T) {
	ctx := context.Background()
	upstream := mockYNAB(t)
	defer upstream.Close()

	// Nothing configured: both prompts, no probe, three summary bits.
	empty := newTestHost("https://aegis.example.com")
	status := NewYNABConnector(empty).OnboardingStatus(ctx)
	summary := strings.Join(status.Summary, " · ")
	if summary != "token: not set · API: unchecked · library: not ready" {
		t.Fatalf("summary = %q", summary)
	}
	if len(status.Hints) != 2 {
		t.Fatalf("hints = %v", status.Hints)
	}
	if status.Hints[0] != "Add a ynab service (this page can save it for you)." {
		t.Fatalf("hint[0] = %q", status.Hints[0])
	}
	if !strings.Contains(status.Hints[1], "Create a Personal Access Token in YNAB → Account Settings → Developer Settings") {
		t.Fatalf("hint[1] = %q", status.Hints[1])
	}

	// Configured but no credential: only the credential prompt remains.
	h := newTestHost("https://aegis.example.com")
	h.setService(YNABServiceID, upstream.URL)
	status = NewYNABConnector(h).OnboardingStatus(ctx)
	if len(status.Hints) != 1 {
		t.Fatalf("hints = %v", status.Hints)
	}

	// Header credential: the summary names the source.
	h.services[YNABServiceID].Headers = map[string]string{"Authorization": "Bearer tok"}
	status = NewYNABConnector(h).OnboardingStatus(ctx)
	summary = strings.Join(status.Summary, " · ")
	if !strings.HasPrefix(summary, "token: configured (header)") {
		t.Fatalf("summary = %q", summary)
	}

	// OAuth credential counts too.
	h2 := newTestHost("https://aegis.example.com")
	h2.setService(YNABServiceID, upstream.URL)
	h2.setToken(YNABServiceID, "oauth-tok")
	status = NewYNABConnector(h2).OnboardingStatus(ctx)
	if !strings.HasPrefix(strings.Join(status.Summary, " · "), "token: configured (oauth)") {
		t.Fatalf("summary = %v", status.Summary)
	}
}

// TestYNABOnboarding_ProbeCodes pins the diagnosis copy.
func TestYNABOnboarding_ProbeCodes(t *testing.T) {
	ctx := context.Background()

	// Unreachable upstream → error, but the flow must answer.
	h := newTestHost("https://aegis.example.com")
	h.setService(YNABServiceID, "http://127.0.0.1:1")
	h.services[YNABServiceID].Headers = map[string]string{"Authorization": "Bearer tok"}
	res, err := NewYNABConnector(h).OnboardingRun(ctx, "probe", shared.OnboardingInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Probe == nil || res.Probe.OK || res.Probe.Code != "error" {
		t.Fatalf("probe = %+v", res.Probe)
	}

	// No credential at all → the distinct "not configured" diagnosis.
	blank := newTestHost("https://aegis.example.com")
	blank.setService(YNABServiceID, "https://api.ynab.com/v1")
	res, err = NewYNABConnector(blank).OnboardingRun(ctx, "probe", shared.OnboardingInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Probe.Message != "no YNAB token configured" {
		t.Fatalf("probe = %+v", res.Probe)
	}

	// 401 from upstream → rejected-token copy.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	rejected := newTestHost("https://aegis.example.com")
	rejected.setService(YNABServiceID, upstream.URL)
	rejected.services[YNABServiceID].Headers = map[string]string{"Authorization": "Bearer tok"}
	res, err = NewYNABConnector(rejected).OnboardingRun(ctx, "probe", shared.OnboardingInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Probe.Message != "YNAB rejected the token — check the Personal Access Token" {
		t.Fatalf("probe = %+v", res.Probe)
	}

	// Healthy upstream → ok with a budget count.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"budgets":[{"id":"b1"},{"id":"b2"}]}}`))
	}))
	defer ok.Close()
	healthy := newTestHost("https://aegis.example.com")
	healthy.setService(YNABServiceID, ok.URL)
	healthy.services[YNABServiceID].Headers = map[string]string{"Authorization": "Bearer tok"}
	res, err = NewYNABConnector(healthy).OnboardingRun(ctx, "probe", shared.OnboardingInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Probe.OK || res.Probe.Metrics["budget_count"] != 2 {
		t.Fatalf("probe = %+v", res.Probe)
	}
}

// TestYNABOnboarding_ConnectionUpdate pins the save mapping: endpoint default
// and repair, bearer normalization, and that a blank token touches nothing.
func TestYNABOnboarding_ConnectionUpdate(t *testing.T) {
	ctx := context.Background()
	c := NewYNABConnector(newTestHost("https://aegis.example.com"))

	res, err := c.OnboardingRun(ctx, "save", shared.OnboardingInput{Values: map[string]string{
		"endpoint": "https://api.ynab.com/v1",
		"token":    "test-pat",
	}})
	if err != nil {
		t.Fatal(err)
	}
	up := res.Updates[0].Connection
	if up.Endpoint != "https://api.ynab.com/v1" {
		t.Fatalf("endpoint = %q", up.Endpoint)
	}
	if up.SecretHeaders["Authorization"] != "Bearer test-pat" {
		t.Fatalf("headers = %v", up.SecretHeaders)
	}

	// Blank endpoint falls back to the public API base.
	res, err = c.OnboardingRun(ctx, "save", shared.OnboardingInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Updates[0].Connection.Endpoint; got != "https://api.ynab.com/v1" {
		t.Fatalf("default endpoint = %q", got)
	}
	// Blank token keeps any stored credential (non-nil but empty header map).
	if len(res.Updates[0].Connection.SecretHeaders) != 0 {
		t.Fatalf("headers = %v", res.Updates[0].Connection.SecretHeaders)
	}

	// "Bearer " prefix is stripped before re-wrapping.
	res, err = c.OnboardingRun(ctx, "save", shared.OnboardingInput{Values: map[string]string{"token": "bearer  tok"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Updates[0].Connection.SecretHeaders["Authorization"]; got != "Bearer tok" {
		t.Fatalf("authorization = %q", got)
	}

	// Non-http endpoint is rejected with the pinned copy.
	_, err = c.OnboardingRun(ctx, "save", shared.OnboardingInput{Values: map[string]string{"endpoint": "not-a-url"}})
	if err == nil || err.Error() != "endpoint must be an http(s) URL" {
		t.Fatalf("err = %v", err)
	}

	// Unknown verb is rejected rather than silently ignored.
	if _, err := c.OnboardingRun(ctx, "nope", shared.OnboardingInput{}); err == nil {
		t.Fatal("expected error for unknown action")
	}
}
