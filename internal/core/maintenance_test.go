package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScrubSignalError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"email", "mail fetch failed for bob@corp.example", "mail fetch failed for user@example.com"},
		{"bearer", "upstream said Bearer sk-abc123xyz expired", "upstream said Bearer REDACTED expired"},
		{"access token", "GET /v1?access_token=abc123&q=1 failed", "GET /v1?access_token=REDACTED&q=1 failed"},
		{"api key", "auth api_key=hunter2 rejected", "auth api_key=REDACTED rejected"},
		{"json style", `body: {"token": "abc12345"}`, `body: {"token=REDACTED"}`},
		{"clean text", "spotify saved tracks returned 502", "spotify saved tracks returned 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubSignalError(tc.in); got != tc.want {
				t.Fatalf("scrubSignalError(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	long := scrubSignalError(strings.Repeat("x", signalErrorMax+100))
	if len([]rune(long)) != signalErrorMax+1 {
		t.Fatalf("truncated length = %d, want %d", len([]rune(long)), signalErrorMax+1)
	}
	if !strings.HasSuffix(long, "…") {
		t.Fatalf("truncated text must end with ellipsis: %q", long)
	}
}

func TestCollectMaintenanceSignals_BuildStamp(t *testing.T) {
	srv := adminSplitServer(t)
	sig := srv.CollectMaintenanceSignals()
	if sig.Version != Version || sig.Commit != Commit {
		t.Fatalf("build stamp = %s/%s, want %s/%s", sig.Version, sig.Commit, Version, Commit)
	}
	if sig.CapturedAt.IsZero() {
		t.Fatal("CapturedAt must be set")
	}
	if sig.Sync != nil {
		t.Fatal("Sync must be omitted when no job store is configured")
	}
}

func TestCollectMaintenanceSignals_SyncFailures(t *testing.T) {
	srv := adminSplitServer(t)
	store := testJobStore(t)
	srv.syncJobs = store
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})

	claimed, ok, err := store.Claim("tester", time.Now())
	if err != nil || !ok {
		t.Fatalf("Claim = ok=%v err=%v", ok, err)
	}
	_, err = store.Fail(claimed, SyncStep{}, fmt.Errorf(
		"%w: 401 from api.example.com for bob@corp.example (access_token=abc12345)", ErrSyncTerminal))
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}

	sig := srv.CollectMaintenanceSignals()
	if sig.Sync == nil || !sig.Sync.Enabled {
		t.Fatalf("Sync = %+v, want enabled", sig.Sync)
	}
	if sig.Sync.Counts.Failed != 1 {
		t.Fatalf("counts = %+v, want one failed", sig.Sync.Counts)
	}
	if sig.Sync.ByService["google-gmail"].Failed != 1 {
		t.Fatalf("by_service = %+v, want google-gmail/failed 1", sig.Sync.ByService)
	}
	if len(sig.Sync.Failures) != 1 {
		t.Fatalf("failures = %+v, want one entry", sig.Sync.Failures)
	}
	f := sig.Sync.Failures[0]
	if f.JobType != "ingest" || f.Service != "google-gmail" {
		t.Fatalf("failure = %+v", f)
	}
	if f.ErrorClass != SyncErrorClassTerminal {
		t.Fatalf("error class = %q, want %q", f.ErrorClass, SyncErrorClassTerminal)
	}
	if strings.Contains(f.Error, "bob@corp.example") || strings.Contains(f.Error, "abc12345") {
		t.Fatalf("failure error not scrubbed: %q", f.Error)
	}
	if !strings.Contains(f.Error, "user@example.com") || !strings.Contains(f.Error, "access_token=REDACTED") {
		t.Fatalf("failure error missing scrubbed placeholders: %q", f.Error)
	}
}

func TestWriteMaintenanceSignals(t *testing.T) {
	dir := t.TempDir()
	sig := MaintenanceSignals{
		CapturedAt: time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC),
		Version:    "dev",
		Commit:     "none",
	}
	path, err := WriteMaintenanceSignals(dir, sig)
	if err != nil {
		t.Fatalf("WriteMaintenanceSignals: %v", err)
	}
	wantPath := filepath.Join(dir, "signals-20260210T120000Z.json")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got MaintenanceSignals
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != "dev" || got.Commit != "none" {
		t.Fatalf("round-trip = %+v", got)
	}
	if _, err := WriteMaintenanceSignals("", sig); err == nil {
		t.Fatal("empty dir must error")
	}
}

func TestHandleAdminMaintenanceSignals_Get(t *testing.T) {
	srv := adminSplitServer(t)
	srv.syncJobs = testJobStore(t)

	req := adminRequest(http.MethodGet, "/v1/admin/maintenance/signals", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceSignals)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got MaintenanceSignals
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Sync == nil || !got.Sync.Enabled {
		t.Fatalf("sync = %+v, want enabled", got.Sync)
	}
}

func TestHandleAdminMaintenanceSignals_AgentKeyRejected(t *testing.T) {
	srv := adminSplitServer(t)
	req := adminRequest(http.MethodGet, "/v1/admin/maintenance/signals", "agent-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceSignals)(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("agent credentials must not read maintenance signals: %d", w.Code)
	}
}

func TestHandleAdminMaintenanceSignals_Capture(t *testing.T) {
	srv := adminSplitServer(t)
	srv.syncJobs = testJobStore(t)
	srv.dataDir = t.TempDir()

	req := adminRequest(http.MethodPost, "/v1/admin/maintenance/signals", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceSignals)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got MaintenanceCaptureResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(got.Path, filepath.Join(srv.dataDir, "maintenance")) {
		t.Fatalf("capture path = %q, want under data dir maintenance/", got.Path)
	}
	if _, err := os.Stat(got.Path); err != nil {
		t.Fatalf("captured bundle missing: %v", err)
	}
}

func TestHandleAdminMaintenanceSignals_MethodAndConfig(t *testing.T) {
	srv := adminSplitServer(t)

	req := adminRequest(http.MethodPut, "/v1/admin/maintenance/signals", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceSignals)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/maintenance/signals", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceSignals)(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("POST without data dir = %d, want 500", w.Code)
	}
}
