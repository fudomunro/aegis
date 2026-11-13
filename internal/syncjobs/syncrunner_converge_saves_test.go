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

// --- albums and artists converge runners (issue #375) ---

func albumsGroup(t *testing.T, hub, member *saveStubConn) (*Runners, *JobStore, *SyncWorker) {
	t.Helper()
	return convergeTestRunners(t, "albums", "google-music", []string{"google-music", "icloud"},
		map[string]connectors.ServiceConnector{"google-music": hub, "icloud": member})
}

func artistsGroup(t *testing.T, hub, member *saveStubConn) (*Runners, *JobStore, *SyncWorker) {
	t.Helper()
	return convergeTestRunners(t, "artists", "google-music", []string{"google-music", "icloud"},
		map[string]connectors.ServiceConnector{"google-music": hub, "icloud": member})
}

func TestConvergeAlbums_FansOutAndKeepsMemberSaves(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedAlbums("A", "B")
	member := newSaveStubConn("icloud")
	member.seedAlbums("Solo")
	run, store, w := albumsGroup(t, hub, member)

	job := runConverge(t, store, w, "albums")
	if got := member.heldAlbumNames(); strings.Join(got, ",") != "Solo,A,B" {
		t.Fatalf("member holds %v, want its own save plus the canonical two (never un-saves)", got)
	}
	if hub.placeCalls != 0 {
		t.Fatalf("canonical home must not receive placements, calls=%d", hub.placeCalls)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["planned"]) != 2 || progressInt(prog["copied"]) != 2 {
		t.Fatalf("progress = %v, want planned=2 copied=2", prog)
	}
	rec, ok := run.Copies.Get(library.MirrorKindAlbums, "icloud", "")
	if !ok {
		t.Fatal("collection copy row must be stamped")
	}
	if rec.SourceService != "google-music" || rec.Version.ItemCount != 3 {
		t.Fatalf("copy row = %+v, want source google-music and the member's 3 saves", rec)
	}
}

func TestConvergeAlbums_RerunSkipsHeld(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedAlbums("A", "B")
	member := newSaveStubConn("icloud")
	_, store, w := albumsGroup(t, hub, member)

	runConverge(t, store, w, "albums")
	job := runConverge(t, store, w, "albums")
	if progressInt(jobProgress(t, job)["copied"]) != 0 {
		t.Fatalf("run 2 copied = %s, want 0 (everything is already saved)", job.Progress)
	}
	if got := member.heldAlbumNames(); strings.Join(got, ",") != "A,B" {
		t.Fatalf("member holds %v, want no duplicates", got)
	}
}

func TestConvergeAlbums_RestoresMemberShortfall(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedAlbums("A", "B")
	member := newSaveStubConn("icloud")
	member.seedAlbums("B") // the member un-saved A
	_, store, w := albumsGroup(t, hub, member)

	job := runConverge(t, store, w, "albums")
	if got := member.heldAlbumNames(); strings.Join(got, ",") != "B,A" {
		t.Fatalf("member holds %v, want the missing save restored (never short of the canonical list)", got)
	}
	if progressInt(jobProgress(t, job)["copied"]) != 1 {
		t.Fatalf("copied = %s, want 1", job.Progress)
	}
}

func TestConvergeAlbums_MemberExtrasConflict(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedAlbums("A", "B")
	member := newSaveStubConn("icloud")
	member.seedAlbums("A", "B", "X")
	run, store, w := albumsGroup(t, hub, member)

	runConverge(t, store, w, "albums")
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want the one items conflict", open)
	}
	c := open[0]
	if c.Field != ConflictFieldItems || c.MemberValue != "3 items" || c.LastValue != "2 items" {
		t.Fatalf("conflict = %+v, want items 3 vs 2", c)
	}
	if c.EntityKey != library.MirrorEntityKey(library.MirrorKindAlbums, "google-music", "") {
		t.Fatalf("entity key = %q", c.EntityKey)
	}
	if got := member.heldAlbumNames(); strings.Join(got, ",") != "A,B,X" {
		t.Fatalf("member holds %v, want its extra save untouched", got)
	}
}

func TestResolveAlbumsItemsAcceptAdoptsAndRejectKeepsLocal(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedAlbums("A", "B")
	member := newSaveStubConn("icloud")
	member.seedAlbums("A", "B", "X")
	run, _, _ := albumsGroup(t, hub, member)
	run.Mirror.RecordMirrorCopy(library.MirrorKindAlbums, "google-music", "",
		"icloud", "", "saved albums", 3, "", "")
	rec := openConflict(t, run, SyncConflict{
		Kind: "albums", Service: "icloud",
		EntityKey: library.MirrorEntityKey(library.MirrorKindAlbums, "google-music", ""),
		Subject:   "saved albums", Field: ConflictFieldItems,
		MemberValue: "3 items", LastValue: "2 items", Source: ConflictSourceConverge,
	})

	// Accept: the member's save becomes canonical and fans out.
	resolved, err := run.ResolveConflict(context.Background(), rec.ID, true)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !strings.Contains(resolved.Note, "adopted") {
		t.Fatalf("note = %q, want adoption", resolved.Note)
	}
	if got := hub.heldAlbumNames(); strings.Join(got, ",") != "A,B,X" {
		t.Fatalf("canonical holds %s, want the member's save adopted", got)
	}
	if len(run.Jobs.List(SyncFilter{})) != 1 {
		t.Fatal("accept must fan out")
	}

	// Reject on a fresh divergence: the save stays member-local.
	member.seedAlbums("Y")
	rec2 := openConflict(t, run, SyncConflict{
		Kind: "albums", Service: "icloud",
		EntityKey: library.MirrorEntityKey(library.MirrorKindAlbums, "google-music", ""),
		Subject:   "saved albums", Field: ConflictFieldItems,
		MemberValue: "4 items", LastValue: "3 items", Source: ConflictSourceConverge,
	})
	resolved, err = run.ResolveConflict(context.Background(), rec2.ID, false)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if !strings.Contains(resolved.Note, "kept local") {
		t.Fatalf("note = %q, want kept local", resolved.Note)
	}
	if got := member.heldAlbumNames(); !strings.Contains(strings.Join(got, ","), "Y") {
		t.Fatalf("member holds %v, want its save kept local", got)
	}
	if got := hub.heldAlbumNames(); strings.Contains(strings.Join(got, ","), "Y") {
		t.Fatalf("canonical holds %s, want it untouched by the rejection", got)
	}
}

func TestConvergeAlbums_RetriesUpstreamError(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedAlbums("A")
	member := newSaveStubConn("icloud")
	member.setFailPlace(&SyncRetryAfter{Err: fmt.Errorf("upstream 502"), After: time.Millisecond})
	_, store, w := albumsGroup(t, hub, member)

	convergeJob(t, store, "albums")
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
	if got := member.heldAlbumNames(); strings.Join(got, ",") != "A" {
		t.Fatalf("member holds %v, want A exactly once", got)
	}
}

func TestConvergeArtists_FansOutAndKeepsMemberFollows(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedArtists("A", "B")
	member := newSaveStubConn("icloud")
	member.seedArtists("Solo")
	run, store, w := artistsGroup(t, hub, member)

	job := runConverge(t, store, w, "artists")
	if got := member.heldArtistNames(); strings.Join(got, ",") != "Solo,A,B" {
		t.Fatalf("member holds %v, want its own follow plus the canonical two (never unfollows)", got)
	}
	if hub.placeCalls != 0 {
		t.Fatalf("canonical home must not receive placements, calls=%d", hub.placeCalls)
	}
	prog := jobProgress(t, job)
	if progressInt(prog["planned"]) != 2 || progressInt(prog["copied"]) != 2 {
		t.Fatalf("progress = %v, want planned=2 copied=2", prog)
	}
	rec, ok := run.Copies.Get(library.MirrorKindArtists, "icloud", "")
	if !ok {
		t.Fatal("collection copy row must be stamped")
	}
	if rec.SourceService != "google-music" || rec.Version.ItemCount != 3 {
		t.Fatalf("copy row = %+v, want source google-music and the member's 3 follows", rec)
	}
}

func TestConvergeArtists_MemberExtrasConflict(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedArtists("A", "B")
	member := newSaveStubConn("icloud")
	member.seedArtists("A", "B", "X")
	run, store, w := artistsGroup(t, hub, member)

	runConverge(t, store, w, "artists")
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want the one items conflict", open)
	}
	c := open[0]
	if c.Field != ConflictFieldItems || c.MemberValue != "3 items" || c.LastValue != "2 items" {
		t.Fatalf("conflict = %+v, want items 3 vs 2", c)
	}
	if c.EntityKey != library.MirrorEntityKey(library.MirrorKindArtists, "google-music", "") {
		t.Fatalf("entity key = %q", c.EntityKey)
	}
}

func TestResolveArtistsItemsAcceptAdoptsAndRejectKeepsLocal(t *testing.T) {
	hub := newSaveStubConn("google-music")
	hub.seedArtists("A", "B")
	member := newSaveStubConn("icloud")
	member.seedArtists("A", "B", "X")
	run, _, _ := artistsGroup(t, hub, member)
	run.Mirror.RecordMirrorCopy(library.MirrorKindArtists, "google-music", "",
		"icloud", "", "followed artists", 3, "", "")
	rec := openConflict(t, run, SyncConflict{
		Kind: "artists", Service: "icloud",
		EntityKey: library.MirrorEntityKey(library.MirrorKindArtists, "google-music", ""),
		Subject:   "followed artists", Field: ConflictFieldItems,
		MemberValue: "3 items", LastValue: "2 items", Source: ConflictSourceConverge,
	})

	resolved, err := run.ResolveConflict(context.Background(), rec.ID, true)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !strings.Contains(resolved.Note, "adopted") {
		t.Fatalf("note = %q, want adoption", resolved.Note)
	}
	if got := hub.heldArtistNames(); strings.Join(got, ",") != "A,B,X" {
		t.Fatalf("canonical holds %s, want the member's follow adopted", got)
	}
}
