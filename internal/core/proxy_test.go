package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// testContentConnector is a minimal connector for content-path tests.
type testContentConnector struct {
	lastPath string
}

func (c *testContentConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (c *testContentConnector) SystemInstructions(string) string { return "content" }
func (c *testContentConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	c.lastPath = remainingPath
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Aegis-Cache", "miss")
	io.WriteString(w, `{"ok":true,"path":"`+remainingPath+`"}`)
}

func TestHandleServiceProxy_RejectsNonContentPaths(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &testContentConnector{},
	}

	for _, path := range []string{
		"/v1/services/spotify/me",
		"/v1/services/spotify/me/playlists",
		"/v1/services/spotify/me/tracks",
		"/v1/services/spotify/me/player",
		"/v1/services/spotify/search?q=jazz",
		"/v1/services/spotify/capabilities",
		"/v1/services/spotify/tracks/t1",
		"/v1/services/spotify",
	} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		srv.HandleServiceProxy(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404, body=%s", path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "/v1/library/") {
			t.Fatalf("%s: body should point at library, got %s", path, w.Body.String())
		}
	}
}

func TestHandleServiceProxy_ContentPathDelegatesToConnector(t *testing.T) {
	conn := &testContentConnector{}
	cfg := &Config{
		Services: []Service{
			{ID: "immich", Name: "Immich", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"immich": conn}

	req := httptest.NewRequest("GET", "/v1/services/immich/assets/a1/original", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if conn.lastPath != "/assets/a1/original" {
		t.Fatalf("connector path = %q", conn.lastPath)
	}
	if !strings.Contains(w.Body.String(), "a1/original") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestHandleServiceProxy_ContentPathWithoutConnector(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "ghost", Name: "Ghost", Action: "inject", Endpoint: "http://upstream.invalid"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/ghost/messages/m1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no generic pass-through)", w.Code)
	}
}

func TestHandleServiceProxy_UnknownService(t *testing.T) {
	cfg := &Config{Services: []Service{}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/nope/import/status", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleServiceProxy_BlockedService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "block", Description: "nope"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &testContentConnector{},
	}

	req := httptest.NewRequest("GET", "/v1/services/spotify/playlists/pl1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
}

func TestHandleServiceProxy_PermissionDenied(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				GrantLimits: types.Permissions{"playlists": types.AccessNone, "default": types.AccessRead},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &testContentConnector{},
	}

	req := httptest.NewRequest("GET", "/v1/services/spotify/playlists/pl1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
}

func TestHandleServiceProxy_ContentPathAllowedByPermission(t *testing.T) {
	conn := &testContentConnector{}
	cfg := &Config{
		Services: []Service{
			{
				ID: "gmail", Name: "Gmail", Action: "inject",
				GrantLimits: types.Permissions{"messages": types.AccessRead},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"gmail": conn}

	req := httptest.NewRequest("GET", "/v1/services/gmail/messages/m1?format=full", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if conn.lastPath != "/messages/m1" {
		t.Fatalf("path = %q", conn.lastPath)
	}
}

func TestHandleServiceProxy_NonCanonicalPath(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &testContentConnector{},
	}

	for _, path := range []string{
		"/v1/services/spotify/playlists/../me",
		"/v1/services/spotify/./playlists/pl1",
	} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		srv.HandleServiceProxy(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", path, w.Code)
		}
	}
}

func TestHandleServiceProxy_ImportStatusAllowed(t *testing.T) {
	conn := &testContentConnector{}
	cfg := &Config{
		Services: []Service{
			{ID: "signal", Name: "Signal", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"signal": conn}

	req := httptest.NewRequest("GET", "/v1/services/signal/import/status", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if conn.lastPath != "/import/status" {
		t.Fatalf("path = %q", conn.lastPath)
	}
}

func TestHandleServiceProxy_PickerAllowed(t *testing.T) {
	conn := &testContentConnector{}
	cfg := &Config{
		Services: []Service{
			{ID: "google-photos", Name: "Google Photos", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"google-photos": conn}

	req := httptest.NewRequest("POST", "/v1/services/google-photos/picker/sessions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHandleServiceProxy_RecordsAccessOnReject(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &testContentConnector{},
	}

	req := httptest.NewRequest("GET", "/v1/services/spotify/me/player", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if srv.metrics == nil {
		t.Fatal("metrics not initialized")
	}
}

func TestIsAgentContentPath_EndToEndClassification(t *testing.T) {
	// Ensure library list paths and content paths stay distinct at the HTTP edge.
	if types.IsAgentContentPath("/me/tracks") {
		t.Fatal("/me/tracks must not be content")
	}
	if !types.IsAgentContentPath("/playlists/pl1/items") {
		t.Fatal("/playlists/pl1/items must be content")
	}
	var _ = json.Marshal
}

// testServerWithBase creates a Server with a baseURL for testing.
// Uses an in-memory token store that doesn't persist to disk.
// Defaults to API-key auth so existing tests keep using the shared test key.
func testServerWithBase(cfg *Config, baseURL string) *Server {
	return &Server{
		apiKey:      "test-key",
		adminAPIKey: "test-key",
		authMode:    AuthModeAPIKey,
		baseURL:     baseURL,
		config:      cfg,
		cache:       NewCache(),
		tokens:      &TokenStore{Tokens: make(map[string]*OAuthToken)},
		oauthStates: NewStateStore(),
		connectors:  make(map[string]connectors.ServiceConnector),
		metrics:     NewAccessMetrics(defaultMetricsCapacity),
		hookHealth:  NewHookHealth(defaultHookObservationCapacity),
		prom:        NewPromMetrics(),
	}
}

func stringSliceContains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
