package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/admin"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/syncjobs"
)

// testJobStore opens a throwaway sync job store for hub tests. The engine's
// own copy of this helper lives in internal/core/syncjobs.
func testJobStore(t *testing.T) *syncjobs.JobStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sync_jobs.db")
	s, err := syncjobs.OpenJobStore(nil, path)
	if err != nil {
		t.Fatalf("OpenJobStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// mustEnqueue is the hub-test shorthand for a successful Enqueue.
func mustEnqueue(t *testing.T, s *syncjobs.JobStore, req syncjobs.SyncEnqueue) syncjobs.SyncJob {
	t.Helper()
	job, err := s.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue(%+v): %v", req, err)
	}
	return job
}

// testScheduleStore opens a throwaway sync schedule store for hub tests. The
// engine's own copy of this helper lives in internal/core/syncjobs.
func testScheduleStore(t *testing.T) *syncjobs.ScheduleStore {
	t.Helper()
	s, err := syncjobs.OpenScheduleStore(nil, filepath.Join(t.TempDir(), "sync_jobs.db"))
	if err != nil {
		t.Fatalf("OpenScheduleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// stubRunner is a syncjobs.SyncRunner whose behavior the test supplies. The
// engine's own copy lives in internal/core/syncjobs.
type stubRunner struct {
	kind     string
	jobTypes []string
	fn       func(ctx context.Context, job syncjobs.SyncJob) (syncjobs.SyncStep, error)
	calls    int
}

func (r *stubRunner) JobTypes() []string { return r.jobTypes }
func (r *stubRunner) Kind() string       { return r.kind }
func (r *stubRunner) Run(ctx context.Context, job syncjobs.SyncJob) (syncjobs.SyncStep, error) {
	r.calls++
	return r.fn(ctx, job)
}

// --- admin surface ---

func syncAdminServer(t *testing.T) *Server {
	t.Helper()
	srv := adminSplitServer(t)
	store := testJobStore(t)
	srv.syncJobs = store
	// Mirror production: the registry lives on the Server so admin enqueue
	// can validate even when the worker pool is off.
	srv.syncRunners = newSyncRunnerRegistry(srv)
	srv.syncWorker = syncjobs.NewSyncWorker(store, srv.syncRunners, syncjobs.SyncWorkerOptions{Workers: 1})
	return srv
}

func TestHandleAdminSync_GetStatus(t *testing.T) {
	srv := syncAdminServer(t)
	mustEnqueue(t, srv.syncJobs, syncjobs.SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})

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
	if !got.Enabled {
		t.Fatal("enabled must be true")
	}
	if got.Counts.Queued != 1 {
		t.Fatalf("counts = %+v, want one queued", got.Counts)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("jobs = %v, want one", got.Jobs)
	}
	if got.ByKind["messages"].Queued != 1 {
		t.Fatalf("by_kind = %v, want messages/queued", got.ByKind)
	}
	if got.ByJobType["ingest"].Queued != 1 {
		t.Fatalf("by_job_type = %v, want ingest/queued", got.ByJobType)
	}
	if len(got.Kinds) == 0 || got.Kinds[0] != "messages" {
		t.Fatalf("kinds = %v, want [messages]", got.Kinds)
	}
}

func TestHandleAdminSync_EnqueueAndCancel(t *testing.T) {
	srv := syncAdminServer(t)

	body := `{"kind":"messages","job_type":"ingest","service":"google-gmail","payload":{"max_messages":5}}`
	req := adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", body)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, body %s", w.Code, w.Body.String())
	}
	var job syncjobs.SyncJob
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if job.ID == "" || job.State != syncjobs.SyncJobQueued {
		t.Fatalf("job = %+v, want a queued job with an id", job)
	}
	if !strings.Contains(string(job.Payload), "5") {
		t.Fatalf("payload = %s, want max_messages=5", job.Payload)
	}

	// Repeat enqueue dedupes on the service entity key.
	req = adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", body)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("second enqueue status = %d", w.Code)
	}
	var again syncjobs.SyncJob
	_ = json.Unmarshal(w.Body.Bytes(), &again)
	if again.ID != job.ID {
		t.Fatalf("repeat enqueue id = %s, want %s (idempotent)", again.ID, job.ID)
	}

	// Cancel by id.
	req = adminRequest(http.MethodPost, "/v1/admin/sync/"+job.ID+"/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body %s", w.Code, w.Body.String())
	}
	var canceled syncjobs.SyncJob
	_ = json.Unmarshal(w.Body.Bytes(), &canceled)
	if canceled.State != syncjobs.SyncJobCanceled {
		t.Fatalf("cancel state = %s, want canceled", canceled.State)
	}

	// Unknown id is a 404.
	req = adminRequest(http.MethodPost, "/v1/admin/sync/9999/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown cancel status = %d, want 404", w.Code)
	}
}

func TestHandleAdminSync_Validation(t *testing.T) {
	srv := syncAdminServer(t)

	for _, body := range []string{
		`{}`,
		`{"kind":"messages"}`,
		`{"job_type":"ingest"}`,
		`{"kind":"photos","job_type":"ingest"}`, // no runner serves photos
	} {
		req := adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", body)
		w := httptest.NewRecorder()
		srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s → status = %d, want 400", body, w.Code)
		}
	}

	req := adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", `not-json`)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d, want 400", w.Code)
	}

	req = adminRequest(http.MethodPut, "/v1/admin/sync", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", w.Code)
	}

	req = adminRequest(http.MethodGet, "/v1/admin/sync/nope/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET cancel status = %d, want 405", w.Code)
	}
}

func TestHandleAdminSync_DisabledQueue(t *testing.T) {
	srv := adminSplitServer(t)
	// No syncJobs wired: enqueue must report the queue is off, not panic.
	req := adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", `{"kind":"messages","job_type":"ingest"}`)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled enqueue status = %d, want 503", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync/1/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled cancel status = %d, want 503", w.Code)
	}

	// GET still answers (fail-open read model).
	req = adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disabled GET status = %d, want 200", w.Code)
	}
}

func TestHandleAdminSync_RequiresAdminAuth(t *testing.T) {
	srv := adminSplitServer(t)
	store := testJobStore(t)
	srv.syncJobs = store
	srv.syncRunners = newSyncRunnerRegistry(srv)
	srv.syncWorker = syncjobs.NewSyncWorker(store, srv.syncRunners, syncjobs.SyncWorkerOptions{Workers: 1})

	// Agent key must not reach the admin sync surface.
	req := adminRequest(http.MethodGet, "/v1/admin/sync", "agent-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("agent GET status = %d, want 401/403", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync", "agent-key", `{"kind":"messages","job_type":"ingest"}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("agent POST status = %d, want 401/403", w.Code)
	}
	if store.Counts("", "").Total() != 0 {
		t.Fatal("an unauthenticated enqueue must not create jobs")
	}

	// No credentials at all.
	req = adminRequest(http.MethodGet, "/v1/admin/sync", "", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("anonymous GET status = %d, want 401/403", w.Code)
	}
}

func TestSyncRoutes_Registered(t *testing.T) {
	srv := syncAdminServer(t)
	mux := http.NewServeMux()
	registerAdminRoutes(mux, srv)

	req := adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("routed GET /v1/admin/sync = %d, body %s", w.Code, w.Body.String())
	}

	body := `{"kind":"messages","job_type":"ingest"}`
	req = adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", body)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("routed POST /v1/admin/sync = %d, body %s", w.Code, w.Body.String())
	}
}

// --- prom metrics ---

func TestPromMetrics_SyncSeries(t *testing.T) {
	p := metrics.NewPromMetrics("", "")
	p.ObserveSyncJob("ingest", "google-gmail", syncjobs.SyncJobSucceeded, 1500*time.Millisecond)
	p.ObserveSyncJob("ingest", "google-gmail", syncjobs.SyncJobQueued, 10*time.Millisecond)
	p.ObserveSyncJob("ingest", "", syncjobs.SyncJobSucceeded, time.Millisecond)
	p.ObserveSyncRetry("ingest", syncjobs.SyncErrorClassRetry)
	p.ObserveSyncRetry("", "")
	p.SetSyncQueueDepth(syncjobs.SyncCounts{Queued: 2, Running: 1, Succeeded: 7})
	p.NoteSyncSuccess("messages", "google-gmail", time.Unix(1700000000, 0).UTC())

	out := p.PromText()
	for _, want := range []string{
		`aegis_sync_jobs_total{job_type="ingest",state="succeeded"}`,
		`aegis_sync_jobs_total{job_type="ingest",state="queued"}`,
		`aegis_sync_retries_total{job_type="ingest",error_class="retryable"}`,
		`aegis_sync_retries_total{job_type="unknown",error_class="unknown"}`,
		`aegis_sync_job_duration_seconds_count{job_type="ingest",service="google-gmail"} 2`,
		`aegis_sync_job_duration_seconds_count{job_type="ingest",service="none"} 1`,
		`aegis_sync_queue_depth{state="queued"} 2`,
		`aegis_sync_queue_depth{state="running"} 1`,
		`aegis_sync_queue_depth{state="succeeded"} 7`,
		`aegis_sync_last_success_timestamp_seconds{kind="messages",service="google-gmail"}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics output missing %q\n%s", want, out)
		}
	}

	// Nil-safe.
	var nilProm *metrics.PromMetrics
	nilProm.ObserveSyncJob("ingest", "s", syncjobs.SyncJobSucceeded, time.Second)
	nilProm.ObserveSyncRetry("ingest", "retryable")
	nilProm.SetSyncQueueDepth(syncjobs.SyncCounts{})
	nilProm.NoteSyncSuccess("messages", "s", time.Now())
	if nilProm.PromText() != "" {
		t.Fatal("nil prom must render empty")
	}
}

func TestHandleAdminSync_WorkerDisabledStillValidates(t *testing.T) {
	srv := adminSplitServer(t)
	srv.syncJobs = testJobStore(t)
	srv.syncRunners = newSyncRunnerRegistry(srv)
	// No syncWorker on purpose: the pool is off.

	req := adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", w.Code)
	}
	var status admin.SyncStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !status.Enabled {
		t.Fatal("the queue is enabled even with the worker off")
	}
	if status.Workers != 0 {
		t.Fatalf("workers = %d, want 0", status.Workers)
	}

	// Unknown job type is still rejected (validation does not need the pool).
	req = adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key",
		`{"kind":"photos","job_type":"ingest"}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown runner status = %d, want 400", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key",
		`{"kind":"messages","job_type":"ingest","service":"google-gmail"}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, body %s", w.Code, w.Body.String())
	}
	if got := srv.syncJobs.Counts("", ""); got.Queued != 1 {
		t.Fatalf("counts = %+v, want one queued", got)
	}

	// Cancel with no worker must not panic either.
	var job syncjobs.SyncJob
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	req = adminRequest(http.MethodPost, "/v1/admin/sync/"+job.ID+"/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body %s", w.Code, w.Body.String())
	}
}

// A terminal failure on a first attempt is counted as failed, not queued:
// the metric must follow the state the store landed on.
func TestSyncWorker_MetricStateFollowsStore(t *testing.T) {
	store := testJobStore(t)
	reg := syncjobs.NewRunnerRegistry()
	reg.Register(&stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job syncjobs.SyncJob) (syncjobs.SyncStep, error) {
			return syncjobs.SyncStep{}, fmt.Errorf("%w: nope", syncjobs.ErrSyncTerminal)
		},
	})
	prom := metrics.NewPromMetrics("", "")
	w := syncjobs.NewSyncWorker(store, reg, syncjobs.SyncWorkerOptions{Workers: 1, Prom: prom})
	mustEnqueue(t, store, syncjobs.SyncEnqueue{Kind: "messages", JobType: "ingest", MaxAttempts: 5})

	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := store.List(syncjobs.SyncFilter{})[0]
	if job.State != syncjobs.SyncJobFailed {
		t.Fatalf("state = %s, want failed (terminal on the first attempt)", job.State)
	}
	out := prom.PromText()
	if !strings.Contains(out, `aegis_sync_jobs_total{job_type="ingest",state="failed"} 1`) {
		t.Fatalf("metrics must count the terminal failure as failed:\n%s", out)
	}
	if strings.Contains(out, `aegis_sync_jobs_total{job_type="ingest",state="queued"}`) {
		t.Fatal("metrics must not count a terminal failure as queued")
	}
}

// CancelJob stops an in-flight step; the runner sees a cancelled context.
