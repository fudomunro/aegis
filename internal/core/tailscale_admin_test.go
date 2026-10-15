package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockTailscaleAdmin is a tiny fake of api.tailscale.com/api/v2 for Immich
// VIP Service setup checks and fixes (issue #124).
type mockTailscaleAdmin struct {
	services map[string]tailscaleService
	devices  []tailscaleDevice
	approved map[string]bool // device id → approved for svc:immich
	policy   tailscalePolicy

	calls []string
}

func newMockTailscaleAdmin() *mockTailscaleAdmin {
	return &mockTailscaleAdmin{
		services: map[string]tailscaleService{},
		approved: map[string]bool{},
		policy: tailscalePolicy{
			TagOwners: map[string][]string{},
		},
	}
}

func (m *mockTailscaleAdmin) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls = append(m.calls, r.Method+" "+r.URL.Path)
		// Auth: Basic (tskey) or Bearer.
		user, _, ok := r.BasicAuth()
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		authed := (ok && user != "") || bearer != ""
		if !authed {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v2")
		switch {
		case r.Method == http.MethodGet && path == "/tailnet/-/acl/policy":
			writeJSON(w, m.policy)
		case r.Method == http.MethodGet && path == "/tailnet/-/devices":
			writeJSON(w, map[string]any{"devices": m.devices})
		case r.Method == http.MethodGet && path == "/tailnet/-/services/svc:immich":
			svc, ok := m.services["svc:immich"]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, svc)
		case r.Method == http.MethodPut && path == "/tailnet/-/services/svc:immich":
			var svc tailscaleService
			json.NewDecoder(r.Body).Decode(&svc)
			if svc.Name == "" {
				svc.Name = "svc:immich"
			}
			m.services[svc.Name] = svc
			writeJSON(w, svc)
		case r.Method == http.MethodGet && path == "/tailnet/-/services/svc:immich/devices":
			var out []map[string]any
			for _, d := range m.devices {
				if m.approved[d.ID] {
					out = append(out, map[string]any{"id": d.ID, "hostname": d.Hostname, "approved": true})
				}
			}
			writeJSON(w, map[string]any{"devices": out})
		case strings.HasPrefix(path, "/tailnet/-/services/svc:immich/device/") &&
			strings.HasSuffix(path, "/approved"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/tailnet/-/services/svc:immich/device/"), "/approved")
			if r.Method == http.MethodGet {
				writeJSON(w, map[string]any{"approved": m.approved[id]})
				return
			}
			if r.Method == http.MethodPost {
				var body struct {
					Approved bool `json:"approved"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				m.approved[id] = body.Approved
				writeJSON(w, map[string]any{"approved": body.Approved})
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/device/") && strings.HasSuffix(path, "/tags"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/device/"), "/tags")
			var body struct {
				Tags []string `json:"tags"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for i := range m.devices {
				if m.devices[i].ID == id {
					m.devices[i].Tags = body.Tags
					writeJSON(w, m.devices[i])
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func tailscaleTestServer(t *testing.T, mock *mockTailscaleAdmin) (*Server, *Config) {
	t.Helper()
	prevBase := tailscaleAdminAPIBase
	ts := httptest.NewServer(mock.handler())
	tailscaleAdminAPIBase = ts.URL + "/api/v2"
	t.Cleanup(func() {
		tailscaleAdminAPIBase = prevBase
		ts.Close()
	})

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := os.WriteFile(cfgPath, []byte(`{"services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json")); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(cfg, "https://aegis.heron-census.ts.net")
	return srv, cfg
}

func TestTailscaleAdminKey_PersistPresenceOnly(t *testing.T) {
	mock := newMockTailscaleAdmin()
	srv, cfg := tailscaleTestServer(t, mock)

	body, _ := json.Marshal(ImmichTailnetConfigRequest{
		APIKey:       "tskey-api-secret",
		HostDeviceID: "n1",
		HostTag:      "tag:immich-host",
	})
	req := httptest.NewRequest("POST", "/v1/admin/immich/tailnet/config", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("config status = %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "tskey-api-secret") {
		t.Fatal("API key must not be echoed back")
	}
	snap := cfg.TailscaleAdminSnapshot()
	if !snap.HasAPIKey || snap.HostDeviceID != "n1" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if cfg.TailscaleAdminAPIKey() != "tskey-api-secret" {
		t.Fatal("internal key store lost the value")
	}

	// Reload from disk keeps the key.
	fresh, err := LoadConfig(filepath.Join(filepath.Dir(cfg.path), "aegis.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.InitServiceConnectionsStore(filepath.Join(filepath.Dir(cfg.path), "service_connections.json")); err != nil {
		t.Fatal(err)
	}
	if fresh.TailscaleAdminAPIKey() != "tskey-api-secret" {
		t.Fatal("reload lost tailscale api key")
	}
}

func TestImmichTailnetSteps_CheckOnlyOffersFixWhereNeeded(t *testing.T) {
	mock := newMockTailscaleAdmin()
	mock.policy.TagOwners = map[string][]string{"tag:immich-host": {"autogroup:admin"}}
	mock.devices = []tailscaleDevice{
		{ID: "n1", Hostname: "photos-box", Tags: []string{"tag:servers"}},
	}
	// Service missing → create_service needs fix. Host untagged → tag_host needs fix.
	srv, cfg := tailscaleTestServer(t, mock)
	if err := cfg.SetTailscaleAdmin(tailscaleAdminEntry{
		APIKey:       "tskey-api-test",
		HostDeviceID: "n1",
	}); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(ImmichTailnetApplyRequest{Steps: []string{"create_service", "tag_host", "approve", "acl_tag", "serve"}})
	req := httptest.NewRequest("POST", "/v1/admin/immich/tailnet/apply", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("apply status = %d body=%s", w.Code, w.Body.String())
	}

	var res ImmichTailnetApplyResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	// create_service + tag_host + approve should be applied (they needed fixing).
	applied := map[string]bool{}
	for _, id := range res.Applied {
		applied[id] = true
	}
	if !applied["create_service"] || !applied["tag_host"] {
		t.Fatalf("applied = %v details=%v", res.Applied, res.Details)
	}
	// acl_tag already OK → skipped, not applied.
	skipped := map[string]bool{}
	for _, id := range res.Skipped {
		skipped[id] = true
	}
	if !skipped["acl_tag"] {
		t.Fatalf("acl_tag should be skipped (already OK), got applied=%v skipped=%v details=%v",
			res.Applied, res.Skipped, res.Details)
	}
	// serve is not auto-fixable.
	if !skipped["serve"] {
		t.Fatalf("serve should be skipped as not fixable: %+v", res)
	}

	// Second apply of the same steps should skip already-OK work.
	body2, _ := json.Marshal(ImmichTailnetApplyRequest{Steps: []string{"create_service", "tag_host"}})
	req2 := httptest.NewRequest("POST", "/v1/admin/immich/tailnet/apply", strings.NewReader(string(body2)))
	req2.Header.Set("Authorization", "Bearer test-key")
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w2, req2)
	var res2 ImmichTailnetApplyResult
	if err := json.Unmarshal(w2.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	if len(res2.Applied) != 0 {
		t.Fatalf("second apply should skip everything: %+v", res2)
	}
	if len(res2.Skipped) < 2 {
		t.Fatalf("second apply skipped = %v", res2.Skipped)
	}
}

func TestImmichTailnetCheck_WithoutKey(t *testing.T) {
	mock := newMockTailscaleAdmin()
	srv, _ := tailscaleTestServer(t, mock)

	req := httptest.NewRequest("GET", "/v1/admin/immich/setup", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("setup status = %d body=%s", w.Code, w.Body.String())
	}
	var status ImmichSetupStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.TailnetLive == nil {
		t.Fatal("expected tailnet_live")
	}
	if status.TailnetLive.HasAPIKey {
		t.Fatal("should not report a key")
	}
	if status.TailnetLive.Checked {
		t.Fatal("should not claim live checks without a key")
	}
	if status.TailnetLive.Error == "" {
		t.Fatal("expected an error explaining the missing key")
	}
	if status.Tailnet == nil || status.Tailnet.ServiceName != "svc:immich" {
		t.Fatal("expected copyable guide to remain")
	}
}

func TestImmichTailnetApply_RequiresKey(t *testing.T) {
	mock := newMockTailscaleAdmin()
	srv, _ := tailscaleTestServer(t, mock)

	body, _ := json.Marshal(ImmichTailnetApplyRequest{Steps: []string{"create_service"}})
	req := httptest.NewRequest("POST", "/v1/admin/immich/tailnet/apply", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("apply status = %d body=%s", w.Code, w.Body.String())
	}
	var res ImmichTailnetApplyResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) == 0 {
		t.Fatalf("expected failed without key: %+v", res)
	}
	// Must not have created anything.
	if len(mock.services) != 0 {
		t.Fatal("must not mutate tailnet without a key")
	}
}

func TestImmichTailnetSteps_SkipAlreadyOK(t *testing.T) {
	mock := newMockTailscaleAdmin()
	mock.policy.TagOwners = map[string][]string{"tag:immich-host": {"autogroup:admin"}}
	mock.services["svc:immich"] = tailscaleService{
		Name: "svc:immich", DisplayName: "Immich", Ports: []string{"tcp:443"},
		Tags: []string{"tag:immich-host"},
	}
	mock.devices = []tailscaleDevice{
		{ID: "n1", Hostname: "photos-box", Tags: []string{"tag:immich-host"}},
	}
	mock.approved["n1"] = true

	srv, cfg := tailscaleTestServer(t, mock)
	if err := cfg.SetTailscaleAdmin(tailscaleAdminEntry{APIKey: "tskey-api-test", HostDeviceID: "n1"}); err != nil {
		t.Fatal(err)
	}

	// Check endpoint reports all fixable steps OK and none need fix.
	req := httptest.NewRequest("POST", "/v1/admin/immich/tailnet/check", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	var live ImmichTailnetLiveStatus
	if err := json.Unmarshal(w.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if !live.Checked {
		t.Fatalf("live = %+v", live)
	}
	for _, st := range live.Steps {
		switch st.ID {
		case "acl_tag", "create_service", "tag_host", "approve":
			if !st.OK {
				t.Fatalf("step %s should be OK: %+v", st.ID, st)
			}
			if st.NeedsFix {
				t.Fatalf("step %s should not need fix: %+v", st.ID, st)
			}
		}
	}

	// Apply of already-OK steps must not call mutating methods again for them.
	before := len(mock.calls)
	body, _ := json.Marshal(ImmichTailnetApplyRequest{Steps: []string{"create_service", "tag_host", "approve"}})
	req2 := httptest.NewRequest("POST", "/v1/admin/immich/tailnet/apply", strings.NewReader(string(body)))
	req2.Header.Set("Authorization", "Bearer test-key")
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w2, req2)
	var res ImmichTailnetApplyResult
	if err := json.Unmarshal(w2.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("should skip all: %+v", res)
	}
	// Mutations would be PUT service or POST tags/approved — none expected.
	for _, call := range mock.calls[before:] {
		if strings.HasPrefix(call, "PUT ") ||
			(strings.HasPrefix(call, "POST ") && !strings.Contains(call, "/approved") && strings.Contains(call, "/tags")) {
			// allow GETs and the re-check GETs; flag unexpected PUT/tag POSTs
			if strings.HasPrefix(call, "PUT ") {
				t.Fatalf("unexpected mutation: %s", call)
			}
		}
	}
}
