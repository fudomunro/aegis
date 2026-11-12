package syncjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

// Converge job runners (issue #365): the scheduled job that moves a kind's
// data from its canonical home out to the group's other write-capable
// members. One runner serves one kind. Entities chain one member per step
// (the mirror runner's chain model, member list instead of source cursor);
// playlists chain one item page per step — members outermost, then the
// canonical home's playlists, then each playlist's pages.
//
// Convergence never deletes on a member. The write surface is Place* only —
// create-or-update with identity matching — so items that exist only on a
// member survive untouched. Placement order is source order (order is
// fidelity); an already-converged playlist gains only what is missing.
// Conflicts (a member record changed underfoot, If-Match refusal or a
// member-side rename) are counted and reported, never overwritten and never
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

	// Playlist chain state (kind=playlists): which canonical playlist is in
	// flight, how far through its items, and the member playlist it maps to.
	// The member playlist id comes from the copy ledger (what this hub
	// playlist converged to before) or from the create that placed it.
	PlaylistIndex int    `json:"playlist_index,omitempty"`
	SourceID      string `json:"source_id,omitempty"`
	Name          string `json:"name,omitempty"`
	ItemCursor    string `json:"item_cursor,omitempty"`
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

// resetPlaylistChain clears the in-flight playlist so the next step picks
// the following one (or moves to the next member).
func (p *convergeJobPayload) resetPlaylistChain() {
	p.SourceID = ""
	p.Name = ""
	p.ItemCursor = ""
	p.TargetID = ""
	p.RowName = ""
}

// convergeKinds are the kinds a converge runner is registered for. Entities
// (contacts, events) place their whole collection per member; playlists and
// liked tracks fan out one item page at a time (issue #365). Playlists also
// propagate titles.
var convergeKinds = []string{library.MirrorKindContacts, library.MirrorKindEvents, library.MirrorKindPlaylists, library.MirrorKindTracks}

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

	if r.kind == library.MirrorKindPlaylists {
		return r.runPlaylistStep(ctx, p, target)
	}
	if r.kind == library.MirrorKindTracks {
		return r.runTracksStep(ctx, p, target)
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

	// Watermark enrichment (issue #365 phase 3): attach the member's known
	// ref+etag to each item so placement takes its update path — re-runs
	// update in place instead of creating duplicates, and a member-side
	// edit surfaces as an If-Match conflict instead of being overwritten.
	s.attachMemberRefs(&items, p.Kind, target)

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

	// If-Match refusals are the watermark signal that a member record
	// changed underfoot: recorded as conflicts, never overwritten.
	s.recordConflicts(p.Kind, target, res.Conflicts)

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
	if !k.WriteCapable(conn) {
		return false
	}
	if kind == library.MirrorKindPlaylists {
		// Playlist convergence re-reads the member's items so a re-run
		// appends only what is missing. A member that cannot list its items
		// cannot converge idempotently — it is not a converge target.
		if _, ok := conn.(kinds.PlaylistItemsProvider); !ok {
			return false
		}
	}
	if kind == library.MirrorKindTracks {
		// Liked-tracks convergence reads the member's likes for the same
		// reason (issue #366's identity ladder does the matching).
		if _, ok := conn.(kinds.LikedTrackPageProvider); !ok {
			return false
		}
	}
	return true
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

// attachMemberRefs rewrites each item's sources to carry the member's known
// ref+etag from the per-item watermark rows (issue #365 phase 3). Placement
// then takes its update path — If-Match against the last-seen etag — so
// re-runs update in place instead of creating duplicates, and a member-side
// edit surfaces as a conflict instead of being overwritten. Items with no
// recorded copy on the member are left untouched (first placement creates
// them and stamps the row).
func (s *Runners) attachMemberRefs(items *entitySet, kind, target string) {
	if s == nil || s.Copies == nil || !s.Copies.Enabled() {
		return
	}
	rows := s.Copies.List(copies.Filter{Kind: kind, Service: target})
	if len(rows) == 0 {
		return
	}
	byEntity := make(map[string]copies.Record, len(rows))
	for _, row := range rows {
		prev, ok := byEntity[row.EntityKey]
		if !ok || (prev.Watermark == "" && row.Watermark != "") {
			byEntity[row.EntityKey] = row
		}
	}
	etagOf := func(row copies.Record) string {
		if row.Watermark != "" {
			return row.Watermark
		}
		return row.Version.ETag
	}
	for i := range items.contacts {
		row, ok := byEntity[kind+":"+strings.TrimSpace(contactDisplayName(items.contacts[i]))]
		if !ok {
			continue
		}
		items.contacts[i].Sources = upsertSourceRef(items.contacts[i].Sources, target, row.ExternalID, etagOf(row))
	}
	for i := range items.events {
		row, ok := byEntity[kind+":"+strings.TrimSpace(items.events[i].Title)]
		if !ok {
			continue
		}
		items.events[i].Sources = upsertSourceEventRef(items.events[i].Sources, target, row.ExternalID, etagOf(row))
	}
}

// contactDisplayName is the placement-side display name convention
// (mirrors the connectors' placeContactDisplay): name, else first email.
func contactDisplayName(c kinds.Contact) string {
	if c.Name != "" {
		return c.Name
	}
	if len(c.Emails) > 0 {
		return c.Emails[0]
	}
	return ""
}

// upsertSourceRef patches a contact's source for service with id+etag,
// replacing any previous entry for that service.
func upsertSourceRef(srcs []kinds.ContactSource, service, id, etag string) []kinds.ContactSource {
	out := make([]kinds.ContactSource, 0, len(srcs)+1)
	replaced := false
	for _, src := range srcs {
		if src.Service == service {
			if !replaced {
				src.ID = id
				src.Extra = withETag(src.Extra, etag)
				out = append(out, src)
				replaced = true
			}
			continue
		}
		out = append(out, src)
	}
	if !replaced {
		out = append(out, kinds.ContactSource{Service: service, ID: id, Extra: withETag(nil, etag)})
	}
	return out
}

// upsertSourceEventRef is upsertSourceRef for event sources.
func upsertSourceEventRef(srcs []kinds.CalendarEventSource, service, id, etag string) []kinds.CalendarEventSource {
	out := make([]kinds.CalendarEventSource, 0, len(srcs)+1)
	replaced := false
	for _, src := range srcs {
		if src.Service == service {
			if !replaced {
				src.ID = id
				src.Extra = withETag(src.Extra, etag)
				out = append(out, src)
				replaced = true
			}
			continue
		}
		out = append(out, src)
	}
	if !replaced {
		out = append(out, kinds.CalendarEventSource{Service: service, ID: id, Extra: withETag(nil, etag)})
	}
	return out
}

func withETag(extra map[string]any, etag string) map[string]any {
	if etag == "" {
		return extra
	}
	if extra == nil {
		return map[string]any{"etag": etag}
	}
	extra["etag"] = etag
	return extra
}

// recordConflicts records placement If-Match refusals as record conflicts:
// the member record changed underfoot, and it is reported, never
// overwritten (issue #365 phase 3).
func (s *Runners) recordConflicts(kind, target string, conflicts []kinds.PlaceConflict) {
	for _, c := range conflicts {
		s.recordConflict(SyncConflict{
			Kind:        kind,
			Service:     target,
			EntityKey:   kind + ":" + strings.TrimSpace(c.Name),
			ExternalID:  c.ID,
			Subject:     c.Name,
			Field:       ConflictFieldRecord,
			MemberValue: "changed on this copy",
			LastValue:   "not overwritten",
			Source:      ConflictSourceConverge,
		})
	}
}

// runPlaylistStep executes one bounded playlist step: at most one canonical
// item page placed onto one member. Members are outermost; within a member
// the canonical home's playlists run in listing order, each chained through
// its item pages. The member playlist is looked up in the copy ledger (what
// this canonical playlist converged to before) so re-runs append and rename
// instead of creating duplicates — which is also why playlist convergence
// needs the copy ledger enabled.
func (r *convergeRunner) runPlaylistStep(ctx context.Context, p convergeJobPayload, target string) (SyncStep, error) {
	s := r.run
	if s.Copies == nil || !s.Copies.Enabled() {
		return SyncStep{}, fmt.Errorf("%w: playlist convergence needs the copy ledger to remember member playlists", ErrSyncTerminal)
	}

	if p.SourceID == "" {
		if err := r.pickPlaylist(ctx, &p, target); err != nil {
			return SyncStep{}, err
		}
		if p.SourceID == "" {
			// This member's playlist list is exhausted: chain to the next
			// member, or close the job when they all are.
			return r.step(p, target, p.Index >= len(p.Targets), ""), nil
		}
	}

	// What the member already holds: the skip filter for a re-run, its item
	// count for the copy stamp, and its title for rename-conflict detection.
	state := memberPlaylistState{held: kinds.NewTrackMatcher(nil)}
	if p.TargetID != "" {
		var err error
		state, err = loadMemberPlaylistState(ctx, s, target, p.TargetID)
		if err != nil {
			// Provider trouble is retryable; the chain state is durable.
			return r.step(p, target, false, ""), err
		}
	}

	src, err := library.MirrorOpFor[kinds.PlaylistItemsProvider](s.Mirror, p.SourceService, "converge source")
	if err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	ref, page, err := src.ListPlaylistItems(ctx, p.SourceID, p.ItemCursor, library.MirrorTrackCap)
	if err != nil {
		return r.step(p, target, false, ""), err
	}
	if p.Name == "" {
		p.Name = ref.Name
	}

	// Placement order is source order (order is fidelity); what the member
	// already holds is dropped from placement only — member-only rows and
	// any member-side order survive untouched. The identity ladder is the
	// merge layer's (issue #366): durable id first, then duration-guarded
	// name — a plain name match would duplicate re-recordings.
	alreadyHeld := 0
	newTracks := make([]kinds.Track, 0, len(page.Tracks))
	for _, t := range page.Tracks {
		if state.held.Has(t) {
			alreadyHeld++
			continue
		}
		newTracks = append(newTracks, t)
	}

	// A member title that differs from both what we last set and the
	// canonical title is a member-side rename: reported as a conflict and
	// left alone until the user accepts or rejects it (issue #365 phase 3).
	conflicted := p.TargetID != "" && state.title != "" && state.title != p.Name && state.title != p.RowName
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
	if len(newTracks) > 0 || p.TargetID == "" || page.Next == "" {
		dst, err := library.MirrorOpFor[kinds.PlaylistItemWriteProvider](s.Mirror, target, "converge target")
		if err != nil {
			return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		res, perr := dst.PlacePlaylistItems(ctx, p.TargetID, title, newTracks)
		if res.PlaylistID != "" {
			p.TargetID = res.PlaylistID // the create happened even on partial failure
		}
		if perr != nil {
			// Counters stay unmutated on failure so a retry cannot
			// double-count. Placement is create-or-append, so the retry is
			// safe — and never deletes.
			return r.step(p, target, false, ""), perr
		}
		copied, skips = res.Copied, len(res.Skips)
	}

	p.Planned += len(page.Tracks)
	p.Copied += copied
	p.Skipped += alreadyHeld + skips
	if conflicted && p.ItemCursor == "" {
		p.Conflicts++ // once per member copy, counted on the first page
		// The rename is recorded for the admin's accept/reject decision
		// (issue #365 phase 3) — reported and left alone until then.
		s.recordConflict(SyncConflict{
			Kind:        library.MirrorKindPlaylists,
			Service:     target,
			EntityKey:   library.MirrorEntityKey(library.MirrorKindPlaylists, p.SourceService, p.SourceID),
			ExternalID:  p.TargetID,
			Subject:     p.Name,
			Field:       ConflictFieldTitle,
			MemberValue: state.title,
			LastValue:   p.Name,
			Source:      ConflictSourceConverge,
		})
	}

	if page.Next != "" {
		p.ItemCursor = page.Next
		return r.step(p, target, false, ""), nil
	}

	// Canonical playlist exhausted on this member: stamp the copy row the
	// freshness pass reads — the member's item count as it stands now, and
	// the title evidence (a conflicted copy keeps the name we last set).
	stampedName := p.Name
	if conflicted {
		stampedName = p.RowName
	}
	s.Mirror.RecordMirrorCopy(library.MirrorKindPlaylists, p.SourceService, p.SourceID,
		target, p.TargetID, stampedName, state.count+copied, "")

	// Member-only items are non-hub creates: recorded here (the watermark
	// count diverges from the canonical total) for accept — adopt onto the
	// canonical home — or reject — keep local. Convergence itself never
	// removes them.
	if memberAfter, hubTotal := state.count+copied, ref.TrackCount; hubTotal > 0 && memberAfter > hubTotal {
		s.recordConflict(SyncConflict{
			Kind:        library.MirrorKindPlaylists,
			Service:     target,
			EntityKey:   library.MirrorEntityKey(library.MirrorKindPlaylists, p.SourceService, p.SourceID),
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
// playlist to converge onto target and resolves the member playlist it maps
// to via the copy ledger. An empty SourceID on return means this member's
// playlist list is exhausted — the caller chains to the next member (or
// closes the job). A terminal error means the group cannot converge.
func (r *convergeRunner) pickPlaylist(ctx context.Context, p *convergeJobPayload, target string) error {
	s := r.run
	src, err := library.MirrorOpFor[kinds.PlaylistProvider](s.Mirror, p.SourceService, "converge source")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	all, truncated, err := src.ListUserPlaylists(ctx)
	if err != nil {
		return err
	}
	if truncated {
		p.Truncated = true
	}
	for p.PlaylistIndex < len(all) {
		pl := all[p.PlaylistIndex]
		p.SourceID = playlistSourceID(pl, p.SourceService)
		if p.SourceID == "" {
			// No service-local identity on the canonical home: nothing to
			// converge from.
			p.Skipped++
			p.PlaylistIndex++
			p.SourceID = ""
			continue
		}
		p.Name = pl.Name
		p.ItemCursor = ""
		p.TargetID = ""
		p.RowName = ""
		entity := library.MirrorEntityKey(library.MirrorKindPlaylists, p.SourceService, p.SourceID)
		for _, row := range s.Copies.GetByEntity(library.MirrorKindPlaylists, entity) {
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

// memberPlaylistState is what a member's playlist holds now: the identity
// matcher for the skip filter (issue #366), the raw item count for the copy
// stamp, and the title for rename-conflict detection.
type memberPlaylistState struct {
	held  *kinds.TrackMatcher
	count int
	title string
}

// loadMemberPlaylistState pages a member playlist's items.
func loadMemberPlaylistState(ctx context.Context, s *Runners, service, playlistID string) (memberPlaylistState, error) {
	src, err := library.MirrorOpFor[kinds.PlaylistItemsProvider](s.Mirror, service, "converge target")
	if err != nil {
		return memberPlaylistState{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	st := memberPlaylistState{held: kinds.NewTrackMatcher(nil)}
	cursor := ""
	for i := 0; i < shared.LibraryPageCap; i++ {
		ref, page, err := src.ListPlaylistItems(ctx, playlistID, cursor, 100)
		if err != nil {
			return memberPlaylistState{}, err
		}
		if i == 0 {
			st.title = ref.Name
		}
		for _, t := range page.Tracks {
			st.held.Add(t)
			st.count++
		}
		if page.Next == "" {
			return st, nil
		}
		cursor = page.Next
	}
	return st, nil
}

// runTracksStep executes one bounded liked-tracks step: one canonical item
// page saved onto one member. Members are outermost; pages chain through
// the canonical home's liked list. Saving is create-or-update and never
// un-likes — a member's own likes survive untouched (issue #365).
func (r *convergeRunner) runTracksStep(ctx context.Context, p convergeJobPayload, target string) (SyncStep, error) {
	s := r.run

	// What the member already likes: the skip filter (issue #366's
	// identity ladder) and the count for the extras check and stamp.
	state, err := loadMemberTrackState(ctx, s, target)
	if err != nil {
		return r.step(p, target, false, ""), err
	}

	src, err := library.MirrorOpFor[kinds.LikedTrackPageProvider](s.Mirror, p.SourceService, "converge source")
	if err != nil {
		return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	page, err := src.ListUserLikedTracksPage(ctx, p.ItemCursor, library.MirrorTrackCap)
	if err != nil {
		return r.step(p, target, false, ""), err
	}

	alreadyHeld := 0
	newTracks := make([]kinds.Track, 0, len(page.Tracks))
	for _, t := range page.Tracks {
		if state.held.Has(t) {
			alreadyHeld++
			continue
		}
		newTracks = append(newTracks, t)
	}

	copied, skips := 0, 0
	if len(newTracks) > 0 {
		dst, err := library.MirrorOpFor[kinds.LikedTrackWriteProvider](s.Mirror, target, "converge target")
		if err != nil {
			return SyncStep{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		res, perr := dst.SaveLikedTracks(ctx, newTracks)
		if perr != nil {
			// Counters stay unmutated on failure so a retry cannot
			// double-count. Saving is idempotent, so the retry is safe.
			return r.step(p, target, false, ""), perr
		}
		copied, skips = res.Copied, len(res.Skips)
	}

	p.Planned += len(page.Tracks)
	p.Copied += copied
	p.Skipped += alreadyHeld + skips

	if page.Next != "" {
		p.ItemCursor = page.Next
		return r.step(p, target, false, ""), nil
	}

	// Canonical liked list exhausted on this member: stamp the collection
	// row freshness reads — the member's likes as they stand now.
	s.Mirror.RecordMirrorCopy(library.MirrorKindTracks, p.SourceService, "",
		target, "", "liked/saved tracks", state.count+copied, "")

	// Member-only likes are non-hub creates: recorded for accept — adopt
	// onto the canonical home — or reject — keep local. Convergence itself
	// never un-likes.
	if memberAfter, hubTotal := state.count+copied, page.Total; hubTotal > 0 && memberAfter > hubTotal {
		s.recordConflict(SyncConflict{
			Kind:        library.MirrorKindTracks,
			Service:     target,
			EntityKey:   library.MirrorEntityKey(library.MirrorKindTracks, p.SourceService, ""),
			Subject:     "liked/saved tracks",
			Field:       ConflictFieldItems,
			MemberValue: fmt.Sprintf("%d items", memberAfter),
			LastValue:   fmt.Sprintf("%d items", hubTotal),
			Source:      ConflictSourceConverge,
		})
	}

	p.resetPlaylistChain() // clears the page cursor for the next member
	p.TargetDone = p.Index + 1
	p.Index++
	if p.Index >= len(p.Targets) {
		return r.step(p, target, true, ""), nil
	}
	return r.step(p, target, false, ""), nil
}

// memberTrackState is what a member's liked list holds now: the identity
// matcher for the skip filter and the raw count for the extras check.
type memberTrackState struct {
	held  *kinds.TrackMatcher
	count int
}

// loadMemberTrackState pages a member's liked/saved list.
func loadMemberTrackState(ctx context.Context, s *Runners, service string) (memberTrackState, error) {
	src, err := library.MirrorOpFor[kinds.LikedTrackPageProvider](s.Mirror, service, "converge target")
	if err != nil {
		return memberTrackState{}, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	st := memberTrackState{held: kinds.NewTrackMatcher(nil)}
	cursor := ""
	for i := 0; i < shared.LibraryPageCap; i++ {
		page, err := src.ListUserLikedTracksPage(ctx, cursor, 100)
		if err != nil {
			return memberTrackState{}, err
		}
		for _, t := range page.Tracks {
			st.held.Add(t)
			st.count++
		}
		if page.Next == "" {
			return st, nil
		}
		cursor = page.Next
	}
	return st, nil
}

// playlistSourceID is a playlist's service-local id on the canonical home.
func playlistSourceID(pl kinds.Playlist, hub string) string {
	for _, src := range pl.Sources {
		if src.Service == hub {
			return src.ID
		}
	}
	if len(pl.Sources) > 0 {
		return pl.Sources[0].ID
	}
	return ""
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
