package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/audit"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/kinds"
)

func TestRecordAccess_WritesAuditAndProm(t *testing.T) {
	dir := t.TempDir()
	a, err := audit.Open(filepath.Join(dir, "audit.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.audit = a

	srv.recordAccess(metrics.AccessEvent{
		Agent:    "alice@example.com",
		Surface:  metrics.MetricSurfaceLibrary,
		Target:   "playlists",
		Kind:     "playlists",
		Cache:    metrics.MetricCacheHit,
		Decision: audit.DecisionAllow,
		Method:   http.MethodGet,
	})
	srv.recordAccess(metrics.AccessEvent{
		Agent:    "alice@example.com",
		Surface:  metrics.MetricSurfaceService,
		Target:   "spotify",
		Kind:     "player",
		Decision: audit.DecisionDeny,
		Reason:   "permission",
		Method:   http.MethodPost,
		Status:   http.StatusForbidden,
	})

	rows, err := a.List(audit.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(rows))
	}
	snap := srv.metricsSnapshot(time.Hour)
	if snap.TotalHits != 2 {
		t.Fatalf("ring hits = %d, want 2", snap.TotalHits)
	}
	text := srv.prom.PromText()
	if !strings.Contains(text, `aegis_access_total{surface="library",target="playlists",decision="allow"} 1`) {
		t.Fatalf("missing allow series:\n%s", text)
	}
	if !strings.Contains(text, `aegis_access_total{surface="service",target="spotify",decision="deny"} 1`) {
		t.Fatalf("missing deny series:\n%s", text)
	}

	// Defaults fill agent and decision; nil sinks stay safe.
	srv.audit = nil
	srv.metrics = nil
	srv.prom = nil
	srv.recordAccess(metrics.AccessEvent{Surface: "library", Target: "tracks"})
}

func TestLibraryDeny_RecordsAudit(t *testing.T) {
	dir := t.TempDir()
	a, err := audit.Open(filepath.Join(dir, "audit.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				GrantLimits: policy.Permissions{"playlists": policy.AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.audit = a
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []kinds.Playlist{{Name: "Jazz", Sources: []kinds.PlaylistSource{{Service: "spotify", ID: "sp1"}}}},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("playlists"))(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	rows, err := a.List(audit.Filter{Decision: audit.DecisionDeny})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("expected deny audit rows for permission-denied library access")
	}
	var sawService, sawLibrary bool
	for _, row := range rows {
		if row.Agent != "api-key" {
			t.Fatalf("agent = %q, want api-key", row.Agent)
		}
		if row.Surface == metrics.MetricSurfaceService && row.Target == "spotify" && row.Kind == "playlists" {
			sawService = true
		}
		if row.Surface == metrics.MetricSurfaceLibrary && row.Target == "playlists" {
			sawLibrary = true
		}
	}
	if !sawService || !sawLibrary {
		t.Fatalf("missing deny rows service=%v library=%v rows=%+v", sawService, sawLibrary, rows)
	}
}

func TestResolveStatePaths_AuditLog(t *testing.T) {
	dir := t.TempDir()
	paths, err := ResolveStatePaths(StatePathOverrides{Dir: dir, AuditLogSet: true, AuditLog: filepath.Join(dir, "custom-audit.db")})
	if err != nil {
		t.Fatal(err)
	}
	if paths.AuditLog != filepath.Join(dir, "custom-audit.db") {
		t.Fatalf("audit.Log = %q", paths.AuditLog)
	}
	paths, err = ResolveStatePaths(StatePathOverrides{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if paths.AuditLog != filepath.Join(dir, defaultAuditLogName) {
		t.Fatalf("default audit.Log = %q", paths.AuditLog)
	}
	paths, err = ResolveStatePaths(StatePathOverrides{Dir: dir, AuditLogSet: true, AuditLog: ""})
	if err != nil {
		t.Fatal(err)
	}
	if paths.AuditLog != "" {
		t.Fatalf("disabled audit.Log = %q", paths.AuditLog)
	}
	if err := EnsureDataPaths(paths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}

// Ensure fakePlaylistProvider still satisfies the connector surface used by
// the deny path (compile-time check).
var _ kinds.LikedTrackProvider = (*fakeLikedTracks)(nil)

type fakeLikedTracks struct {
	fakePlaylistProvider
	tracks []kinds.Track
}

func (f *fakeLikedTracks) ListUserLikedTracks(context.Context) ([]kinds.Track, bool, error) {
	return f.tracks, false, nil
}

func TestAgentForRequest_ScopePrincipal(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/library/contacts", nil)
	if got := agentForRequest(req); got != "api-key" {
		t.Fatalf("bare = %q", got)
	}
	req = scopes.WithDataScope(req, &scopes.Scope{ID: "family"})
	if got := agentForRequest(req); got != "scope:family" {
		t.Fatalf("scope principal = %q", got)
	}
}

func TestRecordAccessFrom_FillsClientIP(t *testing.T) {
	a, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := &Server{
		metrics: metrics.NewAccessMetrics(8),
		audit:   a,
		config:  &Config{TrustedProxies: []string{"127.0.0.1"}, ClientIPHeader: "x-real-ip"},
	}
	if err := validateConfig(srv.config); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/library/contacts", nil)
	req.RemoteAddr = "127.0.0.1:52000"
	req.Header.Set("X-Real-IP", "203.0.113.9")
	srv.recordAccessFrom(req, metrics.AccessEvent{Agent: "scope:family", Surface: metrics.MetricSurfaceLibrary, Target: "contacts"})

	rows, err := a.List(audit.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].ClientIP != "203.0.113.9" {
		t.Fatalf("client_ip = %q, want the trusted-proxy forwarded address", rows[0].ClientIP)
	}
	if rows[0].Agent != "scope:family" {
		t.Fatalf("agent = %q", rows[0].Agent)
	}
}

func TestDenyAuditRows_CarryClientIP(t *testing.T) {
	a, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := testServerWithBase(&Config{Services: []Service{
		{ID: "sample", Name: "Sample", Action: "inject", Endpoint: "sample://library"},
	}}, "https://aegis.example.com")
	srv.audit = a

	// Auth denial: bad credential, resolved client IP must land on the row.
	req := httptest.NewRequest(http.MethodGet, "/v1/library/contacts", nil)
	req.RemoteAddr = "203.0.113.44:41000"
	req.Header.Set("X-Api-Key", "wrong-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("contacts"))(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}

	// Proxy denial: not a content path (audited — unlike the "unknown
	// service" 404, which must not leak existence).
	req = httptest.NewRequest(http.MethodPost, "/v1/services/sample/me/player", nil)
	req.RemoteAddr = "203.0.113.44:41000"
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleServiceProxy)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("proxy status = %d body=%s", w.Code, w.Body.String())
	}

	rows, err := a.List(audit.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.ClientIP != "203.0.113.44" {
			t.Fatalf("deny row missing client_ip: %+v", row)
		}
	}
}
