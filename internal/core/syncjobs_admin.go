package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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
	Enabled   bool                 `json:"enabled"`
	Workers   int                  `json:"workers"`
	StorePath string               `json:"store_path,omitempty"`
	Kinds     []string             `json:"kinds,omitempty"`
	Counts    SyncCounts           `json:"counts"`
	ByKind    map[string]SyncCounts `json:"by_kind,omitempty"`
	ByJobType map[string]SyncCounts `json:"by_job_type,omitempty"`
	Jobs      []SyncJob            `json:"jobs"`
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
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
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

		byKind := make(map[string]SyncCounts)
		byType := make(map[string]SyncCounts)
		for _, job := range status.Jobs {
			c := byKind[job.Kind]
			bumpSyncCount(&c, job.State)
			byKind[job.Kind] = c
			t := byType[job.JobType]
			bumpSyncCount(&t, job.State)
			byType[job.JobType] = t
		}
		if len(byKind) > 0 {
			status.ByKind = byKind
		}
		if len(byType) > 0 {
			status.ByJobType = byType
		}
	}
	if s.syncWorker != nil {
		status.Kinds = s.syncWorker.runners.Kinds()
	}
	writeJSON(w, status)
}

func bumpSyncCount(c *SyncCounts, state string) {
	switch state {
	case SyncJobQueued:
		c.Queued++
	case SyncJobRunning:
		c.Running++
	case SyncJobSucceeded:
		c.Succeeded++
	case SyncJobFailed:
		c.Failed++
	case SyncJobCanceled:
		c.Canceled++
	case SyncJobPaused:
		c.Paused++
	}
}

func (s *Server) handleSyncEnqueue(w http.ResponseWriter, r *http.Request) {
	if s.syncJobs == nil || !s.syncJobs.Enabled() {
		writeJSONError(w, http.StatusServiceUnavailable, "sync queue disabled")
		return
	}
	var req SyncEnqueueRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeBodyReadError(w, err)
				return
			}
			if r.ContentLength != 0 {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	req.Kind = strings.TrimSpace(req.Kind)
	req.JobType = strings.TrimSpace(req.JobType)
	if req.Kind == "" || req.JobType == "" {
		writeJSONError(w, http.StatusBadRequest, "kind and job_type are required")
		return
	}
	if _, ok := s.syncWorker.runners.For(req.Kind, req.JobType); !ok {
		writeJSONError(w, http.StatusBadRequest,
			"no sync runner for kind="+req.Kind+" job_type="+req.JobType)
		return
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
		writeJSONError(w, http.StatusInternalServerError, "failed to enqueue sync job")
		return
	}
	if s.syncWorker != nil {
		s.syncWorker.Kick()
	}
	writeJSON(w, job)
}

// HandleAdminSyncCancel parks a queued or running job: POST /v1/admin/sync/{id}/cancel
func (s *Server) HandleAdminSyncCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.syncJobs == nil || !s.syncJobs.Enabled() {
		writeJSONError(w, http.StatusServiceUnavailable, "sync queue disabled")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/admin/sync/")
	id = strings.TrimSuffix(strings.Trim(id, "/"), "/cancel")
	id = strings.TrimSpace(id)
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "job id required")
		return
	}
	job, err := s.syncJobs.Cancel(id)
	if errors.Is(err, ErrSyncJobNotFound) {
		writeJSONError(w, http.StatusNotFound, "unknown sync job")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel sync job")
		return
	}
	writeJSON(w, job)
}
