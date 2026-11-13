package syncjobs

import (
	"context"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/library"
)

// Playlist order reconciliation (issue #378): the canonical order fans
// out to members, a member-side reorder is a conflict the user settles,
// and neither direction ever changes membership.

func TestConvergePlaylists_PropagatesHubReorder(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B", "C")
	member := newPlaylistStubConn("icloud")
	run, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	if got := strings.Join(member.trackNames(memberID), ","); got != "A,B,C" {
		t.Fatalf("member holds %s, want the canonical order", got)
	}

	// A reorder on the canonical home re-orders the copy (order is
	// fidelity) and leaves nothing behind as a conflict.
	hub.reorderTo(hubP1, "C", "A", "B")
	runConverge(t, store, w, "playlists")
	if got := strings.Join(member.trackNames(memberID), ","); got != "C,A,B" {
		t.Fatalf("member holds %s, want the canonical reorder C,A,B", got)
	}
	if member.orderCalls == 0 {
		t.Fatal("the member's order write must have run")
	}
	if open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen}); len(open) != 0 {
		t.Fatalf("open conflicts = %+v, want none for a hub-side reorder", open)
	}
}

func TestConvergePlaylists_MemberReorderIsAConflict(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hub.seed("Road trip", "A", "B", "C")
	member := newPlaylistStubConn("icloud")
	run, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	before, ok := run.Copies.Get(library.MirrorKindPlaylists, "icloud", memberID)
	if !ok || before.Watermark == "" {
		t.Fatalf("copy row = %+v, want the order watermark stamped", before)
	}
	member.reorderTo(memberID, "B", "A", "C")

	runConverge(t, store, w, "playlists")
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want the one order conflict", open)
	}
	c := open[0]
	if c.Field != ConflictFieldOrder || c.Service != "icloud" {
		t.Fatalf("conflict = %+v, want the member's order divergence", c)
	}
	if c.MemberValue != "B | A | C" || c.LastValue != "A | B | C" {
		t.Fatalf("conflict values = %q vs %q, want the two sequences", c.MemberValue, c.LastValue)
	}
	if got := strings.Join(member.trackNames(memberID), ","); got != "B,A,C" {
		t.Fatalf("member holds %s, want its own order left alone", got)
	}
	// The watermark stays on the recorded baseline so a pending move
	// keeps re-detecting (and keeps suppressing the write) until resolved.
	after, _ := run.Copies.Get(library.MirrorKindPlaylists, "icloud", memberID)
	if after.Watermark != before.Watermark {
		t.Fatalf("watermark = %q, want the recorded baseline %q", after.Watermark, before.Watermark)
	}
}

func TestConvergePlaylists_OrderRejectRestoresCanonical(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hub.seed("Road trip", "A", "B", "C")
	member := newPlaylistStubConn("icloud")
	run, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	member.reorderTo(memberID, "B", "A", "C")
	runConverge(t, store, w, "playlists")
	rec := onlyOpenConflict(t, run)

	if _, err := run.ResolveConflict(context.Background(), rec.ID, false); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}
	if got := strings.Join(member.trackNames(memberID), ","); got != "A,B,C" {
		t.Fatalf("member holds %s, want the canonical order restored", got)
	}
	// The rejection stamps what the member now holds, so the move does
	// not re-flag on the next run.
	row, _ := run.Copies.Get(library.MirrorKindPlaylists, "icloud", memberID)
	if row.Watermark == "" {
		t.Fatal("the restored order's watermark must be stamped")
	}
	runConverge(t, store, w, "playlists")
	if open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen}); len(open) != 0 {
		t.Fatalf("open conflicts = %+v, want none after a rejection", open)
	}
}

func TestConvergePlaylists_OrderAcceptMakesCanonical(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B", "C")
	member := newPlaylistStubConn("icloud")
	run, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	member.reorderTo(memberID, "B", "A", "C")
	runConverge(t, store, w, "playlists")
	rec := onlyOpenConflict(t, run)

	if _, err := run.ResolveConflict(context.Background(), rec.ID, true); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}
	if got := strings.Join(hub.trackNames(hubP1), ","); got != "B,A,C" {
		t.Fatalf("hub holds %s, want the member's order made canonical", got)
	}
	// The member itself is unchanged; its watermark now matches what it
	// holds so the fan-out updates instead of conflicting again.
	if got := strings.Join(member.trackNames(memberID), ","); got != "B,A,C" {
		t.Fatalf("member holds %s, want its own order kept", got)
	}
	row, _ := run.Copies.Get(library.MirrorKindPlaylists, "icloud", memberID)
	if row.Watermark == "" {
		t.Fatal("the member's watermark must be refreshed on accept")
	}
}

func TestConvergePlaylists_OrderKeepsMemberOnlyItems(t *testing.T) {
	hub := newPlaylistStubConn("google")
	hubP1 := hub.seed("Road trip", "A", "B", "C")
	member := newPlaylistStubConn("icloud")
	run, store, w := playlistGroup(t, hub, member)

	runConverge(t, store, w, "playlists")
	memberID := member.ids()[0]
	member.addTrack(memberID, "Solo")
	hub.reorderTo(hubP1, "C", "B", "A")
	runConverge(t, store, w, "playlists")

	// The canonical order lands; the member-only track survives behind it.
	got := strings.Join(member.trackNames(memberID), ",")
	if got != "C,B,A,Solo" {
		t.Fatalf("member holds %s, want C,B,A with Solo kept at the end", got)
	}
	// The extra surfaces through the items channel (count divergence),
	// never as an order conflict — a reorder does not touch membership.
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 || open[0].Field != ConflictFieldItems {
		t.Fatalf("open conflicts = %+v, want the one items conflict", open)
	}
}

// onlyOpenConflict returns the fixture's single open conflict.
func onlyOpenConflict(t *testing.T, run *Runners) SyncConflict {
	t.Helper()
	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 1 {
		t.Fatalf("open conflicts = %+v, want exactly one", open)
	}
	return open[0]
}
