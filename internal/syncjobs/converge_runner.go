package syncjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
)

// Converge job runners (issue #365): the scheduled job that moves a kind's
// data from its canonical home out to the group's other write-capable
// members. One runner serves one kind; a job chains through its members one
// target per step (the mirror runner's chain model, member list instead of
// source cursor).
//
// Convergence never deletes on a member. The write surface is Place* only —
// create-or-update with identity matching — so items that exist only on a
// member survive untouched. Conflicts (a member record changed underfoot,
// If-Match refusal) are counted and reported, never overwritten and never
// retried in place; the conflict hand-off is a later phase.

// convergeJobPayload is the job payload for one converge step. The target
// list is resolved once (first step) and carried forward so a chained job
// converges each member exactly once even as the group changes underneath.
type convergeJobPayload struct {
	Kind string `json:"kind"`
	// SourceService is the kind's canonical home, recorded on the first step.
	SourceService string `json:"source_service,omitempty"`
	// Targets are the write-capable members to converge, in order.
	Targets []string `json:"targets,omitempty"`
	// Index is the next target to converge.
	Index int `json:"index,omitempty"`

	// Accumulated across steps for the admin panel.
	Planned    int  `json:"planned,omitempty"`
	Copied     int  `json:"copied,omitempty"`
	Skipped    int  `json:"skipped,omitempty"`
	Conflicts  int  `json:"conflicts,omitempty"`
	Truncated  bool `json:"truncated,omitempty"`
	TargetDone int  `json:"target_done,omitempty"`
}

// convergeKinds are the kinds a converge runner is registered for: the
// entity kinds whose write placement is whole-collection (contacts, events).
// Playlists converge in a later phase — item paging and order fidelity need
// the chained placement contract first.
var convergeKinds = []string{library.MirrorKindContacts, library.MirrorKindEvents}

// SyncConvergeKinds are the kinds the build registers converge runners for —
// the schedule seed's kind list (the store does not know them).
var SyncConvergeKinds = convergeKinds

// convergeRunner runs converge jobs for one library kind.
type convergeRunner struct {
	run  *Runners
	kind string
}

// JobTypes implements SyncRunner.
func (r *convergeRunner) JobTypes() []string {
	return []string{SyncJobTypeConverge}
}

// Kind implements SyncRunner.
func (r *convergeRunner) Kind() string { return r.kind }

// Run executes one bounded converge step: one member per step, chained
// through the resolved target list until the group is converged.
func (r *convergeRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	if r == nil || r.run == nil {
		return SyncStep{}, fmt.Errorf("%w: converge runner not wired", ErrSyncTerminal)
	}
	s := r.run
	var p convergeJobPayload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return SyncStep{}, fmt.Errorf("%w: bad converge payload: %v", ErrSyncTerminal, err)
		}
	}
	// The runner's kind is authoritative — dispatch already matched it.
	p.Kind = r.kind

	if p.Targets == nil {
		note, err := r.resolveTargets(&p)
		if err != nil {
			return SyncStep{}, err
		}
		if note != "" {
			return r.step(p, "", true, note), nil
		}
	}
	if p.Index >= len(p.Targets) {
		return r.step(p, "", true, ""), nil
	}
	target := p.Targets[p.Index]

	// Background sync runs at owner tier: no request key is in play.
	if err := s.Mirror.RequireMirrorWritable(s.Host.OwnerGrants(), target, p.Kind); err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	if err := s.Mirror.RequireMirrorReadable(s.Host.OwnerGrants(), p.SourceService, p.Kind); err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}

	items, truncated, err := r.listCanonical(ctx, p.SourceService)
	if err != nil {
		// Provider trouble is retryable; the target list and index are
		// already durable in the job payload.
		return r.step(p, target, false, ""), err
	}
	if len(items.contacts)+len(items.events) > library.MirrorTrackCap {
		items = items.cap(library.MirrorTrackCap)
		truncated = true
	}

	res, err := r.placeOn(ctx, target, items)
	if err != nil {
		// Counters stay unmutated on failure so a retry of this step cannot
		// double-count. Placement is create-or-update, so the retry is safe.
		return r.step(p, target, false, ""), err
	}

	// Per-item watermarks on the member, then one copy row per member —
	// the rows freshness reads.
	for _, rec := range res.Placed {
		s.Mirror.RecordMirrorItemCopy(p.Kind, rec.Name, target, rec.ExternalID, rec.Name, rec.ETag)
	}
	s.Mirror.RecordMirrorCopy(p.Kind, p.SourceService, "", target, "", p.Kind, res.Copied, "")

	p.Planned += items.len()
	p.Copied += res.Copied
	p.Skipped += len(res.Skips)
	p.Conflicts += len(res.Conflicts)
	if truncated {
		p.Truncated = true
	}
	p.TargetDone = p.Index + 1
	p.Index++
	if p.Index >= len(p.Targets) {
		return r.step(p, target, true, ""), nil
	}
	return r.step(p, target, false, ""), nil
}

// resolveTargets fills the payload's source and target list on the first
// step. The note is set when there is nothing to do (no canonical home, or
// no member that can receive writes) — that completes the job rather than
// failing it. A terminal error means the group is misconfigured in a way
// retrying cannot fix.
func (r *convergeRunner) resolveTargets(p *convergeJobPayload) (string, error) {
	s := r.run
	hub, members := s.Host.SyncGroup(p.Kind)
	if hub == "" {
		return "no canonical home designated for " + p.Kind, nil
	}
	p.SourceService = hub
	p.Targets = make([]string, 0, len(members))
	for _, id := range members {
		if id == hub || !convergeTargetOK(s, p.Kind, id) {
			continue
		}
		p.Targets = append(p.Targets, id)
	}
	if len(p.Targets) == 0 {
		return "no write-capable members to converge", nil
	}
	p.Index = 0
	return "", nil
}

// convergeTargetOK reports whether a group member can receive converged
// writes: configured, not blocked, not a read-only local import store, and
// write-capable for the kind (issue #365).
func convergeTargetOK(s *Runners, kind, serviceID string) bool {
	if s.Host.ServiceBlocked(serviceID) {
		return false
	}
	if connectors.LocalImportStoreIDs()[serviceID] {
		return false
	}
	conn, ok := s.Connectors[serviceID]
	if !ok {
		return false
	}
	k, ok := s.Mirror.RegistryKind(kind)
	if !ok {
		return false
	}
	return k.WriteCapable(conn)
}

// entitySet is one kind's canonical collection for a step. Only one field is
// populated; it is the runner's kind that says which.
type entitySet struct {
	contacts []kinds.Contact
	events   []kinds.CalendarEvent
}

func (e entitySet) len() int { return len(e.contacts) + len(e.events) }

// cap trims the collection to n items (MirrorTrackCap).
func (e entitySet) cap(n int) entitySet {
	if len(e.contacts) > n {
		e.contacts = e.contacts[:n]
	}
	if len(e.events) > n {
		e.events = e.events[:n]
	}
	return e
}

// listCanonical reads the kind's collection from the canonical home.
func (r *convergeRunner) listCanonical(ctx context.Context, hub string) (entitySet, bool, error) {
	switch r.kind {
	case library.MirrorKindContacts:
		src, err := library.MirrorOpFor[kinds.ContactProvider](r.run.Mirror, hub, "converge source")
		if err != nil {
			return entitySet{}, false, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		contacts, truncated, err := src.ListUserContacts(ctx)
		if err != nil {
			return entitySet{}, false, err
		}
		return entitySet{contacts: contacts}, truncated, nil
	case library.MirrorKindEvents:
		src, err := library.MirrorOpFor[kinds.CalendarEventProvider](r.run.Mirror, hub, "converge source")
		if err != nil {
			return entitySet{}, false, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		events, truncated, err := src.ListCalendarEvents(ctx)
		if err != nil {
			return entitySet{}, false, err
		}
		return entitySet{events: events}, truncated, nil
	default:
		return entitySet{}, false, fmt.Errorf("%w: kind %q does not converge", ErrSyncTerminal, r.kind)
	}
}

// placeOn places the canonical collection on one member. Matching,
// already-held skips, and If-Match conflict reporting live in the write
// operation — convergence never deletes.
func (r *convergeRunner) placeOn(ctx context.Context, target string, items entitySet) (kinds.PlaceEntitiesResult, error) {
	switch r.kind {
	case library.MirrorKindContacts:
		dst, err := library.MirrorOpFor[kinds.ContactWriteProvider](r.run.Mirror, target, "converge target")
		if err != nil {
			return kinds.PlaceEntitiesResult{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		return dst.PlaceContacts(ctx, items.contacts)
	case library.MirrorKindEvents:
		dst, err := library.MirrorOpFor[kinds.CalendarEventWriteProvider](r.run.Mirror, target, "converge target")
		if err != nil {
			return kinds.PlaceEntitiesResult{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		return dst.PlaceEvents(ctx, items.events)
	default:
		return kinds.PlaceEntitiesResult{}, fmt.Errorf("%w: kind %q does not converge", ErrSyncTerminal, r.kind)
	}
}

// step projects the accumulated payload onto a SyncStep. done closes the
// job; otherwise the step carries the target index forward for the next one.
// target names the member this step converged (empty when none ran).
func (r *convergeRunner) step(p convergeJobPayload, target string, done bool, note string) SyncStep {
	if p.Kind == "" {
		p.Kind = r.kind
	}
	s := SyncStep{
		Done: done,
		Progress: map[string]any{
			"kind":           p.Kind,
			"source_service": p.SourceService,
			"target":         target,
			"targets":        len(p.Targets),
			"target_done":    p.TargetDone,
			"planned":        p.Planned,
			"copied":         p.Copied,
			"skipped":        p.Skipped,
			"conflicts":      p.Conflicts,
			"truncated":      p.Truncated,
		},
	}
	if note != "" {
		s.Progress["note"] = note
		slog.Info("converge skipped", "kind", p.Kind, "note", note)
	}
	if b, err := json.Marshal(p); err == nil {
		s.Payload = b
	}
	return s
}

// newConvergeRunners builds one converge runner per supported kind.
func newConvergeRunners(run *Runners) []SyncRunner {
	out := make([]SyncRunner, 0, len(convergeKinds))
	for _, kind := range convergeKinds {
		out = append(out, &convergeRunner{run: run, kind: kind})
	}
	return out
}

// registerConvergeRunners adds the converge runners to a registry.
func registerConvergeRunners(reg *RunnerRegistry, run *Runners) {
	for _, r := range newConvergeRunners(run) {
		reg.Register(r)
	}
}
