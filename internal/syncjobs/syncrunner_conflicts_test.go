package syncjobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
)

// --- conflict detection (issue #365 phase 3) ---

func TestConvergeRecordsTitleConflict(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A")
	member := newPlaylistStubConn("icloud")
	run, store, w := convergeTestRunners(t, "playlists", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	member.renameTo(memberID, "Mine")
	hub.renameTo(hubP1, "Summer")

	runConverge(t, store, w, "playlists")
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want the one title conflict", open)
	}
	c := open[0]
	if c.Field != ConflictFieldTitle || c.MemberValue != "Mine" || c.LastValue != "Summer" {
		t.Fatalf("conflict = %+v, want Mine renamed against Summer", c)
	}
	if member.title(memberID) != "Mine" {
		t.Fatal("the member's rename must be left alone until it is resolved")
	}
}

func TestConvergeRecordsMemberExtras(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B")
	member := newPlaylistStubConn("icloud")
	run, store, w := convergeTestRunners(t, "playlists", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	member.addTrack(memberID, "X")
	runConverge(t, store, w, "playlists")

	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want the one items conflict", open)
	}
	c := open[0]
	if c.Field != ConflictFieldItems || c.MemberValue != "3 items" || c.LastValue != "2 items" {
		t.Fatalf("conflict = %+v, want items 3 vs 2", c)
	}
	if c.Subject != "Road trip" || c.Service != "icloud" {
		t.Fatalf("conflict = %+v, want the member's Road trip copy", c)
	}
	if c.EntityKey != library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubP1) {
		t.Fatalf("entity key = %q", c.EntityKey)
	}
	_ = hub
}

func TestConvergeRecordsIfMatchConflicts(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada", Emails: []string{"a@x"}}}}
	member := &entityStubConn{}
	member.setConflicts([]kinds.PlaceConflict{
		{Name: "Ada", Service: "icloud", ID: "id-Ada", Reason: "etag mismatch"},
	}, nil)
	run, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})

	runConverge(t, store, w, "contacts")
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want the one record conflict", open)
	}
	c := open[0]
	if c.Field != ConflictFieldRecord || c.Subject != "Ada" || c.ExternalID != "id-Ada" {
		t.Fatalf("conflict = %+v, want the If-Match refusal as a record", c)
	}
	if c.EntityKey != "contacts:Ada" {
		t.Fatalf("entity key = %q, want contacts:Ada", c.EntityKey)
	}
}

func TestConvergeEnrichesEntityRefsFromWatermarks(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada", Emails: []string{"a@x"}}}}
	member := &entityStubConn{}
	_, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})

	runConverge(t, store, w, "contacts")
	if len(member.heldContacts()) != 1 {
		t.Fatalf("member holds %v, want Ada placed once", member.heldContacts())
	}

	// The second run must carry the member's ref+etag from the watermark
	// row — the update path — not create a duplicate.
	runConverge(t, store, w, "contacts")
	member.mu.Lock()
	in := append([]kinds.Contact(nil), member.lastContactIn...)
	calls := member.placeCalls
	member.mu.Unlock()
	if calls != 2 || len(member.heldContacts()) != 1 {
		t.Fatalf("calls=%d holds=%v, want two placements and one contact", calls, member.heldContacts())
	}
	if len(in) != 1 {
		t.Fatalf("second input = %+v, want one contact", in)
	}
	id, etag := "", ""
	for _, src := range in[0].Sources {
		if src.Service == "icloud" {
			id = src.ID
			etag, _ = src.Extra["etag"].(string)
		}
	}
	if id != "id-Ada" || etag == "" {
		t.Fatalf("enriched ref = (%q,%q), want id-Ada and a watermark etag", id, etag)
	}
}

func TestConvergeEntities_RenameKeepsUpdatePath(t *testing.T) {
	// The watermark row keys on the contact's durable identity (issue
	// #384): a rename on the canonical home still finds it, so the re-run
	// carries the member's ref and updates in place instead of placing a
	// fresh copy under the new name.
	hub := &entityStubConn{contacts: []kinds.Contact{{
		Name: "Ada", Emails: []string{"a@x"}, Extra: map[string]any{"uid": "card-1"},
	}}}
	member := &entityStubConn{}
	_, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})

	runConverge(t, store, w, "contacts")
	if len(member.heldContacts()) != 1 {
		t.Fatalf("member holds %v, want Ada placed once", member.heldContacts())
	}

	hub.mu.Lock()
	hub.contacts[0].Name = "Ada Lovelace"
	hub.mu.Unlock()

	runConverge(t, store, w, "contacts")
	if got := member.heldContacts(); len(got) != 1 {
		t.Fatalf("member holds %v, want one contact updated in place after the rename", got)
	}
	member.mu.Lock()
	in := append([]kinds.Contact(nil), member.lastContactIn...)
	member.mu.Unlock()
	if len(in) != 1 {
		t.Fatalf("second input = %+v, want one contact", in)
	}
	id := ""
	for _, src := range in[0].Sources {
		if src.Service == "icloud" {
			id = src.ID
		}
	}
	if id != "id-Ada" {
		t.Fatalf("enriched ref = %q, want the member's original id-Ada", id)
	}
}

func TestFreshnessRecordsWatermarkDivergence(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B")
	member := newPlaylistStubConn("icloud")
	memberID := member.seedAs("PL-m", "Road trip", "A", "B")
	run, _, _ := convergeTestRunners(t, "playlists", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})
	run.Mirror.RecordMirrorCopy(library.MirrorKindPlaylists, "google", hubP1,
		"icloud", memberID, "Road trip", 2, "", "")

	// The member gains an item after the watermark was stamped.
	member.addTrack(memberID, "X")
	// A diverged row on the canonical home is its own edit — it propagates
	// rather than conflicts, and must not be recorded even though its
	// tokens would classify.
	run.Mirror.RecordMirrorCopy(library.MirrorKindPlaylists, "icloud", "PL-x",
		"google", "google-extra", "Old Name", 7, "", "")
	hub.seedAs("google-extra", "Ignored", "Z")

	engine := NewFreshnessEngine(run, newCheckerRegistry(run))
	batch, err := engine.RunBatch(context.Background(), "playlists", 0, 50)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if batch.Checked < 2 {
		t.Fatalf("checked = %d, want both rows checked", batch.Checked)
	}
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want exactly the member's items divergence", open)
	}
	c := open[0]
	if c.Field != ConflictFieldItems || c.Service != "icloud" || c.Source != ConflictSourceFreshness {
		t.Fatalf("conflict = %+v", c)
	}
	if c.MemberValue != "3 items" || c.LastValue != "2 items" {
		t.Fatalf("values = %q/%q, want 3 items/2 items", c.MemberValue, c.LastValue)
	}
}

// --- conflict resolution (issue #365 phase 3) ---

func resolveFixture(t *testing.T) (*Runners, *playlistStubConn, *playlistStubConn, string, string) {
	t.Helper()
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B")
	member := newPlaylistStubConn("icloud")
	memberID := member.seedAs("PL-m", "Mine", "A", "B", "X")
	run, _, _ := convergeTestRunners(t, "playlists", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})
	run.Mirror.RecordMirrorCopy(library.MirrorKindPlaylists, "google", hubP1,
		"icloud", memberID, "Road trip", 3, "", "")
	return run, hub, member, hubP1, memberID
}

func openConflict(t *testing.T, run *Runners, rec SyncConflict) SyncConflict {
	t.Helper()
	out, err := run.Conflicts.Upsert(rec)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	return out
}

func TestResolveTitleAcceptRenamesCanonical(t *testing.T) {
	run, hub, _, hubP1, memberID := resolveFixture(t)
	rec := openConflict(t, run, SyncConflict{
		Kind: "playlists", Service: "icloud",
		EntityKey:  library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubP1),
		ExternalID: memberID, Subject: "Road trip", Field: ConflictFieldTitle,
		MemberValue: "Mine", LastValue: "Road trip", Source: ConflictSourceConverge,
	})

	resolved, err := run.ResolveConflict(context.Background(), rec.ID, true)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if resolved.Status != ConflictStatusAccepted || !strings.Contains(resolved.Note, "fanning out") {
		t.Fatalf("resolved = %+v", resolved)
	}
	if got := hub.title(hubP1); got != "Mine" {
		t.Fatalf("canonical title = %q, want Mine (the member's edit)", got)
	}
	// Fan-out is a queued converge run.
	jobs := run.Jobs.List(SyncFilter{})
	if len(jobs) != 1 || jobs[0].JobType != SyncJobTypeConverge {
		t.Fatalf("jobs = %+v, want one converge fan-out", jobs)
	}
	if _, err := run.ResolveConflict(context.Background(), rec.ID, true); err == nil {
		t.Fatal("resolving twice must fail")
	}
}

func TestResolveTitleRejectRestoresMember(t *testing.T) {
	run, _, member, hubP1, memberID := resolveFixture(t)
	rec := openConflict(t, run, SyncConflict{
		Kind: "playlists", Service: "icloud",
		EntityKey:  library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubP1),
		ExternalID: memberID, Subject: "Road trip", Field: ConflictFieldTitle,
		MemberValue: "Mine", LastValue: "Road trip", Source: ConflictSourceConverge,
	})

	if _, err := run.ResolveConflict(context.Background(), rec.ID, false); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got := member.title(memberID); got != "Road trip" {
		t.Fatalf("member title = %q, want the canonical title restored", got)
	}
}

func TestResolveItemsAcceptAdoptsExtras(t *testing.T) {
	run, hub, _, hubP1, memberID := resolveFixture(t)
	rec := openConflict(t, run, SyncConflict{
		Kind: "playlists", Service: "icloud",
		EntityKey:  library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubP1),
		ExternalID: memberID, Subject: "Road trip", Field: ConflictFieldItems,
		MemberValue: "3 items", LastValue: "2 items", Source: ConflictSourceConverge,
	})

	resolved, err := run.ResolveConflict(context.Background(), rec.ID, true)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !strings.Contains(resolved.Note, "adopted") {
		t.Fatalf("note = %q, want adoption", resolved.Note)
	}
	if got := strings.Join(hub.trackNames(hubP1), ","); got != "A,B,X" {
		t.Fatalf("canonical holds %s, want the member's extra adopted", got)
	}
	if got := hub.title(hubP1); got != "Road trip" {
		t.Fatalf("canonical title = %q, want it untouched by adoption", got)
	}
}

func TestResolveItemsRejectKeepsLocal(t *testing.T) {
	run, hub, member, hubP1, memberID := resolveFixture(t)
	rec := openConflict(t, run, SyncConflict{
		Kind: "playlists", Service: "icloud",
		EntityKey:  library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubP1),
		ExternalID: memberID, Subject: "Road trip", Field: ConflictFieldItems,
		MemberValue: "3 items", LastValue: "2 items", Source: ConflictSourceConverge,
	})

	resolved, err := run.ResolveConflict(context.Background(), rec.ID, false)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if !strings.Contains(resolved.Note, "kept local") {
		t.Fatalf("note = %q, want the extras kept local", resolved.Note)
	}
	if got := strings.Join(member.trackNames(memberID), ","); got != "A,B,X" {
		t.Fatalf("member holds %s, want its extras untouched", got)
	}
	if got := strings.Join(hub.trackNames(hubP1), ","); got != "A,B" {
		t.Fatalf("canonical holds %s, want it untouched", got)
	}
	if len(run.Jobs.List(SyncFilter{})) != 1 {
		t.Fatal("reject must queue a re-converge")
	}
}

func TestResolveMissingRefusesAcceptAndRecreatesOnReject(t *testing.T) {
	run, hub, _, hubP1, memberID := resolveFixture(t)
	rec := openConflict(t, run, SyncConflict{
		Kind: "playlists", Service: "icloud",
		EntityKey:  library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubP1),
		ExternalID: memberID, Subject: "Road trip", Field: ConflictFieldMissing,
		MemberValue: "missing on this copy", LastValue: "Road trip", Source: ConflictSourceFreshness,
	})

	_, err := run.ResolveConflict(context.Background(), rec.ID, true)
	if !errors.Is(err, ErrSyncTerminal) {
		t.Fatalf("accept on missing = %v, want a terminal refusal (delete propagation is later)", err)
	}

	resolved, err := run.ResolveConflict(context.Background(), rec.ID, false)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if resolved.Status != ConflictStatusRejected {
		t.Fatalf("resolved = %+v", resolved)
	}
	// The stale row is cleared so converge re-creates the copy fresh.
	for _, row := range run.Copies.GetByEntity(library.MirrorKindPlaylists,
		library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubP1)) {
		if row.Service == "icloud" && row.ExternalID == memberID {
			t.Fatalf("stale copy row survived the reject: %+v", row)
		}
	}
	if len(run.Jobs.List(SyncFilter{})) != 1 {
		t.Fatal("reject must queue a re-converge")
	}
	_ = hub
}

func TestResolveRecordAcceptAndReject(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{
		Name: "Ada", Emails: []string{"a@x"},
		Sources: []kinds.ContactSource{{Service: "google", ID: "id-A", Extra: map[string]any{"etag": "h1"}}},
	}}}
	member := &entityStubConn{contacts: []kinds.Contact{{
		Name: "Ada", Emails: []string{"b@x"},
		Sources: []kinds.ContactSource{{Service: "icloud", ID: "id-Ada", Extra: map[string]any{"etag": "m1"}}},
	}}}
	run, _, _ := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})
	run.Mirror.RecordMirrorItemCopy("contacts", "Ada", "icloud", "id-Ada", "Ada", "m1")
	rec := openConflict(t, run, SyncConflict{
		Kind: "contacts", Service: "icloud", EntityKey: "contacts:Ada",
		ExternalID: "id-Ada", Subject: "Ada", Field: ConflictFieldRecord,
		MemberValue: "changed on this copy", LastValue: "not overwritten", Source: ConflictSourceConverge,
	})

	// Accept: the member's version becomes canonical.
	if _, err := run.ResolveConflict(context.Background(), rec.ID, true); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got := strings.Join(hub.contact("Ada").Emails, ","); got != "b@x" {
		t.Fatalf("canonical emails = %q, want the member's b@x", got)
	}
	if len(run.Jobs.List(SyncFilter{})) != 1 {
		t.Fatal("accept must fan out")
	}

	// A fresh record conflict on the same copy, resolved the other way.
	// The accept re-keyed the member's watermark row to the item's
	// identity key (issue #384) — later detections land on that locus.
	member.mu.Lock()
	member.contacts = []kinds.Contact{{
		Name: "Ada", Emails: []string{"c@x"},
		Sources: []kinds.ContactSource{{Service: "icloud", ID: "id-Ada", Extra: map[string]any{"etag": "m2"}}},
	}}
	member.mu.Unlock()
	rec2 := openConflict(t, run, SyncConflict{
		Kind: "contacts", Service: "icloud", EntityKey: "contacts:email:b@x",
		ExternalID: "id-Ada", Subject: "Ada", Field: ConflictFieldRecord,
		MemberValue: "changed again", Source: ConflictSourceConverge,
	})
	if _, err := run.ResolveConflict(context.Background(), rec2.ID, false); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got := strings.Join(member.contact("Ada").Emails, ","); got != "b@x" {
		t.Fatalf("member emails = %q, want the canonical b@x restored", got)
	}
}

func TestResolveRecordVanishedMemberIsNoop(t *testing.T) {
	member := &entityStubConn{}
	run, _, _ := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": &entityStubConn{}, "icloud": member})
	run.Mirror.RecordMirrorItemCopy("contacts", "Ada", "icloud", "id-Ada", "Ada", "m1")
	rec := openConflict(t, run, SyncConflict{
		Kind: "contacts", Service: "icloud", EntityKey: "contacts:Ada",
		ExternalID: "id-Ada", Subject: "Ada", Field: ConflictFieldRecord,
		MemberValue: "changed on this copy", Source: ConflictSourceConverge,
	})
	resolved, err := run.ResolveConflict(context.Background(), rec.ID, true)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if resolved.Status != ConflictStatusAccepted || !strings.Contains(resolved.Note, "gone") {
		t.Fatalf("resolved = %+v, want a no-op resolution", resolved)
	}
}

func TestResolveUnknownAndDisabled(t *testing.T) {
	run, _, _, hubP1, memberID := resolveFixture(t)
	if _, err := run.ResolveConflict(context.Background(), "9999", true); err == nil {
		t.Fatal("unknown id must fail")
	}
	rec := openConflict(t, run, SyncConflict{
		Kind: "playlists", Service: "icloud",
		EntityKey:  library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubP1),
		ExternalID: memberID, Field: ConflictFieldTitle,
		MemberValue: "Mine", LastValue: "Road trip",
	})
	run.Conflicts = &ConflictStore{}
	if _, err := run.ResolveConflict(context.Background(), rec.ID, true); err == nil {
		t.Fatal("a disabled store must fail the resolution")
	}
}
