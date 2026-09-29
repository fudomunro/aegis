package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceConnectionsOverlay_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	bootstrap := `{
  "services": [
    {
      "id": "google-photos",
      "name": "Google Photos",
      "action": "inject",
      "canonical_for": ["photos"]
    }
  ]
}`
	if err := os.WriteFile(cfgPath, []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	svcConnPath := filepath.Join(dir, "service_connections.json")
	if err := cfg.InitServiceConnectionsStore(svcConnPath); err != nil {
		t.Fatalf("InitServiceConnectionsStore: %v", err)
	}

	err = cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Name:        "Immich",
		Action:      "inject",
		Endpoint:    "http://photos.test:2283/api",
		Headers:     map[string]string{"X-API-Key": "secret-key"},
		CanonicalFor: []string{"photos"},
		CacheTTL:    "5m",
	})
	if err != nil {
		t.Fatalf("SetServiceConnection: %v", err)
	}

	// Claim moves to Immich; Google Photos loses photos.
	for _, svc := range cfg.ServiceSnapshots() {
		if svc.ID == "immich" {
			if !svc.IsCanonicalFor("photos") {
				t.Fatalf("immich should own photos: %+v", svc.CanonicalFor)
			}
			if svc.Endpoint != "http://photos.test:2283/api" {
				t.Fatalf("endpoint = %q", svc.Endpoint)
			}
			if !svc.HasStaticAPIKey() {
				t.Fatal("expected static API key")
			}
		}
		if svc.ID == "google-photos" && svc.IsCanonicalFor("photos") {
			t.Fatal("google-photos should not still claim photos")
		}
	}

	// Reload from disk keeps the overlay and does not echo secrets via snapshots
	// more than the in-memory service already carries for auth.
	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig reload: %v", err)
	}
	immich := fresh.GetService("immich")
	if immich == nil {
		t.Fatal("immich missing after reload")
	}
	if immich.Endpoint != "http://photos.test:2283/api" {
		t.Fatalf("reload endpoint = %q", immich.Endpoint)
	}
	if !immich.IsCanonicalFor("photos") {
		t.Fatal("reload lost canonical claim")
	}
	if gp := fresh.GetService("google-photos"); gp != nil && gp.IsCanonicalFor("photos") {
		t.Fatal("reload restored google-photos claim")
	}
	if immich.Headers["X-API-Key"] != "secret-key" {
		t.Fatal("reload lost API key header")
	}
}

func TestSetServiceConnection_ClearCanonical(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := os.WriteFile(cfgPath, []byte(`{"services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json")); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Endpoint:     "http://x/api",
		CanonicalFor: []string{"photos"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Endpoint:          "http://x/api",
		ClearCanonicalFor: true,
	}); err != nil {
		t.Fatal(err)
	}
	svc := cfg.GetService("immich")
	if svc == nil || svc.IsCanonicalFor("photos") {
		t.Fatalf("claim should be cleared: %+v", svc)
	}
}

func TestHandleAdminImmichSetup_StatusAndConfig(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/me"):
			if r.Header.Get("X-API-Key") != "good-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"id": "u1"})
		case strings.HasSuffix(r.URL.Path, "/server/ping"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"res":"pong"}`))
		case strings.HasSuffix(r.URL.Path, "/search/metadata"):
			if r.Header.Get("X-API-Key") != "good-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"assets": map[string]any{
					"total": 1,
					"items": []map[string]any{
						{
							"id":               "asset-1",
							"type":             "IMAGE",
							"originalFileName": "beach.jpg",
							"fileCreatedAt":    "2024-06-01T12:00:00Z",
							"checksum":         "abc123",
							"exifInfo": map[string]any{
								"dateTimeOriginal": "2024-06-01T12:00:00Z",
								"exifImageWidth":   100,
								"exifImageHeight":  80,
								"description":      "sun",
							},
						},
					},
					"nextPage": nil,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mock.Close()

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

	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors[immichServiceID] = NewImmichConnector(srv)

	// Unauthenticated → 401
	req := httptest.NewRequest("GET", "/v1/admin/immich/setup", nil)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", w.Code)
	}

	// Save config from the admin UI.
	body, _ := json.Marshal(ImmichConfigRequest{
		Endpoint:                mock.URL + "/api",
		APIKey:                  "good-key",
		ClaimCanonicalForPhotos: true,
	})
	req = httptest.NewRequest("POST", "/v1/admin/immich/config", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("config status = %d body=%s", w.Code, w.Body.String())
	}
	var status ImmichSetupStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Configured || !status.HasAPIKey {
		t.Fatalf("status = %+v", status)
	}
	if len(status.CanonicalFor) != 1 || status.CanonicalFor[0] != "photos" {
		t.Fatalf("canonical_for = %v", status.CanonicalFor)
	}
	if strings.Contains(w.Body.String(), "good-key") {
		t.Fatal("API key must not be echoed back")
	}

	// Checks should pass against the mock Immich.
	req = httptest.NewRequest("POST", "/v1/admin/immich/check", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("check status = %d body=%s", w.Code, w.Body.String())
	}
	status = ImmichSetupStatus{}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.LibraryStatus != "ok" || status.LibraryCount != 1 {
		t.Fatalf("library = %+v", status)
	}
	wantOK := map[string]bool{
		"service_configured": true,
		"api_key":            true,
		"canonical_owner":    true,
		"endpoint_reachable": true,
		"library_listing":    true,
		"canonical_stamp":    true,
		"bytes_stay_upstream": true,
	}
	for _, c := range status.Checks {
		if wantOK[c.ID] && !c.OK {
			t.Fatalf("check %s failed: %s", c.ID, c.Detail)
		}
	}

	// Test endpoint with bad key.
	bad, _ := json.Marshal(ImmichTestRequest{Endpoint: mock.URL + "/api", APIKey: "nope"})
	req = httptest.NewRequest("POST", "/v1/admin/immich/test", strings.NewReader(string(bad)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	var testRes ImmichTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &testRes); err != nil {
		t.Fatal(err)
	}
	if testRes.OK {
		t.Fatalf("bad key should fail: %+v", testRes)
	}
}

func TestImmichSetupStatus_NotConfigured(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	status := srv.immichSetupStatus(context.Background(), true)
	if status.Configured {
		t.Fatal("expected not configured")
	}
	if len(status.Checks) == 0 {
		t.Fatal("expected checks")
	}
	for _, c := range status.Checks {
		if c.ID == "service_configured" && c.OK {
			t.Fatal("service_configured should fail")
		}
		if c.ID == "bytes_stay_upstream" && !c.OK {
			t.Fatal("product rule check should pass")
		}
	}
	if status.Snippet == "" || !strings.Contains(status.Snippet, "immich") {
		t.Fatalf("snippet = %q", status.Snippet)
	}
}

func TestImmichProbe_TimeoutAndEmpty(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	res := srv.probeImmich(context.Background(), "", "x")
	if res.OK {
		t.Fatal("empty endpoint should fail")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	res = srv.probeImmich(ctx, "http://127.0.0.1:1/api", "x")
	if res.OK {
		t.Fatal("unreachable should fail")
	}
}

func TestHandleAdminImmichSetup_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("POST", "/v1/admin/immich/setup", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
	req = httptest.NewRequest("GET", "/v1/admin/immich/nope", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminImmichSetup)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestAdminHTML_ImmichSetupPanel(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	body := w.Body.String()
	for _, want := range []string{
		`id="immich-setup-panel"`,
		`id="immich-endpoint"`,
		`id="immich-api-key"`,
		`id="immich-claim-canonical"`,
		`id="immich-newcomer"`,
		`id="immich-next-step"`,
		`id="immich-steps"`,
		`id="immich-starter-env"`,
		`id="copy-immich-env"`,
		`id="immich-api-permissions"`,
		`id="immich-go-install"`,
		`id="copy-immich-go-install"`,
		`brew install immich-go`,
		`Get photos into Immich`,
		`immich-go`,
		`takeout.google.com`,
		`asset.read`,
		`asset.upload`,
		`album.create`,
		`job.create`,
		`One API key for Aegis + immich-go`,
		`asset.view`,
		`asset.download`,
		`album.read`,
		`user.read`,
		`UPLOAD_LOCATION=./library`,
		`empty section between colons`,
		`ERR_CONNECTION_REFUSED`,
		`CREATE DATABASE immich`,
		`New to Immich?`,
		`docs.immich.app/overview/quick-start`,
		`/v1/admin/immich/setup`,
		`/v1/admin/immich/config`,
		`/v1/admin/immich/check`,
		`/v1/admin/immich/test`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin HTML missing %s", want)
		}
	}
}

func TestNormalizeImmichEndpoint(t *testing.T) {
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
		got, changed := normalizeImmichEndpoint(tc.in)
		if got != tc.want {
			t.Fatalf("normalizeImmichEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if changed != tc.changed {
			t.Fatalf("normalizeImmichEndpoint(%q) changed=%v, want %v", tc.in, changed, tc.changed)
		}
	}
}

func TestImmichGuidedSteps_NextStep(t *testing.T) {
	status := ImmichSetupStatus{}
	steps := immichGuidedSteps(&status)
	if len(steps) != 4 {
		t.Fatalf("steps = %d", len(steps))
	}
	if steps[0].ID != "install" || !steps[0].Current {
		t.Fatalf("first current step = %+v", steps[0])
	}
	next := immichNextStepFrom(steps, &ImmichTestResponse{
		OK: false, Code: immichCodeUnreachable, Hint: "Start Immich with Docker.",
	}, false)
	if next != "Start Immich with Docker." {
		t.Fatalf("next = %q", next)
	}

	status = ImmichSetupStatus{
		Configured: true,
		HasAPIKey:  true,
		LastProbe:  &ImmichTestResponse{OK: true, Code: immichCodeOK},
		Checks: []ImmichSetupCheck{
			{ID: "service_configured", OK: true},
			{ID: "api_key", OK: true},
			{ID: "bytes_stay_upstream", OK: true},
		},
	}
	steps = immichGuidedSteps(&status)
	if !steps[3].OK {
		t.Fatalf("verify should be ok: %+v", steps[3])
	}
	next = immichNextStepFrom(steps, nil, false)
	if !strings.Contains(next, "complete") {
		t.Fatalf("next = %q", next)
	}
	// Empty library gets seed guidance rather than a bare "run checks".
	next = immichNextStepFrom(immichGuidedSteps(&ImmichSetupStatus{
		Configured: true, HasAPIKey: true,
		LastProbe: &ImmichTestResponse{OK: true},
		LibraryStatus: "ok", LibraryCount: 0,
	}), nil, true)
	if !strings.Contains(next, "no photos") && !strings.Contains(next, "Takeout") {
		t.Fatalf("empty-library next = %q", next)
	}
}

func TestProbeImmich_ActionableCodes(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	// HTML on /server/ping → wrong_path / not_immich with hint.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<!doctype html><html><body>Not found</body></html>`))
	}))
	defer mock.Close()
	res := srv.probeImmich(context.Background(), mock.URL+"/photos/abc", "")
	if res.OK {
		t.Fatal("html page should fail")
	}
	if res.Code != immichCodeWrongPath && res.Code != immichCodeNotImmich {
		t.Fatalf("code = %q", res.Code)
	}
	if res.Hint == "" {
		t.Fatal("expected hint")
	}

	// Unreachable host → unreachable + install hint.
	res = srv.probeImmich(context.Background(), "http://127.0.0.1:1/api", "")
	if res.OK || res.Code != immichCodeUnreachable {
		t.Fatalf("unreachable probe = %+v", res)
	}
	if !strings.Contains(res.Hint, "Docker") && !strings.Contains(res.Hint, "Immich") {
		t.Fatalf("hint = %q", res.Hint)
	}
}

func TestImmichStarterEnv_File(t *testing.T) {
	data, err := os.ReadFile("docs/immich-starter.env")
	if err != nil {
		t.Fatalf("read starter env: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"UPLOAD_LOCATION=./library",
		"DB_DATA_LOCATION=./postgres",
		"IMMICH_VERSION=v3",
		"DB_PASSWORD=postgres",
		"DB_USERNAME=postgres",
		"DB_DATABASE_NAME=immich",
		"named .env",
		"ERR_CONNECTION_REFUSED",
		"CREATE DATABASE immich",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("starter env missing %q", want)
		}
	}
	if strings.Count(s, "DB_DATABASE_NAME=") != 1 {
		t.Fatal("DB_DATABASE_NAME should appear once")
	}
}

func TestImmichAPIKeyPermissions_SingleKeyUnion(t *testing.T) {
	need := []string{
		"asset.read", "asset.view", "asset.download",
		"album.read", "user.read",
		"asset.upload", "album.create", "job.create",
	}
	set := map[string]bool{}
	for _, p := range immichAPIKeyPermissions {
		set[p] = true
	}
	for _, n := range need {
		if !set[n] {
			t.Fatalf("missing permission %q in %v", n, immichAPIKeyPermissions)
		}
	}
	if !strings.Contains(immichAPIKeyPermissionsSummary(), "asset.upload") {
		t.Fatal("summary should include upload")
	}
}
