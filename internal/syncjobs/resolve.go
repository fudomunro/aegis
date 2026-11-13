package syncjobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/kinds"
)

// Conflict resolution (issue #365 phase 3, kind-owned plans since issue
// #382): the admin's accept/reject decision on a detected non-hub edit.
// Accept applies the member's edit to the canonical home and fans out to
// the other members. Reject re-converges that member from the canonical
// home. Neither deletes on a member: member-only items survive a reject
// (they are simply not adopted), and adopting a member's deletions is
// delete propagation, a later phase.
//
// The writes behind each decision are the kind's converge plan ops
// (extras diff/adopt, title read/rename, record resolve); the engine
// keeps the decision flow, the copy-row lookups, and the conflict store.

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
	case rec.Field == ConflictFieldOrder:
		return r.resolveOrder(ctx, rec, row, ok, accept)
	default:
		return "", fmt.Errorf("%w: unknown conflict field %q", ErrSyncTerminal, rec.Field)
	}
}

// planFor resolves a kind's converge plan from the registry (issue #382)
// — the engine never names a kind.
func (r *Runners) planFor(kind string) *kinds.ConvergePlan {
	if r == nil || r.Mirror == nil {
		return nil
	}
	k, ok := r.Mirror.RegistryKind(kind)
	if !ok {
		return nil
	}
	return k.Converge()
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

// resolveTitle renames: accept moves the canonical copy to the member's
// title, reject restores the canonical title on the member (issue #365
// phase 3). The read/rename pair is the kind's title ops (the playlist
// shape's plan).
func (r *Runners) resolveTitle(ctx context.Context, rec SyncConflict, row copies.Record, haveRow bool, accept bool) (string, error) {
	if !haveRow || row.SourceID == "" || row.ExternalID == "" {
		return "", fmt.Errorf("%w: the copy row for this playlist is gone", ErrSyncTerminal)
	}
	plan := r.planFor(rec.Kind)
	if plan == nil || plan.TitleOps == nil {
		return "", fmt.Errorf("%w: kind %q has no title resolution", ErrSyncTerminal, rec.Kind)
	}
	memberTitle, err := plan.TitleOps.Read(ctx, rec.Service, row.ExternalID)
	if err != nil {
		return "", planErr(err)
	}
	hubTitle, err := plan.TitleOps.Read(ctx, row.SourceService, row.SourceID)
	if err != nil {
		return "", planErr(err)
	}
	if accept {
		if err := plan.TitleOps.Rename(ctx, row.SourceService, row.SourceID, memberTitle); err != nil {
			return "", planErr(err)
		}
		r.restampCopy(row, memberTitle, row.Watermark)
		if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("canonical title set to %q; fanning out", memberTitle), nil
	}
	if err := plan.TitleOps.Rename(ctx, rec.Service, row.ExternalID, hubTitle); err != nil {
		return "", planErr(err)
	}
	r.restampCopy(row, hubTitle, row.Watermark)
	return fmt.Sprintf("member title restored to %q", hubTitle), nil
}

// resolveOrder settles a member-side item reorder (issue #378): accept
// makes the member's order canonical and fans out to the other copies,
// reject restores the canonical order on the member. A reorder never
// changes membership — items that exist only on the copy stay there.
// The probe/write pair is the kind's OrderOps plan op.
func (r *Runners) resolveOrder(ctx context.Context, rec SyncConflict, row copies.Record, haveRow bool, accept bool) (string, error) {
	if !haveRow {
		return "", fmt.Errorf("%w: the copy row for this conflict is gone", ErrSyncTerminal)
	}
	plan := r.planFor(rec.Kind)
	if plan == nil || plan.OrderOps == nil {
		return "", fmt.Errorf("%w: kind %q has no order resolution", ErrSyncTerminal, rec.Kind)
	}
	hub := r.conflictHub(rec, row)
	watermark, found, err := plan.OrderOps.Resolve(ctx, hub, rec.Service, row.SourceID, row.ExternalID, accept)
	if err != nil {
		return "", planErr(err)
	}
	if !found {
		return "the member copy is gone; nothing to decide", nil
	}
	r.restampCopy(row, row.Version.Name, watermark)
	if !accept {
		return "canonical order restored on the member", nil
	}
	// The member itself is unchanged: its watermark was refreshed to the
	// order it holds so the next converge updates instead of conflicting.
	if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
		return "", err
	}
	return "member order made canonical; fanning out", nil
}

// resolveItems adopts (accept) or keeps local (reject) the items a member
// holds beyond the canonical home. Rejections never remove anything from
// the member — convergence is add-only by design. The diff and adoption
// are the kind's MemberExtras plan op.
func (r *Runners) resolveItems(ctx context.Context, rec SyncConflict, row copies.Record, haveRow bool, accept bool) (string, error) {
	if !haveRow {
		return "", fmt.Errorf("%w: the copy row for this conflict is gone", ErrSyncTerminal)
	}
	plan := r.planFor(rec.Kind)
	if plan == nil || plan.MemberExtras == nil {
		return "", fmt.Errorf("%w: kind %q has no item adoption", ErrSyncTerminal, rec.Kind)
	}
	hub := r.conflictHub(rec, row)
	count, adopt, err := plan.MemberExtras(ctx, hub, rec.Service, row.SourceID, row.ExternalID)
	if err != nil {
		return "", planErr(err)
	}
	if !accept {
		if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
			return "", err
		}
		if count == 0 {
			return "member re-converged from the canonical home", nil
		}
		return fmt.Sprintf("%d member-only items kept local; the member was re-converged", count), nil
	}
	if count == 0 {
		return "nothing left to adopt", nil
	}
	if err := adopt(ctx); err != nil {
		return "", planErr(err)
	}
	if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d member-only items adopted onto the canonical home; fanning out", count), nil
}

// resolveRecord settles a member-side entity edit: accept writes the
// member's version to the canonical home, reject writes the canonical
// version back onto the member (If-Match against the member's live etag,
// so the overwrite is the user's decision, not a silent one). The record
// reads, ref merges, and writes are the kind's RecordResolve plan op.
func (r *Runners) resolveRecord(ctx context.Context, rec SyncConflict, row copies.Record, haveRow bool, accept bool) (string, error) {
	if !haveRow {
		return "", fmt.Errorf("%w: the copy row for this conflict is gone", ErrSyncTerminal)
	}
	plan := r.planFor(rec.Kind)
	if plan == nil || plan.RecordResolve == nil {
		return "", fmt.Errorf("%w: kind %q has no record resolution", ErrSyncTerminal, rec.Kind)
	}
	hub := r.conflictHub(rec, row)
	stamps, found, err := plan.RecordResolve(ctx, hub, rec.Service, rec.ExternalID, rec.Subject, accept)
	if err != nil {
		return "", planErr(err)
	}
	if !found {
		return "the member record is gone; nothing to decide", nil
	}
	r.stampPlaced(rec.Kind, stamps)
	if !accept {
		return "canonical version restored on the member", nil
	}
	// The member itself is unchanged: its watermark was refreshed to the
	// live etag so the next converge updates instead of conflicting again.
	if err := r.enqueueConverge(rec.Kind, rec.ID); err != nil {
		return "", err
	}
	return "member version written to the canonical home; fanning out", nil
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

// enqueueConverge schedules a converge run so an accept fans out and a
// reject re-converges the member (issue #365 phase 3).
func (r *Runners) enqueueConverge(kind, conflictID string) error {
	if r.Jobs == nil || !r.Jobs.Enabled() {
		return nil // fail-open: the scheduled run still fans out
	}
	_, err := r.Jobs.Enqueue(NewSyncEnqueue(kind, SyncJobTypeConverge, "", "conflict:"+conflictID, nil))
	return err
}

// restampCopy refreshes the copy row after a resolution so the watermark
// records what is now on the member (and the next check stays quiet).
// name and watermark carry the fields the resolution changed; callers pass
// the row's own values to preserve the rest.
func (r *Runners) restampCopy(row copies.Record, name, watermark string) {
	r.Mirror.RecordMirrorCopy(row.Kind, row.SourceService, row.SourceID,
		row.Service, row.ExternalID, name, row.Version.ItemCount, row.Version.SnapshotID, watermark)
}

// stampPlaced records per-item watermarks for a resolution write.
func (r *Runners) stampPlaced(kind string, stamps []kinds.ItemStamp) {
	for _, st := range stamps {
		r.Mirror.RecordMirrorItemCopy(kind, st.EntityName, st.Service, st.ExternalID, st.Name, st.ETag)
	}
}

// conflictHub resolves the kind's canonical home for a resolution write.
func (r *Runners) conflictHub(rec SyncConflict, row copies.Record) string {
	if hub, _ := r.Host.SyncGroup(rec.Kind); hub != "" {
		return hub
	}
	return row.SourceService
}
