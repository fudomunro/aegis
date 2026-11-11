package core

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/syncjobs"
)

// TestMaintenanceLoop_FakeDriftToSignals drives the loop's capture half end
// to end with injected drift (issue #86): a canary job against the fake
// upstream fails under a contract drift and the failure lands — scrubbed —
// in a signal bundle. The scanner that clusters and tickets this evidence
// lives with the maintenance tool outside the application (issue #344) and
// reads the bundles as files.
func TestMaintenanceLoop_FakeDriftToSignals(t *testing.T) {
	fu := connectors.NewFakeUpstream()
	up := httptest.NewServer(fu)
	t.Cleanup(up.Close)

	dir := t.TempDir()
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: up.URL,
	}}}, "https://aegis.example.com")
	srv.dataDir = dir
	srv.tokens.Save("spotify", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
	srv.syncJobs = testJobStore(t)
	srv.syncRunners = syncjobs.NewRunnerRegistry()
	srv.runners().Register(srv.syncRunners)

	w := syncjobs.NewSyncWorker(srv.syncJobs, srv.syncRunners, syncjobs.SyncWorkerOptions{Workers: 1, Poll: 10 * time.Millisecond})
	w.Start()
	t.Cleanup(w.Stop)

	waitFor := func(t *testing.T, want int, counts func() int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if counts() == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d (have %d)", want, counts())
	}

	// Healthy fake: the canary succeeds and the bundle is clean.
	mustEnqueue(t, srv.syncJobs, syncjobs.SyncEnqueue{Kind: "tracks", JobType: syncjobs.SyncJobTypeCanary, Service: "spotify", MaxAttempts: 1})
	w.Kick()
	waitFor(t, 1, func() int { return srv.syncJobs.Counts("", "").Succeeded })
	if sig := srv.runners().CollectMaintenanceSignals(); sig.Sync != nil && len(sig.Sync.Failures) > 0 {
		t.Fatalf("healthy canary must not fail: %+v", sig.Sync.Failures)
	}

	// Injected contract drift: an unmapped rename of items[] with total
	// intact — the silent-degradation shape this loop exists to catch.
	if err := fu.SetMode(connectors.FakeModeDriftUnmapped); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, srv.syncJobs, syncjobs.SyncEnqueue{Kind: "tracks", JobType: syncjobs.SyncJobTypeCanary, Service: "spotify", MaxAttempts: 1})
	w.Kick()
	waitFor(t, 1, func() int { return srv.syncJobs.Counts("", "").Failed })

	sig := srv.runners().CollectMaintenanceSignals()
	if sig.Sync == nil || len(sig.Sync.Failures) != 1 {
		t.Fatalf("bundle failures = %+v", sig.Sync)
	}
	f := sig.Sync.Failures[0]
	if f.Service != "spotify" || f.JobType != syncjobs.SyncJobTypeCanary || f.ErrorClass != syncjobs.SyncErrorClassTerminal {
		t.Fatalf("failure = %+v", f)
	}
	if !strings.Contains(f.Error, "no items") {
		t.Fatalf("failure must name the silent-drift symptom: %q", f.Error)
	}

	// The capture writes the evidence as a self-contained bundle file for
	// the out-of-process scanner and dashboard tool to read.
	sig.CapturedAt = time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC)
	path, err := srv.runners().WriteMaintenanceSignals(sig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("bundle path = %q, want under %q", path, dir)
	}
}
