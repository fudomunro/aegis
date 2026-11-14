package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/admin"
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/tokens"
)

// mockYNABUpstream serves the budgets listing YNAB's probe calls.
func mockYNABUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/budgets" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"budgets":[{"id":"b1","name":"Household"}]}}`))
	}))
}

// TestAdminOnboarding_YNABStatusAndConfig drives the generic setup runner
// through YNAB's flow and pins the write-path contract (issue #265): the
// token lands in the encrypted store, never in service_connections.json and
// never in a response.
func TestAdminOnboarding_YNABStatusAndConfig(t *testing.T) {
	dir := t.TempDir()
	upstream := mockYNABUpstream(t)
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{{ID: "ynab", Name: "YNAB", Action: "inject", Endpoint: upstream.URL}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.adminAPIKey = "admin-key"
	srv.connectors[connectors.YNABServiceID] = connectors.NewYNABConnector(srv)
	srv.tokens = mustSecretStore(t, filepath.Join(dir, "tokens.json"))
	if err := srv.config.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json"), srv.tokens); err != nil {
		t.Fatal(err)
	}

	req := adminRequest(http.MethodGet, "/v1/admin/setup/ynab", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var view admin.OnboardingView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Status.Fields["token"].Set {
		t.Fatal("expected no token yet")
	}
	// The panel renders only what the spec declares — provider copy lives here.
	if view.Spec.Title != "YNAB — budget transactions" {
		t.Fatalf("spec title = %q", view.Spec.Title)
	}
	if !strings.Contains(view.Spec.Guides[0].Body, "Create a Personal Access Token in YNAB → Account Settings → Developer Settings.") {
		t.Fatalf("guide copy missing: %q", view.Spec.Guides[0].Body)
	}

	body := `{"values":{"endpoint":"` + upstream.URL + `","token":"test-pat"}}`
	req2 := adminRequest(http.MethodPost, "/v1/admin/setup/ynab/save", "admin-key", body)
	w2 := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("save status %d body=%s", w2.Code, w2.Body.String())
	}
	var saved admin.OnboardingActionResult
	if err := json.Unmarshal(w2.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Status.Fields["token"].Set {
		t.Fatal("expected token after save")
	}
	// Secret must not be echoed.
	if strings.Contains(w2.Body.String(), "test-pat") {
		t.Fatalf("token leaked in response: %s", w2.Body.String())
	}
	// Static credentials live in the encrypted token store (issue #184),
	// never in plaintext service_connections.json.
	raw, err := os.ReadFile(filepath.Join(dir, "service_connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "test-pat") {
		t.Fatal("token must not be written in plaintext to service_connections.json")
	}
	if srv.tokens.GetSecret(tokens.ServiceHeadersSecretKey("ynab")) == "" {
		t.Fatal("token not persisted to encrypted token store")
	}
	var hdrs map[string]string
	if err := json.Unmarshal([]byte(srv.tokens.GetSecret(tokens.ServiceHeadersSecretKey("ynab"))), &hdrs); err != nil {
		t.Fatal(err)
	}
	if hdrs["Authorization"] != "Bearer test-pat" {
		t.Fatalf("stored headers = %+v", hdrs)
	}
	// The refreshed status reflects post-save state (runner recomputes it).
	if !strings.Contains(strings.Join(saved.Status.Summary, " · "), "token: configured (header)") {
		t.Fatalf("summary = %v", saved.Status.Summary)
	}

	req3 := adminRequest(http.MethodPost, "/v1/admin/setup/ynab/probe", "admin-key", "")
	w3 := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("probe status %d body=%s", w3.Code, w3.Body.String())
	}
	var probed admin.OnboardingActionResult
	if err := json.Unmarshal(w3.Body.Bytes(), &probed); err != nil {
		t.Fatal(err)
	}
	if probed.Probe == nil || !probed.Probe.OK {
		t.Fatalf("probe = %+v", probed.Probe)
	}
	if probed.Probe.Metrics["budget_count"] != 1 {
		t.Fatalf("probe metrics = %v", probed.Probe.Metrics)
	}
}

// TestAdminOnboarding_SaveRejectsBadEndpoint pins the save validation copy.
func TestAdminOnboarding_SaveRejectsBadEndpoint(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Services: []Service{{ID: "ynab", Action: "inject", Endpoint: "https://api.ynab.com/v1"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.adminAPIKey = "admin-key"
	srv.connectors[connectors.YNABServiceID] = connectors.NewYNABConnector(srv)
	srv.tokens = mustSecretStore(t, filepath.Join(dir, "tokens.json"))
	if err := srv.config.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json"), srv.tokens); err != nil {
		t.Fatal(err)
	}

	req := adminRequest(http.MethodPost, "/v1/admin/setup/ynab/save", "admin-key",
		`{"values":{"endpoint":"not-a-url","token":"x"}}`)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "endpoint must be an http(s) URL") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

// TestAdminOnboarding_UnknownServiceAndMethod pins the runner's routing.
func TestAdminOnboarding_UnknownServiceAndMethod(t *testing.T) {
	cfg := &Config{Services: []Service{}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.adminAPIKey = "admin-key"
	srv.connectors[connectors.YNABServiceID] = connectors.NewYNABConnector(srv)

	// No connector registered under this id.
	req := adminRequest(http.MethodGet, "/v1/admin/setup/ghost", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown service status %d", w.Code)
	}

	// A registered connector with no setup flow.
	srv.connectors["sample-calendar"] = connectors.NewSampleCalendarConnector()
	req2 := adminRequest(http.MethodGet, "/v1/admin/setup/sample-calendar", "admin-key", "")
	w2 := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("no setup flow status %d", w2.Code)
	}

	req3 := adminRequest(http.MethodPost, "/v1/admin/setup/ynab", "admin-key", "")
	w3 := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w3, req3)
	if w3.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status %d", w3.Code)
	}
}

func TestAdminSummary_YNABOnlyTransactionsType(t *testing.T) {
	cfg := &Config{
		Services: []Service{{ID: "ynab", Name: "YNAB", Action: "inject"}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.adminAPIKey = "admin-key"
	srv.connectors[connectors.YNABServiceID] = connectors.NewYNABConnector(srv)
	srv.connectors[connectors.QuickenServiceID] = connectors.NewQuickenConnector(nil)
	// Quicken is file-based: even if present in config, keep it off the list.
	cfg.Services = append(cfg.Services, Service{ID: connectors.QuickenServiceID, Name: "Quicken", Action: "inject"})

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminSummary)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("summary status %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Services []admin.AdminService `json:"services"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var ynab *admin.AdminService
	for i := range resp.Services {
		if resp.Services[i].ID == connectors.QuickenServiceID {
			t.Fatal("quicken must not appear in admin services list")
		}
		if resp.Services[i].ID == connectors.YNABServiceID {
			ynab = &resp.Services[i]
		}
	}
	if ynab == nil {
		t.Fatal("ynab missing from summary")
	}
	if !ynab.HasTransaction {
		t.Fatal("has_transaction_provider not set")
	}
}

func TestYNABCapabilities_OnlyTransactions(t *testing.T) {
	caps := connectors.NewYNABConnector(&Server{}).Capabilities()
	if len(caps.Kinds) != 1 || caps.Kinds[0].Name != "transactions" {
		t.Fatalf("caps = %+v", caps)
	}
	qcaps := connectors.NewQuickenConnector(nil).Capabilities()
	if len(qcaps.Kinds) != 1 || qcaps.Kinds[0].Name != "transactions" {
		t.Fatalf("quicken caps = %+v", qcaps)
	}
}

// mockImmichUpstream answers the two setup probes plus the library listing
// the acceptance checklist runs.
func mockImmichUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "good-key" && r.URL.Path != "/api/server/ping" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/users/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"u1","name":"Owner"}`))
		case r.URL.Path == "/api/server/ping":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"res":"pong"}`))
		case r.URL.Path == "/api/search/metadata":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"a1","type":"IMAGE","originalFileName":"IMG_1.jpg","fileCreatedAt":"2024-01-01T00:00:00Z","checksum":"abc"}],"nextPage":null}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestAdminOnboarding_ImmichStatusAndConfig drives Immich's flow through the
// generic runner and pins the write path (issue #265): the key lands in the
// encrypted store, the claim moves with one owner, and the acceptance
// checklist turns green against a healthy Immich.
func TestAdminOnboarding_ImmichStatusAndConfig(t *testing.T) {
	dir := t.TempDir()
	upstream := mockImmichUpstream(t)
	defer upstream.Close()

	cfg := &Config{Services: []Service{{ID: "immich", Name: "Immich", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.adminAPIKey = "admin-key"
	srv.connectors[connectors.ImmichServiceID] = connectors.NewImmichConnector(srv)
	srv.tokens = mustSecretStore(t, filepath.Join(dir, "tokens.json"))
	if err := srv.config.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json"), srv.tokens); err != nil {
		t.Fatal(err)
	}

	// Unauthenticated callers get 401 from the perimeter.
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w, httptest.NewRequest(http.MethodGet, "/v1/admin/setup/immich", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d", w.Code)
	}

	// Save the connection, claiming the photo home copy.
	body := `{"values":{"endpoint":"` + upstream.URL + `","api_key":"good-key"},"flags":{"claim_canonical_for_photos":true}}`
	req := adminRequest(http.MethodPost, "/v1/admin/setup/immich/save", "admin-key", body)
	w2 := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("save status %d body=%s", w2.Code, w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "good-key") {
		t.Fatalf("api key leaked in response: %s", w2.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "service_connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "good-key") {
		t.Fatal("api key must not be written in plaintext to service_connections.json")
	}
	if srv.tokens.GetSecret(tokens.ServiceHeadersSecretKey("immich")) == "" {
		t.Fatal("api key not persisted to encrypted token store")
	}
	saved := w2.Body.String()
	if !strings.Contains(saved, `"canonical":["photos"]`) && !strings.Contains(saved, `"canonical: photos"`) {
		// The claim is visible in the summary the panel renders.
		if !strings.Contains(saved, "canonical: photos") {
			t.Fatalf("canonical claim missing from status: %s", saved)
		}
	}

	// The refreshed status reflects post-save state and the checklist is green.
	var res admin.OnboardingActionResult
	if err := json.Unmarshal(w2.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Status.Fields["api_key"].Set {
		t.Fatal("expected api_key to read as set")
	}
	if !res.Status.Fields["claim_canonical_for_photos"].Checked {
		t.Fatal("expected the claim checkbox to read as checked")
	}
	got := map[string]bool{}
	for _, c := range res.Status.Checks {
		got[c.ID] = c.OK
	}
	for _, id := range []string{
		"service_configured", "api_key", "canonical_owner",
		"endpoint_reachable", "library_listing", "canonical_stamp", "bytes_stay_upstream",
	} {
		if !got[id] {
			t.Fatalf("check %q not OK in %+v", id, res.Status.Checks)
		}
	}
	if res.Status.Metrics["photo_count"] != 1 {
		t.Fatalf("metrics = %v", res.Status.Metrics)
	}
	if !strings.Contains(res.Status.Next, "complete") {
		t.Fatalf("next = %q", res.Status.Next)
	}

	// Probe with a rejected key fails loudly with actionable copy.
	bad := `{"values":{"endpoint":"` + upstream.URL + `","api_key":"bad-key"}}`
	req3 := adminRequest(http.MethodPost, "/v1/admin/setup/immich/probe", "admin-key", bad)
	w3 := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("probe status %d body=%s", w3.Code, w3.Body.String())
	}
	var probed admin.OnboardingActionResult
	if err := json.Unmarshal(w3.Body.Bytes(), &probed); err != nil {
		t.Fatal(err)
	}
	if probed.Probe == nil || probed.Probe.OK {
		t.Fatalf("probe = %+v", probed.Probe)
	}
}

// TestAdminOnboarding_ImmichNotConfigured pins the fail-open empty state.
func TestAdminOnboarding_ImmichNotConfigured(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Services: []Service{}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.adminAPIKey = "admin-key"
	srv.connectors[connectors.ImmichServiceID] = connectors.NewImmichConnector(srv)
	srv.tokens = mustSecretStore(t, filepath.Join(dir, "tokens.json"))
	if err := srv.config.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json"), srv.tokens); err != nil {
		t.Fatal(err)
	}

	req := adminRequest(http.MethodGet, "/v1/admin/setup/immich", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminOnboarding)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var view admin.OnboardingView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range view.Status.Checks {
		got[c.ID] = c.OK
	}
	if got["service_configured"] {
		t.Fatal("service_configured should fail")
	}
	if !got["bytes_stay_upstream"] {
		t.Fatal("product rule check should pass")
	}
	snippet := view.Status.Guides["snippet"].Command
	if snippet == "" || !strings.Contains(snippet, "immich") {
		t.Fatalf("snippet = %q", snippet)
	}
}
