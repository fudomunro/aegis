package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/admin"
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/syncjobs"
)

func TestHandleAdminSyncPauseResume(t *testing.T) {
	srv := syncAdminServer(t)
	mustEnqueue(t, srv.syncJobs, syncjobs.SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})

	req := adminRequest(http.MethodPost, "/v1/admin/sync/pause", "admin-key", `{"service":"google-gmail","reason":"quota"}`)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncPause)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pause status = %d, body %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	paused, _ := out["paused"].([]any)
	if len(paused) != 1 || paused[0].(string) != "google-gmail" {
		t.Fatalf("paused = %v, want [google-gmail]", out["paused"])
	}
	if got := srv.syncJobs.List(syncjobs.SyncFilter{State: syncjobs.SyncJobPaused}); len(got) != 1 {
		t.Fatalf("paused jobs = %+v, want 1", got)
	}

	// The status model reports the pause.
	req = adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	var status admin.SyncStatus
	_ = json.Unmarshal(w.Body.Bytes(), &status)
	if len(status.Paused) != 1 || status.Paused[0] != "google-gmail" {
		t.Fatalf("status.paused = %v", status.Paused)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync/resume", "admin-key", `{"service":"google-gmail"}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncResume)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body %s", w.Code, w.Body.String())
	}
	if got := srv.syncJobs.List(syncjobs.SyncFilter{State: syncjobs.SyncJobPaused}); len(got) != 0 {
		t.Fatalf("after resume paused jobs = %+v", got)
	}

	// Global pause with an empty body.
	req = adminRequest(http.MethodPost, "/v1/admin/sync/pause", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncPause)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("global pause status = %d", w.Code)
	}
	if !srv.syncJobs.IsPaused("anything") {
		t.Fatal("global pause must hold every service")
	}
	req = adminRequest(http.MethodPost, "/v1/admin/sync/resume", "admin-key", `{}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncResume)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("global resume status = %d", w.Code)
	}

	// Method and disabled-store guards.
	req = adminRequest(http.MethodGet, "/v1/admin/sync/pause", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncPause)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET pause = %d, want 405", w.Code)
	}

	off := syncAdminServer(t)
	off.syncJobs = &syncjobs.JobStore{}
	req = adminRequest(http.MethodPost, "/v1/admin/sync/pause", "admin-key", `{}`)
	w = httptest.NewRecorder()
	off.AdminMiddleware(off.admin().HandleAdminSyncPause)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled pause = %d, want 503", w.Code)
	}
}

func TestParseServiceConcurrency(t *testing.T) {
	got := parseServiceConcurrency("spotify=1,youtube-music=2, broken , x=abc, y=0, z=3")
	want := map[string]int{"spotify": 1, "youtube-music": 2, "z": 3}
	if len(got) != len(want) {
		t.Fatalf("parse = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parse[%s] = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	if got := parseServiceConcurrency(""); len(got) != 0 {
		t.Fatalf("empty parse = %v", got)
	}
}

func TestHandleAdminSync_ReportsFreshnessAndSchedules(t *testing.T) {
	srv := syncAdminServer(t)
	ledger, err := copies.Open(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	srv.copyLedger = ledger
	_, _ = srv.copyLedger.Upsert(copies.Record{Kind: "playlists", EntityKey: "a", Service: "spotify", ExternalID: "1",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Status: copies.CopyStatusStale})
	srv.syncSchedules = testScheduleStore(t)
	if _, err := srv.syncSchedules.Upsert(syncjobs.SyncSchedule{
		Name: "Daily copy freshness", Kind: "playlists", JobType: syncjobs.SyncJobTypeFreshness,
		EverySeconds: 3600, Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert schedule: %v", err)
	}

	req := adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got admin.SyncStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Freshness) != 1 || got.Freshness[0].Kind != "playlists" || got.Freshness[0].Stale != 1 {
		t.Fatalf("freshness = %+v, want one stale playlists row", got.Freshness)
	}
	if len(got.Schedules) != 1 || got.Schedules[0].JobType != syncjobs.SyncJobTypeFreshness {
		t.Fatalf("schedules = %+v, want one freshness template", got.Schedules)
	}
}

func TestHandleAdminSyncSchedule_Toggle(t *testing.T) {
	srv := syncAdminServer(t)
	store := testScheduleStore(t)
	srv.syncSchedules = store
	sc, err := store.Upsert(syncjobs.SyncSchedule{
		Kind: "playlists", JobType: syncjobs.SyncJobTypeFreshness, EverySeconds: 3600, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := adminRequest(http.MethodPost,
		fmt.Sprintf("/v1/admin/sync/schedules/%d/disable", sc.ID), "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncSchedule)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body %s", w.Code, w.Body.String())
	}
	var out syncjobs.SyncSchedule
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Enabled {
		t.Fatal("schedule must be disabled")
	}

	req = adminRequest(http.MethodPost,
		fmt.Sprintf("/v1/admin/sync/schedules/%d/enable", sc.ID), "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncSchedule)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enable status = %d", w.Code)
	}

	for _, path := range []string{
		"/v1/admin/sync/schedules/9999/disable",
		"/v1/admin/sync/schedules/abc/disable",
		"/v1/admin/sync/schedules/1/poke",
		"/v1/admin/sync/schedules//disable",
	} {
		req = adminRequest(http.MethodPost, path, "admin-key", "")
		w = httptest.NewRecorder()
		srv.AdminMiddleware(srv.admin().HandleAdminSyncSchedule)(w, req)
		if w.Code < 400 {
			t.Fatalf("%s = %d, want an error", path, w.Code)
		}
	}

	req = adminRequest(http.MethodGet, fmt.Sprintf("/v1/admin/sync/schedules/%d/disable", sc.ID), "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncSchedule)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", w.Code)
	}
}

func TestHandleAdminSyncSchedule_DisabledStore(t *testing.T) {
	srv := syncAdminServer(t)
	req := adminRequest(http.MethodPost, "/v1/admin/sync/schedules/1/disable", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncSchedule)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// --- end to end ---

func TestHandleAdminMaintenanceSignals_Get(t *testing.T) {
	srv := syncAdminServer(t)
	srv.runners().Jobs = testJobStore(t)

	req := adminRequest(http.MethodGet, "/v1/admin/maintenance/signals", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminMaintenanceSignals)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got syncjobs.MaintenanceSignals
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Sync == nil || !got.Sync.Enabled {
		t.Fatalf("sync = %+v, want enabled", got.Sync)
	}
}

func TestHandleAdminMaintenanceSignals_AgentKeyRejected(t *testing.T) {
	srv := syncAdminServer(t)
	req := adminRequest(http.MethodGet, "/v1/admin/maintenance/signals", "agent-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminMaintenanceSignals)(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("agent credentials must not read maintenance signals: %d", w.Code)
	}
}

func TestHandleAdminMaintenanceSignals_Capture(t *testing.T) {
	srv := syncAdminServer(t)
	dir := t.TempDir()
	srv.runners().DataDir = dir
	srv.admin().DataDir = dir
	srv.runners().Jobs = testJobStore(t)

	req := adminRequest(http.MethodPost, "/v1/admin/maintenance/signals", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminMaintenanceSignals)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got syncjobs.MaintenanceCaptureResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(got.Path, filepath.Join(dir, "maintenance")) {
		t.Fatalf("capture path = %q, want under data dir maintenance/", got.Path)
	}
	if _, err := os.Stat(got.Path); err != nil {
		t.Fatalf("captured bundle missing: %v", err)
	}
}

func TestHandleAdminMaintenanceSignals_MethodAndConfig(t *testing.T) {
	srv := syncAdminServer(t)

	req := adminRequest(http.MethodPut, "/v1/admin/maintenance/signals", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminMaintenanceSignals)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/maintenance/signals", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminMaintenanceSignals)(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("POST without data dir = %d, want 500", w.Code)
	}
}

func TestMailIngestFor_Wired(t *testing.T) {
	srv := mailTestServer(t, openTestMailArchive(t))
	if srv.mailIngests[connectors.GoogleGmailServiceID] == nil {
		t.Fatal("google-gmail must build an ingest")
	}
	if srv.mailIngests[connectors.TakeoutServiceID] == nil {
		t.Fatal("takeout must build an ingest")
	}
	if srv.mailIngests["nope"] != nil {
		t.Fatal("unknown service must not build an ingest")
	}
	// A connector that does not implement MailIngest yields nil.
	srv.connectors["quickenish"] = connectors.NewQuickenConnector(nil)
	srv.wireMailIngests()
	if srv.mailIngests["quickenish"] != nil {
		t.Fatal("non-Gmail connector must not build a mail ingest")
	}
}

// The HTTP route and the queue runner must report the same bounded result for
// the same input. before the cleanup the route called syncjobs.SyncMailArchive
// directly, so the two could drift.
func TestMailSyncRouteAndRunnerShareOnePath(t *testing.T) {
	disableGmailArchivePace(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/users/me/messages/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			w.Write([]byte(gmailPayloadJSON(id)))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{{"id": "m1"}, {"id": "m2"}},
		})
	}))
	t.Cleanup(up.Close)

	// Route first.
	a1 := openTestMailArchive(t)
	srv1 := mailTestServer(t, a1)
	srv1.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv1.tokens.Save(connectors.GoogleGmailServiceID, &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	w := doAdminMailReq(t, srv1, http.MethodPost, "/v1/admin/mail/sync", `{"max_messages":5,"reset_cursor":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("route status = %d body=%s", w.Code, w.Body.String())
	}
	var routeResult shared.MailIngestResult
	if err := json.Unmarshal(w.Body.Bytes(), &routeResult); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Runner second, fresh archive, same request.
	a2 := openTestMailArchive(t)
	srv2 := mailTestServer(t, a2)
	srv2.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv2.tokens.Save(connectors.GoogleGmailServiceID, &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	runnerResult, err := srv2.runners().DoMailIngest(context.Background(), connectors.GoogleGmailServiceID, shared.MailIngestOptions{
		MaxMessages: 5, ResetCursor: true,
	})
	if err != nil {
		t.Fatalf("doMailIngest: %v", err)
	}

	for _, field := range []struct {
		name          string
		route, runner any
	}{
		{"stored", routeResult.Stored, runnerResult.Stored},
		{"listed", routeResult.Listed, runnerResult.Listed},
		{"failed", routeResult.Failed, runnerResult.Failed},
		{"skipped", routeResult.Skipped, runnerResult.Skipped},
		{"done", routeResult.Done, runnerResult.Done},
		{"truncated", routeResult.Truncated, runnerResult.Truncated},
		{"service", routeResult.Service, runnerResult.Service},
		{"query", routeResult.Query, runnerResult.Query},
	} {
		if field.route != field.runner {
			t.Fatalf("%s drifted: route=%v runner=%v", field.name, field.route, field.runner)
		}
	}
	if routeResult.Stored == 0 {
		t.Fatal("both paths must actually store something for this to prove anything")
	}
}

// A service whose connector cannot sync the archive stays a 501 on the route.
func TestMailSyncRoute_KeepsNotImplementedContract(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.connectors["dummy"] = &stubNoSyncConnector{}
	srv.config.Services = append(srv.config.Services, Service{
		ID: "dummy", Name: "Dummy", Action: "inject",
	})
	w := doAdminMailReq(t, srv, http.MethodPost, "/v1/admin/mail/sync", `{"service":"dummy"}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d, want 501 (the runner's terminal error must not become a 200)", w.Code)
	}
	// The queue path still runs the same code and reports it as terminal.
	res, err := srv.runners().DoMailIngest(context.Background(), "dummy", shared.MailIngestOptions{})
	if err == nil || !strings.Contains(err.Error(), syncjobs.ErrSyncTerminal.Error()) {
		t.Fatalf("runner err = %v, want terminal (res %+v)", err, res)
	}
}
