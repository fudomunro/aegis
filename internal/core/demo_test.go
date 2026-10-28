package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDemoMode(t *testing.T) {
	t.Setenv(demoModeEnv, "")
	if resolveDemoMode(false) {
		t.Fatal("default must be off")
	}
	if !resolveDemoMode(true) {
		t.Fatal("flag must enable")
	}
	t.Setenv(demoModeEnv, "1")
	if !resolveDemoMode(false) {
		t.Fatal("env must enable")
	}
}

func TestValidateDemoConfig_RejectsLiveServices(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "spotify", Action: "inject", Endpoint: "https://api.spotify.com/v1"},
	}}
	scopes, err := NewDataScopeStore(writeDataScopes(t, []DataScope{
		{ID: "shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDemoConfig(cfg, scopes); err == nil {
		t.Fatal("live service must be rejected in demo mode")
	}
}

func TestValidateDemoConfig_RequiresScopes(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "sample-library-shire", Action: "inject", Endpoint: SampleLibraryEndpoint, Dataset: "shire"},
	}}
	empty, err := NewDataScopeStore(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDemoConfig(cfg, empty); err == nil {
		t.Fatal("demo mode must require at least one data scope")
	}
}

func TestValidateDemoConfig_AllowsSampleOnly(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "sample-library-shire", Action: "inject", Endpoint: SampleLibraryEndpoint, Dataset: "shire"},
		{ID: "sample-calendar", Action: "inject", Endpoint: "sample://calendar"},
		{ID: "blocked-live", Action: "block", Endpoint: "https://api.spotify.com/v1"},
	}}
	scopes, err := NewDataScopeStore(writeDataScopes(t, []DataScope{
		{ID: "shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDemoConfig(cfg, scopes); err != nil {
		t.Fatalf("sample-only config should pass: %v", err)
	}
}

func TestForceReadOnlyAll(t *testing.T) {
	path := writeDataScopes(t, []DataScope{
		{ID: "a", Dataset: "a", APIKey: "YOUR_A_KEY"},
		{ID: "b", Dataset: "b", APIKey: "YOUR_B_KEY", ReadOnly: true},
	})
	s, err := NewDataScopeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.ForceReadOnlyAll()
	if !s.Get("a").ReadOnly || !s.Get("b").ReadOnly {
		t.Fatal("all scopes must be read-only")
	}
	if sc := s.FindByAPIKey("YOUR_A_KEY"); sc == nil || !sc.ReadOnly {
		t.Fatalf("resolved scope = %#v", sc)
	}
}

func TestDemoLockdown_BlocksMutationsAndOAuth(t *testing.T) {
	srv := &Server{demoMode: true}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := srv.demoLockdown(inner)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/v1/library/contacts", http.StatusOK},
		{http.MethodGet, "/v1/admin/summary", http.StatusOK},
		{http.MethodPost, "/v1/mail/sync", http.StatusForbidden},
		{http.MethodPost, "/v1/library/mirror", http.StatusForbidden},
		{http.MethodPost, "/v1/admin/data-grants", http.StatusForbidden},
		{http.MethodGet, "/v1/auth/spotify/login", http.StatusForbidden},
		{http.MethodGet, "/v1/auth/status", http.StatusOK},
		{http.MethodPost, "/v1/pre-request", http.StatusOK},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Fatalf("%s %s = %d want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func TestDemoStartup_RejectsLiveService(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	raw, _ := json.Marshal(map[string]any{
		"services": []map[string]any{
			{"id": "spotify", "name": "Spotify", "action": "inject", "endpoint": "https://api.spotify.com/v1"},
		},
	})
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := NewDataScopeStore(writeDataScopes(t, []DataScope{
		{ID: "shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDemoConfig(cfg, scopes); err == nil {
		t.Fatal("expected live service rejection")
	}
}
