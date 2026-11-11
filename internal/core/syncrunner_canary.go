package core

import (
	"context"
	"fmt"

	"github.com/fudomunro/aegis/internal/core/syncjobs"
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

// JobTypes implements syncjobs.SyncRunner.
func (r *canaryRunner) JobTypes() []string { return []string{syncjobs.SyncJobTypeCanary} }

// Kind implements syncjobs.SyncRunner. Liked-tracks is the canaried surface for now.
func (r *canaryRunner) Kind() string { return "tracks" }

// Run implements syncjobs.SyncRunner: fetch one page and judge it.
func (r *canaryRunner) Run(ctx context.Context, job syncjobs.SyncJob) (syncjobs.SyncStep, error) {
	src, err := r.srv.library().MirrorSourceFor(job.Service, "tracks")
	if err != nil {
		return syncjobs.SyncStep{}, fmt.Errorf("%w: %v", syncjobs.ErrSyncTerminal, err)
	}
	page, err := src.ListUserLikedTracksPage(ctx, "", canaryPageLimit)
	if err != nil {
		return syncjobs.SyncStep{}, err
	}
	if page.Total > 0 && len(page.Tracks) == 0 {
		return syncjobs.SyncStep{}, fmt.Errorf(
			"%w: canary got total=%d but no items — response shape drift?", syncjobs.ErrSyncTerminal, page.Total)
	}
	return syncjobs.SyncStep{
		Done: true,
		Progress: map[string]any{
			"service": job.Service,
			"listed":  len(page.Tracks),
			"total":   page.Total,
		},
	}, nil
}

// registerCanaryRunners adds the canary runners to a registry.
func registerCanaryRunners(reg *syncjobs.RunnerRegistry, srv *Server) {
	reg.Register(&canaryRunner{srv: srv})
}
