package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

// TestImmichOnboarding_Spec pins Immich's declared setup contract (issue #265):
// the provider copy has exactly one owner — this spec.
func TestImmichOnboarding_Spec(t *testing.T) {
	spec := NewImmichConnector(newTestHost("https://aegis.example.com")).Onboarding()

	if spec.ServiceID != ImmichServiceID || spec.DisplayName != ImmichServiceName {
		t.Fatalf("service = %q / %q", spec.ServiceID, spec.DisplayName)
	}
	if spec.Title != "Immich — local canonical photos" {
		t.Fatalf("title = %q", spec.Title)
	}
	if spec.WalkthroughLabel != "Setup guide" {
		t.Fatalf("walkthrough label = %q", spec.WalkthroughLabel)
	}

	fields := map[string]types.OnboardingField{}
	for _, f := range spec.Fields {
		fields[f.ID] = f
	}
	if f := fields["endpoint"]; f.Label != "Immich endpoint" || f.Kind != "url" {
		t.Fatalf("endpoint field = %+v", f)
	}
	if f := fields["api_key"]; f.Label != "Immich API key" || f.Kind != "password" || !f.Secret {
		t.Fatalf("api_key field = %+v", f)
	}
	if f := fields["claim_canonical_for_photos"]; f.Label != "Claim canonical for photos" || f.Kind != "checkbox" {
		t.Fatalf("claim field = %+v", f)
	}

	wantSteps := []string{
		"Immich installed and reachable",
		"Immich API key created",
		"Saved into Aegis",
		"Verified end-to-end",
	}
	if len(spec.Steps) != len(wantSteps) {
		t.Fatalf("steps = %+v", spec.Steps)
	}
	for i, label := range wantSteps {
		if spec.Steps[i].Label != label {
			t.Fatalf("step[%d] = %q, want %q", i, spec.Steps[i].Label, label)
		}
	}

	wantChecks := []string{
		"Immich service configured",
		"API key present (X-API-Key)",
		"Immich is canonical_for photos",
		"Immich endpoint reachable",
		"Library lists Immich photos",
		"Sources stamped canonical + proxy blob_url",
		"Photo bytes stay on Immich",
	}
	if len(spec.Checks) != len(wantChecks) {
		t.Fatalf("checks = %+v", spec.Checks)
	}
	for i, label := range wantChecks {
		if spec.Checks[i].Label != label {
			t.Fatalf("check[%d] = %q, want %q", i, spec.Checks[i].Label, label)
		}
	}

	actions := map[string]types.OnboardingAction{}
	for _, a := range spec.Actions {
		actions[a.ID] = a
	}
	for id, label := range map[string]string{
		"probe": "Test",
		"save":  "Save to Aegis",
		"check": "Run checks",
	} {
		if actions[id].Label != label {
			t.Fatalf("action %q label = %q, want %q", id, actions[id].Label, label)
		}
	}
	if actions["save"].Kind != types.ActionSave || actions["check"].Kind != types.ActionCheck {
		t.Fatalf("action kinds = %+v / %+v", actions["save"].Kind, actions["check"].Kind)
	}

	guides := map[string]types.OnboardingGuide{}
	for _, g := range spec.Guides {
		guides[g.ID] = g
	}
	if g := guides["help"]; !strings.Contains(g.Body, "Bytes stay on Immich") {
		t.Fatalf("help guide = %+v", g)
	}
	if g := guides["api_key_permissions"]; g.Title != "One API key for Aegis + immich-go" || g.Button != "Copy list" || g.Walkthrough != true {
		t.Fatalf("perms guide = %+v", g)
	}
	if g := guides["snippet"]; g.Title != "Prefer aegis.json? Show bootstrap snippet" ||
		g.Label != "services[] entry" || g.Button != "Copy JSON" || !g.Collapsible {
		t.Fatalf("snippet guide = %+v", g)
	}

	if spec.Connection.Name != "Immich" || spec.Connection.Action != "inject" || spec.Connection.CacheTTL != "5m" {
		t.Fatalf("connection = %+v", spec.Connection)
	}
	if len(spec.Connection.SecretHeaders) != 1 || spec.Connection.SecretHeaders[0] != "X-API-Key" {
		t.Fatalf("secret headers = %v", spec.Connection.SecretHeaders)
	}
	if len(spec.Connection.Aliases) != 8 {
		t.Fatalf("aliases = %v", spec.Connection.Aliases)
	}
}

// TestImmichOnboarding_PermissionSetGenerated pins the single-owner contract:
// the copyable checkbox list is generated from the same groups the connector
// enforces, so admin.html and docs have nothing left to restate.
func TestImmichOnboarding_PermissionSetGenerated(t *testing.T) {
	need := []string{
		"asset.read", "asset.view", "asset.download",
		"album.read", "user.read",
		"asset.upload", "album.create", "job.create",
	}
	set := map[string]bool{}
	for _, p := range ImmichAPIKeyPermissions() {
		set[p] = true
	}
	for _, n := range need {
		if !set[n] {
			t.Fatalf("missing permission %q in %v", n, ImmichAPIKeyPermissions())
		}
	}
	if !strings.Contains(ImmichAPIKeyPermissionsSummary(), "asset.upload") {
		t.Fatal("summary should include upload")
	}

	block := immichAPIKeyPermissionsBlock()
	if !strings.HasPrefix(block, "# Single Immich key — Aegis library/proxy AND immich-go Takeout import") {
		t.Fatalf("block header = %q", strings.SplitN(block, "\n", 2)[0])
	}
	for _, header := range []string{
		"# Aegis",
		"# immich-go upload from-google-photos",
		"# immich-go pauses jobs during import (admin-linked key)",
	} {
		if !strings.Contains(block, header) {
			t.Fatalf("block missing group %q", header)
		}
	}
	for _, p := range ImmichAPIKeyPermissions() {
		if !strings.Contains(block, p) {
			t.Fatalf("block missing %q", p)
		}
	}

	// The status hands the generated blocks to the panel.
	c := NewImmichConnector(newTestHost("https://aegis.example.com"))
	status := c.OnboardingStatus(context.Background())
	if got := status.Guides["api_key_permissions"].Command; got != block {
		t.Fatalf("status perms block = %q", got)
	}
	if got := status.Guides["snippet"].Command; !strings.Contains(got, `"id": "immich"`) {
		t.Fatalf("status snippet = %q", got)
	}
}

// TestImmichNormalizeEndpoint pins the paste-repair rules.
func TestImmichNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		changed bool
	}{
		{"", "", false},
		{"photos.home.ts.net:2283", "http://photos.home.ts.net:2283/api", true},
		{"http://photos.home.ts.net:2283", "http://photos.home.ts.net:2283/api", true},
		{"http://photos.home.ts.net:2283/", "http://photos.home.ts.net:2283/api", true},
		{"http://photos.home.ts.net:2283/api", "http://photos.home.ts.net:2283/api", false},
		{"http://localhost:2283", "http://localhost:2283/api", true},
		{"localhost", "http://localhost:2283/api", true},
		{"http://photos.home.ts.net:2283/photos/abc", "http://photos.home.ts.net:2283/api", true},
	}
	for _, tc := range cases {
		got, changed := NormalizeImmichEndpoint(tc.in)
		if got != tc.want {
			t.Fatalf("NormalizeImmichEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if changed != tc.changed {
			t.Fatalf("NormalizeImmichEndpoint(%q) changed=%v, want %v", tc.in, changed, tc.changed)
		}
	}
}

// TestImmichTailnetHelpers pins the endpoint suggestions the wizard shows.
func TestImmichTailnetHelpers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://aegis.example.ts.net", "example.ts.net"},
		{"https://aegis.example.com", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := TailnetDNSFromBaseURL(tc.in); got != tc.want {
			t.Fatalf("TailnetDNSFromBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := ImmichSuggestedEndpoint("example.ts.net"); got != "https://immich.example.ts.net/api" {
		t.Fatalf("suggested = %q", got)
	}
	if got := ImmichSuggestedEndpoint(""); got != "http://photos.home.ts.net:2283/api" {
		t.Fatalf("fallback = %q", got)
	}
}

// TestImmichProbe_ActionableCodes pins the diagnosis copy.
func TestImmichProbe_ActionableCodes(t *testing.T) {
	ctx := context.Background()
	c := NewImmichConnector(newTestHost("https://aegis.example.com"))

	// HTML on /server/ping → wrong_path / not_immich with a hint.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<!doctype html><html><body>Not found</body></html>`))
	}))
	defer mock.Close()
	res, err := c.OnboardingRun(ctx, "probe", types.OnboardingInput{Values: map[string]string{
		"endpoint": mock.URL + "/photos/abc",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Probe.OK {
		t.Fatal("html page should fail")
	}
	if res.Probe.Code != immichCodeWrongPath && res.Probe.Code != immichCodeNotImmich {
		t.Fatalf("code = %q", res.Probe.Code)
	}
	if res.Probe.Hint == "" {
		t.Fatal("expected hint")
	}

	// Unreachable host → unreachable + install hint.
	res, err = c.OnboardingRun(ctx, "probe", types.OnboardingInput{Values: map[string]string{
		"endpoint": "http://127.0.0.1:1/api",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Probe.OK || res.Probe.Code != immichCodeUnreachable {
		t.Fatalf("unreachable probe = %+v", res.Probe)
	}
	if !strings.Contains(res.Probe.Hint, "Docker") && !strings.Contains(res.Probe.Hint, "Immich") {
		t.Fatalf("hint = %q", res.Probe.Hint)
	}

	// Empty endpoint gets the install hint, not a URL error.
	res, err = c.OnboardingRun(ctx, "probe", types.OnboardingInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Probe.Code != immichCodeEmptyEndpoint || res.Probe.OK {
		t.Fatalf("empty endpoint = %+v", res.Probe)
	}
}

// TestImmichConnectionUpdate pins the save mapping, including the
// single-owner canonical claim semantics.
func TestImmichConnectionUpdate(t *testing.T) {
	ctx := context.Background()
	c := NewImmichConnector(newTestHost("https://aegis.example.com"))

	res, err := c.OnboardingRun(ctx, "save", types.OnboardingInput{
		Values: map[string]string{"endpoint": "photos.home.ts.net:2283", "api_key": "good-key"},
		Flags:  map[string]bool{"claim_canonical_for_photos": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	up := res.Updates[0].Connection
	if up.Endpoint != "http://photos.home.ts.net:2283/api" {
		t.Fatalf("endpoint = %q", up.Endpoint)
	}
	if up.SecretHeaders["X-API-Key"] != "good-key" {
		t.Fatalf("headers = %v", up.SecretHeaders)
	}
	if len(up.CanonicalFor) != 1 || up.CanonicalFor[0] != "photos" || up.ClearCanonicalFor {
		t.Fatalf("claim = %+v", up)
	}

	// Unchecking must clear the claim, not leave a stale owner behind.
	res, err = c.OnboardingRun(ctx, "save", types.OnboardingInput{
		Values: map[string]string{"endpoint": "http://localhost:2283/api"},
		Flags:  map[string]bool{"claim_canonical_for_photos": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	up = res.Updates[0].Connection
	if !up.ClearCanonicalFor || len(up.CanonicalFor) != 0 {
		t.Fatalf("uncheck = %+v", up)
	}
	// Blank key keeps the stored credential.
	if len(up.SecretHeaders) != 0 {
		t.Fatalf("headers = %v", up.SecretHeaders)
	}

	// Missing endpoint is rejected with the pinned copy.
	if _, err := c.OnboardingRun(ctx, "save", types.OnboardingInput{}); err == nil ||
		err.Error() != "endpoint is required — e.g. https://immich.your-tailnet.ts.net/api" {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.OnboardingRun(ctx, "nope", types.OnboardingInput{}); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

// TestImmichNextStep pins the first-run ladder and the single next action.
func TestImmichNextStep(t *testing.T) {
	// Nothing configured: the install step leads and next is the probe hint.
	c := NewImmichConnector(newTestHost("https://aegis.example.com"))
	status := c.OnboardingStatus(context.Background())
	if len(status.Steps) != 4 {
		t.Fatalf("steps = %+v", status.Steps)
	}
	if status.Steps[0].ID != immichStepInstall || !status.Steps[0].Current {
		t.Fatalf("first step = %+v", status.Steps[0])
	}
	if status.Next == "" {
		t.Fatal("expected a next step")
	}

	// A failed probe's hint becomes the next action.
	got := immichNextStepFrom(
		[]types.OnboardingStepState{{ID: immichStepInstall, Current: true}},
		&types.ProbeResult{OK: false, Code: immichCodeUnreachable, Hint: "Start Immich with Docker."},
		nil,
	)
	if got != "Start Immich with Docker." {
		t.Fatalf("next = %q", got)
	}

	// Everything done → completion copy.
	done := []types.OnboardingStepState{
		{ID: immichStepInstall, OK: true}, {ID: immichStepAPIKey, OK: true},
		{ID: immichStepConnect, OK: true}, {ID: immichStepVerify, OK: true},
	}
	if got := immichNextStepFrom(done, nil, nil); !strings.Contains(got, "complete") {
		t.Fatalf("next = %q", got)
	}

	// Empty library gets seed guidance rather than a bare "run checks".
	empty := []types.OnboardingStepState{
		{ID: immichStepInstall, OK: true}, {ID: immichStepAPIKey, OK: true},
		{ID: immichStepConnect, OK: true}, {ID: immichStepVerify, Current: true},
	}
	got = immichNextStepFrom(empty, nil, []types.OnboardingCheckState{
		{ID: immichCheckListing, Detail: "0 photos — Immich is empty."},
	})
	if !strings.Contains(got, "no photos") && !strings.Contains(got, "Takeout") {
		t.Fatalf("empty-library next = %q", got)
	}
}
