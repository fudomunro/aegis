package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessMetrics_RecordAndSnapshotWindows(t *testing.T) {
	m := NewAccessMetrics(16)
	now := time.Now()

	m.Record(AccessEvent{At: now.Add(-2 * time.Minute), Agent: "alice@example.com", Surface: MetricSurfaceService, Target: "spotify", DataType: "tracks", Cache: MetricCacheMiss})
	m.Record(AccessEvent{At: now.Add(-10 * time.Minute), Agent: "bob@example.com", Surface: MetricSurfaceService, Target: "spotify", DataType: "playlists", Cache: MetricCacheHit})
	m.Record(AccessEvent{At: now.Add(-30 * time.Minute), Agent: "alice@example.com", Surface: MetricSurfaceLibrary, Target: "tracks", DataType: "tracks", Cache: MetricCacheMiss})
	m.Record(AccessEvent{At: now.Add(-2 * time.Hour), Agent: "carol@example.com", Surface: MetricSurfaceService, Target: "youtube-music", DataType: "tracks"})

	five := m.Snapshot(5 * time.Minute)
	if five.Agents.Distinct != 1 {
		t.Fatalf("5m agents = %d, want 1", five.Agents.Distinct)
	}
	if five.Services["spotify"].Hits != 1 {
		t.Fatalf("5m spotify hits = %d, want 1", five.Services["spotify"].Hits)
	}

	oneHour := m.Snapshot(time.Hour)
	if oneHour.Agents.Distinct != 2 {
		t.Fatalf("1h agents = %d, want 2", oneHour.Agents.Distinct)
	}
	if oneHour.Services["spotify"].Hits != 2 {
		t.Fatalf("1h spotify hits = %d, want 2", oneHour.Services["spotify"].Hits)
	}
	if oneHour.Services["spotify"].CacheHits != 1 || oneHour.Services["spotify"].CacheMiss != 1 {
		t.Fatalf("1h spotify cache = hits %d misses %d, want 1/1",
			oneHour.Services["spotify"].CacheHits, oneHour.Services["spotify"].CacheMiss)
	}
	if oneHour.Libraries["tracks"].Hits != 1 {
		t.Fatalf("1h tracks library hits = %d, want 1", oneHour.Libraries["tracks"].Hits)
	}
	if oneHour.Services["spotify"].DataTypes["tracks"] != 1 {
		t.Fatalf("1h spotify tracks type count = %d, want 1", oneHour.Services["spotify"].DataTypes["tracks"])
	}

	day := m.Snapshot(24 * time.Hour)
	if day.Services["youtube-music"].Hits != 1 {
		t.Fatalf("24h youtube hits = %d, want 1", day.Services["youtube-music"].Hits)
	}
	if day.Agents.Distinct != 3 {
		t.Fatalf("24h agents = %d, want 3", day.Agents.Distinct)
	}
}

func TestAccessMetrics_RingEviction(t *testing.T) {
	m := NewAccessMetrics(3)
	now := time.Now()
	for i := 0; i < 5; i++ {
		m.Record(AccessEvent{
			At:      now,
			Agent:   "alice@example.com",
			Surface: MetricSurfaceService,
			Target:  "svc",
		})
	}
	snap := m.Snapshot(time.Hour)
	if snap.Services["svc"].Hits != 3 {
		t.Fatalf("hits after eviction = %d, want 3", snap.Services["svc"].Hits)
	}
}

func TestAccessMetrics_NilSafe(t *testing.T) {
	var m *AccessMetrics
	m.Record(AccessEvent{Agent: "x"})
	snap := m.Snapshot(time.Hour)
	if snap.TotalHits != 0 || snap.Services == nil {
		t.Fatalf("nil metrics snapshot = %+v", snap)
	}
}

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
		w.Write([]byte(`{"ok":true}`))
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

	req := httptest.NewRequest("GET", "/v1/services/spotify/me/tracks", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleServiceProxy)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	snap := srv.metricsSnapshot(time.Hour)
	st := snap.Services["spotify"]
	if st.Hits != 1 {
		t.Fatalf("spotify hits = %d, want 1", st.Hits)
	}
	if st.DataTypes["tracks"] != 1 {
		t.Fatalf("data types = %v, want tracks=1", st.DataTypes)
	}
	if st.CacheMiss != 1 {
		t.Fatalf("cache misses = %d, want 1", st.CacheMiss)
	}
	if snap.Agents.Distinct != 1 || snap.Agents.Sample[0] != "api-key" {
		t.Fatalf("agents = %+v, want distinct 1 sample api-key", snap.Agents)
	}

	// Second request should be a cache hit.
	req2 := httptest.NewRequest("GET", "/v1/services/spotify/me/tracks", nil)
	req2.Header.Set("X-Api-Key", "test-key")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleServiceProxy)(w2, req2)
	if w2.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q, want hit", w2.Header().Get("X-Aegis-Cache"))
	}
	snap = srv.metricsSnapshot(time.Hour)
	st = snap.Services["spotify"]
	if st.Hits != 2 || st.CacheHits != 1 || st.CacheMiss != 1 {
		t.Fatalf("after cache hit stats = %+v, want hits 2 cache 1/1", st)
	}
}

func TestHandleLibraryPlaylists_RecordsServiceAndLibraryHits(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID:          "spotify",
				Name:        "Spotify",
				Action:      "inject",
				Permissions: Permissions{PermissionDefaultKey: AccessRead},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = &fakePlaylistProvider{
		playlists: []Playlist{{Name: "Mix", Sources: []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}}}},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	snap := srv.metricsSnapshot(time.Hour)
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
				ID:          "spotify",
				Name:        "Spotify",
				Action:      "inject",
				Permissions: Permissions{PermissionDefaultKey: AccessRead},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = &fakePlaylistProvider{
		playlists: []Playlist{{Name: "Mix", Sources: []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}}}},
	}

	req := httptest.NewRequest("GET", "/v1/admin/library/playlists", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminLibrary)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"playlists"`) {
		t.Fatalf("expected playlists payload, body=%s", w.Body.String())
	}

	snap := srv.metricsSnapshot(time.Hour)
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
	srv.AuthMiddleware(srv.HandleAdminLibrary)(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("cached admin status = %d body=%s", w2.Code, w2.Body.String())
	}
	if w2.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q, want hit", w2.Header().Get("X-Aegis-Cache"))
	}
	snap = srv.metricsSnapshot(time.Hour)
	if snap.TotalHits != 0 || snap.Libraries["playlists"].Hits != 0 {
		t.Fatalf("metrics after admin cache hit = total %d libraries %v, want all zero",
			snap.TotalHits, snap.Libraries)
	}

	// Agent path still records.
	req3 := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req3.Header.Set("X-Api-Key", "test-key")
	w3 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("agent status = %d body=%s", w3.Code, w3.Body.String())
	}
	snap = srv.metricsSnapshot(time.Hour)
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
		srv.AuthMiddleware(srv.HandleAdminLibrary)(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 body=%s", path, w.Code, w.Body.String())
		}
	}

	req := httptest.NewRequest("POST", "/v1/admin/library/playlists", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminLibrary)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d, want 405", w.Code)
	}
}

func TestHandleAdminRoutes_LibraryRequiresAuth(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin/library/playlists", nil)
	w := httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestMetrics_Redacted_NoBodiesOrKeys(t *testing.T) {
	m := NewAccessMetrics(8)
	m.Record(AccessEvent{
		Agent:    "api-key",
		Surface:  MetricSurfaceService,
		Target:   "spotify",
		DataType: "tracks",
		Cache:    MetricCacheHit,
	})
	raw, err := json.Marshal(m.Snapshot(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, banned := range []string{"Bearer", "secret", "access_token", "Authorization", "/me/tracks?"} {
		if strings.Contains(s, banned) {
			t.Fatalf("stats leaked %q: %s", banned, s)
		}
	}
}
