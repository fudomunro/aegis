package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fudomunro/aegis/internal/core/syncjobs"
)

// Freshness-check runner (issue #227 phase 2). One runner per library kind,
// all sharing a FreshnessEngine, so the (kind, job_type) registry seam is
// unchanged. Each job step walks a bounded batch of copy rows and chains
// through the ledger via after_id, the same shape as the mail ingest runner.

// freshnessJobPayload is the job payload for one freshness batch.
type freshnessJobPayload struct {
	Kind    string `json:"kind,omitempty"`
	AfterID int64  `json:"after_id,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// freshnessRunner runs freshness_check jobs for one library kind.
type freshnessRunner struct {
	engine *FreshnessEngine
	kind   string
}

// JobTypes implements syncjobs.SyncRunner.
func (r *freshnessRunner) JobTypes() []string { return []string{syncjobs.SyncJobTypeFreshness} }

// Kind implements syncjobs.SyncRunner.
func (r *freshnessRunner) Kind() string { return r.kind }

// Run checks one bounded batch of copy rows and chains until the ledger is
// exhausted. Always succeeds as long as the store answers: an uncheckable row
// is skipped and left alone, never failed.
func (r *freshnessRunner) Run(ctx context.Context, job syncjobs.SyncJob) (syncjobs.SyncStep, error) {
	if r == nil || r.engine == nil {
		return syncjobs.SyncStep{}, fmt.Errorf("%w: freshness engine not wired", syncjobs.ErrSyncTerminal)
	}
	var p freshnessJobPayload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return syncjobs.SyncStep{}, fmt.Errorf("%w: bad freshness payload: %v", syncjobs.ErrSyncTerminal, err)
		}
	}
	kind := p.Kind
	if kind == "" {
		kind = r.kind
	}
	batch, err := r.engine.RunBatch(ctx, kind, p.AfterID, p.Limit)
	if err != nil {
		return syncjobs.SyncStep{}, err
	}

	step := syncjobs.SyncStep{
		Done: batch.Done,
		Progress: map[string]any{
			"kind":       batch.Kind,
			"checked":    batch.Checked,
			"in_sync":    batch.InSync,
			"stale":      batch.Stale,
			"missing":    batch.Missing,
			"skipped":    batch.Skipped,
			"after_id":   batch.NextAfter,
			"batch_done": batch.Done,
		},
	}
	if !batch.Done {
		next := freshnessJobPayload{
			Kind:    kind,
			AfterID: batch.NextAfter,
			Limit:   p.Limit,
		}
		if b, merr := json.Marshal(next); merr == nil {
			step.Payload = b
		}
	}
	return step, nil
}

// freshnessKinds are the library kinds a freshness_check runner is registered
// for today. Copy rows for other kinds (contacts, events, photos) still show
// up in the freshness rollup as unchecked — they simply have no checker yet.
var freshnessKinds = []string{"messages", "playlists", "tracks"}

// newFreshnessRunners builds one freshness runner per supported kind.
func newFreshnessRunners(srv *Server) []syncjobs.SyncRunner {
	engine := NewFreshnessEngine(srv, newCheckerRegistry(srv))
	out := make([]syncjobs.SyncRunner, 0, len(freshnessKinds))
	for _, kind := range freshnessKinds {
		out = append(out, &freshnessRunner{engine: engine, kind: kind})
	}
	return out
}

// registerFreshnessRunners adds the freshness runners to a registry.
func registerFreshnessRunners(reg *syncjobs.RunnerRegistry, srv *Server) {
	for _, r := range newFreshnessRunners(srv) {
		reg.Register(r)
	}
}
