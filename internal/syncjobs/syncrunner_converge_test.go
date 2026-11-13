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
