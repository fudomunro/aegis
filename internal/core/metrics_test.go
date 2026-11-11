package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
)

func TestAgentForRequest_APIKeyFallback(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	if got := agentForRequest(req); got != "api-key" {
		t.Fatalf("agent = %q, want api-key", got)
	}

	id := &PeerIdentity{LoginName: "alice@example.com"}
	req = req.WithContext(req.Context())
	req = withPeerIdentity(req, id)
	if got := agentForRequest(req); got != "alice@example.com" {
		t.Fatalf("agent = %q, want alice@example.com", got)
	}
}

func TestHandleServiceProxy_RecordsAccessStats(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"pl1","name":"Jazz","tracks":{"total":1,"items":[]}}`))
	}))
	defer upstream.Close()

	cfg := &Config{
		Services: []Service{
			{
				ID:       "spotify",
				Name:     "Spotify",
				Action:   "inject",
				Endpoint: upstream.URL,
				CacheTTL: Duration{Duration: time.Minute},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{
		AccessToken: "tok",
		Expiry:      futureTime(),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/spotify/playlists/pl1", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().HandleServiceProxy)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	snap := srv.admin().MetricsSnapshot(time.Hour)
	st := snap.Services["spotify"]
	if st.Hits != 1 {
		t.Fatalf("spotify hits = %d, want 1", st.Hits)
	}
	if st.Kinds["playlists"] != 1 {
		t.Fatalf("kinds = %v, want playlists=1", st.Kinds)
	}
	if st.CacheMiss != 1 {
		t.Fatalf("cache misses = %d, want 1", st.CacheMiss)
	}
	if snap.Agents.Distinct != 1 || snap.Agents.Sample[0] != "api-key" {
		t.Fatalf("agents = %+v, want distinct 1 sample api-key", snap.Agents)
	}

	// Second request should be a cache hit.
	req2 := httptest.NewRequest("GET", "/v1/services/spotify/playlists/pl1", nil)
	req2.Header.Set("X-Api-Key", "test-key")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().HandleServiceProxy)(w2, req2)
	if w2.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q, want hit", w2.Header().Get("X-Aegis-Cache"))
	}
	snap = srv.admin().MetricsSnapshot(time.Hour)
	st = snap.Services["spotify"]
	if st.Hits != 2 || st.CacheHits != 1 || st.CacheMiss != 1 {
		t.Fatalf("after cache hit stats = %+v, want hits 2 cache 1/1", st)
	}
}

func TestHandleLibraryPlaylists_RecordsServiceAndLibraryHits(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID:     "spotify",
				Name:   "Spotify",
				Action: "inject",
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = &fakePlaylistProvider{
		playlists: []kinds.Playlist{{Name: "Mix", Sources: []kinds.PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}}}},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("playlists"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	snap := srv.admin().MetricsSnapshot(time.Hour)
	if snap.Libraries["playlists"].Hits != 1 {
		t.Fatalf("library hits = %d, want 1", snap.Libraries["playlists"].Hits)
	}
	if snap.Services["spotify"].Hits != 1 {
		t.Fatalf("provider hits = %d, want 1", snap.Services["spotify"].Hits)
	}
}

func TestHandleAdminLibrary_DoesNotRecordMapAccess(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID:     "spotify",
				Name:   "Spotify",
				Action: "inject",
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = &fakePlaylistProvider{
		playlists: []kinds.Playlist{{Name: "Mix", Sources: []kinds.PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}}}},
	}

	req := httptest.NewRequest("GET", "/v1/admin/library/playlists", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminLibrary)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"playlists"`) {
		t.Fatalf("expected playlists payload, body=%s", w.Body.String())
	}

	snap := srv.admin().MetricsSnapshot(time.Hour)
	if snap.Libraries["playlists"].Hits != 0 {
		t.Fatalf("library hits after admin view = %d, want 0", snap.Libraries["playlists"].Hits)
	}
	if snap.Services["spotify"].Hits != 0 {
		t.Fatalf("provider hits after admin view = %d, want 0", snap.Services["spotify"].Hits)
	}
	if snap.TotalHits != 0 {
		t.Fatalf("total hits after admin view = %d, want 0", snap.TotalHits)
	}

	// Cache-hit path must also stay off the Map.
	req2 := httptest.NewRequest("GET", "/v1/admin/library/playlists", nil)
	req2.Header.Set("X-Api-Key", "test-key")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminLibrary)(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("cached admin status = %d body=%s", w2.Code, w2.Body.String())
	}
	if w2.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q, want hit", w2.Header().Get("X-Aegis-Cache"))
	}
	snap = srv.admin().MetricsSnapshot(time.Hour)
	if snap.TotalHits != 0 || snap.Libraries["playlists"].Hits != 0 {
		t.Fatalf("metrics after admin cache hit = total %d libraries %v, want all zero",
			snap.TotalHits, snap.Libraries)
	}

	// Agent path still records.
	req3 := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req3.Header.Set("X-Api-Key", "test-key")
	w3 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("playlists"))(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("agent status = %d body=%s", w3.Code, w3.Body.String())
	}
	snap = srv.admin().MetricsSnapshot(time.Hour)
	if snap.Libraries["playlists"].Hits != 1 {
		t.Fatalf("library hits after agent view = %d, want 1", snap.Libraries["playlists"].Hits)
	}
}

func TestHandleAdminLibrary_UnknownKind(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	for _, path := range []string{
		"/v1/admin/library/",
		"/v1/admin/library/nope",
		"/v1/admin/library/playlists/extra",
		"/v1/admin/library/review",
	} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Api-Key", "test-key")
		w := httptest.NewRecorder()
		srv.AuthMiddleware(srv.admin().HandleAdminLibrary)(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 body=%s", path, w.Code, w.Body.String())
		}
	}

	req := httptest.NewRequest("POST", "/v1/admin/library/playlists", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminLibrary)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d, want 405", w.Code)
	}
}

func TestHandleAdminRoutes_LibraryRequiresAuth(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin/library/playlists", nil)
	w := httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
