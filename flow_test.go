package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func flowTestServer() *Server {
	cfg := &Config{
		Services: []Service{
			{
				ID:       "spotify",
				Name:     "Spotify",
				Action:   "inject",
				Endpoint: "https://api.spotify.com/v1",
				CacheTTL: Duration{Duration: 10 * time.Minute},
				Permissions: Permissions{
					PermissionDefaultKey: AccessRead,
					"player":             AccessNone,
				},
				OAuth: &OAuthConfig{
					ClientID:     "client-id-value",
					ClientSecret: "super-secret-value",
					Scopes:       []string{"user-library-read"},
				},
			},
			{
				ID:     "youtube-music",
				Name:   "YouTube Music",
				Action: "inject",
				OAuth: &OAuthConfig{
					ClientID:     "yt-id",
					ClientSecret: "yt-secret",
				},
			},
			{
				ID:     "google-photos",
				Name:   "Google Photos",
				Action: "inject",
				OAuth: &OAuthConfig{
					ClientID:     "gp-id",
					ClientSecret: "gp-secret",
				},
			},
			{ID: "tidal", Name: "Tidal", Action: "block"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors["spotify"] = NewSpotifyConnector(srv)
	srv.connectors["youtube-music"] = NewYouTubeMusicConnector(srv)
	srv.connectors["google-photos"] = NewGooglePhotosConnector(srv)
	srv.tokens.Tokens["spotify"] = &OAuthToken{
		AccessToken:  "access-token-secret",
		RefreshToken: "refresh-token-secret",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"user-library-read"},
	}
	srv.tokens.Tokens["youtube-music"] = &OAuthToken{
		AccessToken:  "yt-access-secret",
		RefreshToken: "yt-refresh-secret",
		Expiry:       time.Now().Add(-time.Hour),
		Scopes:       []string{"youtube.readonly"},
	}
	return srv
}

func TestHandleAdminFlow_Topology(t *testing.T) {
	srv := flowTestServer()

	req := httptest.NewRequest("GET", "/v1/admin/flow", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminFlow)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	raw := w.Body.String()
	for _, secret := range []string{
		"super-secret-value",
		"access-token-secret",
		"refresh-token-secret",
		"client-id-value",
		"yt-access-secret",
		"yt-refresh-secret",
		"gp-secret",
	} {
		if strings.Contains(raw, secret) {
			t.Fatalf("flow response leaked secret %q", secret)
		}
	}

	var topo FlowTopology
	if err := json.Unmarshal(w.Body.Bytes(), &topo); err != nil {
		t.Fatalf("parse flow: %v", err)
	}

	if topo.Runtime.AuthMode != AuthModeAPIKey {
		t.Fatalf("auth mode = %q", topo.Runtime.AuthMode)
	}
	if topo.Narrative == "" {
		t.Fatal("expected narrative")
	}

	byID := map[string]FlowNode{}
	for _, n := range topo.Nodes {
		byID[n.ID] = n
	}
	for _, id := range []string{"agents", "aperture", "guardian", "tokens", "cache", "svc:spotify", "svc:tidal"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing node %q", id)
		}
	}

	if byID["svc:spotify"].Status != FlowStatusConnected {
		t.Fatalf("spotify status = %q, want connected", byID["svc:spotify"].Status)
	}
	if byID["svc:youtube-music"].Status != FlowStatusExpired {
		t.Fatalf("youtube status = %q, want expired", byID["svc:youtube-music"].Status)
	}
	if byID["svc:google-photos"].Status != FlowStatusNeedsAuth {
		t.Fatalf("photos status = %q, want needs-auth", byID["svc:google-photos"].Status)
	}
	if byID["svc:tidal"].Status != FlowStatusBlocked {
		t.Fatalf("tidal status = %q, want blocked", byID["svc:tidal"].Status)
	}

	if byID["guardian"].Parent != "aperture" {
		t.Fatalf("guardian parent = %q, want aperture", byID["guardian"].Parent)
	}
	if byID["tokens"].Parent != "guardian" {
		t.Fatalf("tokens parent = %q, want guardian", byID["tokens"].Parent)
	}
	if byID["cache"].Parent != "guardian" {
		t.Fatalf("cache parent = %q, want guardian", byID["cache"].Parent)
	}

	hasLibraryType := false
	hasReadablePlaylists := false
	for _, dt := range topo.DataTypes {
		if dt.Kind == "library" {
			hasLibraryType = true
		}
		if dt.ID == "playlists" && dt.Kind == "library" && dt.Readable && len(dt.Providers) > 0 {
			hasReadablePlaylists = true
		}
	}
	if !hasLibraryType {
		t.Fatal("expected library data types in topology")
	}
	if !hasReadablePlaylists {
		t.Fatal("expected readable playlists data type with providers")
	}

	hasPhotoNote := false
	for _, note := range byID["svc:google-photos"].Notes {
		if strings.Contains(strings.ToLower(note), "photo bytes") {
			hasPhotoNote = true
		}
	}
	if !hasPhotoNote {
		t.Fatal("expected photo-bytes note on google-photos node")
	}

	foundHook := false
	foundDenied := false
	foundLibrary := false
	for _, e := range topo.Edges {
		if e.From == "aperture" && e.To == "guardian" && e.Style == FlowEdgeHook {
			foundHook = true
		}
		if e.From == "guardian" && e.To == "svc:tidal" && e.Style == FlowEdgeDenied {
			foundDenied = true
		}
		if e.From == "svc:spotify" && e.To == "guardian" && e.Style == FlowEdgeLibrary {
			foundLibrary = true
		}
	}
	if !foundHook {
		t.Fatal("expected aperture → guardian hook edge")
	}
	if !foundDenied {
		t.Fatal("expected guardian → tidal denied edge")
	}
	if !foundLibrary {
		t.Fatal("expected spotify → guardian library fan-in edge")
	}

	var playlists *FlowLibrary
	for i := range topo.Libraries {
		if topo.Libraries[i].ID == "playlists" {
			playlists = &topo.Libraries[i]
		}
	}
	if playlists == nil {
		t.Fatal("expected playlists library entry")
	}
	if len(playlists.Providers) == 0 {
		t.Fatal("expected playlist providers")
	}
	for _, p := range playlists.Providers {
		if p == "tidal" {
			t.Fatal("blocked services must not appear as library providers")
		}
	}
}

func TestHandleAdminFlow_MethodNotAllowed(t *testing.T) {
	srv := flowTestServer()
	req := httptest.NewRequest("POST", "/v1/admin/flow", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminFlow)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestAdminFlow_RouteRequiresAuth(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	req := httptest.NewRequest("GET", "/v1/admin/flow", nil)
	w := httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}

	req = httptest.NewRequest("GET", "/v1/admin/flow", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestHandleAdmin_HTMLIncludesMap(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="tab-map"`) {
		t.Fatal("expected Map tab")
	}
	if !strings.Contains(body, "/v1/admin/flow") {
		t.Fatal("expected flow API reference")
	}
	if !strings.Contains(body, "map-svg-wrap") {
		t.Fatal("expected map svg container")
	}
}
