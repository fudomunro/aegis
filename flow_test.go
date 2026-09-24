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
		Connections: map[string]Connection{
			"spotify": {
				Name:         "Spotify",
				ClientID:     "client-id-value",
				ClientSecret: "super-secret-value",
				AuthURL:      "https://accounts.spotify.com/authorize",
				TokenURL:     "https://accounts.spotify.com/api/token",
				Scopes:       []string{"user-library-read"},
			},
			"google": {
				Name:         "Google",
				ClientID:     "yt-id",
				ClientSecret: "yt-secret",
				AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL:     "https://oauth2.googleapis.com/token",
				Scopes:       []string{"youtube.readonly"},
			},
		},
		Services: []Service{
			{
				ID:          "spotify",
				Name:        "Spotify",
				Action:      "inject",
				Endpoint:    "https://api.spotify.com/v1",
				CacheTTL:    Duration{Duration: 10 * time.Minute},
				Connection:  "spotify",
				OAuthScopes: []string{"user-library-read"},
				Permissions: Permissions{
					PermissionDefaultKey: AccessRead,
					"player":             AccessNone,
				},
			},
			{
				ID:         "youtube-music",
				Name:       "YouTube Music",
				Action:     "inject",
				Connection: "google",
			},
			{
				ID:         "google-photos",
				Name:       "Google Photos",
				Action:     "inject",
				Connection: "google",
			},
			{ID: "tidal", Name: "Tidal", Action: "block"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = NewSpotifyConnector(srv)
	srv.connectors["youtube-music"] = NewYouTubeMusicConnector(srv)
	srv.connectors["google-photos"] = NewGooglePhotosConnector(srv)
	srv.tokens.Tokens["spotify"] = &OAuthToken{
		AccessToken:  "access-token-secret",
		RefreshToken: "refresh-token-secret",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"user-library-read"},
	}
	srv.tokens.Tokens["google"] = &OAuthToken{
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
	for _, id := range []string{"agents", "aperture", "aegis", "tokens", "cache", "svc:spotify", "svc:tidal"} {
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
	// Photos shares the Google connection, so it inherits the same token status.
	if byID["svc:google-photos"].Status != FlowStatusExpired {
		t.Fatalf("photos status = %q, want expired", byID["svc:google-photos"].Status)
	}
	if byID["svc:tidal"].Status != FlowStatusBlocked {
		t.Fatalf("tidal status = %q, want blocked", byID["svc:tidal"].Status)
	}

	if byID["aegis"].Parent != "aperture" {
		t.Fatalf("aegis parent = %q, want aperture", byID["aegis"].Parent)
	}
	if byID["tokens"].Parent != "aegis" {
		t.Fatalf("tokens parent = %q, want aegis", byID["tokens"].Parent)
	}
	if byID["cache"].Parent != "aegis" {
		t.Fatalf("cache parent = %q, want aegis", byID["cache"].Parent)
	}
	if byID["cache"].Label != "Data cache" {
		t.Fatalf("cache label = %q, want Data cache", byID["cache"].Label)
	}

	// Token vault lists held connections as child nodes (issue #32 map cleanup).
	var tokenChildren []string
	for _, n := range topo.Nodes {
		if n.Parent == "tokens" && n.Kind == FlowKindToken {
			tokenChildren = append(tokenChildren, n.ID)
		}
	}
	if len(tokenChildren) == 0 {
		t.Fatal("expected token vault children for held tokens")
	}
	foundSpotifyToken := false
	for _, id := range tokenChildren {
		if id == "tok:spotify" {
			foundSpotifyToken = true
		}
	}
	if !foundSpotifyToken {
		t.Fatalf("token children = %v, want tok:spotify", tokenChildren)
	}
	if byID["tok:spotify"].Label == "" {
		t.Fatal("expected token child label")
	}
	if byID["svc:spotify"].ConnectionID == "" {
		t.Fatal("expected service connection_id for token-origin auth edges")
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
	for _, dt := range topo.DataTypes {
		if dt.Kind != "library" {
			t.Fatalf("map data type %q kind = %q, want library only (proxy chips removed)", dt.ID, dt.Kind)
		}
	}
	if len(topo.DataTypes) != len(topo.Libraries) {
		t.Fatalf("data types = %d, libraries = %d; map chips should match synced library kinds",
			len(topo.DataTypes), len(topo.Libraries))
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
		if e.From == "aperture" && e.To == "aegis" && e.Style == FlowEdgeHook {
			foundHook = true
		}
		if e.From == "aegis" && e.To == "svc:tidal" && e.Style == FlowEdgeDenied {
			foundDenied = true
		}
		if e.From == "svc:spotify" && e.To == "aegis" && e.Style == FlowEdgeLibrary {
			foundLibrary = true
		}
	}
	if !foundHook {
		t.Fatal("expected aperture → aegis hook edge")
	}
	if !foundDenied {
		t.Fatal("expected aegis → tidal denied edge")
	}
	if !foundLibrary {
		t.Fatal("expected spotify → aegis library fan-in edge")
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
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
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

func TestHandleAdminFlow_StatsBlock(t *testing.T) {
	srv := flowTestServer()
	srv.recordAccess(AccessEvent{
		Agent:    "alice@example.com",
		Surface:  MetricSurfaceService,
		Target:   "spotify",
		DataType: "tracks",
		Cache:    MetricCacheMiss,
	})
	srv.recordAccess(AccessEvent{
		Agent:    "alice@example.com",
		Surface:  MetricSurfaceLibrary,
		Target:   "playlists",
		DataType: "playlists",
		Cache:    MetricCacheHit,
	})
	srv.recordAccess(AccessEvent{
		Agent:    "bob@example.com",
		Surface:  MetricSurfaceService,
		Target:   "spotify",
		DataType: "playlists",
	})

	req := httptest.NewRequest("GET", "/v1/admin/flow?window=1h", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminFlow)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	raw := w.Body.String()
	for _, secret := range []string{
		"alice@example.com", // sample may include logins — that's ok for admin
		"super-secret-value",
		"access-token-secret",
	} {
		if secret == "alice@example.com" {
			continue
		}
		if strings.Contains(raw, secret) {
			t.Fatalf("flow leaked secret %q", secret)
		}
	}
	// Login names are admin-visible samples; tokens must not appear.
	if strings.Contains(raw, "access-token-secret") {
		t.Fatal("stats must not include OAuth tokens")
	}

	var topo FlowTopology
	if err := json.Unmarshal(w.Body.Bytes(), &topo); err != nil {
		t.Fatalf("parse flow: %v", err)
	}
	if topo.Stats.Window != "1h" {
		t.Fatalf("window = %q, want 1h", topo.Stats.Window)
	}
	if len(topo.Stats.Windows) != 3 {
		t.Fatalf("windows = %v", topo.Stats.Windows)
	}
	if topo.Stats.Agents.Distinct != 2 {
		t.Fatalf("agents = %d, want 2", topo.Stats.Agents.Distinct)
	}
	if topo.Stats.Services["spotify"].Hits != 2 {
		t.Fatalf("spotify hits = %d, want 2", topo.Stats.Services["spotify"].Hits)
	}
	if topo.Stats.Libraries["playlists"].Hits != 1 {
		t.Fatalf("playlists hits = %d, want 1", topo.Stats.Libraries["playlists"].Hits)
	}

	byID := map[string]FlowNode{}
	for _, n := range topo.Nodes {
		byID[n.ID] = n
	}
	if byID["svc:spotify"].Hits != 2 {
		t.Fatalf("spotify node hits = %d, want 2", byID["svc:spotify"].Hits)
	}
	if byID["agents"].Agents != 2 {
		t.Fatalf("agents node agents = %d, want 2", byID["agents"].Agents)
	}
}

func TestHandleAdminFlow_WindowParam(t *testing.T) {
	srv := flowTestServer()
	srv.recordAccess(AccessEvent{
		Agent:   "alice@example.com",
		Surface: MetricSurfaceService,
		Target:  "spotify",
		At:      time.Now().Add(-10 * time.Minute),
	})

	for _, tc := range []struct {
		query string
		want  string
		hits  int
	}{
		{"", "1h", 1},
		{"window=5m", "5m", 0},
		{"window=1h", "1h", 1},
		{"window=24h", "24h", 1},
		{"window=bogus", "1h", 1},
	} {
		req := httptest.NewRequest("GET", "/v1/admin/flow?"+tc.query, nil)
		req.Header.Set("X-Api-Key", "test-key")
		w := httptest.NewRecorder()
		srv.AuthMiddleware(srv.HandleAdminFlow)(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("query %q status = %d", tc.query, w.Code)
		}
		var topo FlowTopology
		if err := json.Unmarshal(w.Body.Bytes(), &topo); err != nil {
			t.Fatalf("parse %q: %v", tc.query, err)
		}
		if topo.Stats.Window != tc.want {
			t.Fatalf("query %q window = %q, want %q", tc.query, topo.Stats.Window, tc.want)
		}
		if got := topo.Stats.Services["spotify"].Hits; got != tc.hits {
			t.Fatalf("query %q spotify hits = %d, want %d", tc.query, got, tc.hits)
		}
	}
}

func TestAdminFlow_HTMLIncludesMap(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
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
	if !strings.Contains(body, "data-flow-window") {
		t.Fatal("expected access-stats window picker")
	}
	if !strings.Contains(body, "FLOW_ICONS") {
		t.Fatal("expected map icon set")
	}
	if !strings.Contains(body, "map-recent") {
		t.Fatal("expected recent access panel")
	}
	if !strings.Contains(body, "edgeStrokeWidth") {
		t.Fatal("expected hit-scaled edge width")
	}
	if !strings.Contains(body, "flowGatewayPath") {
		t.Fatal("expected simplified gateway bi-arrow")
	}
	if !strings.Contains(body, "marker-start") {
		t.Fatal("expected bidirectional gateway markers")
	}
	if !strings.Contains(body, "typeItems") {
		t.Fatal("expected data-type row access arrows")
	}
	if !strings.Contains(body, "connection_id") {
		t.Fatal("expected service connection_id for token-origin auth arrows")
	}
}
