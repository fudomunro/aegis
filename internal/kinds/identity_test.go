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

// TestAlbumsMatchLadder pins the album placement identity ladder (issue
// #375): durable id first, then title + primary artist — the merge rules,
// applied by placement and convergence.
func TestAlbumsMatchLadder(t *testing.T) {
	mk := func(name, upc, artist string) Album {
		a := Album{Name: name, EntityID: upc}
		if artist != "" {
			a.Artists = []Artist{{Name: artist}}
		}
		return a
	}
	cases := []struct {
		name  string
		a, b  Album
		match bool
	}{
		{"same upc", mk("Album", "012345678901", "Artist"), mk("Other Title", "012345678901", "B"), true},
		{"distinct upcs stay separate", mk("Album", "012345678901", "Artist"), mk("Album", "012345678902", "Artist"), false},
		{"name absorbs into id", mk("Album", "", "Artist"), mk("Album", "012345678901", "Artist"), true},
		{"title + artist", mk("Album", "", "Artist"), mk("Album", "", "Artist"), true},
		{"different artists", mk("Album", "", "A"), mk("Album", "", "B"), false},
		{"different titles", mk("Album", "", "A"), mk("Other", "", "A"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AlbumsMatch(tc.a, tc.b); got != tc.match {
				t.Fatalf("AlbumsMatch = %v, want %v", got, tc.match)
			}
			if got := AlbumsMatch(tc.b, tc.a); got != tc.match {
				t.Fatalf("AlbumsMatch (reversed) = %v, want %v", got, tc.match)
			}
		})
	}

	// The match key names its method (recorded per placement).
	if _, how := AlbumMatchKey(mk("Album", "012345678901", "A")); how != AlbumMatchUPC {
		t.Fatalf("how = %q, want upc", how)
	}
	if _, how := AlbumMatchKey(mk("Album", "550e8400-e29b-41d4-a716-446655440000", "A")); how != AlbumMatchMBID {
		t.Fatalf("how = %q, want mbid", how)
	}
	if _, how := AlbumMatchKey(mk("Album", "", "A")); how != TrackMatchName {
		t.Fatalf("how = %q, want name", how)
	}

	// The matcher agrees with the ladder across a held list.
	m := NewAlbumMatcher([]Album{mk("Held", "012345678901", "Artist"), mk("Named", "", "Artist")})
	if !m.Has(mk("Held", "", "Artist")) {
		t.Fatal("name-only twin of an id'd album must be held")
	}
	if m.Has(mk("Held", "012345678902", "Artist")) {
		t.Fatal("a different release must not be held")
	}
	if !m.Has(mk("Named", "", "Artist")) {
		t.Fatal("title + artist match must be held")
	}
	if m.Has(mk("Other", "", "Artist")) {
		t.Fatal("a different album must not be held")
	}
}

// TestArtistsMatchLadder pins the artist placement identity ladder (issue
// #375): MBID first, then normalized name.
func TestArtistsMatchLadder(t *testing.T) {
	mk := func(name, mbid string) Artist {
		return Artist{Name: name, EntityID: mbid}
	}
	cases := []struct {
		name  string
		a, b  Artist
		match bool
	}{
		{"same mbid", mk("Act", "550e8400-e29b-41d4-a716-446655440000"), mk("Other Name", "550e8400-e29b-41d4-a716-446655440000"), true},
		{"distinct mbids stay separate", mk("Act", "550e8400-e29b-41d4-a716-446655440000"), mk("Act", "650e8400-e29b-41d4-a716-446655440000"), false},
		{"name absorbs into mbid", mk("Act", ""), mk("Act", "550e8400-e29b-41d4-a716-446655440000"), true},
		{"same name", mk("Act", ""), mk(" Act ", ""), true},
		{"different names", mk("Act", ""), mk("Other", ""), false},
		{"nameless never matches", Artist{}, Artist{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ArtistsMatch(tc.a, tc.b); got != tc.match {
				t.Fatalf("ArtistsMatch = %v, want %v", got, tc.match)
			}
			if got := ArtistsMatch(tc.b, tc.a); got != tc.match {
				t.Fatalf("ArtistsMatch (reversed) = %v, want %v", got, tc.match)
			}
		})
	}

	if _, how := ArtistMatchKey(mk("Act", "550e8400-e29b-41d4-a716-446655440000")); how != ArtistMatchMBID {
		t.Fatalf("how = %q, want mbid", how)
	}
	if _, how := ArtistMatchKey(mk("Act", "")); how != TrackMatchName {
		t.Fatalf("how = %q, want name", how)
	}

	m := NewArtistMatcher([]Artist{mk("Held", "550e8400-e29b-41d4-a716-446655440000"), mk("Named", "")})
	if !m.Has(mk("Held", "")) {
		t.Fatal("name-only twin of an mbid'd artist must be held")
	}
	if m.Has(mk("Held", "650e8400-e29b-41d4-a716-446655440000")) {
		t.Fatal("a different act must not be held")
	}
	if !m.Has(mk("Named", "")) {
		t.Fatal("name match must be held")
	}
}

// TestContactsMatchLadder pins the contact identity ladder: shared vCard
// UID first, then email, then phone, then handle — with the display name as
// a weak key only when a card has no email, phone, or handle. A shared UID
// folds cards even when their emails drifted; a shared email folds cards
// that carry different UIDs (the UID names the card, the email names the
// person).
func TestContactsMatchLadder(t *testing.T) {
	mk := func(name string, uid string, emails ...string) Contact {
		c := Contact{Name: name, Emails: emails}
		if uid != "" {
			c.Extra = map[string]any{"uid": uid}
		}
		return c
	}
	cases := []struct {
		name  string
		a, b  Contact
		match bool
	}{
		{"same uid links drifted emails", mk("Ada", "u1", "old@example.com"), mk("Ada L.", "u1", "new@example.com"), true},
		{"same uid links an email-less twin", mk("Bob", "u2", "bob@example.com"), mk("Bob", "u2"), true},
		{"shared email links distinct uids", mk("Ada", "u1", "ada@example.com"), mk("Ada L.", "u2", "ada@example.com"), true},
		{"phone links different cards", Contact{Name: "Bob", Phones: []string{"+15550001111"}}, Contact{Name: "Robert", Phones: []string{"+1 (555) 000-1111"}}, true},
		{"name folds name-eligible cards", mk("Grace Hopper", "u1"), mk("Grace Hopper", ""), true},
		{"name stays off an email card", mk("Alex Kim", "u1"), mk("Alex Kim", "u2", "alex@example.com"), false},
		{"name-only twins fold", mk("Alex Kim", ""), mk("Alex Kim", ""), true},
		{"different people stay separate", mk("Ada", "u1", "ada@example.com"), mk("Grace", "u2", "grace@example.com"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContactsMatch(tc.a, tc.b); got != tc.match {
				t.Fatalf("ContactsMatch = %v, want %v", got, tc.match)
			}
			if got := ContactsMatch(tc.b, tc.a); got != tc.match {
				t.Fatalf("ContactsMatch (reversed) = %v, want %v", got, tc.match)
			}
		})
	}

	// The matcher agrees with the ladder across a held list.
	m := NewContactMatcher([]Contact{
		mk("Ada Lovelace", "u1", "ada@example.com"),
		mk("Grace Hopper", ""),
	})
	if !m.Has(mk("Ada L.", "u1", "ada@example.com")) {
		t.Fatal("a drifted twin of a held card must be held")
	}
	if !m.Has(mk("Grace Hopper", "")) {
		t.Fatal("a name-only twin must be held")
	}
	if m.Has(mk("Alex Kim", "u9", "alex@example.com")) {
		t.Fatal("an unrelated contact must not be held")
	}
}
