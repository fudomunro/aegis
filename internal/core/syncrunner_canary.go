package core

import (
	"context"
	"fmt"
)

// Canary runner (issue #86): the scheduled-test job type that turns upstream
// drift into sync-job failures the maintenance scanner can ticket. One step,
// one fetch — a page of the user's saved tracks through the same
// mirrorSource seam the mirror jobs use, so it follows the configured
// endpoint (the fake upstream, a mock, or the real provider).
//
// Two failure classes surface here:
//   - the fetch/parse fails (error statuses, garbage payloads), and
//   - the fetch "succeeds" into silence: pagination metadata claims a
//     library but the items array yields nothing (response shape drift).
//
// A genuinely empty library (total 0) is health, not drift.

// canaryPageLimit is the one page a canary fetches. Enough to notice a
// broken shape; no need to walk the whole library.
const canaryPageLimit = 50

type canaryRunner struct {
	srv *Server
}

// JobTypes implements SyncRunner.
func (r *canaryRunner) JobTypes() []string { return []string{SyncJobTypeCanary} }

// Kind implements SyncRunner. Liked-tracks is the canaried surface for now.
func (r *canaryRunner) Kind() string { return "tracks" }

// Run implements SyncRunner: fetch one page and judge it.
func (r *canaryRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	src, err := r.srv.mirrorSourceFor(job.Service, "tracks")
	if err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	tracks, total, _, err := src.loadLikedTracksPage(ctx, "", canaryPageLimit)
	if err != nil {
		return SyncStep{}, err
	}
	if total > 0 && len(tracks) == 0 {
		return SyncStep{}, fmt.Errorf(
			"%w: canary got total=%d but no items — response shape drift?", ErrSyncTerminal, total)
	}
	return SyncStep{
		Done: true,
		Progress: map[string]any{
			"service": job.Service,
			"listed":  len(tracks),
			"total":   total,
		},
	}, nil
}

// registerCanaryRunners adds the canary runners to a registry.
func registerCanaryRunners(reg *RunnerRegistry, srv *Server) {
	reg.Register(&canaryRunner{srv: srv})
}
