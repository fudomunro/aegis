package syncjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
)

// Converge job runners (issue #365, kind-owned plans since issue #382):
// the scheduled job that moves a kind's data from its canonical home out
// to the group's other write-capable members. One runner serves one kind
// — declared by that kind's converge plan (reader, writer, matcher,
// copy-row name, extras/conflict ops). The engine below is a
// reconciliation policy over kinds: it schedules, chains, retries, and
// records; it never names a kind. Entities chain one member per step (the
// mirror runner's chain model, member list instead of source cursor);
// saves and playlists chain one item batch per step — members outermost,
// then the canonical home's source list, then each source's pages.
//
// Convergence never deletes on a member. The write surface is place-only —
// create-or-update with identity matching — so items that exist only on a
// member survive untouched. Placement order is source order (order is
// fidelity); an already-converged playlist gains only what is missing.
// Conflicts (a member record changed underfoot, If-Match refusal or a
// member-side rename) are counted and reported, never overwritten and
// never retried in place; the conflict hand-off is a later phase.

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

	// ItemCursor chains the source list's pages (the save and playlist
	// shapes) across steps.
	ItemCursor string `json:"item_cursor,omitempty"`

	// Playlist chain state (the playlist shape): which canonical playlist
	// is in flight, its title, and the member playlist it maps to. The
	// member playlist id comes from the copy ledger (what this hub
	// playlist converged to before) or from the create that placed it.
	PlaylistIndex int    `json:"playlist_index,omitempty"`
	SourceID      string `json:"source_id,omitempty"`
	Name          string `json:"name,omitempty"`
	TargetID      string `json:"target_id,omitempty"`
	// RowName is the title we last set on the member (the copy row's name):
	// a member title that differs from both it and the canonical title is a
	// member-side rename — a conflict, not an overwrite.
	RowName       string `json:"row_name,omitempty"`
	PlaylistsDone int    `json:"playlists_done,omitempty"`

	// Accumulated across steps for the admin panel.
	Planned    int  `json:"planned,omitempty"`
	Copied     int  `json:"copied,omitempty"`
	Skipped    int  `json:"skipped,omitempty"`
	Conflicts  int  `json:"conflicts,omitempty"`
	Truncated  bool `json:"truncated,omitempty"`
	TargetDone int  `json:"target_done,omitempty"`
}

// resetPlaylistChain clears the in-flight source so the next step picks
// the following one (or moves to the next member).
func (p *convergeJobPayload) resetPlaylistChain() {
	p.SourceID = ""
	p.Name = ""
	p.ItemCursor = ""
	p.TargetID = ""
	p.RowName = ""
}

// SyncConvergeKinds lists the kinds a converge runner is registered for
// — the schedule seed's kind list (the store does not know them). Derived
// from the kind registry's converge plans (issue #382): a convergent kind
// declares its plan beside its data, and the engine never names one.
func SyncConvergeKinds() []string { return kinds.ConvergeKindIDs() }

// convergeRunner runs converge jobs for one library kind, iterating that
// kind's declared converge plan.
type convergeRunner struct {
	run  *Runners
	kind string
	plan *kinds.ConvergePlan
}

// JobTypes implements SyncRunner.
func (r *convergeRunner) JobTypes() []string {
	return []string{SyncJobTypeConverge}
}

// Kind implements SyncRunner.
func (r *convergeRunner) Kind() string { return r.kind }

// planErr classifies a converge-plan operation failure: a composition
// failure (a service that cannot serve the plan's op) is terminal — the
// group is misconfigured in a way retrying cannot fix. Provider trouble
// stays retryable and is returned unchanged.
func planErr(err error) error {
	if err == nil || !errors.Is(err, kinds.ErrConvergeUnsupported) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrSyncTerminal, err)
}

// Run executes one bounded converge step: one source batch on one member
// per step, chained through the resolved target list until the group is
// converged.
func (r *convergeRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	if r == nil || r.run == nil || r.plan == nil {
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

	if r.plan.Shape == kinds.ConvergePlaylists {
		return r.runPlaylistStep(ctx, p, target)
	}
	return r.runCollectionStep(ctx, p, target)
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
		if id == hub || !convergeTargetOK(s, r.kind, r.plan, id) {
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
// writes: configured, not blocked, not a read-only local import store,
// write-capable for the kind, and ready per the kind's converge plan
// (issue #382) — able to list back what it holds when the shape needs it.
func convergeTargetOK(s *Runners, kind string, plan *kinds.ConvergePlan, serviceID string) bool {
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
	if !ok || !k.WriteCapable(conn) {
		return false
	}
	return plan.TargetReady == nil || plan.TargetReady(conn)
}

// runCollectionStep executes one bounded step of the collection shapes
// (the create-only saves and the etag entities): one source batch placed
// onto one member. Members are outermost; the source list chains through
// its pages via the cursor. Placement is create-or-update and never
// deletes — a member's own items survive untouched (issue #365).
func (r *convergeRunner) runCollectionStep(ctx context.Context, p convergeJobPayload, target string) (SyncStep, error) {
	s := r.run
	plan := r.plan

	// What the member already holds: the skip filter (issue #366's
	// identity ladder) and the count for the extras check and stamp. The
	// entity shapes place everything and match inside the write.
	state := kinds.MemberState{}
	if plan.LoadMember != nil {
		var err error
		state, err = plan.LoadMember(ctx, target, "")
		if err != nil {
			return r.step(p, target, false, ""), planErr(err)
		}
	}

	page, err := plan.ReadSource(ctx, p.SourceService, "", p.ItemCursor, library.MirrorTrackCap)
	if err != nil {
		// Provider trouble is retryable; the target list and index are
		// already durable in the job payload.
		return r.step(p, target, false, ""), planErr(err)
	}
	if page.Items.Len() > library.MirrorTrackCap {
		page.Items = page.Items.Truncate(library.MirrorTrackCap)
		page.Truncated = true
	}

	// Watermark enrichment (the entity shapes, issue #365 phase 3):
	// attach the member's known ref+etag to each item so placement takes
	// its update path — re-runs update in place instead of creating
	// duplicates, and a member-side edit surfaces as an If-Match conflict
	// instead of being overwritten.
	if plan.AttachRefs != nil {
		page.Items = plan.AttachRefs(page.Items, target, s.memberItemRefs(p.Kind, target))
	}

	kept, held := page.Items, 0
	if plan.FilterHeld != nil {
		kept, held = plan.FilterHeld(state, page.Items)
	}

	// Placement is create-or-update, so a retry of this step is safe —
	// and never deletes. Counters stay unmutated on failure so the retry
	// cannot double-count.
	res, err := plan.Place(ctx, target, "", "", kept)
	if err != nil {
		return r.step(p, target, false, ""), planErr(err)
	}

	// Per-item watermarks on the member, then the collection row at chain
	// end — the rows freshness reads.
	for _, rec := range res.Placed {
		s.Mirror.RecordMirrorItemCopy(p.Kind, rec.Name, target, rec.ExternalID, rec.Name, rec.ETag)
	}
	// If-Match refusals are the watermark signal that a member record
	// changed underfoot: recorded as conflicts, never overwritten.
	s.recordConflicts(p.Kind, target, res.Conflicts)

	p.Planned += page.Items.Len()
	p.Copied += res.Copied
	p.Skipped += held + len(res.Skips)
	p.Conflicts += len(res.Conflicts)
	if page.Truncated {
		p.Truncated = true
	}

	if page.Next != "" {
		p.ItemCursor = page.Next
		return r.step(p, target, false, ""), nil
	}

	// Canonical collection exhausted on this member: stamp the collection
	// row freshness reads — the member's items as they stand now. Only
	// the shapes that track a member count (LoadMember) add one; the
	// entity shapes stamp what this run placed.
	memberCount := res.Copied
	if plan.LoadMember != nil {
		memberCount = state.Count + res.Copied
	}
	s.Mirror.RecordMirrorCopy(p.Kind, p.SourceService, "", target, "", plan.RowName, memberCount, "")

	// Member-only items are non-hub creates: recorded here (the watermark
	// count diverges from the canonical total) for accept — adopt onto
	// the canonical home — or reject — keep local. Convergence itself
	// never removes them. A truncated hub list cannot judge extras, and
	// only the shapes that track a member count raise one — the entity
	// shapes place the whole collection and have no extras channel here.
	if plan.LoadMember != nil {
		if memberAfter, hubTotal := memberCount, page.HubTotal; !page.Truncated && hubTotal > 0 && memberAfter > hubTotal {
			s.recordConflict(SyncConflict{
				Kind:        p.Kind,
				Service:     target,
				EntityKey:   library.MirrorEntityKey(p.Kind, p.SourceService, ""),
				Subject:     plan.RowName,
				Field:       ConflictFieldItems,
				MemberValue: fmt.Sprintf("%d items", memberAfter),
				LastValue:   fmt.Sprintf("%d items", hubTotal),
				Source:      ConflictSourceConverge,
			})
		}
	}

	p.resetPlaylistChain() // clears the page cursor for the next member
	p.TargetDone = p.Index + 1
	p.Index++
	if p.Index >= len(p.Targets) {
		return r.step(p, target, true, ""), nil
	}
	return r.step(p, target, false, ""), nil
}

// memberItemRefs reads the member's per-item watermark rows (the ref+etag
// map the entity plans' AttachRefs consume).
func (s *Runners) memberItemRefs(kind, target string) map[string]kinds.ItemRef {
	out := map[string]kinds.ItemRef{}
	if s == nil || s.Copies == nil || !s.Copies.Enabled() {
		return out
	}
	rows := s.Copies.List(copies.Filter{Kind: kind, Service: target})
	if len(rows) == 0 {
		return out
	}
	byEntity := make(map[string]copies.Record, len(rows))
	for _, row := range rows {
		prev, ok := byEntity[row.EntityKey]
		if !ok || (prev.Watermark == "" && row.Watermark != "") {
			byEntity[row.EntityKey] = row
		}
	}
	for key, row := range byEntity {
		etag := row.Watermark
		if etag == "" {
			etag = row.Version.ETag
		}
		out[key] = kinds.ItemRef{ExternalID: row.ExternalID, ETag: etag}
	}
	return out
}

// recordConflicts records placement If-Match refusals as record conflicts:
// the member record changed underfoot, and it is reported, never
// overwritten (issue #365 phase 3).
func (s *Runners) recordConflicts(kind, target string, conflicts []kinds.PlaceConflict) {
	for _, c := range conflicts {
		s.recordConflict(SyncConflict{
			Kind:        kind,
			Service:     target,
			EntityKey:   kinds.ItemEntityKey(kind, c.Name),
			ExternalID:  c.ID,
			Subject:     c.Name,
			Field:       ConflictFieldRecord,
			MemberValue: "changed on this copy",
			LastValue:   "not overwritten",
			Source:      ConflictSourceConverge,
		})
	}
}

// runPlaylistStep executes one bounded step of the playlist shape: at
// most one canonical item page placed onto one member. Members are
// outermost; within a member the canonical home's playlists run in
// listing order, each chained through its item pages. The member playlist
// is looked up in the copy ledger (what this canonical playlist converged
// to before) so re-runs append and rename instead of creating duplicates
// — which is also why playlist convergence needs the copy ledger enabled.
func (r *convergeRunner) runPlaylistStep(ctx context.Context, p convergeJobPayload, target string) (SyncStep, error) {
	s := r.run
	plan := r.plan
	if s.Copies == nil || !s.Copies.Enabled() {
		return SyncStep{}, fmt.Errorf("%w: playlist convergence needs the copy ledger to remember member playlists", ErrSyncTerminal)
	}

	if p.SourceID == "" {
		if err := r.pickPlaylist(ctx, &p, target); err != nil {
			return SyncStep{}, err
		}
		if p.SourceID == "" {
			// This member's source list is exhausted: chain to the next
			// member, or close the job when they all are.
			return r.step(p, target, p.Index >= len(p.Targets), ""), nil
		}
	}

	// What the member already holds: the skip filter for a re-run, its
	// item count for the copy stamp, and its title for rename-conflict
	// detection. A member with no recorded playlist yet holds nothing.
	var state kinds.MemberState
	if p.TargetID != "" {
		var err error
		state, err = plan.LoadMember(ctx, target, p.TargetID)
		if err != nil {
			// Provider trouble is retryable; the chain state is durable.
			return r.step(p, target, false, ""), planErr(err)
		}
	}

	page, err := plan.ReadSource(ctx, p.SourceService, p.SourceID, p.ItemCursor, library.MirrorTrackCap)
	if err != nil {
		return r.step(p, target, false, ""), planErr(err)
	}
	if p.Name == "" {
		p.Name = page.Title
	}

	// Placement order is source order (order is fidelity); what the
	// member already holds is dropped from placement only — member-only
	// rows and any member-side order survive untouched. The identity
	// ladder is the merge layer's (issue #366): durable id first, then
	// duration-guarded name — a plain name match would duplicate
	// re-recordings.
	kept, alreadyHeld := page.Items, 0
	if plan.FilterHeld != nil {
		kept, alreadyHeld = plan.FilterHeld(state, page.Items)
	}

	// A member title that differs from both what we last set and the
	// canonical title is a member-side rename: reported as a conflict and
	// left alone until the user accepts or rejects it (issue #365 phase 3).
	conflicted := p.TargetID != "" && state.Title != "" && state.Title != p.Name && state.Title != p.RowName
	title := p.Name
	if conflicted || (p.ItemCursor != "" && page.Next != "") {
		// Conflicted: keep the member's title. Middle pages append only —
		// the first page creates/renames and the last one re-asserts the
		// title so a rename propagates even when no new items land.
		title = ""
	}

	copied, skips := 0, 0
	// The final page always places: it carries the title write and closes
	// the playlist even when every item is already on the member.
	if kept.Len() > 0 || p.TargetID == "" || page.Next == "" {
		res, perr := plan.Place(ctx, target, p.TargetID, title, kept)
		if res.EntityID != "" {
			p.TargetID = res.EntityID // the create happened even on partial failure
		}
		if perr != nil {
			// Counters stay unmutated on failure so a retry cannot
			// double-count. Placement is create-or-append, so the retry is
			// safe — and never deletes.
			return r.step(p, target, false, ""), planErr(perr)
		}
		copied, skips = res.Copied, len(res.Skips)
	}

	p.Planned += page.Items.Len()
	p.Copied += copied
	p.Skipped += alreadyHeld + skips
	if conflicted && p.ItemCursor == "" {
		p.Conflicts++ // once per member copy, counted on the first page
		// The rename is recorded for the admin's accept/reject decision
		// (issue #365 phase 3) — reported and left alone until then.
		s.recordConflict(SyncConflict{
			Kind:        p.Kind,
			Service:     target,
			EntityKey:   library.MirrorEntityKey(p.Kind, p.SourceService, p.SourceID),
			ExternalID:  p.TargetID,
			Subject:     p.Name,
			Field:       ConflictFieldTitle,
			MemberValue: state.Title,
			LastValue:   p.Name,
			Source:      ConflictSourceConverge,
		})
	}

	if page.Next != "" {
		p.ItemCursor = page.Next
		return r.step(p, target, false, ""), nil
	}

	// Canonical source exhausted on this member: stamp the copy row the
	// freshness pass reads — the member's item count as it stands now,
	// and the title evidence (a conflicted copy keeps the name we last
	// set).
	stampedName := p.Name
	if conflicted {
		stampedName = p.RowName
	}
	s.Mirror.RecordMirrorCopy(p.Kind, p.SourceService, p.SourceID,
		target, p.TargetID, stampedName, state.Count+copied, "")

	// Member-only items are non-hub creates: recorded here (the watermark
	// count diverges from the canonical total) for accept — adopt onto
	// the canonical home — or reject — keep local. Convergence itself
	// never removes them.
	if memberAfter, hubTotal := state.Count+copied, page.HubTotal; hubTotal > 0 && memberAfter > hubTotal {
		s.recordConflict(SyncConflict{
			Kind:        p.Kind,
			Service:     target,
			EntityKey:   library.MirrorEntityKey(p.Kind, p.SourceService, p.SourceID),
			ExternalID:  p.TargetID,
			Subject:     p.Name,
			Field:       ConflictFieldItems,
			MemberValue: fmt.Sprintf("%d items", memberAfter),
			LastValue:   fmt.Sprintf("%d items", hubTotal),
			Source:      ConflictSourceConverge,
		})
	}

	p.PlaylistsDone++
	p.resetPlaylistChain()
	p.PlaylistIndex++
	return r.step(p, target, false, ""), nil
}

// pickPlaylist advances the playlist chain: picks the next canonical
// playlist to converge onto target and resolves the member playlist it
// maps to via the copy ledger. An empty SourceID on return means this
// member's playlist list is exhausted — the caller chains to the next
// member (or closes the job). A terminal error means the group cannot
// converge.
func (r *convergeRunner) pickPlaylist(ctx context.Context, p *convergeJobPayload, target string) error {
	s := r.run
	srcs, truncated, err := r.plan.ListSources(ctx, p.SourceService)
	if err != nil {
		return planErr(err)
	}
	if truncated {
		p.Truncated = true
	}
	for p.PlaylistIndex < len(srcs) {
		pl := srcs[p.PlaylistIndex]
		p.SourceID = pl.ID
		if p.SourceID == "" {
			// No service-local identity on the canonical home: nothing to
			// converge from.
			p.Skipped++
			p.PlaylistIndex++
			p.SourceID = ""
			continue
		}
		p.Name = pl.Title
		p.ItemCursor = ""
		p.TargetID = ""
		p.RowName = ""
		entity := library.MirrorEntityKey(p.Kind, p.SourceService, p.SourceID)
		for _, row := range s.Copies.GetByEntity(p.Kind, entity) {
			if row.Service == target {
				p.TargetID = row.ExternalID
				p.RowName = row.Version.Name
				break
			}
		}
		return nil
	}
	// This member holds every canonical playlist: chain to the next one.
	p.resetPlaylistChain()
	p.PlaylistIndex = 0
	p.TargetDone = p.Index + 1
	p.Index++
	return nil
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
			"playlist":       p.Name,
			"playlists_done": p.PlaylistsDone,
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

// newConvergeRunners builds one converge runner per kind that declares a
// converge plan (issue #382) — the registry is the source of truth, so a
// convergent kind needs no engine edit.
func newConvergeRunners(run *Runners) []SyncRunner {
	if run == nil || run.Mirror == nil {
		return nil
	}
	var out []SyncRunner
	for _, k := range run.Mirror.KindRegistry() {
		plan := k.Converge()
		if plan == nil {
			continue
		}
		out = append(out, &convergeRunner{run: run, kind: k.Def().ID, plan: plan})
	}
	return out
}

// registerConvergeRunners adds the converge runners to a registry.
func registerConvergeRunners(reg *RunnerRegistry, run *Runners) {
	for _, r := range newConvergeRunners(run) {
		reg.Register(r)
	}
}
