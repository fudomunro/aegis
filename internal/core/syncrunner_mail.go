package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/types"
)

// Mail archive ingest (issue #227). doMailIngest is the single implementation
// behind both the HTTP sync route and the sync queue runner, so the two
// surfaces cannot drift. The runner adds queue bookkeeping on top; the route
// keeps its response contract. Core-owned (like mail_archive_sync.go) — the
// Host seam for connector-side runners stays open.

// mailIngestKind is the library kind the mail ingest runner serves.
const mailIngestKind = "messages"

// mailIngestPayload is the job payload for an ingest step.
type mailIngestPayload struct {
	Service     string `json:"service,omitempty"`
	MaxMessages int    `json:"max_messages,omitempty"`
	Query       string `json:"query,omitempty"`
	Path        string `json:"path,omitempty"`
	ResetCursor bool   `json:"reset_cursor,omitempty"`
}

func (p mailIngestPayload) options() types.MailIngestOptions {
	return types.MailIngestOptions{
		MaxMessages: p.MaxMessages,
		Query:       p.Query,
		Path:        p.Path,
		ResetCursor: p.ResetCursor,
	}
}

// mailIngestRunner runs ingest and import_snapshot jobs for the local mail
// archive. `ingest` is live cursor paging (Gmail); `import_snapshot` is a
// snapshot import (Google Takeout). Both chain through the same bounded-run
// contract, so one runner serves both.
type mailIngestRunner struct {
	srv *Server
}

// JobTypes implements SyncRunner.
func (r *mailIngestRunner) JobTypes() []string {
	return []string{SyncJobTypeIngest, SyncJobTypeImport}
}

// Kind implements SyncRunner.
func (r *mailIngestRunner) Kind() string { return mailIngestKind }

// Run executes one bounded archive ingest step.
func (r *mailIngestRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	if r == nil || r.srv == nil {
		return SyncStep{}, fmt.Errorf("%w: mail ingest runner not wired", ErrSyncTerminal)
	}
	p, err := parseMailIngestPayload(job)
	if err != nil {
		return SyncStep{}, err
	}
	result, err := r.srv.doMailIngest(ctx, p)
	step := SyncStep{
		Done: result.Done,
		Progress: mergeMailProgress(job.Progress, map[string]any{
			"service":     result.Service,
			"listed":      result.Listed,
			"stored":      result.Stored,
			"skipped":     result.Skipped,
			"failed":      result.Failed,
			"truncated":   result.Truncated,
			"done":        result.Done,
			"query":       result.Query,
			"last_stored": result.LastStored,
		}),
	}
	if err != nil {
		// Leave step.Payload empty so a retry keeps the job's payload as-is,
		// including reset_cursor — a failed first step must not silently
		// resume from the saved cursor instead of starting over.
		// RetryAfter is provider knowledge the ingest reports (issue #286).
		if result.RetryAfter > 0 {
			step.RetryAfter = result.RetryAfter
			return step, &SyncRetryAfter{Err: err, After: result.RetryAfter}
		}
		return step, err
	}
	// Only after a successful step: carry the service forward but drop the
	// one-shot reset_cursor so the chain's later steps resume normally.
	if !result.Done {
		next := mailIngestPayload{
			Service:     p.Service,
			MaxMessages: p.MaxMessages,
			Query:       p.Query,
			Path:        p.Path,
		}
		if b, merr := json.Marshal(next); merr == nil {
			step.Payload = b
		}
	}
	return step, nil
}

// parseMailIngestPayload fills a payload from a job, defaulting the service
// from the job row so a chained step need not repeat it. The job type picks
// the default: `import_snapshot` is a snapshot import (Takeout), `ingest` is
// live cursor paging (Gmail).
func parseMailIngestPayload(job SyncJob) (mailIngestPayload, error) {
	var p mailIngestPayload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return p, fmt.Errorf("%w: bad ingest payload: %v", ErrSyncTerminal, err)
		}
	}
	if p.Service == "" {
		p.Service = strings.TrimSpace(job.Service)
	}
	if p.Service == "" {
		if job.JobType == SyncJobTypeImport {
			p.Service = connectors.TakeoutServiceID
		} else {
			p.Service = connectors.GoogleGmailServiceID
		}
	}
	return p, nil
}

// doMailIngest runs one bounded ingest and reports the raw mail result. Both
// the HTTP sync route and the queue runner go through here. The ingest is a
// connector-owned implementation behind the MailSink (issue #286).
func (s *Server) doMailIngest(ctx context.Context, p mailIngestPayload) (types.MailIngestResult, error) {
	if s.mail == nil {
		return types.MailIngestResult{Service: p.Service, Error: "mail archive disabled"},
			fmt.Errorf("%w: mail archive disabled", ErrSyncTerminal)
	}
	ingest := s.mailIngestFor(p.Service)
	if ingest == nil {
		return types.MailIngestResult{Service: p.Service},
			fmt.Errorf("%w: service %s cannot sync the mail archive", ErrSyncTerminal, p.Service)
	}
	// Live (connector-backed) sources must have a non-blocked config entry;
	// ingest-only sync locations like the Takeout export are not services.
	if _, live := s.connectors[p.Service]; live {
		svc := s.config.GetService(p.Service)
		if svc == nil || svc.Action == "block" {
			return types.MailIngestResult{Service: p.Service},
				fmt.Errorf("%w: service %s is blocked", ErrSyncTerminal, p.Service)
		}
	}

	// Serialize with every other ingest so two page cursors cannot interleave
	// into one store — the HTTP surface and the queue share this lock.
	s.mailSyncMu.Lock()
	defer s.mailSyncMu.Unlock()

	result, err := ingest.IngestMail(ctx, mail.Sink{A: s.mail}, p.options())
	if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	// An ingest whose location is missing or rejected is terminal for the
	// queue and a client error on the HTTP surface.
	if errors.Is(err, types.ErrMailConfig) {
		err = fmt.Errorf("%w: %w", ErrSyncTerminal, err)
	}
	if err == nil && result.Snapshot != nil {
		s.recordTakeoutSnapshot(result.Snapshot.Path, result.Snapshot.Scanned,
			result.Snapshot.Stored, result.Snapshot.Skipped, result.Snapshot.Done)
	}
	// Fail-open: report what was stored even when the run failed mid-batch.
	return result, err
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

// mailProgressCounters are the per-step fields that accumulate across a chain
// so an operator sees the running total, not the last page's counts.
var mailProgressCounters = []string{"listed", "stored", "skipped", "failed"}

// mergeMailProgress folds one step's counts onto the job's running totals.
// Boolean/identity fields (done, truncated, service, query) reflect the
// latest step.
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
