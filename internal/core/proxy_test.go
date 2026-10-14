package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestHandleServiceProxy_GetForwarding(t *testing.T) {
	// Mock upstream service.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("upstream got method %q, want GET", r.Method)
		}
		if r.URL.Path != "/playlists" {
			t.Errorf("upstream got path %q, want /playlists", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"playlists":["jazz","rock"]}`)
	}))
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: upstream.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("cache header = %q, want miss", w.Header().Get("X-Aegis-Cache"))
	}
	body := w.Body.String()
	if !strings.Contains(body, "jazz") {
		t.Fatalf("response body = %q, expected jazz", body)
	}
}

func TestHandleServiceProxy_CacheHit(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		io.WriteString(w, `{"cached":true}`)
	}))
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: upstream.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/data", nil)
	req.Header.Set("Authorization", "Bearer test-key")

	// First request — miss.
	w := httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)
	if w.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("first request: cache = %q, want miss", w.Header().Get("X-Aegis-Cache"))
	}

	// Second request — hit.
	w = httptest.NewRecorder()
	srv.HandleServiceProxy(w, req)
	if w.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("second request: cache = %q, want hit", w.Header().Get("X-Aegis-Cache"))
	}

	if callCount != 1 {
		t.Fatalf("upstream called %d times, want 1", callCount)
	}
}

func TestHandleServiceProxy_ErrorGETNotCached(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"upstream boom"}`)
	}))
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: upstream.URL, CacheTTL: Duration{Duration: time.Hour}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/data", nil)
	req.Header.Set("Authorization", "Bearer test-key")

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		srv.HandleServiceProxy(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("request %d: status = %d, want 500", i, w.Code)
		}
		if w.Header().Get("X-Aegis-Cache") != "miss" {
			t.Fatalf("request %d: cache = %q, want miss (errors must not cache)", i, w.Header().Get("X-Aegis-Cache"))
		}
	}
	if callCount != 2 {
		t.Fatalf("upstream called %d times, want 2", callCount)
	}
}

func TestHandleServiceProxy_PostNotCached(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		// Echo the body back.
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: upstream.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	postBody := `{"query":"jazz playlists"}`
	req := httptest.NewRequest("POST", "/v1/services/spotify/search", strings.NewReader(postBody))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")

	// Two POSTs should both hit upstream.
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		srv.HandleServiceProxy(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, w.Code)
		}
	}

	if callCount != 2 {
		t.Fatalf("upstream called %d times, want 2 (POST not cached)", callCount)
	}
}

func TestHandleServiceProxy_ServiceNotFound(t *testing.T) {
	cfg := &Config{Services: []Service{}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/nonexistent/data", nil)
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
			{ID: "tidal", Name: "Tidal", Action: "block", Description: "not allowed"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/tidal/data", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}

	var errResp map[string]string
	json.NewDecoder(w.Body).Decode(&errResp)
	if !strings.Contains(errResp["error"], "Tidal") {
		t.Fatalf("error = %q, expected mention of Tidal", errResp["error"])
	}
}

func TestHandleServiceProxy_NoEndpoint(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/data", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestHandleServiceProxy_UpstreamDown(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: "http://127.0.0.1:1"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/data", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestHandleServiceProxy_ServiceHeadersApplied(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{
			{
				ID:       "spotify",
				Name:     "Spotify",
				Action:   "inject",
				Endpoint: upstream.URL,
				Headers:  map[string]string{"Authorization": "Bearer upstream-token"},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/data", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if gotAuth != "Bearer upstream-token" {
		t.Fatalf("upstream got auth %q, want %q", gotAuth, "Bearer upstream-token")
	}
}

func TestHandleServiceProxy_QueryParamsForwarded(t *testing.T) {
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: upstream.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/search?q=jazz&limit=10", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if gotQuery != "q=jazz&limit=10" {
		t.Fatalf("upstream query = %q, want %q", gotQuery, "q=jazz&limit=10")
	}
}

func TestHandleServiceProxy_RootPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("upstream path = %q, want /", r.URL.Path)
		}
		io.WriteString(w, `{"status":"ok"}`)
	}))
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: upstream.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHandleServiceProxy_Unauthorized(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/data", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()

	srv.AuthMiddleware(srv.HandleServiceProxy)(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandleServiceProxy_Capabilities_Spotify(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)

	req := httptest.NewRequest("GET", "/v1/services/spotify/capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var caps types.ServiceCapabilities
	json.NewDecoder(w.Body).Decode(&caps)

	if len(caps.DataTypes) == 0 {
		t.Fatal("expected data types in capabilities")
	}

	// Find "tracks" and verify it has "read" and "search".
	var tracksCap *types.DataTypeCapability
	for i, dt := range caps.DataTypes {
		if dt.Name == "tracks" {
			tracksCap = &caps.DataTypes[i]
			break
		}
	}
	if tracksCap == nil {
		t.Fatal("expected tracks data type")
	}

	// Verify tracks has structured operations.
	opNames := make([]string, len(tracksCap.Operations))
	for i, op := range tracksCap.Operations {
		opNames[i] = op.Name
	}
	if !stringSliceContains(opNames, "read_saved") {
		t.Fatal("expected tracks to have read_saved operation")
	}
	if !stringSliceContains(opNames, "read_top") {
		t.Fatal("expected tracks to have read_top operation")
	}

	// Verify read_saved operation has method and path.
	var readSaved *types.Operation
	for i, op := range tracksCap.Operations {
		if op.Name == "read_saved" {
			readSaved = &tracksCap.Operations[i]
			break
		}
	}
	if readSaved == nil {
		t.Fatal("expected read_saved operation")
	}
	if readSaved.Method != "GET" {
		t.Fatalf("method = %s, want GET", readSaved.Method)
	}
	if readSaved.Path != "/me/tracks" {
		t.Fatalf("path = %s, want /me/tracks", readSaved.Path)
	}
	if len(readSaved.Parameters) == 0 {
		t.Fatal("expected parameters for read_saved")
	}

	// Verify search data type has the search operation.
	var searchCap *types.DataTypeCapability
	for i, dt := range caps.DataTypes {
		if dt.Name == "search" {
			searchCap = &caps.DataTypes[i]
			break
		}
	}
	if searchCap == nil {
		t.Fatal("expected search data type")
	}
	if len(searchCap.Operations) != 1 || searchCap.Operations[0].Name != "search" {
		t.Fatalf("search operations = %v", searchCap.Operations)
	}
	if searchCap.Operations[0].Method != "GET" {
		t.Fatalf("search method = %s", searchCap.Operations[0].Method)
	}
}

func TestHandleServiceProxy_Capabilities_AmazonMusic(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "amazon-music", Name: "Amazon Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["amazon-music"] = connectors.NewAmazonMusicConnector(srv)

	req := httptest.NewRequest("GET", "/v1/services/amazon-music/capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var caps types.ServiceCapabilities
	json.NewDecoder(w.Body).Decode(&caps)

	if len(caps.DataTypes) == 0 {
		t.Fatal("expected data types for amazon music")
	}

	// Amazon Music should have data types but no operations.
	for _, dt := range caps.DataTypes {
		if len(dt.Operations) > 0 {
			t.Fatalf("amazon music %s should have no operations, got %v", dt.Name, dt.Operations)
		}
	}
}

func TestHandleServiceProxy_Capabilities_NoConnector(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "deezer", Name: "Deezer", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/deezer/capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleServiceProxy_PermissionDenied(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: types.Permissions{"default": types.AccessRead, "player": types.AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)

	req := httptest.NewRequest("GET", "/v1/services/spotify/me/player", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

// Issue #94: non-canonical request paths must not change the resolved data
// type or the effective permission.
func TestHandleServiceProxy_NonCanonicalPathCannotBypassPermissions(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: types.Permissions{"default": types.AccessRead, "player": types.AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
	handler := srv.AuthMiddleware(srv.HandleServiceProxy)

	// %2f decodes to "/" inside URL.Path, producing "//me/player".
	bypasses := []string{
		"/v1/services/spotify/%2fme%2fplayer",
		"/v1/services/spotify/%2fme%2fplayer%2f",
		"/v1/services/spotify//me/player",
		"/v1/services/spotify///me//player",
	}
	for _, target := range bypasses {
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403 (player:none), body = %s", target, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "permission denied") {
			t.Fatalf("%s: body = %s", target, w.Body.String())
		}
	}

	// Dot segments are rejected outright — they must not reach classification.
	rejected := []string{
		"/v1/services/spotify/me/./player",
		"/v1/services/spotify/me/../player",
		"/v1/services/spotify/./me/player",
		"/v1/services/spotify/../me/player",
		"/v1/services/spotify/me/player/..",
	}
	for _, target := range rejected {
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400, body = %s", target, w.Code, w.Body.String())
		}
	}

	// Canonical player path is still denied by the same policy.
	req := httptest.NewRequest("GET", "/v1/services/spotify/me/player", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("canonical player: status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
}

// Issue #94: unknown paths must not inherit a more permissive default when
// the service has restrictive type-level permissions.
func TestHandleServiceProxy_UnknownPathUsesStrictestPermission(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: types.Permissions{"default": types.AccessRead, "player": types.AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
	handler := srv.AuthMiddleware(srv.HandleServiceProxy)

	req := httptest.NewRequest("GET", "/v1/services/spotify/unknown/endpoint", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestHandleServiceProxy_Capabilities_FilteredByPermission(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: types.Permissions{"player": types.AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)

	req := httptest.NewRequest("GET", "/v1/services/spotify/capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var caps types.ServiceCapabilities
	json.NewDecoder(w.Body).Decode(&caps)
	for _, dt := range caps.DataTypes {
		if dt.Name == "player" {
			t.Fatal("player data type should be filtered from capabilities")
		}
	}
}

// testServerWithBase creates a Server with a baseURL for testing.
// Uses an in-memory token store that doesn't persist to disk.
// Defaults to API-key auth so existing tests keep using the shared test key.
func testServerWithBase(cfg *Config, baseURL string) *Server {
	return &Server{
		apiKey:      "test-key",
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
