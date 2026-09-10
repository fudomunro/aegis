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
