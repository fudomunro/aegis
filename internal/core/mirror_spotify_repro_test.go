package core

import (
	"context"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

// TestSpotifyMirror_LikedTracksItemsRenamed replays the issue #240 repro
// fixture through the mirror source parse: the saved-tracks page arrives with
// the items array renamed to entries while total still claims a library.
// Expected: three tracks. On main this parse reads items[] only and goes
// silently empty — the shape drift the canary flagged.
func TestSpotifyMirror_LikedTracksItemsRenamed(t *testing.T) {
	fx, err := connectors.LoadReproFixture(
		"../connectors/testdata/repro/spotify/me-tracks-items-renamed/fixture.json")
	if err != nil {
		t.Fatalf("LoadReproFixture: %v", err)
	}
	srv := connectors.NewReplayServer(fx)
	t.Cleanup(srv.Close)

	host := testServerWithBase(&Config{Services: []Service{{
		ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: srv.URL + "/v1",
	}}}, "https://aegis.example.com")
	host.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	m := &spotifyMirror{s: host}
	tracks, total, _, err := m.loadLikedTracksPage(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("loadLikedTracksPage: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(tracks) != 3 {
		t.Fatalf("tracks = %d (%+v), want 3 (renamed items array must be read)", len(tracks), tracks)
	}
	if tracks[0].Name != "Fixture Track One" || tracks[2].Name != "Fixture Track Three" {
		t.Fatalf("tracks = %+v", tracks)
	}
}
