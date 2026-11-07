package core

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/tokens"
)

// TestMaintenanceLoop_FakeDriftToTicket drives the sync-maintenance loop
// end to end with injected drift (issue #86): a canary job against the fake
// upstream fails under a contract drift, the failure lands in a signal
// bundle, and the scanner opens a ticket with the recurrence gate applied —
// no real provider drift required.
func TestMaintenanceLoop_FakeDriftToTicket(t *testing.T) {
	fu := connectors.NewFakeUpstream()
	up := httptest.NewServer(fu)
	t.Cleanup(up.Close)

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: up.URL,
	}}}, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
	srv.syncJobs = testJobStore(t)
	srv.syncRunners = NewRunnerRegistry()
	registerCanaryRunners(srv.syncRunners, srv)

	w := NewSyncWorker(srv.syncJobs, srv.syncRunners, SyncWorkerOptions{Workers: 1, Poll: 10 * time.Millisecond})
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
	mustEnqueue(t, srv.syncJobs, SyncEnqueue{Kind: "tracks", JobType: SyncJobTypeCanary, Service: "spotify", MaxAttempts: 1})
	w.Kick()
	waitFor(t, 1, func() int { return srv.syncJobs.Counts("", "").Succeeded })
	if sig := srv.CollectMaintenanceSignals(); sig.Sync != nil && len(sig.Sync.Failures) > 0 {
		t.Fatalf("healthy canary must not fail: %+v", sig.Sync.Failures)
	}

	// Injected contract drift: an unmapped rename of items[] with total
	// intact — the silent-degradation shape this loop exists to catch.
	if err := fu.SetMode(connectors.FakeModeDriftUnmapped); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, srv.syncJobs, SyncEnqueue{Kind: "tracks", JobType: SyncJobTypeCanary, Service: "spotify", MaxAttempts: 1})
	w.Kick()
	waitFor(t, 1, func() int { return srv.syncJobs.Counts("", "").Failed })

	sig := srv.CollectMaintenanceSignals()
	if sig.Sync == nil || len(sig.Sync.Failures) != 1 {
		t.Fatalf("bundle failures = %+v", sig.Sync)
	}
	f := sig.Sync.Failures[0]
	if f.Service != "spotify" || f.JobType != SyncJobTypeCanary || f.ErrorClass != SyncErrorClassTerminal {
		t.Fatalf("failure = %+v", f)
	}
	if !strings.Contains(f.Error, "no items") {
		t.Fatalf("failure must name the silent-drift symptom: %q", f.Error)
	}

	// Two captures of the same evidence satisfy the scanner's recurrence
	// gate; a single observation would stay a metric.
	dir := t.TempDir()
	sig.CapturedAt = time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC)
	if _, err := WriteMaintenanceSignals(dir, sig); err != nil {
		t.Fatal(err)
	}
	sig.CapturedAt = time.Date(2026, 2, 10, 11, 0, 0, 0, time.UTC)
	if _, err := WriteMaintenanceSignals(dir, sig); err != nil {
		t.Fatal(err)
	}

	tracker := &fakeIssueTracker{}
	report, err := ScanMaintenanceDir(context.Background(), dir, tracker)
	if err != nil {
		t.Fatalf("ScanMaintenanceDir: %v", err)
	}
	if len(report.Tickets) != 1 || report.Tickets[0].Action != "opened" {
		t.Fatalf("scan tickets = %+v", report.Tickets)
	}
	if len(tracker.calls) != 1 {
		t.Fatalf("tracker calls = %+v", tracker.calls)
	}
	ticket := tracker.calls[0]
	if !strings.HasPrefix(ticket.Signature, "spotify|canary|terminal|") {
		t.Fatalf("signature = %q", ticket.Signature)
	}
	if ticket.Service != "spotify" || ticket.JobType != SyncJobTypeCanary ||
		ticket.ReproState != ReproStateNeedsFixture || ticket.Count != 2 {
		t.Fatalf("ticket = %+v", ticket)
	}
}
