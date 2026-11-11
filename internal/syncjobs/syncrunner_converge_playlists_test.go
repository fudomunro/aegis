package syncjobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/library"
)

// --- playlist converge runner (issue #365) ---

func playlistGroup(t *testing.T, hub, member *playlistStubConn) (*Runners, *JobStore, *SyncWorker) {
	t.Helper()
	return convergeTestRunners(t, "playlists", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})
}

// runConverge drains the queue and returns the finished job.
func runConverge(t *testing.T, store *JobStore, w *SyncWorker, kind string) SyncJob {
	t.Helper()
	convergeJob(t, store, kind)
	for i := 0; i < 16 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (progress %s, err %s)",
			job.State, job.Progress, job.LastError)
	}
	return job
}

func TestConvergePlaylists_FansOutCreatesAndKeepsOrder(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B", "C")
	hub.seed("Empty")
	member := newPlaylistStubConn("icloud")
	run, store, w := playlistGroup(t, hub, member)

	job := runConverge(t, store, w, "playlists")

	ids := member.ids()
	if len(ids) != 2 {
		t.Fatalf("member holds %d playlists, want 2 (creates propagate)", len(ids))
	}
	var roadTrip string
	for _, id := range ids {
		if member.title(id) == "Road trip" {
			roadTrip = id
		}
	}
	if roadTrip == "" {
		t.Fatalf("member titles %v/%v, want a Road trip copy",
			member.title(ids[0]), member.title(ids[1]))
	}
	if got := member.trackNames(roadTrip); strings.Join(got, ",") != "A,B,C" {
		t.Fatalf("member order = %v, want A,B,C (order is fidelity)", got)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["playlists_done"]) != 2 || progressInt(prog["copied"]) != 3 {
		t.Fatalf("progress = %v, want playlists_done=2 copied=3", prog)
	}
	// One copy row per (canonical playlist, member) — freshness-readable.
	for _, hubID := range []string{hubP1, hub.ids()[1]} {
		entity := library.MirrorEntityKey(library.MirrorKindPlaylists, "google", hubID)
		rows := run.Copies.GetByEntity(library.MirrorKindPlaylists, entity)
		if len(rows) != 1 || rows[0].Service != "icloud" {
			t.Fatalf("copy rows for %s = %+v, want one row on icloud", hubID, rows)
		}
	}
}

func TestConvergePlaylists_RerunAppendsOnlyNewItems(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B")
	member := newPlaylistStubConn("icloud")
	_, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	hub.addTrack(hubP1, "C")
	// Re-enqueue refreshes the terminal job with a fresh payload — a full
	// second converge run over the same group.
	job := runConverge(t, store, w, "playlists")

	roadTrip := member.ids()[0]
	if got := member.trackNames(roadTrip); strings.Join(got, ",") != "A,B,C" {
		t.Fatalf("member holds %v, want A,B,C (a re-run must not duplicate)", got)
	}
	if progressInt(jobProgress(t, job)["copied"]) != 1 {
		t.Fatalf("run 2 copied = %s, want 1 (only the new item)", job.Progress)
	}
}

func TestConvergePlaylists_RenamePropagates(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A")
	member := newPlaylistStubConn("icloud")
	_, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	if member.title(memberID) != "Road trip" {
		t.Fatalf("member title = %q, want Road trip", member.title(memberID))
	}

	hub.renameTo(hubP1, "Summer trip")
	runConverge(t, store, w, "playlists")
	if got := member.title(memberID); got != "Summer trip" {
		t.Fatalf("member title = %q, want Summer trip (renames propagate)", got)
	}
}

func TestConvergePlaylists_NeverDeletesMemberItems(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A")
	member := newPlaylistStubConn("icloud")
	_, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	member.addTrack(memberID, "Solo") // a member-only item appears
	hub.addTrack(hubP1, "B")

	job := runConverge(t, store, w, "playlists")
	got := member.trackNames(memberID)
	found := false
	for _, n := range got {
		if n == "Solo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("member-only track was destroyed: %v (convergence never deletes)", got)
	}
	if strings.Join(got, ",") != "A,Solo,B" {
		t.Fatalf("member holds %v, want A,Solo,B", got)
	}
	if progressInt(jobProgress(t, job)["copied"]) != 1 {
		t.Fatalf("copied = %s, want 1", job.Progress)
	}
}

func TestConvergePlaylists_TitleConflictCountedNotOverwritten(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A")
	member := newPlaylistStubConn("icloud")
	_, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	// The user renames the member copy and the canonical home diverges too.
	member.renameTo(memberID, "Mine")
	hub.renameTo(hubP1, "Hub name")
	hub.addTrack(hubP1, "B")

	job := runConverge(t, store, w, "playlists")
	if got := member.title(memberID); got != "Mine" {
		t.Fatalf("member title = %q, want Mine (a member-side rename is a conflict, not an overwrite)", got)
	}
	if got := member.trackNames(memberID); strings.Join(got, ",") != "A,B" {
		t.Fatalf("member holds %v, want A,B (items still converge)", got)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["conflicts"]) < 1 {
		t.Fatalf("progress = %v, want the title conflict counted", prog)
	}
	if progressInt(prog["copied"]) != 1 {
		t.Fatalf("copied = %s, want 1", prog["copied"])
	}

	// The conflict is sticky: the next run still reports it and still does
	// not overwrite the member's title.
	job = runConverge(t, store, w, "playlists")
	if got := member.title(memberID); got != "Mine" {
		t.Fatalf("member title = %q, want Mine on the follow-up run too", got)
	}
	if progressInt(jobProgress(t, job)["conflicts"]) < 1 {
		t.Fatalf("follow-up progress = %s, want the conflict still counted", job.Progress)
	}
}

func TestConvergePlaylists_AdoptsRecordedMemberPlaylist(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B")
	member := newPlaylistStubConn("icloud")
	// The member copy a previous mirror/converge created, at the title the
	// copy row records — the hub has been renamed to "Road trip" since.
	member.seedAs("PL-existing", "Old name", "A")
	run, store, w := playlistGroup(t, hub, member)
	run.Mirror.RecordMirrorCopy(library.MirrorKindPlaylists, "google", hubP1,
		"icloud", "PL-existing", "Old name", 1, "")

	runConverge(t, store, w, "playlists")
	if member.created != 0 {
		t.Fatalf("created = %d playlists, want 0 (the recorded member playlist is adopted)", member.created)
	}
	if got := member.trackNames("PL-existing"); strings.Join(got, ",") != "A,B" {
		t.Fatalf("PL-existing holds %v, want A,B", got)
	}
	if got := member.title("PL-existing"); got != "Road trip" {
		t.Fatalf("PL-existing title = %q, want Road trip (the rename propagates onto the adopted copy)", got)
	}
}

func TestConvergePlaylists_MultiPageChains(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hub.pageLimit = 2
	hub.seed("Road trip", "A", "B", "C", "D", "E")
	member := newPlaylistStubConn("icloud")
	_, store, w := playlistGroup(t, hub, member)

	job := runConverge(t, store, w, "playlists")
	roadTrip := member.ids()[0]
	if got := member.trackNames(roadTrip); strings.Join(got, ",") != "A,B,C,D,E" {
		t.Fatalf("member holds %v, want A,B,C,D,E exactly once", got)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["planned"]) != 5 || progressInt(prog["copied"]) != 5 {
		t.Fatalf("progress = %v, want planned=5 copied=5", prog)
	}
}

func TestConvergePlaylists_RetriesUpstreamError(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hub.seed("Road trip", "A")
	member := newPlaylistStubConn("icloud")
	member.setFailPlace(&SyncRetryAfter{Err: fmt.Errorf("upstream 502"), After: time.Millisecond})
	_, store, w := playlistGroup(t, hub, member)

	convergeJob(t, store, "playlists")
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobQueued {
		t.Fatalf("state = %s, want queued (retryable upstream error)", job.State)
	}
	if !strings.Contains(job.LastError, "upstream 502") {
		t.Fatalf("last_error = %q, want the upstream error", job.LastError)
	}

	time.Sleep(5 * time.Millisecond)
	for i := 0; i < 8 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job = store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s, want succeeded after retry (err %s)", job.State, job.LastError)
	}
	if got := member.trackNames(member.ids()[0]); strings.Join(got, ",") != "A" {
		t.Fatalf("member holds %v, want A exactly once (a retried create must not duplicate)", got)
	}
}

func TestConvergePlaylists_NeedsCopyLedger(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hub.seed("Road trip", "A")
	member := newPlaylistStubConn("icloud")
	run, store, w := playlistGroup(t, hub, member)
	// A disabled ledger cannot remember member playlists, so convergence
	// would create a duplicate copy per run — it refuses instead.
	run.Copies = &copies.Ledger{}

	convergeJob(t, store, "playlists")
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobFailed {
		t.Fatalf("state = %s, want failed terminal (err %s)", job.State, job.LastError)
	}
	if !strings.Contains(job.LastError, "copy ledger") {
		t.Fatalf("last_error = %q, want the copy-ledger explanation", job.LastError)
	}
	if member.placeCalls != 0 {
		t.Fatalf("nothing may be placed without the ledger, calls=%d", member.placeCalls)
	}
}
