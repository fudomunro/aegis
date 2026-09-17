package main

import (
	"testing"
)

func TestNormalizeIdentityKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Britney Spears", "britney spears"},
		{"  britney   SPEARS  ", "britney spears"},
		{"Miles Davis - Topic", "miles davis"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeIdentityKey(c.in); got != c.want {
			t.Errorf("NormalizeIdentityKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMergeArtists_SamePersonAcrossServices(t *testing.T) {
	a := newArtist("spotify", "Spotify", "sp-art-1", "Britney Spears")
	b := newArtist("tidal", "Tidal", "td-art-9", "britney  spears")
	merged := mergeArtists(a, b)
	if len(merged) != 1 {
		t.Fatalf("got %d artists, want 1: %+v", len(merged), merged)
	}
	if merged[0].Name != "Britney Spears" {
		t.Fatalf("name = %s", merged[0].Name)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestMergePlaylistsSync_SameNameMultiService(t *testing.T) {
	in := []Playlist{
		{
			Name:       "Road Trip",
			TrackCount: 12,
			Sources:    []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}},
		},
		{
			Name:        "road trip",
			TrackCount:  15,
			Description: "car songs",
			Sources:     []PlaylistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "PL1"}},
		},
		{
			Name:    "Unrelated",
			Sources: []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp2"}},
		},
	}
	merged := MergePlaylistsSync(in)
	if len(merged) != 2 {
		t.Fatalf("got %d playlists, want 2", len(merged))
	}
	// Sorted by name: Road Trip, Unrelated.
	rt := merged[0]
	if rt.Name != "Road Trip" {
		t.Fatalf("name = %s", rt.Name)
	}
	if rt.TrackCount != 15 {
		t.Fatalf("track_count = %d, want max 15", rt.TrackCount)
	}
	if rt.Description != "car songs" {
		t.Fatalf("description = %q", rt.Description)
	}
	if len(rt.Sources) != 2 {
		t.Fatalf("sources = %+v", rt.Sources)
	}
	// Sources ordered by service name: Spotify before YouTube Music.
	if rt.Sources[0].Service != "spotify" || rt.Sources[1].Service != "youtube-music" {
		t.Fatalf("source order = %+v", rt.Sources)
	}
	if len(merged[1].Sources) != 1 {
		t.Fatalf("unrelated should keep one source: %+v", merged[1])
	}
}

func TestNewArtist_EmptyName(t *testing.T) {
	if got := newArtist("spotify", "Spotify", "", "  "); got.Name != "" {
		t.Fatalf("expected empty artist, got %+v", got)
	}
}

func TestCleanYouTubeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"So What (Official Video)", "So What"},
		{"So What (Official Music Video)", "So What"},
		{"So What (Official Audio)", "So What"},
		{"So What (Lyrics)", "So What"},
		{"So What [Official Video]", "So What"},
		{"Blue in Green (Official Video) (HD)", "Blue in Green"},
		{"So What", "So What"},
		{"", ""},
	}
	for _, c := range cases {
		if got := cleanYouTubeTitle(c.in); got != c.want {
			t.Errorf("cleanYouTubeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMergeTracksSync_ISRCPreferredOverName(t *testing.T) {
	// Same display name, different ISRCs → stay separate (different recordings).
	merged := MergeTracksSync([]Track{
		{
			Name:     "Song",
			Artists:  []Artist{newArtist("spotify", "Spotify", "", "A")},
			EntityID: "USRC12345678",
			Sources:  []TrackSource{{Service: "spotify", ID: "sp1"}},
		},
		{
			Name:     "Song",
			Artists:  []Artist{newArtist("spotify", "Spotify", "", "A")},
			EntityID: "USRC87654321",
			Sources:  []TrackSource{{Service: "spotify", ID: "sp2"}},
		},
	})
	if len(merged) != 2 {
		t.Fatalf("distinct ISRCs must not merge: %+v", merged)
	}

	// Shared ISRC across services → one entity, match=isrc.
	merged = MergeTracksSync([]Track{
		{
			Name:     "So What",
			Artists:  []Artist{newArtist("spotify", "Spotify", "", "Miles Davis")},
			EntityID: "usrc17000001",
			Sources:  []TrackSource{{Service: "spotify", ID: "sp1"}},
		},
		{
			Name:     "So What (Remastered)",
			Artists:  []Artist{newArtist("amazon-music", "Amazon Music", "", "Miles Davis")},
			EntityID: "USRC17000001",
			Sources:  []TrackSource{{Service: "amazon-music", ID: "am1"}},
		},
	})
	if len(merged) != 1 {
		t.Fatalf("shared ISRC should merge: %+v", merged)
	}
	if merged[0].EntityID != "USRC17000001" {
		t.Fatalf("entity_id = %q", merged[0].EntityID)
	}
	if merged[0].Match != TrackMatchISRC {
		t.Fatalf("match = %q, want isrc", merged[0].Match)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestMergeTracksSync_NameFallbackAbsorbsIntoISRCGroup(t *testing.T) {
	// YouTube has no ISRC; cleaned title should join the Spotify ISRC entity.
	merged := MergeTracksSync([]Track{
		{
			Name:     "So What",
			Artists:  []Artist{newArtist("spotify", "Spotify", "", "Miles Davis")},
			EntityID: "USRC17000001",
			Sources:  []TrackSource{{Service: "spotify", ID: "sp1"}},
		},
		{
			Name:    "So What",
			Artists: []Artist{newArtist("youtube-music", "YouTube Music", "", "Miles Davis - Topic")},
			Sources: []TrackSource{{Service: "youtube-music", ID: "vid1"}},
		},
	})
	if len(merged) != 1 {
		t.Fatalf("name fallback should absorb into ISRC group: %+v", merged)
	}
	if merged[0].EntityID != "USRC17000001" {
		t.Fatalf("entity_id = %q", merged[0].EntityID)
	}
	if merged[0].Match != TrackMatchISRC {
		t.Fatalf("match = %q, want isrc (survivor keeps durable id)", merged[0].Match)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestMergeTracksSync_NameOnlyMultiSource(t *testing.T) {
	merged := MergeTracksSync([]Track{
		{
			Name:    "Song",
			Artists: []Artist{newArtist("spotify", "Spotify", "", "A")},
			Sources: []TrackSource{{Service: "spotify", ID: "1"}},
		},
		{
			Name:    "song",
			Artists: []Artist{newArtist("tidal", "Tidal", "", "A")},
			Sources: []TrackSource{{Service: "tidal", ID: "2"}},
		},
	})
	if len(merged) != 1 {
		t.Fatalf("got %d", len(merged))
	}
	if merged[0].Match != TrackMatchName {
		t.Fatalf("match = %q, want name", merged[0].Match)
	}
}

func TestIso8601DurationMs(t *testing.T) {
	if got := iso8601DurationMs("PT3M42S"); got != 222000 {
		t.Fatalf("PT3M42S = %d", got)
	}
	if got := iso8601DurationMs("PT1H2M3S"); got != 3723000 {
		t.Fatalf("PT1H2M3S = %d", got)
	}
	if got := iso8601DurationMs("bogus"); got != 0 {
		t.Fatalf("bogus = %d", got)
	}
}
