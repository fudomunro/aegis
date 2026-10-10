package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenAuditLog_Disabled(t *testing.T) {
	a, err := OpenAuditLog("", 30)
	if err != nil {
		t.Fatal(err)
	}
	if a.Enabled() || a.Path() != "" {
		t.Fatalf("empty path should disable: enabled=%v path=%q", a.Enabled(), a.Path())
	}
	a.Write(AuditEvent{Agent: "x", Surface: "library", Target: "tracks", Decision: DecisionAllow})
	if n := a.Count(); n != 0 {
		t.Fatalf("disabled write should no-op, count=%d", n)
	}
	if rows, err := a.List(AuditFilter{}); err != nil || rows != nil {
		t.Fatalf("disabled list = %v %v", rows, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var nilLog *AuditLog
	if nilLog.Enabled() || nilLog.Path() != "" || nilLog.Retention() != 0 || nilLog.Count() != 0 {
		t.Fatal("nil audit log should be inert")
	}
	nilLog.Write(AuditEvent{})
	if err := nilLog.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := nilLog.List(AuditFilter{}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditLog_WriteListAndRedaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	a, err := OpenAuditLog(path, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.Enabled() || a.Path() != path {
		t.Fatalf("enabled=%v path=%q", a.Enabled(), a.Path())
	}
	if a.Retention() != 30*24*time.Hour {
		t.Fatalf("retention = %v", a.Retention())
	}

	a.Write(AccessEventToAudit(AccessEvent{
		Agent:    "alice@example.com",
		Surface:  MetricSurfaceLibrary,
		Target:   "tracks",
		DataType: "tracks",
		Cache:    MetricCacheMiss,
		Decision: DecisionAllow,
		Method:   http.MethodGet,
		Status:   200,
	}))
	a.Write(AccessEventToAudit(AccessEvent{
		Agent:    "bob@example.com",
		Surface:  MetricSurfaceService,
		Target:   "spotify",
		DataType: "player",
		Decision: DecisionDeny,
		Reason:   "permission",
		Method:   http.MethodPost,
		Status:   403,
	}))
	// Empty agent/decision defaults.
	a.Write(AuditEvent{Surface: "hook", Target: "hook"})

	if n := a.Count(); n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}

	all, err := a.List(AuditFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("list = %d rows", len(all))
	}
	// Newest first.
	if all[0].Surface != "hook" {
		t.Fatalf("newest = %+v", all[0])
	}
	if all[0].Agent != "api-key" || all[0].Decision != DecisionAllow {
		t.Fatalf("defaults = %+v", all[0])
	}

	denied, err := a.List(AuditFilter{Decision: DecisionDeny})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied) != 1 || denied[0].Agent != "bob@example.com" || denied[0].Reason != "permission" {
		t.Fatalf("denied = %+v", denied)
	}

	byAgent, err := a.List(AuditFilter{Agent: "alice@example.com", Target: "tracks"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 1 {
		t.Fatalf("byAgent = %+v", byAgent)
	}

	// Redaction: rows never carry token material.
	for _, row := range all {
		blob := row.Agent + row.Target + row.Reason + row.DataType + row.Cache + row.Method
		for _, bad := range []string{"Bearer ", "eyJ", "refresh_token", "access_token"} {
			if strings.Contains(blob, bad) {
				t.Fatalf("audit row looks like it holds secret material: %+v", row)
			}
		}
	}

	// Limit clamps; unknown filters return empty.
	if _, err := a.List(AuditFilter{Limit: 0}); err != nil {
		t.Fatal(err)
	}
	if rows, err := a.List(AuditFilter{Agent: "nobody"}); err != nil || len(rows) != 0 {
		t.Fatalf("unknown agent rows=%v err=%v", rows, err)
	}
}

func TestAuditLog_RetentionPrune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	a, err := OpenAuditLog(path, 1) // 1 day
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.pruneEvery = 0 // force prune on every write

	a.Write(AuditEvent{
		At:       time.Now().Add(-48 * time.Hour),
		Agent:    "old",
		Surface:  "library",
		Target:   "tracks",
		Decision: DecisionAllow,
	})
	a.Write(AuditEvent{
		At:       time.Now(),
		Agent:    "new",
		Surface:  "library",
		Target:   "tracks",
		Decision: DecisionAllow,
	})
	if n := a.Count(); n != 1 {
		t.Fatalf("count after prune = %d, want 1", n)
	}
	rows, err := a.List(AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Agent != "new" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAuditLog_RowCap(t *testing.T) {
	dir := t.TempDir()
	a, err := OpenAuditLog(filepath.Join(dir, "audit.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.maxRows = 3
	a.pruneEvery = 0

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		a.Write(AuditEvent{
			At:       base.Add(time.Duration(i) * time.Minute),
			Agent:    "a",
			Surface:  "library",
			Target:   "tracks",
			Decision: DecisionAllow,
		})
	}
	if n := a.Count(); n > 3 {
		t.Fatalf("count = %d, want <= 3 after cap prune", n)
	}
}

func TestRecordAccess_WritesAuditAndProm(t *testing.T) {
	dir := t.TempDir()
	a, err := OpenAuditLog(filepath.Join(dir, "audit.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.audit = a

	srv.recordAccess(AccessEvent{
		Agent:    "alice@example.com",
		Surface:  MetricSurfaceLibrary,
		Target:   "playlists",
		DataType: "playlists",
		Cache:    MetricCacheHit,
		Decision: DecisionAllow,
		Method:   http.MethodGet,
	})
	srv.recordAccess(AccessEvent{
		Agent:    "alice@example.com",
		Surface:  MetricSurfaceService,
		Target:   "spotify",
		DataType: "player",
		Decision: DecisionDeny,
		Reason:   "permission",
		Method:   http.MethodPost,
		Status:   http.StatusForbidden,
	})

	rows, err := a.List(AuditFilter{Limit: 10})
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
	text := srv.prom.promText()
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
	srv.recordAccess(AccessEvent{Surface: "library", Target: "tracks"})
}

func TestLibraryDeny_RecordsAudit(t *testing.T) {
	dir := t.TempDir()
	a, err := OpenAuditLog(filepath.Join(dir, "audit.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: Permissions{"playlists": AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.audit = a
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []Playlist{{Name: "Jazz", Sources: []PlaylistSource{{Service: "spotify", ID: "sp1"}}}},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	rows, err := a.List(AuditFilter{Decision: DecisionDeny})
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
		if row.Surface == MetricSurfaceService && row.Target == "spotify" && row.DataType == "playlists" {
			sawService = true
		}
		if row.Surface == MetricSurfaceLibrary && row.Target == "playlists" {
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
		t.Fatalf("AuditLog = %q", paths.AuditLog)
	}
	paths, err = ResolveStatePaths(StatePathOverrides{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if paths.AuditLog != filepath.Join(dir, defaultAuditLogName) {
		t.Fatalf("default AuditLog = %q", paths.AuditLog)
	}
	paths, err = ResolveStatePaths(StatePathOverrides{Dir: dir, AuditLogSet: true, AuditLog: ""})
	if err != nil {
		t.Fatal(err)
	}
	if paths.AuditLog != "" {
		t.Fatalf("disabled AuditLog = %q", paths.AuditLog)
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
var _ LikedTrackProvider = (*fakeLikedTracks)(nil)

type fakeLikedTracks struct {
	fakePlaylistProvider
	tracks []Track
}

func (f *fakeLikedTracks) ListUserLikedTracks(context.Context) ([]Track, bool, error) {
	return f.tracks, false, nil
}
