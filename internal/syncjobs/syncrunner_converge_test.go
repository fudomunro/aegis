package syncjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
)

// --- converge runner (issue #365) ---

func convergeJob(t *testing.T, store *JobStore, kind string) SyncJob {
	t.Helper()
	return mustEnqueue(t, store, SyncEnqueue{
		Kind: kind, JobType: SyncJobTypeConverge, Service: "",
	})
}

func jobProgress(t *testing.T, job SyncJob) map[string]any {
	t.Helper()
	var prog map[string]any
	if err := json.Unmarshal(job.Progress, &prog); err != nil {
		t.Fatalf("progress %q: %v", job.Progress, err)
	}
	return prog
}

func TestConvergeRunner_PlacesCanonicalContactsOnMembers(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}, {Name: "Bob"}}}
	member := &entityStubConn{}
	// A local import store is a read-only member and must never be targeted,
	// even though the stub would accept writes.
	local := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": member, connectors.TimelineServiceID: local}
	run, store, w := convergeTestRunners(t, "contacts", "google",
		[]string{"google", "icloud", connectors.TimelineServiceID}, conns)

	convergeJob(t, store, "contacts")
	for i := 0; i < 4 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (progress %s)", job.State, job.Progress)
	}
	if got := member.heldContacts(); len(got) != 2 {
		t.Fatalf("member holds %v, want Ada and Bob", got)
	}
	if local.placeCalls != 0 || local.lastPlaced != nil {
		t.Fatalf("read-only local import must never be targeted: calls=%d placed=%v",
			local.placeCalls, local.lastPlaced)
	}
	if hub.placeCalls != 0 {
		t.Fatalf("canonical home must not receive placements, calls=%d", hub.placeCalls)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["planned"]) != 2 || progressInt(prog["copied"]) != 2 {
		t.Fatalf("progress = %v, want planned=2 copied=2", prog)
	}
	if progressInt(prog["target_done"]) != 1 {
		t.Fatalf("target_done = %v, want 1", prog["target_done"])
	}
	_ = run
}

func TestConvergeRunner_NeverDeletesMemberOnlyItems(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}}}
	member := &entityStubConn{contacts: []kinds.Contact{{
		Name:    "Solo",
		Sources: []kinds.ContactSource{{Service: "icloud", ID: "id-solo"}},
	}}}
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": member}
	_, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"}, conns)

	convergeJob(t, store, "contacts")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded", job.State)
	}
	got := member.heldContacts()
	if len(got) != 2 {
		t.Fatalf("member holds %v, want Solo plus Ada (convergence never deletes)", got)
	}
	found := false
	for _, name := range got {
		if name == "Solo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("member-only contact was destroyed: %v", got)
	}
	if progressInt(jobProgress(t, job)["copied"]) != 1 {
		t.Fatalf("copied = %s, want 1", job.Progress)
	}
}

func TestConvergeRunner_ChainsAcrossMembers(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}, {Name: "Bob"}}}
	one := &entityStubConn{}
	two := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": one, "outlook": two}
	_, store, w := convergeTestRunners(t, "contacts", "google",
		[]string{"google", "icloud", "outlook"}, conns)

	convergeJob(t, store, "contacts")
	for i := 0; i < 5 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded", job.State)
	}
	if len(one.heldContacts()) != 2 || len(two.heldContacts()) != 2 {
		t.Fatalf("members hold %v / %v, want two contacts each", one.heldContacts(), two.heldContacts())
	}
	prog := jobProgress(t, job)
	// Two items on two members: two chained steps of planned/copied work.
	if progressInt(prog["planned"]) != 4 || progressInt(prog["copied"]) != 4 {
		t.Fatalf("progress = %v, want planned=4 copied=4 accumulated across steps", prog)
	}
	if progressInt(prog["target_done"]) != 2 || progressInt(prog["targets"]) != 2 {
		t.Fatalf("progress = %v, want 2 of 2 targets done", prog)
	}
}

func TestConvergeRunner_NoCanonicalHomeCompletesWithNote(t *testing.T) {
	member := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"icloud": member}
	_, store, w := convergeTestRunners(t, "contacts", "", []string{"icloud"}, conns)

	convergeJob(t, store, "contacts")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (nothing to do is not a failure)", job.State)
	}
	if member.placeCalls != 0 {
		t.Fatalf("nothing may be placed without a canonical home, calls=%d", member.placeCalls)
	}
	note, _ := jobProgress(t, job)["note"].(string)
	if !strings.Contains(note, "no canonical home") {
		t.Fatalf("note = %q, want the no-canonical-home explanation", note)
	}
}

func TestConvergeRunner_NoWriteCapableMembersCompletesWithNote(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}}}
	local := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"google": hub, connectors.TimelineServiceID: local}
	_, store, w := convergeTestRunners(t, "contacts", "google",
		[]string{"google", connectors.TimelineServiceID}, conns)

	convergeJob(t, store, "contacts")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded", job.State)
	}
	if local.placeCalls != 0 {
		t.Fatalf("read-only member must not be targeted, calls=%d", local.placeCalls)
	}
	note, _ := jobProgress(t, job)["note"].(string)
	if !strings.Contains(note, "no write-capable members") {
		t.Fatalf("note = %q, want the no-write-capable-members explanation", note)
	}
}

func TestConvergeRunner_ConflictsAreCountedNotRetried(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}, {Name: "Bob"}}}
	member := &entityStubConn{}
	member.setConflicts(
		[]kinds.PlaceConflict{{Name: "Ada", Service: "icloud", ID: "id-ada", Reason: "changed"}},
		[]kinds.PlaceSkip{{Name: "Bob", Reason: "no match"}},
	)
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": member}
	_, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"}, conns)

	convergeJob(t, store, "contacts")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (a conflict is reported, not a failure)", job.State)
	}
	if member.placeCalls != 1 {
		t.Fatalf("place calls = %d, want 1 (conflicts are never retried in place)", member.placeCalls)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["conflicts"]) != 1 {
		t.Fatalf("conflicts = %v, want 1", prog["conflicts"])
	}
	if progressInt(prog["skipped"]) != 1 {
		t.Fatalf("skipped = %v, want 1", prog["skipped"])
	}
}

func TestConvergeRunner_StampsCopyRowsFreshnessReads(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}, {Name: "Bob"}}}
	member := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": member}
	run, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"}, conns)

	convergeJob(t, store, "contacts")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	if job := store.List(SyncFilter{})[0]; job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded", job.State)
	}

	// One collection row per member, plus per-item watermark rows — the
	// rows the freshness pass walks.
	rec, ok := run.Copies.Get(library.MirrorKindContacts, "icloud", "")
	if !ok {
		t.Fatal("per-member copy row must be stamped")
	}
	if rec.SourceService != "google" {
		t.Fatalf("copy row source = %q, want google", rec.SourceService)
	}
	for _, id := range []string{"id-Ada", "id-Bob"} {
		item, ok := run.Copies.Get(library.MirrorKindContacts, "icloud", id)
		if !ok {
			t.Fatalf("per-item watermark row missing for %s", id)
		}
		if item.Version.ETag == "" {
			t.Fatalf("per-item row for %s carries no etag watermark", id)
		}
	}
	rows := run.Copies.ListForCheck(library.MirrorKindContacts, 0, 100)
	if len(rows) < 3 {
		t.Fatalf("freshness walk sees %d rows, want the member row plus per-item rows", len(rows))
	}
}

func TestConvergeEntities_StampIsCanonicalCount(t *testing.T) {
	// The collection row records the canonical count fanned out — the
	// watermark a member-side create raises the live count above (issue
	// #377) — not the placements this run managed, which a refused
	// If-Match undercounts.
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}, {Name: "Bob"}}}
	member := &entityStubConn{}
	member.setConflicts([]kinds.PlaceConflict{
		{Name: "Ada", Service: "icloud", ID: "id-Ada", Reason: "etag mismatch"},
	}, nil)
	run, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})

	runConverge(t, store, w, "contacts")
	rec, ok := run.Copies.Get(library.MirrorKindContacts, "icloud", "")
	if !ok {
		t.Fatal("collection copy row must be stamped")
	}
	if rec.Version.ItemCount != 2 {
		t.Fatalf("collection row count = %d, want the canonical 2 (this run placed 1)", rec.Version.ItemCount)
	}
}

func TestConvergeRunner_RetriesUpstreamError(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}}}
	member := &entityStubConn{}
	member.setFailPlace(&SyncRetryAfter{Err: fmt.Errorf("upstream 502"), After: time.Millisecond})
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": member}
	_, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"}, conns)

	convergeJob(t, store, "contacts")
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobQueued {
		t.Fatalf("state = %s, want queued (retryable upstream error)", job.State)
	}
	if !strings.Contains(job.LastError, "502") {
		t.Fatalf("last_error = %q, want the upstream error", job.LastError)
	}
	if progressInt(jobProgress(t, job)["copied"]) != 0 {
		t.Fatalf("failed step must not count copies: %s", job.Progress)
	}

	time.Sleep(5 * time.Millisecond)
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job = store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s, want succeeded after retry", job.State)
	}
	if progressInt(jobProgress(t, job)["copied"]) != 1 {
		t.Fatalf("copied = %s, want 1 after the retried placement", job.Progress)
	}
}

func TestConvergeRunner_BadPayloadIsTerminal(t *testing.T) {
	hub := &entityStubConn{}
	member := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": member}
	run, _, _ := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"}, conns)

	r := &convergeRunner{run: run, kind: "contacts", plan: run.planFor("contacts")}
	_, err := r.Run(context.Background(), SyncJob{Payload: json.RawMessage("{")})
	if !errors.Is(err, ErrSyncTerminal) {
		t.Fatalf("err = %v, want terminal", err)
	}
	if _, err := (&convergeRunner{run: nil}).Run(context.Background(), SyncJob{}); !errors.Is(err, ErrSyncTerminal) {
		t.Fatalf("err = %v, want terminal for an unwired runner", err)
	}
}

func TestConvergeRunner_EventsUseTitlePlacement(t *testing.T) {
	hub := &entityStubConn{events: []kinds.CalendarEvent{{Title: "Standup"}, {Title: "Review"}}}
	member := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"google-calendar": hub, "icloud": member}
	_, store, w := convergeTestRunners(t, "events", "google-calendar",
		[]string{"google-calendar", "icloud"}, conns)

	convergeJob(t, store, "events")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded", job.State)
	}
	member.mu.Lock()
	defer member.mu.Unlock()
	if len(member.events) != 2 {
		t.Fatalf("member holds %d events, want 2", len(member.events))
	}
}

func TestConvergeRunner_LocalImportMembersAreSkipped(t *testing.T) {
	hub := &entityStubConn{contacts: []kinds.Contact{{Name: "Ada"}}}
	remote := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{
		"google":                     hub,
		connectors.TimelineServiceID: &entityStubConn{},
		connectors.QuickenServiceID:  &entityStubConn{},
		"icloud":                     remote,
	}
	_, store, w := convergeTestRunners(t, "contacts", "google",
		[]string{"google", connectors.TimelineServiceID, connectors.QuickenServiceID, "icloud"}, conns)

	convergeJob(t, store, "contacts")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded", job.State)
	}
	if progressInt(jobProgress(t, job)["targets"]) != 1 {
		t.Fatalf("targets = %s, want 1 (only the write-capable remote member)",
			job.Progress)
	}
	if len(remote.heldContacts()) != 1 {
		t.Fatalf("remote member holds %v, want Ada", remote.heldContacts())
	}
}

func TestNewConvergeRunners_Register(t *testing.T) {
	// Runners derive from the kind registry's converge plans (issue #382):
	// every kind that declares a plan gets a converge runner.
	hub := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"google": hub}
	run, _ := convergeStubRunners(t, "contacts", "google", []string{"google"}, conns)

	runners := newConvergeRunners(run)
	if len(runners) != len(SyncConvergeKinds()) {
		t.Fatalf("runners = %d, want %d", len(runners), len(SyncConvergeKinds()))
	}
	reg := NewRunnerRegistry()
	registerConvergeRunners(reg, run)
	for _, kind := range SyncConvergeKinds() {
		r, ok := reg.For(kind, SyncJobTypeConverge)
		if !ok {
			t.Fatalf("kind %s has no converge runner", kind)
		}
		types := r.JobTypes()
		if len(types) != 1 || types[0] != SyncJobTypeConverge {
			t.Fatalf("JobTypes = %v, want converge only", types)
		}
	}
	if got := newConvergeRunners(nil); len(got) != 0 {
		t.Fatalf("unwired run registered %d runners, want 0", len(got))
	}
}

func TestConvergeRunner_DryRunClassifiesWithoutWriting(t *testing.T) {
	ada := kinds.Contact{Name: "Ada", Emails: []string{"ada@example.com"}}
	bob := kinds.Contact{Name: "Bob", Emails: []string{"bob@example.com"}}
	carol := kinds.Contact{Name: "Carol", Emails: []string{"carol@example.com"}}
	withRef := func(c kinds.Contact, id, etag string) kinds.Contact {
		c.Sources = []kinds.ContactSource{{
			Service: "icloud", ServiceName: "icloud", ID: id,
			Extra: map[string]any{"etag": etag},
		}}
		return c
	}
	hub := &entityStubConn{contacts: []kinds.Contact{ada, bob, carol}}
	// The member holds Ada's copy unchanged and Bob's copy edited (etag drift).
	member := &entityStubConn{contacts: []kinds.Contact{
		withRef(ada, "id-ada", "e1"),
		withRef(bob, "id-bob", "e2"),
	}}
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": member}
	run, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"}, conns)

	// What the last real run placed: Ada unchanged (e1), Bob recorded at e1
	// but live at e2 — a would-be If-Match refusal.
	run.Mirror.RecordMirrorItemCopy("contacts", kinds.ContactItemKey(ada), "icloud", "id-ada", "Ada", "e1")
	run.Mirror.RecordMirrorItemCopy("contacts", kinds.ContactItemKey(bob), "icloud", "id-bob", "Bob", "e1")

	mustEnqueue(t, store, SyncEnqueue{
		Kind: "contacts", JobType: SyncJobTypeConverge,
		Payload: map[string]any{"dry_run": true},
	})
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (progress %s)", job.State, job.Progress)
	}
	if member.placeCalls != 0 {
		t.Fatalf("dry run must not write: calls=%d placed=%v", member.placeCalls, member.lastPlaced)
	}
	if hub.placeCalls != 0 {
		t.Fatalf("dry run must not adopt: hub calls=%d", hub.placeCalls)
	}
	// The member's holdings are untouched.
	if got := member.heldContacts(); len(got) != 2 {
		t.Fatalf("member holds %v, want unchanged 2", got)
	}
	prog := jobProgress(t, job)
	if prog["dry_run"] != true {
		t.Fatalf("progress = %v, want dry_run=true", prog)
	}
	if progressInt(prog["matched"]) != 1 || progressInt(prog["created"]) != 1 || progressInt(prog["would_conflict"]) != 1 {
		t.Fatalf("progress = %v, want matched=1 created=1 would_conflict=1", prog)
	}
	if progressInt(prog["planned"]) != 3 {
		t.Fatalf("planned = %v, want 3", prog["planned"])
	}
}

func TestConvergeRunner_DryRunRejectedWithoutClassify(t *testing.T) {
	// Playlist shapes have no Classify hook: a dry-run job is terminal.
	hub := &playlistStubConn{svc: "spotify"}
	member := &playlistStubConn{svc: "icloud"}
	conns := map[string]connectors.ServiceConnector{"spotify": hub, "icloud": member}
	_, store, w := convergeTestRunners(t, "playlists", "spotify", []string{"spotify", "icloud"}, conns)

	mustEnqueue(t, store, SyncEnqueue{
		Kind: "playlists", JobType: SyncJobTypeConverge,
		Payload: map[string]any{"dry_run": true},
	})
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobFailed {
		t.Fatalf("job state = %s, want failed for a shape without Classify", job.State)
	}
}

// deltaContactsStub is a contacts hub whose read is a change feed (issue
// #145): ListUserContactsDelta replays a scripted feed keyed by token and
// records which tokens were requested.
type deltaContactsStub struct {
	*entityStubConn
	feed  map[string]deltaPage
	calls []string
}

type deltaPage struct {
	contacts []kinds.Contact
	next     string
}

func (d *deltaContactsStub) ListUserContactsDelta(_ context.Context, token string) ([]kinds.Contact, string, bool, error) {
	d.calls = append(d.calls, token)
	page := d.feed[token]
	return page.contacts, page.next, false, nil
}

func TestConvergeRunner_DeltaTokenRoundTripsThroughCopyLedger(t *testing.T) {
	ada := kinds.Contact{Name: "Ada", Emails: []string{"ada@example.com"}}
	bob := kinds.Contact{Name: "Bob", Emails: []string{"bob@example.com"}}
	carol := kinds.Contact{Name: "Carol", Emails: []string{"carol@example.com"}}
	hub := &deltaContactsStub{
		entityStubConn: &entityStubConn{},
		feed: map[string]deltaPage{
			"":   {contacts: []kinds.Contact{ada, bob}, next: "t1"},
			"t1": {contacts: []kinds.Contact{carol}, next: "t2"},
		},
	}
	member := &entityStubConn{}
	conns := map[string]connectors.ServiceConnector{"google": hub, "icloud": member}
	run, store, w := convergeTestRunners(t, "contacts", "google", []string{"google", "icloud"}, conns)

	// First run: the feed starts fresh (no watermark yet) and records t1.
	convergeJob(t, store, "contacts")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	if got := member.heldContacts(); len(got) != 2 {
		t.Fatalf("first run member holds %v, want Ada and Bob", got)
	}
	if len(hub.calls) != 1 || hub.calls[0] != "" {
		t.Fatalf("first run delta calls = %v, want one fresh read", hub.calls)
	}
	if wm := run.collectionWatermark("contacts", "google", "icloud"); wm != "t1" {
		t.Fatalf("watermark after first run = %q, want t1", wm)
	}

	// Second run: resumes from the recorded token and advances to t2.
	convergeJob(t, store, "contacts")
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	if got := member.heldContacts(); len(got) != 3 {
		t.Fatalf("second run member holds %v, want 3", got)
	}
	if len(hub.calls) != 2 || hub.calls[1] != "t1" {
		t.Fatalf("delta calls = %v, want ['' 't1']", hub.calls)
	}
	if wm := run.collectionWatermark("contacts", "google", "icloud"); wm != "t2" {
		t.Fatalf("watermark after second run = %q, want t2", wm)
	}
}
