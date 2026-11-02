package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

// Mail archive ingest runner (issue #227, phase 1). Wraps the existing
// MailArchiveSyncer contract so the queue, not a caller loop, owns resume:
// each step is one bounded page batch and the durable cursor keeps its place.
// This is the same seam POST /v1/mail/sync uses, minus the "repeat until
// done" choreography.

// mailIngestKind is the library kind the mail ingest runner serves.
const mailIngestKind = "messages"

// mailIngestPayload is the job payload for an ingest step.
type mailIngestPayload struct {
	Service     string `json:"service,omitempty"`
	MaxMessages int    `json:"max_messages,omitempty"`
	Query       string `json:"query,omitempty"`
	ResetCursor bool   `json:"reset_cursor,omitempty"`
}

// mailIngestRunner runs ingest jobs for the local mail archive.
// Core-owned (like mail_archive_sync.go) — the Host seam for connector-side
// runners stays open (issue #227).
type mailIngestRunner struct {
	srv *Server
}

// JobTypes implements SyncRunner.
func (r *mailIngestRunner) JobTypes() []string { return []string{SyncJobTypeIngest} }

// Kind implements SyncRunner.
func (r *mailIngestRunner) Kind() string { return mailIngestKind }

// Run executes one bounded archive ingest step.
func (r *mailIngestRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	if r == nil || r.srv == nil {
		return SyncStep{}, fmt.Errorf("%w: mail ingest runner not wired", ErrSyncTerminal)
	}
	s := r.srv
	if a := s.mail; a == nil {
		return SyncStep{}, fmt.Errorf("%w: mail archive disabled", ErrSyncTerminal)
	}

	var p mailIngestPayload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return SyncStep{}, fmt.Errorf("%w: bad ingest payload: %v", ErrSyncTerminal, err)
		}
	}
	// The job's service wins only when the payload does not name one, so a
	// chained step can keep its service without repeating it in every payload.
	if p.Service == "" {
		p.Service = strings.TrimSpace(job.Service)
	}
	if p.Service == "" {
		p.Service = connectors.GoogleGmailServiceID
	}

	// Takeout is a read-only import location, not a live connector ingest.
	if p.Service == takeoutServiceID {
		return s.runTakeoutStep(job, p)
	}

	syncer := s.mailArchiveSyncer(p.Service)
	if syncer == nil {
		return SyncStep{}, fmt.Errorf("%w: service %s cannot sync the mail archive", ErrSyncTerminal, p.Service)
	}
	svc := s.config.GetService(p.Service)
	if svc == nil || svc.Action == "block" {
		return SyncStep{}, fmt.Errorf("%w: service %s is blocked", ErrSyncTerminal, p.Service)
	}

	// Serialize with the HTTP sync surface so an admin click and a queued
	// job cannot interleave two page cursors into one store.
	s.mailSyncMu.Lock()
	defer s.mailSyncMu.Unlock()

	max := p.MaxMessages
	if max <= 0 {
		max = 200
	}
	if max > 1000 {
		max = 1000
	}
	result, err := syncer.SyncMailArchive(ctx, s.mail, MailSyncOptions{
		MaxMessages: max,
		Query:       p.Query,
		ResetCursor: p.ResetCursor,
	})
	step := SyncStep{
		Done: result.Done,
		Progress: mergeMailProgress(job.Progress, map[string]any{
			"service":   result.Service,
			"listed":    result.Listed,
			"stored":    result.Stored,
			"skipped":   result.Skipped,
			"failed":    result.Failed,
			"truncated": result.Truncated,
			"done":      result.Done,
		}),
	}
	// Carry the (possibly reset) cursor flags forward only on the first step.
	if !result.Done {
		next := mailIngestPayload{
			Service:     p.Service,
			MaxMessages: p.MaxMessages,
			Query:       p.Query,
		}
		if b, merr := json.Marshal(next); merr == nil {
			step.Payload = b
		}
	}
	if err != nil {
		// Quota and other provider pauses get a longer backoff than the
		// default: the window is measured in minutes, not seconds.
		if isGmailQuotaError(err) {
			step.RetryAfter = quotaSyncBackoff
			return step, &SyncRetryAfter{Err: err, After: quotaSyncBackoff}
		}
		return step, err
	}
	return step, nil
}

// quotaSyncBackoff waits out a provider per-user rate quota window.
const quotaSyncBackoff = 5 * time.Minute

// mailArchiveSyncer builds the ingest adapter for a service, mirroring the
// construction in handleMailSyncRequest. Returns nil for non-syncable services.
func (s *Server) mailArchiveSyncer(service string) MailArchiveSyncer {
	connector, ok := s.connectors[service]
	if !ok {
		return nil
	}
	gc, ok := connector.(*connectors.GoogleGmailConnector)
	if !ok {
		return nil
	}
	return &gmailArchiveSyncer{GoogleGmailConnector: gc, host: s}
}

// runTakeoutStep ingests one bounded batch from the read-only Takeout export.
func (s *Server) runTakeoutStep(job SyncJob, p mailIngestPayload) (SyncStep, error) {
	a := s.mail
	path := strings.TrimSpace(p.Query)
	if strings.HasPrefix(path, "takeout:") {
		path = strings.TrimSpace(strings.TrimPrefix(path, "takeout:"))
	}
	if path == "" {
		path = a.TakeoutPath()
	}
	if path == "" {
		return SyncStep{}, fmt.Errorf("%w: takeout path not configured", ErrSyncTerminal)
	}

	s.mailSyncMu.Lock()
	defer s.mailSyncMu.Unlock()

	max := p.MaxMessages
	if max <= 0 {
		max = 5000
	}
	if max > 20000 {
		max = 20000
	}
	result, err := a.ImportTakeout(path, max)
	if err == nil && result.Error == "" {
		s.recordTakeoutSnapshot(path, result.Scanned, result.Stored, result.Skipped, result.Done)
	}
	step := SyncStep{
		Done: result.Done,
		Progress: mergeMailProgress(job.Progress, map[string]any{
			"service":   takeoutServiceID,
			"listed":    result.Scanned,
			"stored":    result.Stored,
			"skipped":   result.Skipped,
			"failed":    result.Failed,
			"truncated": result.Truncated,
			"done":      result.Done,
			"path":      path,
		}),
	}
	if err != nil {
		return step, err
	}
	return step, nil
}

// mailProgressCounters are the per-step fields that accumulate across a chain
// so an operator sees the running total, not the last page's counts.
var mailProgressCounters = []string{"listed", "stored", "skipped", "failed"}

// mergeMailProgress folds one step's counts onto the job's running totals.
// Boolean/identity fields (done, truncated, service, path) reflect the latest
// step.
func mergeMailProgress(prev json.RawMessage, step map[string]any) map[string]any {
	out := make(map[string]any, len(step))
	if len(prev) > 0 {
		var base map[string]any
		if err := json.Unmarshal(prev, &base); err == nil {
			for k, v := range base {
				out[k] = v
			}
		}
	}
	for k, v := range step {
		out[k] = v
	}
	for _, k := range mailProgressCounters {
		out[k] = toInt(out[k]) + toInt(step[k])
	}
	return out
}

// toInt coerces a JSON-decoded number (float64) or an int to an int.
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// newSyncRunnerRegistry wires the runners this build supports.
func newSyncRunnerRegistry(srv *Server) *RunnerRegistry {
	r := NewRunnerRegistry()
	r.Register(&mailIngestRunner{srv: srv})
	return r
}

// syncEnqueueFromRequest maps an admin enqueue request onto SyncEnqueue.
// Entity keys scope idempotency: a whole-collection ingest dedupes on the
// service, so repeat clicks do not stack duplicate work.
func syncEnqueueFromRequest(kind, jobType, service, entityKey string, payload map[string]any) SyncEnqueue {
	return SyncEnqueue{
		Kind:      strings.TrimSpace(kind),
		JobType:   strings.TrimSpace(jobType),
		Service:   strings.TrimSpace(service),
		EntityKey: strings.TrimSpace(entityKey),
		Payload:   payload,
	}
}
