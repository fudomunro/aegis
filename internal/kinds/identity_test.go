package kinds

import "testing"

func TestExtraString(t *testing.T) {
	if got := ExtraString(nil, "ical_uid"); got != "" {
		t.Fatalf("nil map = %q", got)
	}
	extra := map[string]any{
		"ical_uid": "abc-123",
		"count":    3,
		"blank":    nil,
	}
	if got := ExtraString(extra, "ical_uid"); got != "abc-123" {
		t.Fatalf("string = %q", got)
	}
	if got := ExtraString(extra, "missing"); got != "" {
		t.Fatalf("missing = %q", got)
	}
	if got := ExtraString(extra, "count"); got != "" {
		t.Fatalf("non-string = %q", got)
	}
	if got := ExtraString(extra, "blank"); got != "" {
		t.Fatalf("nil value = %q", got)
	}
}

// TestTracksMatchLadder pins the placement identity ladder (issue #366):
// durable id first, then duration-guarded name — the merge rules, applied
// by placement and repair.
func TestTracksMatchLadder(t *testing.T) {
	artist := []Artist{{Name: "Artist"}}
	mk := func(name, isrc string, durationMs int) Track {
		return Track{Name: name, EntityID: isrc, DurationMs: durationMs, Artists: artist}
	}
	cases := []struct {
		name  string
		a, b  Track
		match bool
	}{
		{"same isrc", mk("Song", "USRC12345678", 200000), mk("Other Title", "USRC12345678", 1), true},
		{"distinct isrcs stay separate", mk("Song", "USRC12345678", 200000), mk("Song", "USRC87654321", 200000), false},
		{"name absorbs into isrc", mk("Song", "", 200000), mk("Song", "USRC12345678", 200000), true},
		{"name within tolerance", mk("Song", "", 200000), mk("Song", "", 203000), true},
		{"name beyond tolerance", mk("Song", "", 200000), mk("Song", "", 260000), false},
		{"different names", mk("Song", "", 200000), mk("Other", "", 200000), false},
		{"unknown durations pass", mk("Song", "", 0), mk("Song", "", 0), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TracksMatch(tc.a, tc.b); got != tc.match {
				t.Fatalf("TracksMatch = %v, want %v", got, tc.match)
			}
			if got := TracksMatch(tc.b, tc.a); got != tc.match {
				t.Fatalf("TracksMatch (reversed) = %v, want %v", got, tc.match)
			}
		})
	}

	// The match key names its method (issue #366 records it per placement).
	if _, how := TrackMatchKey(mk("Song", "USRC12345678", 1)); how != TrackMatchISRC {
		t.Fatalf("how = %q, want isrc", how)
	}
	if _, how := TrackMatchKey(mk("Song", "550e8400-e29b-41d4-a716-446655440000", 1)); how != TrackMatchMBID {
		t.Fatalf("how = %q, want mbid", how)
	}
	if _, how := TrackMatchKey(mk("Song", "", 1)); how != TrackMatchName {
		t.Fatalf("how = %q, want name", how)
	}

	// The matcher agrees with the ladder across a held list.
	m := NewTrackMatcher([]Track{mk("Song", "USRC12345678", 200000), mk("Held", "", 100000)})
	if !m.Has(mk("Song", "", 200000)) {
		t.Fatal("name-only twin of an id'd track must be held")
	}
	if m.Has(mk("Song", "USRC87654321", 200000)) {
		t.Fatal("a different recording must not be held")
	}
	if !m.Has(mk("Held", "", 101000)) {
		t.Fatal("duration-guarded name match must be held")
	}
}
