package syncjobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/library"
)

// --- liked-tracks converge runner (issue #365) ---

func likedGroup(t *testing.T, hub, member *likedStubConn) (*Runners, *JobStore, *SyncWorker) {
	t.Helper()
	return convergeTestRunners(t, "tracks", "google-music", []string{"google-music", "icloud"},
		map[string]connectors.ServiceConnector{"google-music": hub, "icloud": member})
}

func TestConvergeTracks_FansOutAndKeepsMemberLikes(t *testing.T) {
	hub := newLikedStubConn("google-music")
	hub.seed("A", "B")
	member := newLikedStubConn("icloud")
	member.seed("Solo")
	run, store, w := likedGroup(t, hub, member)

	job := runConverge(t, store, w, "tracks")
	if got := member.heldNames(); strings.Join(got, ",") != "Solo,A,B" {
		t.Fatalf("member holds %v, want its own like plus the canonical two (never un-likes)", got)
	}
	if hub.placeCalls != 0 {
		t.Fatalf("canonical home must not receive placements, calls=%d", hub.placeCalls)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["planned"]) != 2 || progressInt(prog["copied"]) != 2 {
		t.Fatalf("progress = %v, want planned=2 copied=2", prog)
	}
	// The collection row the freshness pass reads.
	rec, ok := run.Copies.Get(library.MirrorKindTracks, "icloud", "")
	if !ok {
		t.Fatal("collection copy row must be stamped")
	}
	if rec.SourceService != "google-music" || rec.Version.ItemCount != 3 {
		t.Fatalf("copy row = %+v, want source google-music and the member's 3 likes", rec)
	}
}

func TestConvergeTracks_RerunSkipsHeld(t *testing.T) {
	hub := newLikedStubConn("google-music")
	hub.seed("A", "B")
	member := newLikedStubConn("icloud")
	_, store, w := likedGroup(t, hub, member)

	runConverge(t, store, w, "tracks")
	job := runConverge(t, store, w, "tracks")
	if progressInt(jobProgress(t, job)["copied"]) != 0 {
		t.Fatalf("run 2 copied = %s, want 0 (everything is already liked)", job.Progress)
	}
	if got := member.heldNames(); strings.Join(got, ",") != "A,B" {
		t.Fatalf("member holds %v, want no duplicates", got)
	}
}

func TestConvergeTracks_MultiPageChain(t *testing.T) {
	hub := newLikedStubConn("google-music")
	hub.pageLimit = 2
	hub.seed("A", "B", "C", "D", "E")
	member := newLikedStubConn("icloud")
	_, store, w := likedGroup(t, hub, member)

	job := runConverge(t, store, w, "tracks")
	if got := member.heldNames(); strings.Join(got, ",") != "A,B,C,D,E" {
		t.Fatalf("member holds %v, want all five in canonical order", got)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["planned"]) != 5 || progressInt(prog["copied"]) != 5 {
		t.Fatalf("progress = %v, want planned=5 copied=5 across the chain", prog)
	}
}

func TestConvergeTracks_RestoresMemberShortfall(t *testing.T) {
	hub := newLikedStubConn("google-music")
	hub.seed("A", "B")
	member := newLikedStubConn("icloud")
	member.seed("B") // the member un-liked A
	_, store, w := likedGroup(t, hub, member)

	job := runConverge(t, store, w, "tracks")
	if got := member.heldNames(); strings.Join(got, ",") != "B,A" {
		t.Fatalf("member holds %v, want the missing like restored (never short of the canonical list)", got)
	}
	if progressInt(jobProgress(t, job)["copied"]) != 1 {
		t.Fatalf("copied = %s, want 1", job.Progress)
	}
}

func TestConvergeTracks_MemberExtrasConflict(t *testing.T) {
	hub := newLikedStubConn("google-music")
	hub.seed("A", "B")
	member := newLikedStubConn("icloud")
	member.seed("A", "B", "X")
	run, store, w := likedGroup(t, hub, member)

	runConverge(t, store, w, "tracks")
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want the one items conflict", open)
	}
	c := open[0]
	if c.Field != ConflictFieldItems || c.MemberValue != "3 items" || c.LastValue != "2 items" {
		t.Fatalf("conflict = %+v, want items 3 vs 2", c)
	}
	if c.EntityKey != library.MirrorEntityKey(library.MirrorKindTracks, "google-music", "") {
		t.Fatalf("entity key = %q", c.EntityKey)
	}
	if got := member.heldNames(); strings.Join(got, ",") != "A,B,X" {
		t.Fatalf("member holds %v, want its extra like untouched", got)
	}
}

func TestResolveTracksItemsAcceptAdoptsAndRejectKeepsLocal(t *testing.T) {
	hub := newLikedStubConn("google-music")
	hub.seed("A", "B")
	member := newLikedStubConn("icloud")
	member.seed("A", "B", "X")
	run, _, _ := likedGroup(t, hub, member)
	run.Mirror.RecordMirrorCopy(library.MirrorKindTracks, "google-music", "",
		"icloud", "", "liked/saved tracks", 3, "")
	rec := openConflict(t, run, SyncConflict{
		Kind: "tracks", Service: "icloud",
		EntityKey: library.MirrorEntityKey(library.MirrorKindTracks, "google-music", ""),
		Subject:   "liked/saved tracks", Field: ConflictFieldItems,
		MemberValue: "3 items", LastValue: "2 items", Source: ConflictSourceConverge,
	})

	// Accept: the member's like becomes canonical and fans out.
	resolved, err := run.ResolveConflict(context.Background(), rec.ID, true)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !strings.Contains(resolved.Note, "adopted") {
		t.Fatalf("note = %q, want adoption", resolved.Note)
	}
	if got := hub.heldNames(); strings.Join(got, ",") != "A,B,X" {
		t.Fatalf("canonical holds %s, want the member's like adopted", got)
	}
	if len(run.Jobs.List(SyncFilter{})) != 1 {
		t.Fatal("accept must fan out")
	}

	// Reject on a fresh divergence: the like stays member-local.
	member.seed("Y")
	rec2 := openConflict(t, run, SyncConflict{
		Kind: "tracks", Service: "icloud",
		EntityKey: library.MirrorEntityKey(library.MirrorKindTracks, "google-music", ""),
		Subject:   "liked/saved tracks", Field: ConflictFieldItems,
		MemberValue: "4 items", LastValue: "3 items", Source: ConflictSourceConverge,
	})
	resolved, err = run.ResolveConflict(context.Background(), rec2.ID, false)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if !strings.Contains(resolved.Note, "kept local") {
		t.Fatalf("note = %q, want kept local", resolved.Note)
	}
	if got := member.heldNames(); !strings.Contains(strings.Join(got, ","), "Y") {
		t.Fatalf("member holds %v, want its like kept local", got)
	}
	if got := hub.heldNames(); strings.Contains(strings.Join(got, ","), "Y") {
		t.Fatalf("canonical holds %s, want it untouched by the rejection", got)
	}
}

func TestConvergeTracks_RetriesUpstreamError(t *testing.T) {
	hub := newLikedStubConn("google-music")
	hub.seed("A")
	member := newLikedStubConn("icloud")
	member.setFailPlace(&SyncRetryAfter{Err: fmt.Errorf("upstream 502"), After: time.Millisecond})
	_, store, w := likedGroup(t, hub, member)

	convergeJob(t, store, "tracks")
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobQueued {
		t.Fatalf("state = %s, want queued (retryable upstream error)", job.State)
	}
	if progressInt(jobProgress(t, job)["copied"]) != 0 {
		t.Fatalf("failed step must not count copies: %s", job.Progress)
	}

	time.Sleep(5 * time.Millisecond)
	for i := 0; i < 4 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job = store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s, want succeeded after retry (err %s)", job.State, job.LastError)
	}
	if got := member.heldNames(); strings.Join(got, ",") != "A" {
		t.Fatalf("member holds %v, want A exactly once", got)
	}
}
