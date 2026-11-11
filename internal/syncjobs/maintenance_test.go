package syncjobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
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
	run := &Runners{Version: "dev", Commit: "none"}
	sig := run.CollectMaintenanceSignals()
	if sig.Version != "dev" || sig.Commit != "none" {
		t.Fatalf("build stamp = %s/%s, want dev/none", sig.Version, sig.Commit)
	}
	if sig.CapturedAt.IsZero() {
		t.Fatal("CapturedAt must be set")
	}
	if sig.Sync != nil {
		t.Fatal("Sync must be omitted when no job store is configured")
	}
}

func TestCollectMaintenanceSignals_SyncFailures(t *testing.T) {
	run := &Runners{Version: "dev", Commit: "none"}
	store := testJobStore(t)
	run.Jobs = store
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

	sig := run.CollectMaintenanceSignals()
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
		Hostname:   "box",
		Version:    "dev",
		Commit:     "none",
		Sync: &MaintenanceSync{
			Enabled:   true,
			Counts:    SyncCounts{Succeeded: 3, Failed: 1},
			ByService: map[string]SyncCounts{"spotify": {Succeeded: 3, Failed: 1}},
			Failures: []MaintenanceFailure{{
				ID: "j1", Kind: "tracks", JobType: "canary", Service: "spotify",
				State: "failed", ErrorClass: "terminal", Error: "boom", Attempts: 1,
				UpdatedAt: time.Date(2026, 2, 10, 11, 0, 0, 0, time.UTC),
			}},
		},
		Freshness: []copies.Freshness{{Kind: "tracks", InSync: 2, Stale: 1}},
	}
	path, err := writeMaintenanceSignals(dir, sig)
	if err != nil {
		t.Fatalf("writeMaintenanceSignals: %v", err)
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
	// Pin the file format: the maintenance tool reads these bundles as
	// plain JSON (cmd/aegis-dashboard/bundles.go mirrors these names), so a
	// rename here must fail loudly rather than silently empty its pages.
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, k := range []string{"captured_at", "hostname", "version", "commit", "sync", "freshness"} {
		if _, ok := shape[k]; !ok {
			t.Fatalf("bundle JSON missing %q: %s", k, raw)
		}
	}
	syncObj, ok := shape["sync"].(map[string]any)
	if !ok {
		t.Fatalf("bundle JSON missing sync object: %s", raw)
	}
	for _, k := range []string{"enabled", "counts", "by_service", "failures"} {
		if _, ok := syncObj[k]; !ok {
			t.Fatalf("bundle sync JSON missing %q: %s", k, raw)
		}
	}
	requireJSONKeys(t, raw, syncObj["counts"], "queue rollup", "queued", "running", "succeeded", "failed", "canceled", "paused")
	if svc, ok := syncObj["by_service"].(map[string]any)["spotify"]; ok {
		requireJSONKeys(t, raw, svc, "by_service row", "succeeded", "failed")
	} else {
		t.Fatalf("bundle sync JSON missing by_service.spotify: %s", raw)
	}
	if fails, ok := syncObj["failures"].([]any); ok && len(fails) > 0 {
		requireJSONKeys(t, raw, fails[0], "failure row", "id", "kind", "job_type", "service", "state", "error_class", "error", "attempts", "updated_at")
	} else {
		t.Fatalf("bundle sync JSON missing failure rows: %s", raw)
	}
	if fresh, ok := shape["freshness"].([]any); ok && len(fresh) > 0 {
		requireJSONKeys(t, raw, fresh[0], "freshness row", "kind", "in_sync", "stale", "missing", "unknown", "unchecked", "checked", "total")
	} else {
		t.Fatalf("bundle JSON missing freshness rows: %s", raw)
	}
	if _, err := WriteMaintenanceSignals("", sig); err == nil {
		t.Fatal("empty dir must error")
	}
}

// requireJSONKeys asserts an object carries the named keys.
func requireJSONKeys(t *testing.T, raw []byte, v any, what string, keys ...string) {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %s", what, raw)
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Fatalf("%s missing %q: %s", what, k, raw)
		}
	}
}
