package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dashTime(h int) time.Time {
	return time.Date(2026, 1, 2, h, 0, 0, 0, time.UTC)
}

func dashBundle(at time.Time, counts map[string]SyncCounts, failures []MaintenanceFailure) MaintenanceSignals {
	sig := MaintenanceSignals{CapturedAt: at, Version: "dev", Commit: "none"}
	if counts != nil {
		sig.Sync = &MaintenanceSync{Enabled: true, ByService: counts}
	}
	if failures != nil {
		if sig.Sync == nil {
			sig.Sync = &MaintenanceSync{Enabled: true}
		}
		sig.Sync.Failures = failures
	}
	return sig
}

func TestBuildMaintenanceDashboard_RecordMarks(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	series := []MaintenanceSignals{
		dashBundle(dashTime(0), map[string]SyncCounts{
			"spotify": {Succeeded: 10},
		}, nil),
		dashBundle(dashTime(6), map[string]SyncCounts{
			"spotify": {Succeeded: 15, Failed: 1},
		}, []MaintenanceFailure{
			{ID: "j1", Service: "spotify", JobType: "canary", UpdatedAt: dashTime(3)},
		}),
	}
	issues := []MaintenanceIssueRecord{
		{
			Number: 1, Title: "spotify ingest failed: 502", State: "closed", StateReason: "completed",
			IsTicket: true, TicketService: "spotify", FirstSeen: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ClosedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			Number: 2, Title: "spotify canary failed", State: "open",
			IsTicket: true, TicketService: "spotify", FirstSeen: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
			CreatedAt: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		},
		{
			Number: 3, Title: "spotify paging postmortem", State: "closed", StateReason: "not_planned",
			IsTicket: true, TicketService: "spotify", FirstSeen: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC),
			CreatedAt: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC),
		},
	}
	data := BuildMaintenanceDashboard(series, []DashboardServiceMeta{{ID: "spotify"}}, issues, nil, now)
	if len(data.Services) != 1 {
		t.Fatalf("services = %v, want one", data.Services)
	}
	svc := data.Services[0]
	// Fix rate: 1 resolved of 3 recorded.
	if svc.Scores.Success == nil || *svc.Scores.Success < 33 || *svc.Scores.Success > 34 {
		t.Fatalf("success = %v, want ~33.3", svc.Scores.Success)
	}
	// Last failure 40 days before now: the 30-day quiet window is full.
	if svc.Scores.Stability == nil || *svc.Scores.Stability != 100 {
		t.Fatalf("stability = %v, want 100", svc.Scores.Stability)
	}
	if svc.Scores.Confidence != "medium" {
		t.Fatalf("confidence = %q, want medium (3 items)", svc.Scores.Confidence)
	}
	// Capture history still builds from the series alongside the record.
	if len(svc.History) != 2 || len(svc.Failures) != 1 {
		t.Fatalf("history = %+v failures = %+v", svc.History, svc.Failures)
	}
	// Last activity spans captures and record events: the third ticket's
	// failure onset (Jan 20) is newer than the last capture (Jan 2).
	if !svc.LastSeen.Equal(time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("last seen = %v", svc.LastSeen)
	}
}

func TestScoreDashboardRecord_QuietnessAndDepth(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	twoDaysAgo := now.Add(-48 * time.Hour)
	s := scoreDashboardRecord([]DashboardFix{{State: "fixed", FirstSeen: twoDaysAgo}}, now)
	if s.Success == nil || *s.Success != 100 {
		t.Fatalf("success = %v, want 100", s.Success)
	}
	// 2 quiet days of a 30-day window.
	if s.Stability == nil || *s.Stability < 6 || *s.Stability > 7 {
		t.Fatalf("stability = %v, want ~6.7", s.Stability)
	}
	if s.Confidence != "low" {
		t.Fatalf("confidence = %q, want low on one item", s.Confidence)
	}

	if s := scoreDashboardRecord(nil, now); s.Confidence != "none" || s.Success != nil {
		t.Fatalf("empty record = %+v, want none marks", s)
	}
	if got := recordConfidence(10); got != "high" {
		t.Fatalf("recordConfidence(10) = %q", got)
	}
}

func TestBuildMaintenanceDashboard_CounterResetAndDedup(t *testing.T) {
	series := []MaintenanceSignals{
		dashBundle(dashTime(0), map[string]SyncCounts{
			"spotify": {Succeeded: 30, Failed: 2},
		}, []MaintenanceFailure{
			{ID: "j1", Service: "spotify", JobType: "ingest", UpdatedAt: dashTime(0), Error: "boom"},
		}),
		dashBundle(dashTime(6), map[string]SyncCounts{
			"spotify": {Succeeded: 5},
		}, []MaintenanceFailure{
			// Same job, later re-failure: one event, latest time wins.
			{ID: "j1", Service: "spotify", JobType: "ingest", UpdatedAt: dashTime(6), Error: "boom again"},
		}),
	}
	data := BuildMaintenanceDashboard(series, nil, nil, nil, dashTime(6))
	svc := data.Services[0]
	if !svc.History[1].CounterReset {
		t.Fatalf("capture 2 must flag the counter reset: %+v", svc.History)
	}
	if len(svc.Failures) != 1 {
		t.Fatalf("failures = %+v, want deduped to one", svc.Failures)
	}
	if !svc.Failures[0].UpdatedAt.Equal(dashTime(6)) || svc.Failures[0].Error != "boom again" {
		t.Fatalf("dedup kept the wrong record: %+v", svc.Failures[0])
	}
}

func TestBuildMaintenanceDashboard_NoDataConfidence(t *testing.T) {
	data := BuildMaintenanceDashboard(nil, []DashboardServiceMeta{{ID: "spotify", Name: "Spotify"}}, nil, nil, dashTime(0))
	svc := data.Services[0]
	if svc.Scores.Confidence != "none" || svc.Scores.Success != nil || svc.Scores.Stability != nil {
		t.Fatalf("scores = %+v, want none marks", svc.Scores)
	}
	if svc.NameOrID() != "Spotify" {
		t.Fatalf("name = %q", svc.NameOrID())
	}
}

func TestBuildMaintenanceDashboard_IssueMapping(t *testing.T) {
	issues := []MaintenanceIssueRecord{
		{
			Number: 10, URL: "u/10", Title: "spotify canary failed: shape drift",
			State: "open", IsTicket: true, TicketService: "spotify",
			ReproState: ReproStateReady, CreatedAt: dashTime(1),
		},
		{
			Number: 11, URL: "u/11", Title: "spotify ingest failed: 502",
			State: "closed", StateReason: "completed", IsTicket: true,
			TicketService: "spotify", CreatedAt: dashTime(2), ClosedAt: dashTime(3),
		},
		{
			Number: 20, URL: "u/20", Title: "google-gmail attachments endpoint returns 502",
			State: "closed", StateReason: "completed", ClosedByFix: true,
			CreatedAt: dashTime(4), ClosedAt: dashTime(5),
		},
		{
			Number: 21, URL: "u/21", Title: "Gmail fetch drops attachments",
			State: "closed", StateReason: "completed", ClosedByFix: true,
			CreatedAt: dashTime(6), ClosedAt: dashTime(7),
		},
		{
			Number: 30, URL: "u/30", Title: "Add support for Spotify widgets",
			State: "closed", StateReason: "completed", Labels: []string{"enhancement"},
			CreatedAt: dashTime(8), ClosedAt: dashTime(9),
		},
		{
			Number: 31, URL: "u/31", Title: "Add gmail attachment download",
			State: "closed", StateReason: "completed", CreatedAt: dashTime(8), ClosedAt: dashTime(9),
		},
		{
			Number: 32, URL: "u/32", Title: "gmail gap still open",
			State: "open", Labels: []string{"bug"}, CreatedAt: dashTime(8),
		},
		{
			Number: 33, URL: "u/33", Title: "unrelated cleanup landed",
			State: "closed", StateReason: "completed", CreatedAt: dashTime(8), ClosedAt: dashTime(9),
		},
		{
			Number: 34, URL: "u/34", Title: "gmail outage postmortem",
			State: "closed", StateReason: "not_planned", Labels: []string{"bug"},
			CreatedAt: dashTime(8), ClosedAt: dashTime(9),
		},
	}
	data := BuildMaintenanceDashboard(nil,
		[]DashboardServiceMeta{{ID: "google-gmail"}}, issues, nil, dashTime(10))

	byNumber := map[int]DashboardFix{}
	for _, f := range data.Fixes {
		byNumber[f.Number] = f
	}
	// Tickets 10-11, allowlisted fixes 20-21, allowlisted open problem 32,
	// allowlisted not-planned closure 34. Items without allowlist evidence
	// (30, 31, 33) never enter the record.
	if len(data.Fixes) != 6 {
		t.Fatalf("mapped fixes = %+v, want 6 (10, 11, 20, 21, 32, 34)", data.Fixes)
	}
	if got := byNumber[10]; got.Kind != "ticket" || got.State != "open" || got.Service != "spotify" {
		t.Fatalf("fix 10 = %+v", got)
	}
	if got := byNumber[11]; got.Kind != "ticket" || got.State != "fixed" {
		t.Fatalf("fix 11 = %+v", got)
	}
	if got := byNumber[20]; got.Kind != "issue" || got.State != "fixed" || got.Service != "google-gmail" {
		t.Fatalf("fix 20 = %+v", got)
	}
	if got := byNumber[21]; got.Kind != "issue" || got.Service != "google-gmail" {
		t.Fatalf("fix 21 = %+v", got)
	}
	// Open allowlisted problems stay in the record and count as unresolved.
	if got := byNumber[32]; got.State != "open" || got.Service != "google-gmail" {
		t.Fatalf("fix 32 = %+v", got)
	}
	if got := byNumber[34]; got.State != "closed" {
		t.Fatalf("fix 34 = %+v", got)
	}
	// Newest first (ties broken by issue number).
	if data.Fixes[0].Number != 34 {
		t.Fatalf("fixes not newest-first: %+v", data.Fixes)
	}
	// Ticket-only services open a page even with no queue evidence.
	services := map[string]bool{}
	for _, svc := range data.Services {
		services[svc.ID] = true
	}
	if !services["spotify"] || !services["google-gmail"] {
		t.Fatalf("services = %v", data.Services)
	}
}

func TestMatchServiceInTitle(t *testing.T) {
	ids := []string{"google-gmail", "google-photos", "spotify"}
	cases := []struct {
		title string
		want  string
	}{
		{"google-gmail attachments endpoint returns 502", "google-gmail"},
		{"Gmail fetch loop drops attachments", "google-gmail"},
		{"Google Photos picker stalls", "google-photos"},
		{"spotify saved tracks paging", "spotify"},
		{"nothing relevant here", ""},
		{"signal bundles overflow", ""},
	}
	for _, tc := range cases {
		if got := matchServiceInTitle(tc.title, ids); got != tc.want {
			t.Fatalf("matchServiceInTitle(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestLoadDashboardServiceMetas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.json")
	raw := `[
	  {"id": "spotify", "name": "Spotify"},
	  {"id": "fake-upstream", "name": "Drift-injection harness", "failure_injection": true},
	  {"name": "no id, dropped"}
	]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	metas, err := LoadDashboardServiceMetas(path)
	if err != nil {
		t.Fatalf("LoadDashboardServiceMetas: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("metas = %+v, want 2", metas)
	}
	if metas[0].ID != "spotify" || metas[0].FailureInjection {
		t.Fatalf("meta 0 = %+v", metas[0])
	}
	if metas[1].ID != "fake-upstream" || !metas[1].FailureInjection {
		t.Fatalf("meta 1 = %+v", metas[1])
	}
	if _, err := LoadDashboardServiceMetas(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("want error for missing manifest")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDashboardServiceMetas(bad); err == nil {
		t.Fatal("want error for unparseable manifest")
	}
}

func TestServicePageFile(t *testing.T) {
	if got := servicePageFile("google-gmail"); got != "google-gmail" {
		t.Fatalf("plain id rewritten: %q", got)
	}
	a := servicePageFile("a/b")
	b := servicePageFile("a_b")
	if a == "a/b" || !strings.HasPrefix(a, "a_b-") {
		t.Fatalf("unsafe id = %q", a)
	}
	if a == b {
		t.Fatalf("collision: %q == %q", a, b)
	}
}

func TestWriteMaintenanceDashboard(t *testing.T) {
	data := BuildMaintenanceDashboard(
		[]MaintenanceSignals{
			dashBundle(dashTime(0), map[string]SyncCounts{
				"spotify": {Succeeded: 2, Failed: 1},
			}, []MaintenanceFailure{
				{ID: "j1", Service: "spotify", JobType: "canary", UpdatedAt: dashTime(0),
					Error: `boom <script>alert(1)</script>`},
			}),
		},
		[]DashboardServiceMeta{
			{ID: "spotify", Name: "Spotify", FailureInjection: true},
		},
		[]MaintenanceIssueRecord{
			{Number: 42, URL: "https://github.com/example/repo/issues/42", Title: "spotify canary failed: boom",
				State: "closed", StateReason: "completed", IsTicket: true, TicketService: "spotify",
				CreatedAt: dashTime(0), ClosedAt: dashTime(1)},
		},
		nil,
		dashTime(1),
	)
	dir := t.TempDir()
	index, err := WriteMaintenanceDashboard(dir, data, DashboardPublishedFileMode, DashboardPublishedDirMode)
	if err != nil {
		t.Fatalf("WriteMaintenanceDashboard: %v", err)
	}
	if index != filepath.Join(dir, "index.html") {
		t.Fatalf("index = %q", index)
	}
	if st, err := os.Stat(index); err != nil {
		t.Fatalf("stat index: %v", err)
	} else if st.Mode().Perm() != DashboardPublishedFileMode {
		t.Fatalf("index mode = %v, want %v (Pages upload needs world-readable files)", st.Mode().Perm(), DashboardPublishedFileMode)
	}
	// A rewrite over an existing tree must land the requested posture too:
	// OpenFile keeps old modes and creation is umask-masked.
	if err := os.Chmod(index, DashboardPrivateFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "services"), DashboardPrivateDirMode); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteMaintenanceDashboard(dir, data, DashboardPublishedFileMode, DashboardPublishedDirMode); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if st, err := os.Stat(index); err != nil {
		t.Fatalf("stat index: %v", err)
	} else if st.Mode().Perm() != DashboardPublishedFileMode {
		t.Fatalf("index mode after rewrite = %v, want %v", st.Mode().Perm(), DashboardPublishedFileMode)
	}
	if st, err := os.Stat(filepath.Join(dir, "services")); err != nil {
		t.Fatalf("stat services: %v", err)
	} else if st.Mode().Perm() != DashboardPublishedDirMode {
		t.Fatalf("services mode after rewrite = %v, want %v", st.Mode().Perm(), DashboardPublishedDirMode)
	}
	raw, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	page := string(raw)
	for _, want := range []string{"Sync maintenance dashboard", "spotify", "failure injection"} {
		if !strings.Contains(page, want) {
			t.Fatalf("index missing %q", want)
		}
	}

	svcPath := filepath.Join(dir, "services", "spotify.html")
	raw, err = os.ReadFile(svcPath)
	if err != nil {
		t.Fatalf("read service page: %v", err)
	}
	svcPage := string(raw)
	for _, want := range []string{
		"Failure injection",
		"drift-injection",
		"#42",
		"j1",
		"&lt;script&gt;alert(1)&lt;/script&gt;",
		"Capture history",
	} {
		if !strings.Contains(svcPage, want) {
			t.Fatalf("service page missing %q in:\n%s", want, svcPage)
		}
	}
	if strings.Contains(svcPage, "<script>alert") {
		t.Fatal("failure error must be HTML-escaped")
	}
}

func TestHandleAdminMaintenanceDashboard_Build(t *testing.T) {
	srv := adminSplitServer(t)
	srv.dataDir = t.TempDir()
	srv.syncJobs = testJobStore(t)
	srv.config.EnsureService(Service{
		ID: "spotify", Name: "Spotify", Endpoint: "http://localhost:8080" + fakeUpstreamMount,
	})
	mustEnqueue(t, srv.syncJobs, SyncEnqueue{Kind: "tracks", JobType: "canary", Service: "spotify"})
	srv.maintenanceIssues = &fakeIssueTracker{records: []MaintenanceIssueRecord{
		{Number: 7, URL: "https://github.com/example/repo/issues/7", Title: "spotify canary failed",
			State: "closed", StateReason: "completed", IsTicket: true, TicketService: "spotify",
			CreatedAt: dashTime(0), ClosedAt: dashTime(1)},
	}}

	req := adminRequest(http.MethodPost, "/v1/admin/maintenance/dashboard", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceDashboard)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got MaintenanceDashboardResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(got.Path, filepath.Join(srv.dataDir, "maintenance")) {
		t.Fatalf("dashboard path = %q, want under data dir", got.Path)
	}
	if _, err := os.Stat(got.Index); err != nil {
		t.Fatalf("index missing: %v", err)
	}
	if len(got.Services) == 0 || got.Services[0] != "spotify" {
		t.Fatalf("services = %v, want spotify", got.Services)
	}
	if got.Fixes != 1 {
		t.Fatalf("fixes = %d, want 1", got.Fixes)
	}
	if got.Captures != 1 {
		t.Fatalf("captures = %d, want the live snapshot", got.Captures)
	}
	svcPage := filepath.Join(got.Path, "services", "spotify.html")
	raw, err := os.ReadFile(svcPage)
	if err != nil {
		t.Fatalf("read service page: %v", err)
	}
	if !strings.Contains(string(raw), "Failure injection") {
		t.Fatal("configured fake-upstream service must carry the failure-injection note")
	}
}

func TestHandleAdminMaintenanceDashboard_FixHistoryFailOpen(t *testing.T) {
	srv := adminSplitServer(t)
	srv.dataDir = t.TempDir()
	srv.maintenanceIssues = &fakeIssueTracker{listErr: errors.New("gh unavailable")}

	req := adminRequest(http.MethodPost, "/v1/admin/maintenance/dashboard", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceDashboard)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("scrape failure must fail open, got %d: %s", w.Code, w.Body.String())
	}
	var got MaintenanceDashboardResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.FixHistoryError == "" {
		t.Fatal("fix_history_error must report the scrape failure")
	}
	if _, err := os.Stat(got.Index); err != nil {
		t.Fatalf("index missing despite scrape failure: %v", err)
	}
}

func TestHandleAdminMaintenanceDashboard_MethodAndAuth(t *testing.T) {
	srv := adminSplitServer(t)

	req := adminRequest(http.MethodGet, "/v1/admin/maintenance/dashboard", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceDashboard)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/maintenance/dashboard", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceDashboard)(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("POST without data dir = %d, want 500", w.Code)
	}

	srv.dataDir = t.TempDir()
	req = adminRequest(http.MethodPost, "/v1/admin/maintenance/dashboard", "agent-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminMaintenanceDashboard)(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("agent credentials must not build the dashboard: %d", w.Code)
	}
}

func TestBuildMaintenanceDashboard_Maturity(t *testing.T) {
	cat := MaturityByService()
	spotify, ok := cat["spotify"]
	if !ok {
		t.Fatal("catalog has no spotify entry")
	}
	data := BuildMaintenanceDashboard(nil,
		[]DashboardServiceMeta{{ID: "spotify", Name: "Spotify"}, {ID: "fake-upstream", FailureInjection: true}},
		nil, cat, dashTime(1))

	var svc, harness *DashboardService
	for i := range data.Services {
		switch data.Services[i].ID {
		case "spotify":
			svc = &data.Services[i]
		case "fake-upstream":
			harness = &data.Services[i]
		}
	}
	if svc == nil || harness == nil {
		t.Fatalf("services = %+v, want spotify and fake-upstream", data.Services)
	}
	if svc.Maturity == nil || svc.MaturityGrade == nil {
		t.Fatalf("spotify maturity = %+v, want declaration and grade", svc.Maturity)
	}
	if svc.Maturity.Service != "spotify" || svc.MaturityGrade.Level == "" {
		t.Fatalf("spotify maturity = %+v grade = %+v", svc.Maturity, svc.MaturityGrade)
	}
	if svc.MaturityGrade.Offered != len(spotify.DataTypes) {
		t.Fatalf("offered = %d, want %d", svc.MaturityGrade.Offered, len(spotify.DataTypes))
	}
	if harness.Maturity != nil || harness.MaturityGrade != nil {
		t.Fatalf("harness maturity = %+v, want nil for a non-catalog service", harness.Maturity)
	}
}

func TestWriteMaintenanceDashboard_Maturity(t *testing.T) {
	cat := MaturityByService()
	data := BuildMaintenanceDashboard(nil,
		[]DashboardServiceMeta{{ID: "spotify", Name: "Spotify"}}, nil, cat, dashTime(1))
	dir := t.TempDir()
	if _, err := WriteMaintenanceDashboard(dir, data, DashboardPublishedFileMode, DashboardPublishedDirMode); err != nil {
		t.Fatalf("WriteMaintenanceDashboard: %v", err)
	}
	index, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	level := data.Services[0].MaturityGrade.Level
	for _, want := range []string{"Maturity", "Transport", level, "API"} {
		if !strings.Contains(string(index), want) {
			t.Errorf("index.html missing %q (maturity level %q)", want, level)
		}
	}
	page, err := os.ReadFile(filepath.Join(dir, "services", "spotify.html"))
	if err != nil {
		t.Fatalf("read service page: %v", err)
	}
	for _, want := range []string{
		"Connection maturity", "Data types", "audiobooks", "not supported", "supported",
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("spotify.html missing %q", want)
		}
	}
}
