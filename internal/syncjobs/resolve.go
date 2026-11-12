package syncjobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

// Conflict resolution (issue #365 phase 3): the admin's accept/reject
// decision on a detected non-hub edit. Accept applies the member's edit to
// the canonical home and fans out to the other members. Reject re-converges
// that member from the canonical home. Neither deletes on a member:
// member-only items survive a reject (they are simply not adopted), and
// adopting a member's deletions is delete propagation, a later phase.

// ResolveConflict applies one decision to an open conflict. accept=true
// makes the member's edit canonical; accept=false restores the canonical
// version on the member. The conflict row is kept as history — a later
// divergence at the same locus reopens it only when the member side
// changed again.
func (r *Runners) ResolveConflict(ctx context.Context, id string, accept bool) (SyncConflict, error) {
	if r == nil || r.Conflicts == nil || !r.Conflicts.Enabled() {
		return SyncConflict{}, fmt.Errorf("conflict store is disabled")
	}
	rec, ok := r.Conflicts.Get(strings.TrimSpace(id))
	if !ok {
		return SyncConflict{}, fmt.Errorf("conflict %q not found", id)
	}
	if rec.Status != ConflictStatusOpen {
		return SyncConflict{}, fmt.Errorf("conflict %q is already %s", id, rec.Status)
	}

	note, err := r.applyConflict(ctx, rec, accept)
	if err != nil {
		return SyncConflict{}, err
	}
	status := ConflictStatusRejected
	if accept {
		status = ConflictStatusAccepted
	}
	return r.Conflicts.Resolve(rec.ID, status, note)
}

// applyConflict performs the writes behind one decision and returns the
// note to keep on the resolved row.
func (r *Runners) applyConflict(ctx context.Context, rec SyncConflict, accept bool) (string, error) {
	row, ok := r.conflictCopyRow(rec)
	switch {
	case rec.Field == ConflictFieldMissing:
		return r.resolveMissing(rec, row, ok, accept)
	case rec.Field == ConflictFieldTitle:
		return r.resolveTitle(ctx, rec, row, ok, accept)
	case rec.Field == ConflictFieldItems:
		return r.resolveItems(ctx, rec, row, ok, accept)
	case rec.Field == ConflictFieldRecord:
		return r.resolveRecord(ctx, rec, row, ok, accept)
	default:
		return "", fmt.Errorf("%w: unknown conflict field %q", ErrSyncTerminal, rec.Field)
	}
}

// conflictCopyRow finds the copy row the conflict is about — the pair of
// service-local ids resolve needs.
func (r *Runners) conflictCopyRow(rec SyncConflict) (copies.Record, bool) {
	if r.Copies == nil || !r.Copies.Enabled() {
		return copies.Record{}, false
	}
	for _, row := range r.Copies.GetByEntity(rec.Kind, rec.EntityKey) {
		if row.Service == rec.Service {
			return row, true
		}
	}
	return copies.Record{}, false
}

// resolveTitle renames: accept moves the canonical playlist to the
// member's title, reject restores the canonical title on the member
// (issue #365 phase 3).
func (r *Runners) resolveTitle(ctx context.Context, rec SyncConflict, row copies.Record, haveRow bool, accept bool) (string, error) {
	if !haveRow || row.SourceID == "" || row.ExternalID == "" {
		return "", fmt.Errorf("%w: the copy row for this playlist is gone", ErrSyncTerminal)
	}
	memberTitle, err := r.playlistTitle(ctx, rec.Service, row.ExternalID)
	if err != nil {
		return "", err
	}
	hubTitle, err := r.playlistTitle(ctx, row.SourceService, row.SourceID)
	if err != nil {
		return "", err
	}
	if accept {
		if err := r.renamePlaylist(ctx, row.SourceService, row.SourceID, memberTitle); err != nil {
			return "", err
		}
		r.restampPlaylist(row, memberTitle)
		if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("canonical title set to %q; fanning out", memberTitle), nil
	}
	if err := r.renamePlaylist(ctx, rec.Service, row.ExternalID, hubTitle); err != nil {
		return "", err
	}
	r.restampPlaylist(row, hubTitle)
	return fmt.Sprintf("member title restored to %q", hubTitle), nil
}

// resolveItems adopts (accept) or keeps local (reject) the items a member
// holds beyond the canonical home. Rejections never remove anything from
// the member — convergence is add-only by design.
func (r *Runners) resolveItems(ctx context.Context, rec SyncConflict, row copies.Record, haveRow bool, accept bool) (string, error) {
	if !haveRow {
		return "", fmt.Errorf("%w: the copy row for this conflict is gone", ErrSyncTerminal)
	}
	extras, err := r.memberExtraItems(ctx, rec, row)
	if err != nil {
		return "", err
	}
	if !accept {
		if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
			return "", err
		}
		if len(extras) == 0 {
			return "member re-converged from the canonical home", nil
		}
		return fmt.Sprintf("%d member-only items kept local; the member was re-converged", len(extras)), nil
	}
	if len(extras) == 0 {
		return "nothing left to adopt", nil
	}
	if err := r.adoptExtras(ctx, rec, row, extras); err != nil {
		return "", err
	}
	if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d member-only items adopted onto the canonical home; fanning out", len(extras)), nil
}

// resolveRecord settles a member-side entity edit: accept writes the
// member's version to the canonical home, reject writes the canonical
// version back onto the member (If-Match against the member's live etag,
// so the overwrite is the user's decision, not a silent one).
func (r *Runners) resolveRecord(ctx context.Context, rec SyncConflict, row copies.Record, haveRow bool, accept bool) (string, error) {
	if !haveRow {
		return "", fmt.Errorf("%w: the copy row for this conflict is gone", ErrSyncTerminal)
	}
	hub := r.conflictHub(rec, row)
	switch rec.Kind {
	case library.MirrorKindContacts:
		memberList, _, err := r.listMemberContacts(ctx, rec.Service)
		if err != nil {
			return "", err
		}
		memberCT, found := findContactByRef(memberList, rec.Service, rec.ExternalID)
		if !found {
			return "the member record is gone; nothing to decide", nil
		}
		hubList, _, err := r.listMemberContacts(ctx, hub)
		if err != nil {
			return "", err
		}
		hubCT := findContactByKey(hubList, kinds.PrimaryContactKey(memberCT))
		if contactDisplayName(hubCT) == "" {
			// The edit may have changed the identity key itself — the
			// watermark rows key on the display name, so fall back to it.
			hubCT = findContactByName(hubList, rec.Subject)
		}
		if accept {
			dst, err := library.MirrorOpFor[kinds.ContactWriteProvider](r.Mirror, hub, "conflict target")
			if err != nil {
				return "", fmt.Errorf("%w: %v", ErrSyncTerminal, err)
			}
			merged := memberCT
			merged.Sources = upsertSourceRef(memberCT.Sources, hub, contactRefID(hubCT, hub), contactRefETag(hubCT, hub))
			if _, err := dst.PlaceContacts(ctx, []kinds.Contact{merged}); err != nil {
				return "", err
			}
			// The member itself is unchanged: its watermark is refreshed to
			// the live etag so the next converge updates instead of
			// conflicting again.
			r.Mirror.RecordMirrorItemCopy(rec.Kind, rec.Subject, rec.Service,
				rec.ExternalID, rec.Subject, contactRefETag(memberCT, rec.Service))
			if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
				return "", err
			}
			return "member version written to the canonical home; fanning out", nil
		}
		dst, err := library.MirrorOpFor[kinds.ContactWriteProvider](r.Mirror, rec.Service, "conflict target")
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		merged := hubCT
		merged.Sources = upsertSourceRef(hubCT.Sources, rec.Service, rec.ExternalID, contactRefETag(memberCT, rec.Service))
		res, err := dst.PlaceContacts(ctx, []kinds.Contact{merged})
		if err != nil {
			return "", err
		}
		r.stampPlaced(rec.Kind, rec.Service, res.Placed)
		return "canonical version restored on the member", nil
	case library.MirrorKindEvents:
		memberList, _, err := r.listMemberEvents(ctx, rec.Service)
		if err != nil {
			return "", err
		}
		memberEV, found := findEventByRef(memberList, rec.Service, rec.ExternalID)
		if !found {
			return "the member record is gone; nothing to decide", nil
		}
		hubList, _, err := r.listMemberEvents(ctx, hub)
		if err != nil {
			return "", err
		}
		hubEV := findEventByKey(hubList, kinds.CalendarEventIdentityKey(memberEV))
		if hubEV.Title == "" {
			hubEV = findEventByName(hubList, rec.Subject)
		}
		if accept {
			dst, err := library.MirrorOpFor[kinds.CalendarEventWriteProvider](r.Mirror, hub, "conflict target")
			if err != nil {
				return "", fmt.Errorf("%w: %v", ErrSyncTerminal, err)
			}
			merged := memberEV
			merged.Sources = upsertSourceEventRef(memberEV.Sources, hub, eventRefID(hubEV, hub), eventRefETag(hubEV, hub))
			if _, err := dst.PlaceEvents(ctx, []kinds.CalendarEvent{merged}); err != nil {
				return "", err
			}
			r.Mirror.RecordMirrorItemCopy(rec.Kind, rec.Subject, rec.Service,
				rec.ExternalID, rec.Subject, eventRefETag(memberEV, rec.Service))
			if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
				return "", err
			}
			return "member version written to the canonical home; fanning out", nil
		}
		dst, err := library.MirrorOpFor[kinds.CalendarEventWriteProvider](r.Mirror, rec.Service, "conflict target")
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		merged := hubEV
		merged.Sources = upsertSourceEventRef(hubEV.Sources, rec.Service, rec.ExternalID, eventRefETag(memberEV, rec.Service))
		res, err := dst.PlaceEvents(ctx, []kinds.CalendarEvent{merged})
		if err != nil {
			return "", err
		}
		r.stampPlaced(rec.Kind, rec.Service, res.Placed)
		return "canonical version restored on the member", nil
	default:
		return "", fmt.Errorf("%w: kind %q has no record resolution", ErrSyncTerminal, rec.Kind)
	}
}

// resolveMissing settles a vanished member copy. Reject re-creates it from
// the canonical home (add-only — safe now). Accept would propagate the
// member's deletion to the canonical home: delete propagation is a later
// phase, and it is refused here rather than half-applied.
func (r *Runners) resolveMissing(rec SyncConflict, row copies.Record, haveRow bool, accept bool) (string, error) {
	if accept {
		return "", fmt.Errorf("%w: adopting a member's deletion lands with delete propagation in a later phase", ErrSyncTerminal)
	}
	if haveRow {
		r.Copies.Delete(rec.Kind, row.EntityKey, row.Service, row.ExternalID)
	}
	if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
		return "", err
	}
	return "copy re-created from the canonical home", nil
}

// memberExtraItems lists the member's items that the canonical home does
// not hold — the non-hub creates an accept adopts.
func (r *Runners) memberExtraItems(ctx context.Context, rec SyncConflict, row copies.Record) ([]entityItem, error) {
	hub := r.conflictHub(rec, row)
	switch rec.Kind {
	case library.MirrorKindPlaylists:
		memberTracks, err := r.listPlaylistTracks(ctx, rec.Service, row.ExternalID)
		if err != nil {
			return nil, err
		}
		hubTracks, err := r.listPlaylistTracks(ctx, hub, row.SourceID)
		if err != nil {
			return nil, err
		}
		hubKeys := make(map[string]bool, len(hubTracks))
		for _, t := range hubTracks {
			hubKeys[repairIdentityKey(t)] = true
		}
		var out []entityItem
		for _, t := range memberTracks {
			if !hubKeys[repairIdentityKey(t)] {
				t := t
				out = append(out, entityItem{track: &t})
			}
		}
		return out, nil
	case library.MirrorKindContacts:
		memberList, _, err := r.listMemberContacts(ctx, rec.Service)
		if err != nil {
			return nil, err
		}
		hubList, _, err := r.listMemberContacts(ctx, hub)
		if err != nil {
			return nil, err
		}
		hubKeys := make(map[string]bool, len(hubList))
		for _, c := range hubList {
			hubKeys[kinds.PrimaryContactKey(c)] = true
		}
		var out []entityItem
		for _, c := range memberList {
			if !hubKeys[kinds.PrimaryContactKey(c)] {
				c := c
				out = append(out, entityItem{contact: &c})
			}
		}
		return out, nil
	case library.MirrorKindEvents:
		memberList, _, err := r.listMemberEvents(ctx, rec.Service)
		if err != nil {
			return nil, err
		}
		hubList, _, err := r.listMemberEvents(ctx, hub)
		if err != nil {
			return nil, err
		}
		hubKeys := make(map[string]bool, len(hubList))
		for _, e := range hubList {
			hubKeys[kinds.CalendarEventIdentityKey(e)] = true
		}
		var out []entityItem
		for _, e := range memberList {
			if !hubKeys[kinds.CalendarEventIdentityKey(e)] {
				e := e
				out = append(out, entityItem{event: &e})
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: kind %q has no item adoption", ErrSyncTerminal, rec.Kind)
	}
}

// adoptExtras appends the member's extra items to the canonical home. The
// title write is deliberately empty — adoption never renames the hub list.
func (r *Runners) adoptExtras(ctx context.Context, rec SyncConflict, row copies.Record, extras []entityItem) error {
	hub := r.conflictHub(rec, row)
	switch rec.Kind {
	case library.MirrorKindPlaylists:
		dst, err := library.MirrorOpFor[kinds.PlaylistItemWriteProvider](r.Mirror, hub, "conflict target")
		if err != nil {
			return fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		tracks := make([]kinds.Track, 0, len(extras))
		for _, it := range extras {
			if it.track != nil {
				tracks = append(tracks, *it.track)
			}
		}
		_, err = dst.PlacePlaylistItems(ctx, row.SourceID, "", tracks)
		return err
	case library.MirrorKindContacts:
		dst, err := library.MirrorOpFor[kinds.ContactWriteProvider](r.Mirror, hub, "conflict target")
		if err != nil {
			return fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		contacts := make([]kinds.Contact, 0, len(extras))
		for _, it := range extras {
			if it.contact != nil {
				contacts = append(contacts, *it.contact)
			}
		}
		_, err = dst.PlaceContacts(ctx, contacts)
		return err
	case library.MirrorKindEvents:
		dst, err := library.MirrorOpFor[kinds.CalendarEventWriteProvider](r.Mirror, hub, "conflict target")
		if err != nil {
			return fmt.Errorf("%w: %v", ErrSyncTerminal, err)
		}
		events := make([]kinds.CalendarEvent, 0, len(extras))
		for _, it := range extras {
			if it.event != nil {
				events = append(events, *it.event)
			}
		}
		_, err = dst.PlaceEvents(ctx, events)
		return err
	default:
		return fmt.Errorf("%w: kind %q has no item adoption", ErrSyncTerminal, rec.Kind)
	}
}

// entityItem carries one item of any converge kind (the resolve engine's
// neutral handle over the per-kind shapes).
type entityItem struct {
	track   *kinds.Track
	contact *kinds.Contact
	event   *kinds.CalendarEvent
}

// enqueueConverge schedules a converge run so an accept fans out and a
// reject re-converges the member (issue #365 phase 3).
func (r *Runners) enqueueConverge(kind, conflictID string) error {
	if r.Jobs == nil || !r.Jobs.Enabled() {
		return nil // fail-open: the scheduled run still fans out
	}
	_, err := r.Jobs.Enqueue(NewSyncEnqueue(kind, SyncJobTypeConverge, "", "conflict:"+conflictID, nil))
	return err
}

// playlistTitle reads a playlist's current title.
func (r *Runners) playlistTitle(ctx context.Context, service, playlistID string) (string, error) {
	src, err := library.MirrorOpFor[kinds.PlaylistItemsProvider](r.Mirror, service, "conflict source")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	ref, _, err := src.ListPlaylistItems(ctx, playlistID, "", 1)
	if err != nil {
		return "", err
	}
	return ref.Name, nil
}

// renamePlaylist sets a playlist's title (the append-path title write).
func (r *Runners) renamePlaylist(ctx context.Context, service, playlistID, name string) error {
	dst, err := library.MirrorOpFor[kinds.PlaylistItemWriteProvider](r.Mirror, service, "conflict target")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	_, err = dst.PlacePlaylistItems(ctx, playlistID, name, nil)
	return err
}

// restampPlaylist refreshes the copy row after a rename so the watermark
// records what is now on the member (and the next check stays quiet).
func (r *Runners) restampPlaylist(row copies.Record, name string) {
	r.Mirror.RecordMirrorCopy(row.Kind, row.SourceService, row.SourceID,
		row.Service, row.ExternalID, name, row.Version.ItemCount, row.Version.SnapshotID)
}

// stampPlaced records per-item watermarks for a resolution write.
func (r *Runners) stampPlaced(kind, service string, placed []kinds.PlaceRecord) {
	for _, rec := range placed {
		r.Mirror.RecordMirrorItemCopy(kind, rec.Name, service, rec.ExternalID, rec.Name, rec.ETag)
	}
}

// listPlaylistTracks pages one playlist's full item list.
func (r *Runners) listPlaylistTracks(ctx context.Context, service, playlistID string) ([]kinds.Track, error) {
	src, err := library.MirrorOpFor[kinds.PlaylistItemsProvider](r.Mirror, service, "conflict source")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	var out []kinds.Track
	cursor := ""
	for i := 0; i < shared.LibraryPageCap; i++ {
		_, page, err := src.ListPlaylistItems(ctx, playlistID, cursor, 100)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Tracks...)
		if page.Next == "" {
			return out, nil
		}
		cursor = page.Next
	}
	return out, nil
}

// listMemberContacts reads one service's full contact list.
func (r *Runners) listMemberContacts(ctx context.Context, service string) ([]kinds.Contact, bool, error) {
	src, err := library.MirrorOpFor[kinds.ContactProvider](r.Mirror, service, "conflict source")
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	return src.ListUserContacts(ctx)
}

// listMemberEvents reads one service's full event list.
func (r *Runners) listMemberEvents(ctx context.Context, service string) ([]kinds.CalendarEvent, bool, error) {
	src, err := library.MirrorOpFor[kinds.CalendarEventProvider](r.Mirror, service, "conflict source")
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrSyncTerminal, err)
	}
	return src.ListCalendarEvents(ctx)
}

// findContactByRef finds the contact a member copy row points at.
func findContactByRef(list []kinds.Contact, service, externalID string) (kinds.Contact, bool) {
	for _, c := range list {
		for _, src := range c.Sources {
			if src.Service == service && src.ID == externalID {
				return c, true
			}
		}
	}
	return kinds.Contact{}, false
}

// findContactByKey finds the contact with the given identity key.
func findContactByKey(list []kinds.Contact, key string) kinds.Contact {
	if key == "" {
		return kinds.Contact{}
	}
	for _, c := range list {
		if kinds.PrimaryContactKey(c) == key {
			return c
		}
	}
	return kinds.Contact{}
}

// findContactByName finds the contact with the given display name.
func findContactByName(list []kinds.Contact, name string) kinds.Contact {
	for _, c := range list {
		if contactDisplayName(c) == name {
			return c
		}
	}
	return kinds.Contact{}
}

// findEventByRef finds the event a member copy row points at.
func findEventByRef(list []kinds.CalendarEvent, service, externalID string) (kinds.CalendarEvent, bool) {
	for _, e := range list {
		for _, src := range e.Sources {
			if src.Service == service && src.ID == externalID {
				return e, true
			}
		}
	}
	return kinds.CalendarEvent{}, false
}

// findEventByKey finds the event with the given identity key.
func findEventByKey(list []kinds.CalendarEvent, key string) kinds.CalendarEvent {
	if key == "" {
		return kinds.CalendarEvent{}
	}
	for _, e := range list {
		if kinds.CalendarEventIdentityKey(e) == key {
			return e
		}
	}
	return kinds.CalendarEvent{}
}

// findEventByName finds the event with the given title.
func findEventByName(list []kinds.CalendarEvent, title string) kinds.CalendarEvent {
	for _, e := range list {
		if e.Title == title {
			return e
		}
	}
	return kinds.CalendarEvent{}
}

// contactRefID / contactRefETag read a contact's ref for one service.
func contactRefID(c kinds.Contact, service string) string {
	for _, src := range c.Sources {
		if src.Service == service {
			return src.ID
		}
	}
	return ""
}

func contactRefETag(c kinds.Contact, service string) string {
	for _, src := range c.Sources {
		if src.Service == service {
			etag, _ := src.Extra["etag"].(string)
			return etag
		}
	}
	return ""
}

// eventRefID / eventRefETag read an event's ref for one service.
func eventRefID(e kinds.CalendarEvent, service string) string {
	for _, src := range e.Sources {
		if src.Service == service {
			return src.ID
		}
	}
	return ""
}

func eventRefETag(e kinds.CalendarEvent, service string) string {
	for _, src := range e.Sources {
		if src.Service == service {
			etag, _ := src.Extra["etag"].(string)
			return etag
		}
	}
	return ""
}

// conflictHub resolves the kind's canonical home for a resolution write.
func (r *Runners) conflictHub(rec SyncConflict, row copies.Record) string {
	if hub, _ := r.Host.SyncGroup(rec.Kind); hub != "" {
		return hub
	}
	return row.SourceService
}
