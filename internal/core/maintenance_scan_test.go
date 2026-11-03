package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	// One bundle only: flake, must stay a metric.
	mustWriteBundle(t, dir, scanBundle(time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC),
		scanFailure("ingest", "spotify", SyncErrorClassTerminal, "spotify saved tracks returned 502")))

	report, err := ScanMaintenanceDir(dir)
	if err != nil {
		t.Fatalf("ScanMaintenanceDir: %v", err)
	}
	if report.ScannedBundles != 1 || report.Clusters != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Tickets) != 1 || report.Tickets[0].Action != "skipped" {
		t.Fatalf("one-off failure must skip: %+v", report.Tickets)
	}
	if tickets, _ := ListMaintenanceTickets(dir); len(tickets) != 0 {
		t.Fatalf("no ticket file may exist for a flake: %+v", tickets)
	}

	// Same signature in a second bundle: now it is a ticket.
	mustWriteBundle(t, dir, scanBundle(time.Date(2026, 2, 10, 11, 0, 0, 0, time.UTC),
		scanFailure("ingest", "spotify", SyncErrorClassTerminal, "Spotify saved tracks returned 503")))

	report, err = ScanMaintenanceDir(dir)
	if err != nil {
		t.Fatalf("ScanMaintenanceDir: %v", err)
	}
	if len(report.Tickets) != 1 || report.Tickets[0].Action != "opened" {
		t.Fatalf("recurrent failure must open: %+v", report.Tickets)
	}
	tickets, err := ListMaintenanceTickets(dir)
	if err != nil || len(tickets) != 1 {
		t.Fatalf("ListMaintenanceTickets = %+v, %v", tickets, err)
	}
	tk := tickets[0]
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
	sig := scanBundle(time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC))
	sig.Freshness = []CopyFreshness{
		{Kind: "tracks", InSync: 8, Stale: 2, Missing: 1},
		{Kind: "messages", InSync: 5},
	}
	mustWriteBundle(t, dir, sig)

	report, err := ScanMaintenanceDir(dir)
	if err != nil {
		t.Fatalf("ScanMaintenanceDir: %v", err)
	}
	// State-like signal: one bundle is enough.
	found := false
	for _, ref := range report.Tickets {
		if ref.Action == "opened" && ref.Signature == freshnessSignature("tracks") {
			found = true
		}
		if ref.Signature == freshnessSignature("messages") {
			t.Fatalf("healthy kind must not ticket: %+v", ref)
		}
	}
	if !found {
		t.Fatalf("stale/missing kind must open: %+v", report.Tickets)
	}
}

func TestScanMaintenanceDir_UpdatePreservesTriage(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		mustWriteBundle(t, dir, scanBundle(base.Add(time.Duration(i)*time.Hour),
			scanFailure("ingest", "spotify", SyncErrorClassTerminal, "spotify saved tracks returned 502")))
	}
	if _, err := ScanMaintenanceDir(dir); err != nil {
		t.Fatalf("scan: %v", err)
	}
	tickets, err := ListMaintenanceTickets(dir)
	if err != nil || len(tickets) != 1 {
		t.Fatalf("tickets = %+v, %v", tickets, err)
	}

	// Simulate triage attaching a repro bundle.
	triaged := tickets[0]
	triaged.Title = "Spotify saved-tracks parse regression"
	triaged.Tier = "fixture"
	triaged.ReproState = ReproStateReady
	triaged.Fixture = "internal/connectors/testdata/repro/spotify/track-object-null-items/fixture.json"
	triaged.ReproTest = "TestSpotifyLikedTracks_ReproFixture"
	triaged.Expected = "2 tracks"
	triaged.Actual = "0 tracks"
	path := filepath.Join(dir, ticketsDirName, ticketFileName(triaged.Signature))
	raw, _ := json.MarshalIndent(triaged, "", "  ")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// A third observation arrives; rescan.
	mustWriteBundle(t, dir, scanBundle(base.Add(2*time.Hour),
		scanFailure("ingest", "spotify", SyncErrorClassTerminal, "spotify saved tracks returned 502")))
	report, err := ScanMaintenanceDir(dir)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if report.Tickets[0].Action != "updated" {
		t.Fatalf("action = %+v", report.Tickets[0])
	}
	tickets, _ = ListMaintenanceTickets(dir)
	got := tickets[0]
	if got.Count != 3 {
		t.Fatalf("count = %d, want 3", got.Count)
	}
	if got.Title != triaged.Title || got.Tier != triaged.Tier || got.ReproState != ReproStateReady {
		t.Fatalf("triage clobbered: %+v", got)
	}
	if got.Fixture != triaged.Fixture || got.ReproTest != triaged.ReproTest ||
		got.Expected != triaged.Expected || got.Actual != triaged.Actual {
		t.Fatalf("repro bundle clobbered: %+v", got)
	}
	if !got.FirstSeen.Equal(base) {
		t.Fatalf("first_seen = %v, want earliest %v", got.FirstSeen, base)
	}
}

func TestScanMaintenanceDir_NoBundles(t *testing.T) {
	report, err := ScanMaintenanceDir(t.TempDir())
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

func TestHandleAdminMaintenanceTickets(t *testing.T) {
	srv := adminSplitServer(t)
	srv.dataDir = t.TempDir()
	bundleDir := filepath.Join(srv.dataDir, dataDirMaintenanceSubdir)
	for i := 0; i < 2; i++ {
		mustWriteBundle(t, bundleDir, scanBundle(time.Date(2026, 2, 10, 10+i, 0, 0, 0, time.UTC),
			scanFailure("ingest", "spotify", SyncErrorClassTerminal, "spotify saved tracks returned 502")))
	}
	if _, err := ScanMaintenanceDir(bundleDir); err != nil {
		t.Fatalf("scan: %v", err)
	}

	req := adminRequest(http.MethodGet, "/v1/admin/maintenance/tickets", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceTickets)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Tickets []MaintenanceTicket `json:"tickets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Tickets) != 1 {
		t.Fatalf("tickets = %+v", got.Tickets)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/maintenance/tickets", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceTickets)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
}
