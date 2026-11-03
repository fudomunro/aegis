package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// fakeIssueTracker records upserts for scanner tests.
type fakeIssueTracker struct {
	calls []MaintenanceTicket
	// action/error returned per upsert; defaults "opened".
	action string
	err    error
	// records/listErr feed ListIssues for the dashboard's fix history.
	records []MaintenanceIssueRecord
	listErr error
}

func (f *fakeIssueTracker) UpsertTicket(_ context.Context, t MaintenanceTicket) (IssueRef, string, error) {
	f.calls = append(f.calls, t)
	if f.err != nil {
		return IssueRef{}, "", f.err
	}
	action := f.action
	if action == "" {
		action = "opened"
	}
	return IssueRef{Number: len(f.calls), URL: "https://github.com/example/repo/issues/1"}, action, nil
}

func (f *fakeIssueTracker) ListIssues(_ context.Context) ([]MaintenanceIssueRecord, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.records, nil
}

func TestNormalizeErrorShape(t *testing.T) {
	a := normalizeErrorShape("spotify saved tracks returned 502")
	b := normalizeErrorShape("Spotify saved tracks returned 503")
	if a != b {
		t.Fatalf("status codes must cluster: %q vs %q", a, b)
	}
	c := normalizeErrorShape(`GET https://api.spotify.com/v1/me failed for "bob" id=0123456789abcdef`)
	d := normalizeErrorShape(`GET https://api.spotify.com/v1/you failed for "alice" id=fedcba9876543210`)
	if c != d {
		t.Fatalf("urls/ids/names must cluster: %q vs %q", c, d)
	}
	if normalizeErrorShape("") != "" {
		t.Fatal("empty stays empty")
	}
}

func scanBundle(captured time.Time, failures ...MaintenanceFailure) MaintenanceSignals {
	return MaintenanceSignals{
		CapturedAt: captured,
		Version:    "dev",
		Commit:     "none",
		Sync:       &MaintenanceSync{Enabled: true, Failures: failures},
	}
}

func scanFailure(jobType, service, class, errText string) MaintenanceFailure {
	return MaintenanceFailure{
		JobType:    jobType,
		Service:    service,
		ErrorClass: class,
		State:      SyncJobFailed,
		Error:      errText,
	}
}

func mustWriteBundle(t *testing.T, dir string, sig MaintenanceSignals) {
	t.Helper()
	if _, err := WriteMaintenanceSignals(dir, sig); err != nil {
		t.Fatalf("WriteMaintenanceSignals: %v", err)
	}
}

func TestScanMaintenanceDir_RecurrenceGate(t *testing.T) {
	dir := t.TempDir()
	tracker := &fakeIssueTracker{}

	// One bundle only: flake, must stay a metric.
	mustWriteBundle(t, dir, scanBundle(time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC),
		scanFailure("ingest", "spotify", SyncErrorClassTerminal, "spotify saved tracks returned 502")))

	report, err := ScanMaintenanceDir(context.Background(), dir, tracker)
	if err != nil {
		t.Fatalf("ScanMaintenanceDir: %v", err)
	}
	if report.ScannedBundles != 1 || report.Clusters != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Tickets) != 1 || report.Tickets[0].Action != "skipped" {
		t.Fatalf("one-off failure must skip: %+v", report.Tickets)
	}
	if len(tracker.calls) != 0 {
		t.Fatalf("flake must never reach the tracker: %+v", tracker.calls)
	}

	// Same signature in a second bundle: now it is a ticket.
	mustWriteBundle(t, dir, scanBundle(time.Date(2026, 2, 10, 11, 0, 0, 0, time.UTC),
		scanFailure("ingest", "spotify", SyncErrorClassTerminal, "Spotify saved tracks returned 503")))

	report, err = ScanMaintenanceDir(context.Background(), dir, tracker)
	if err != nil {
		t.Fatalf("ScanMaintenanceDir: %v", err)
	}
	if len(report.Tickets) != 1 || report.Tickets[0].Action != "opened" ||
		report.Tickets[0].Number != 1 || report.Tickets[0].URL == "" {
		t.Fatalf("recurrent failure must open: %+v", report.Tickets)
	}
	if len(tracker.calls) != 1 {
		t.Fatalf("upserts = %+v", tracker.calls)
	}
	tk := tracker.calls[0]
	if tk.Count != 2 || tk.ReproState != ReproStateNeedsFixture || tk.Tier != scanDefaultTier {
		t.Fatalf("ticket = %+v", tk)
	}
	if tk.Service != "spotify" || tk.JobType != "ingest" || tk.ErrorClass != SyncErrorClassTerminal {
		t.Fatalf("ticket identity = %+v", tk)
	}
	if len(tk.Observations) != 2 {
		t.Fatalf("observations = %+v", tk.Observations)
	}
	if !tk.FirstSeen.Equal(time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC)) ||
		!tk.LastSeen.Equal(time.Date(2026, 2, 10, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("seen window = %v .. %v", tk.FirstSeen, tk.LastSeen)
	}
}

func TestScanMaintenanceDir_FreshnessFromLatestBundle(t *testing.T) {
	dir := t.TempDir()
	tracker := &fakeIssueTracker{}
	sig := scanBundle(time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC))
	sig.Freshness = []CopyFreshness{
		{Kind: "tracks", InSync: 8, Stale: 2, Missing: 1},
		{Kind: "messages", InSync: 5},
	}
	mustWriteBundle(t, dir, sig)

	report, err := ScanMaintenanceDir(context.Background(), dir, tracker)
	if err != nil {
		t.Fatalf("ScanMaintenanceDir: %v", err)
	}
	// State-like signal: one bundle is enough.
	if len(tracker.calls) != 1 || tracker.calls[0].Kind != "tracks" {
		t.Fatalf("stale/missing kind must ticket once: %+v (report %+v)", tracker.calls, report)
	}
	for _, ref := range report.Tickets {
		if ref.Signature == freshnessSignature("messages") && ref.Action != "skipped" {
			t.Fatalf("healthy kind must not ticket: %+v", ref)
		}
	}
}

func TestScanMaintenanceDir_NilTrackerAndTrackerError(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		mustWriteBundle(t, dir, scanBundle(base.Add(time.Duration(i)*time.Hour),
			scanFailure("ingest", "spotify", SyncErrorClassTerminal, "spotify saved tracks returned 502")))
	}

	report, err := ScanMaintenanceDir(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("nil tracker: %v", err)
	}
	if len(report.Tickets) != 1 || report.Tickets[0].Action != "disabled" {
		t.Fatalf("nil tracker must report disabled: %+v", report.Tickets)
	}

	broken := &fakeIssueTracker{err: errors.New("gh unavailable")}
	report, err = ScanMaintenanceDir(context.Background(), dir, broken)
	if err != nil {
		t.Fatalf("tracker errors must not sink the scan: %v", err)
	}
	if len(report.Tickets) != 1 || report.Tickets[0].Action != "error" ||
		report.Tickets[0].Error == "" {
		t.Fatalf("tracker error must surface fail-open: %+v", report.Tickets)
	}
}

func TestScanMaintenanceDir_NoBundles(t *testing.T) {
	report, err := ScanMaintenanceDir(context.Background(), t.TempDir(), &fakeIssueTracker{})
	if err != nil {
		t.Fatalf("empty dir must not error: %v", err)
	}
	if report.ScannedBundles != 0 || report.Clusters != 0 || len(report.Tickets) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestHandleAdminMaintenanceScan(t *testing.T) {
	srv := adminSplitServer(t)
	srv.dataDir = t.TempDir()
	srv.maintenanceIssues = &fakeIssueTracker{}
	bundleDir := filepath.Join(srv.dataDir, dataDirMaintenanceSubdir)
	mustWriteBundle(t, bundleDir, scanBundle(time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC),
		scanFailure("ingest", "spotify", SyncErrorClassTerminal, "spotify saved tracks returned 502")))
	mustWriteBundle(t, bundleDir, scanBundle(time.Date(2026, 2, 10, 11, 0, 0, 0, time.UTC),
		scanFailure("ingest", "spotify", SyncErrorClassTerminal, "spotify saved tracks returned 502")))

	req := adminRequest(http.MethodPost, "/v1/admin/maintenance/scan", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceScan)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var report MaintenanceScanReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.ScannedBundles != 2 || len(report.Tickets) != 1 || report.Tickets[0].Action != "opened" {
		t.Fatalf("report = %+v", report)
	}

	req = adminRequest(http.MethodGet, "/v1/admin/maintenance/scan", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceScan)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/maintenance/scan", "agent-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceScan)(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("agent credentials must not run scans")
	}
}
