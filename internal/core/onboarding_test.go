package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
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
	srv.AdminMiddleware(srv.handleAdminOnboarding)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var view onboardingView
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
	srv.AdminMiddleware(srv.handleAdminOnboarding)(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("save status %d body=%s", w2.Code, w2.Body.String())
	}
	var saved onboardingActionResult
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
	if srv.tokens.GetSecret(ServiceHeadersSecretKey("ynab")) == "" {
		t.Fatal("token not persisted to encrypted token store")
	}
	var hdrs map[string]string
	if err := json.Unmarshal([]byte(srv.tokens.GetSecret(ServiceHeadersSecretKey("ynab"))), &hdrs); err != nil {
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
	srv.AdminMiddleware(srv.handleAdminOnboarding)(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("probe status %d body=%s", w3.Code, w3.Body.String())
	}
	var probed onboardingActionResult
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
	srv.AdminMiddleware(srv.handleAdminOnboarding)(w, req)
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
	srv.AdminMiddleware(srv.handleAdminOnboarding)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown service status %d", w.Code)
	}

	// A registered connector with no setup flow.
	srv.connectors["sample-calendar"] = connectors.NewSampleCalendarConnector()
	req2 := adminRequest(http.MethodGet, "/v1/admin/setup/sample-calendar", "admin-key", "")
	w2 := httptest.NewRecorder()
	srv.AdminMiddleware(srv.handleAdminOnboarding)(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("no setup flow status %d", w2.Code)
	}

	req3 := adminRequest(http.MethodPost, "/v1/admin/setup/ynab", "admin-key", "")
	w3 := httptest.NewRecorder()
	srv.AdminMiddleware(srv.handleAdminOnboarding)(w3, req3)
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
	srv.connectors[connectors.QuickenServiceID] = connectors.NewQuickenConnector(srv.quicken)
	// Quicken is file-based: even if present in config, keep it off the list.
	cfg.Services = append(cfg.Services, Service{ID: connectors.QuickenServiceID, Name: "Quicken", Action: "inject"})

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSummary)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("summary status %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Services []AdminService `json:"services"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var ynab *AdminService
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
	if len(ynab.AvailableDataTypes) != 1 || ynab.AvailableDataTypes[0] != "transactions" {
		t.Fatalf("available_data_types = %v, want [transactions]", ynab.AvailableDataTypes)
	}
	if !ynab.HasTransaction {
		t.Fatal("has_transaction_provider not set")
	}
}

func TestYNABCapabilities_OnlyTransactions(t *testing.T) {
	caps := connectors.NewYNABConnector(&Server{}).Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "transactions" {
		t.Fatalf("caps = %+v", caps)
	}
	qcaps := connectors.NewQuickenConnector(nil).Capabilities()
	if len(qcaps.DataTypes) != 1 || qcaps.DataTypes[0].Name != "transactions" {
		t.Fatalf("quicken caps = %+v", qcaps)
	}
}
