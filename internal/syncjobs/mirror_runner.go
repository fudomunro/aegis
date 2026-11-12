package syncjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

// Mirror and repair job runners (issue #227 phase 3). One runner serves both
// job types: `mirror` copies a list from one service to another, `repair`
// re-places a copy that freshness marked stale or missing. Repair is the
// executor surface #86 needs; the policy of *when* to repair stays there.
//
// A playlist mirror chains through the queue one source page at a time,
// appending to a single target playlist (PlaylistItemWriteProvider with a
// known playlist id) instead of creating a playlist per batch. Repair adds a
// target-side existence filter so a re-run converges rather than duplicating
// what is already placed — the same name/identity matching placement uses.

// mirrorJobPayload is the job payload for a mirror or repair step.
type mirrorJobPayload struct {
	Kind          string `json:"kind"`
	SourceService string `json:"source_service,omitempty"`
	SourceID      string `json:"source_id,omitempty"`
	TargetService string `json:"target_service"`
	Name          string `json:"name,omitempty"`
	// Cursor pages the source list. Empty starts at the beginning.
	Cursor string `json:"cursor,omitempty"`
	// TargetID accumulates into one target playlist across steps. Empty on
	// the first step means "create one".
	TargetID string `json:"target_id,omitempty"`
	// DryRun counts what would be placed without writing to the target.
	DryRun bool `json:"dry_run,omitempty"`
	// Limit bounds one step's source page. Defaults to the mirror cap.
	Limit int `json:"limit,omitempty"`
	// Force marks a repair: skip what the target already holds and stamp
	// the copy row fresh when done.
	Force bool `json:"force,omitempty"`

	// Accumulated across steps for the admin panel.
	Planned     int `json:"planned,omitempty"`
	Copied      int `json:"copied,omitempty"`
	Skipped     int `json:"skipped,omitempty"`
	SourceTotal int `json:"source_total,omitempty"`
}

// mirrorRunner runs mirror and repair jobs for one library kind.
type mirrorRunner struct {
	run  *Runners
	kind string
}

// JobTypes implements SyncRunner.
func (r *mirrorRunner) JobTypes() []string {
	return []string{SyncJobTypeMirror, SyncJobTypeRepair}
}

// Kind implements SyncRunner.
func (r *mirrorRunner) Kind() string { return r.kind }

// mirrorKinds are the kinds a mirror runner is registered for. Contacts and
// events mirror as one entity-level call (handleEntityMirror) and do not
// chain, so they are not registered here yet.
var mirrorKinds = []string{library.MirrorKindPlaylists, library.MirrorKindTracks}

// Run executes one bounded mirror step and chains until the source list is
// exhausted.
func (r *mirrorRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	if r == nil || r.run == nil {
		return SyncStep{}, fmt.Errorf("%w: mirror runner not wired", ErrSyncTerminal)
	}
	s := r.run
	var p mirrorJobPayload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return SyncStep{}, fmt.Errorf("%w: bad mirror payload: %v", ErrSyncTerminal, err)
		}
	}
	if p.Kind == "" {
		p.Kind = r.kind
	}
	if p.TargetService == "" {
		return SyncStep{}, fmt.Errorf("%w: target_service is required", ErrSyncTerminal)
	}
	if p.Kind == library.MirrorKindPlaylists && p.SourceID == "" {
		return SyncStep{}, fmt.Errorf("%w: source_id is required for kind=playlists", ErrSyncTerminal)
	}
	if p.SourceService == "" {
		p.SourceService = s.Mirror.DefaultMirrorSource(p.Kind)
	}
	if p.SourceService == "" {
		return SyncStep{}, fmt.Errorf("%w: source_service is required (no canonical home configured)", ErrSyncTerminal)
	}
	if p.SourceService == p.TargetService {
		return SyncStep{}, fmt.Errorf("%w: source_service and target_service must differ", ErrSyncTerminal)
	}
	// Background sync runs at owner tier: no request key is in play.
	if err := s.Mirror.RequireMirrorWritable(s.Host.OwnerGrants(), p.TargetService, p.Kind); err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	if err := s.Mirror.RequireMirrorReadable(s.Host.OwnerGrants(), p.SourceService, p.Kind); err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	src, err := s.Mirror.MirrorSourceFor(p.SourceService, p.Kind)
	if err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	dst, err := s.Mirror.MirrorTargetFor(p.TargetService, p.Kind)
	if err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}

	pageLimit := p.Limit
	if pageLimit <= 0 || pageLimit > library.MirrorTrackCap {
		pageLimit = library.MirrorTrackCap
	}
	var (
		tracks []kinds.Track
		total  int
		next   string
		srcRef library.MirrorRef
	)
	switch p.Kind {
	case library.MirrorKindPlaylists:
		var ref kinds.Playlist
		var page kinds.TrackPage
		ref, page, err = src.ListPlaylistItems(ctx, p.SourceID, p.Cursor, pageLimit)
		tracks, total, next = page.Tracks, page.Total, page.Next
		srcRef = library.MirrorRefFromPlaylist(p.SourceService, ref, total)
	case library.MirrorKindTracks:
		var page kinds.TrackPage
		page, err = src.ListUserLikedTracksPage(ctx, p.Cursor, pageLimit)
		tracks, total, next = page.Tracks, page.Total, page.Next
		srcRef = library.MirrorRef{Service: p.SourceService, Name: "liked/saved tracks", Count: total}
	default:
		return SyncStep{}, fmt.Errorf("%w: kind %q is not a mirrorable list", ErrSyncTerminal, p.Kind)
	}
	if err != nil {
		// Provider trouble is retryable; the cursor is already durable in the
		// job payload so the step can be re-run.
		return r.step(p, false), err
	}
	p.SourceTotal = total

	// Placement matching lives in the write operations: they honor their own
	// service's local references and catalog-search the rest. Service-local
	// IDs never transfer across APIs.

	// The source page is the work this step planned, whether or not some of
	// it turns out to be already placed.
	pageSize := len(tracks)

	// Repair: drop what the target already holds so a re-run converges.
	// The identity ladder is the merge layer's (issue #366): durable id
	// first, then duration-guarded name.
	if p.Force && p.TargetID != "" && len(tracks) > 0 {
		existing, ferr := loadTargetIdentities(ctx, s, p.TargetService, p.TargetID)
		if ferr != nil {
			slog.Warn("mirror repair could not read the target list",
				"service", p.TargetService, "error", ferr)
		} else {
			filtered := tracks[:0]
			for _, t := range tracks {
				if !existing.Has(t) {
					filtered = append(filtered, t)
				}
			}
			p.Skipped += len(tracks) - len(filtered)
			tracks = filtered
		}
	}
	p.Planned += pageSize

	name := strings.TrimSpace(p.Name)
	if name == "" && p.Kind == library.MirrorKindPlaylists {
		name = srcRef.Name
	}
	if name == "" {
		name = defaultMirrorName(p.Kind)
	}

	if !p.DryRun && len(tracks) > 0 {
		switch p.Kind {
		case library.MirrorKindPlaylists:
			res, perr := dst.PlacePlaylistItems(ctx, p.TargetID, name, tracks)
			p.Skipped += len(res.Skips)
			if res.PlaylistID != "" {
				p.TargetID = res.PlaylistID
			}
			p.Copied += res.Copied
			if perr != nil {
				return r.step(p, false), perr
			}
		case library.MirrorKindTracks:
			res, perr := dst.SaveLikedTracks(ctx, tracks)
			p.Skipped += len(res.Skips)
			p.Copied += res.Copied
			if perr != nil {
				return r.step(p, false), perr
			}
		}
	}

	if next != "" {
		p.Cursor = next
		return r.step(p, false), nil
	}

	// Source exhausted: stamp the copy ledger with the finished placement.
	if !p.DryRun {
		switch p.Kind {
		case library.MirrorKindPlaylists:
			s.Mirror.RecordMirrorCopy(library.MirrorKindPlaylists, p.SourceService, p.SourceID,
				p.TargetService, p.TargetID, name, p.Copied, "")
		case library.MirrorKindTracks:
			s.Mirror.RecordMirrorCopy(library.MirrorKindTracks, p.SourceService, "",
				p.TargetService, "", "liked/saved tracks", p.Copied, "")
		}
	}
	return r.step(p, true), nil
}

// step projects the accumulated payload onto a SyncStep. done closes the job;
// otherwise the step carries the cursor forward for the next one.
func (r *mirrorRunner) step(p mirrorJobPayload, done bool) SyncStep {
	if p.Kind == "" {
		p.Kind = r.kind
	}
	s := SyncStep{
		Done: done,
		Progress: map[string]any{
			"kind":           p.Kind,
			"source_service": p.SourceService,
			"target_service": p.TargetService,
			"target_id":      p.TargetID,
			"planned":        p.Planned,
			"copied":         p.Copied,
			"skipped":        p.Skipped,
			"repair":         p.Force,
			"dry_run":        p.DryRun,
			"source_total":   p.SourceTotal,
		},
	}
	if b, err := json.Marshal(p); err == nil {
		s.Payload = b
	}
	return s
}

// defaultMirrorName is the fallback playlist title when the source has none.
func defaultMirrorName(kind string) string {
	if kind == library.MirrorKindTracks {
		return "Aegis liked tracks backup"
	}
	return "Aegis mirror"
}

// loadTargetIdentities indexes what a target playlist already holds so a
// repair can skip those items (issue #366's identity ladder). It reuses the
// items operation on the target service, so no extra adapter method is
// needed.
func loadTargetIdentities(ctx context.Context, s *Runners, targetService, targetID string) (*kinds.TrackMatcher, error) {
	src, err := s.Mirror.MirrorSourceFor(targetService, library.MirrorKindPlaylists)
	if err != nil {
		return nil, err
	}
	out := kinds.NewTrackMatcher(nil)
	cursor := ""
	for i := 0; i < shared.LibraryPageCap; i++ {
		_, page, err := src.ListPlaylistItems(ctx, targetID, cursor, 100)
		if err != nil {
			return out, err
		}
		for _, t := range page.Tracks {
			out.Add(t)
		}
		if page.Next == "" {
			return out, nil
		}
		cursor = page.Next
	}
	return out, nil
}

// newMirrorRunners builds one mirror/repair runner per supported kind.
func newMirrorRunners(run *Runners) []SyncRunner {
	out := make([]SyncRunner, 0, len(mirrorKinds))
	for _, kind := range mirrorKinds {
		out = append(out, &mirrorRunner{run: run, kind: kind})
	}
	return out
}

// registerMirrorRunners adds the mirror/repair runners to a registry.
func registerMirrorRunners(reg *RunnerRegistry, run *Runners) {
	for _, r := range newMirrorRunners(run) {
		reg.Register(r)
	}
}
