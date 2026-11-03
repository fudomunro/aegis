package connectors

import (
	"context"
	"testing"
)

// TestSpotifyLikedTracks_ReproFixture replays the committed scrubbed capture
// of /v1/me/tracks through the real connector (issue #86): a removed local
// file (track: null) must be skipped and pagination must follow next.
func TestSpotifyLikedTracks_ReproFixture(t *testing.T) {
	fx, err := LoadReproFixture("testdata/repro/spotify/track-object-null-items/fixture.json")
	if err != nil {
		t.Fatalf("LoadReproFixture: %v", err)
	}
	srv := NewReplayServer(fx)
	defer srv.Close()

	// Captures record the request URI against the production endpoint
	// (https://api.spotify.com/v1/...), so replay must carry the same /v1
	// base path the configured endpoint would.
	host := newTestHostWithService("spotify", srv.URL+"/v1", "https://aegis.example.com")
	host.setToken("spotify", "test-token")
	conn := NewSpotifyConnector(host)

	tracks, truncated, err := conn.ListUserLikedTracks(context.Background())
	if err != nil {
		t.Fatalf("ListUserLikedTracks: %v", err)
	}
	if truncated {
		t.Fatal("two recorded pages must not report truncation")
	}
	// Page 1 has two items (one with a null track); page 2 has one → 2 tracks.
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d (%+v), want 2 (null item skipped)", len(tracks), tracks)
	}
	if tracks[0].Sources[0].ID != "t1" || tracks[0].Name != "Fixture Track One" {
		t.Fatalf("track 1 = %+v", tracks[0])
	}
	if tracks[1].Sources[0].ID != "t3" || tracks[1].Name != "Fixture Track Three" {
		t.Fatalf("track 2 = %+v", tracks[1])
	}
	if tracks[0].EntityID != "USFAK1234567" {
		t.Fatalf("ISRC = %q", tracks[0].EntityID)
	}
	if tracks[0].Album == nil || tracks[0].Album.Name != "Fixture Album" {
		t.Fatalf("album = %+v", tracks[0].Album)
	}
}

// TestSpotifyLikedTracks_MeTracksItemsRenamed replays the issue #240 repro
// fixture: the saved-tracks page arrives with the items array renamed to
// entries while total still claims a library. Expected: three tracks. On
// main this parse reads items[] only and goes silently empty.
func TestSpotifyLikedTracks_MeTracksItemsRenamed(t *testing.T) {
	fx, err := LoadReproFixture("testdata/repro/spotify/me-tracks-items-renamed/fixture.json")
	if err != nil {
		t.Fatalf("LoadReproFixture: %v", err)
	}
	srv := NewReplayServer(fx)
	defer srv.Close()

	host := newTestHostWithService("spotify", srv.URL+"/v1", "https://aegis.example.com")
	host.setToken("spotify", "test-token")
	conn := NewSpotifyConnector(host)

	tracks, truncated, err := conn.ListUserLikedTracks(context.Background())
	if err != nil {
		t.Fatalf("ListUserLikedTracks: %v", err)
	}
	if truncated {
		t.Fatal("single recorded page must not report truncation")
	}
	if len(tracks) != 3 {
		t.Fatalf("tracks = %d (%+v), want 3 (renamed items array must be read)", len(tracks), tracks)
	}
	if tracks[0].Sources[0].ID != "t1" || tracks[2].Sources[0].ID != "t3" {
		t.Fatalf("tracks = %+v", tracks)
	}
}
