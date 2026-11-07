package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
)

// Admin sync surface (issue #227). Read model for the queue plus on-demand
// enqueue/cancel so the operator can see what is syncing and start work
// without a client-side batch loop. Admin-scoped like the rest of
// /v1/admin — agent credentials never reach these writes (issue #181).
//
// No issue numbers in user-facing copy: strings below stay clean; refs live
// in comments only.

// SyncStatus is the GET /v1/admin/sync response.
type SyncStatus struct {
	Enabled   bool                  `json:"enabled"`
	Workers   int                   `json:"workers"`
	StorePath string                `json:"store_path,omitempty"`
	Kinds     []string              `json:"kinds,omitempty"`
	Counts    SyncCounts            `json:"counts"`
	ByKind    map[string]SyncCounts `json:"by_kind,omitempty"`
	ByJobType map[string]SyncCounts `json:"by_job_type,omitempty"`
	// Freshness is the copy ledger's per-kind status rollup (phase 2): the
	// data an operator needs to see where their copies stand.
	Freshness []CopyFreshness `json:"freshness,omitempty"`
	// Schedules lists the recurring job templates feeding the queue.
	Schedules []SyncSchedule `json:"schedules,omitempty"`
	// Paused lists active pause scopes ("global" and/or service ids).
	Paused []string  `json:"paused,omitempty"`
	Jobs   []SyncJob `json:"jobs"`
}

// SyncPauseRequest is the body for POST /v1/admin/sync/pause|resume.
// An empty service pauses or resumes the whole queue.
type SyncPauseRequest struct {
	Service string `json:"service,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// SyncEnqueueRequest is the POST /v1/admin/sync body.
type SyncEnqueueRequest struct {
	Kind      string         `json:"kind"`
	JobType   string         `json:"job_type"`
	Service   string         `json:"service,omitempty"`
	EntityKey string         `json:"entity_key,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// HandleAdminSync reports queue state (GET) or enqueues work (POST).
func (s *Server) HandleAdminSync(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeSyncStatus(w)
	case http.MethodPost:
		s.handleSyncEnqueue(w, r)
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) writeSyncStatus(w http.ResponseWriter) {
	store := s.syncJobs
	status := SyncStatus{
		Enabled: store != nil && store.Enabled(),
		Jobs:    []SyncJob{},
	}
	if s.syncWorker != nil {
		status.Workers = s.syncWorker.workers
	}
	if store != nil {
		status.StorePath = store.Path()
		status.Counts = store.Counts("", "")
		status.Jobs = store.List(SyncFilter{Limit: 50})
		// Global breakdown from the whole table, matching Counts — not the
		// recent-job window, which would under-report quiet kinds.
		if b := store.Breakdown(); len(b.ByKind) > 0 {
			status.ByKind = b.ByKind
			status.ByJobType = b.ByJobType
		}
	}
	if s.copies != nil {
		status.Freshness = s.copies.FreshnessRollup()
	}
	if s.syncSchedules != nil {
		status.Schedules = s.syncSchedules.List()
	}
	if store != nil && store.Enabled() {
		status.Paused = store.Paused()
	}
	if s.syncRunners != nil {
		status.Kinds = s.syncRunners.Kinds()
	}
	shared.WriteJSON(w, status)
}

// HandleAdminSyncPause holds work for a scope: POST /v1/admin/sync/pause
// Body: {"service":"spotify","reason":"quota"}; an empty service pauses the
// whole queue. Queued work for the scope moves to `paused` and new enqueues
// land there too. In-flight steps keep running — cancel them individually if
// they must stop.
func (s *Server) HandleAdminSyncPause(w http.ResponseWriter, r *http.Request) {
	s.handleSyncPause(w, r, true)
}

// HandleAdminSyncResume re-arms a paused scope:
// POST /v1/admin/sync/resume. Its `paused` jobs go back to `queued`.
func (s *Server) HandleAdminSyncResume(w http.ResponseWriter, r *http.Request) {
	s.handleSyncPause(w, r, false)
}

func (s *Server) handleSyncPause(w http.ResponseWriter, r *http.Request, pause bool) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.syncJobs == nil || !s.syncJobs.Enabled() {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "sync queue disabled")
		return
	}
	var req SyncPauseRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				shared.WriteBodyReadError(w, err)
				return
			}
			if r.ContentLength != 0 {
				shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	scope := strings.TrimSpace(req.Service)
	if scope == "" {
		scope = SyncPauseScopeGlobal
	}
	var err error
	if pause {
		err = s.syncJobs.Pause(scope, req.Reason)
	} else {
		err = s.syncJobs.Resume(scope)
	}
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to update sync pause")
		return
	}
	shared.WriteJSON(w, map[string]any{
		"paused": s.syncJobs.Paused(),
		"scope":  scope,
		"counts": s.syncJobs.Counts("", ""),
	})
}

// HandleAdminSyncSchedule toggles a recurring template:
// POST /v1/admin/sync/schedules/{id}/enable | /disable
func (s *Server) HandleAdminSyncSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.syncSchedules == nil || !s.syncSchedules.Enabled() {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "sync schedules disabled")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/sync/schedules/"), "/")
	enable := true
	switch {
	case strings.HasSuffix(path, "/enable"):
		path = strings.TrimSuffix(path, "/enable")
	case strings.HasSuffix(path, "/disable"):
		path = strings.TrimSuffix(path, "/disable")
		enable = false
	default:
		shared.WriteJSONError(w, http.StatusNotFound, "unknown sync schedule action")
		return
	}
	id := strings.TrimSpace(path)
	if id == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "schedule id required")
		return
	}
	var sid int64
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			shared.WriteJSONError(w, http.StatusBadRequest, "schedule id must be numeric")
			return
		}
		sid = sid*10 + int64(ch-'0')
	}
	sc, ok, err := s.syncSchedules.SetEnabled(sid, enable)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to update sync schedule")
		return
	}
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown sync schedule")
		return
	}
	shared.WriteJSON(w, sc)
}

func (s *Server) handleSyncEnqueue(w http.ResponseWriter, r *http.Request) {
	if s.syncJobs == nil || !s.syncJobs.Enabled() {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "sync queue disabled")
		return
	}
	var req SyncEnqueueRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				shared.WriteBodyReadError(w, err)
				return
			}
			if r.ContentLength != 0 {
				shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	req.Kind = strings.TrimSpace(req.Kind)
	req.JobType = strings.TrimSpace(req.JobType)
	if req.Kind == "" || req.JobType == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "kind and job_type are required")
		return
	}
	if s.syncRunners != nil {
		if _, ok := s.syncRunners.For(req.Kind, req.JobType); !ok {
			shared.WriteJSONError(w, http.StatusBadRequest,
				"no sync runner for kind="+req.Kind+" job_type="+req.JobType)
			return
		}
	}

	entityKey := strings.TrimSpace(req.EntityKey)
	if entityKey == "" {
		// Whole-collection work dedupes on the service so repeat clicks do
		// not stack duplicate ingest runs.
		entityKey = strings.TrimSpace(req.Service)
	}
	job, err := s.syncJobs.Enqueue(syncEnqueueFromRequest(
		req.Kind, req.JobType, req.Service, entityKey, req.Payload))
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to enqueue sync job")
		return
	}
	if s.syncWorker != nil {
		s.syncWorker.Kick()
	}
	shared.WriteJSON(w, job)
}

// HandleAdminSyncCancel parks a queued or running job: POST /v1/admin/sync/{id}/cancel
func (s *Server) HandleAdminSyncCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.syncJobs == nil || !s.syncJobs.Enabled() {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "sync queue disabled")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/admin/sync/")
	id = strings.TrimSuffix(strings.Trim(id, "/"), "/cancel")
	id = strings.TrimSpace(id)
	if id == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "job id required")
		return
	}
	job, err := s.syncJobs.Cancel(id)
	if errors.Is(err, ErrSyncJobNotFound) {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown sync job")
		return
	}
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to cancel sync job")
		return
	}
	// Stop the in-flight step too — parking the row alone would let a runner
	// keep working on work the operator just cancelled.
	if s.syncWorker != nil {
		s.syncWorker.CancelJob(id)
	}
	shared.WriteJSON(w, job)
}
