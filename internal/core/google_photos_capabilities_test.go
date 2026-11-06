package core

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestAlbumServiceName_AndPhotosInstructions(t *testing.T) {
	if types.AlbumServiceName(types.Album{}) != "" {
		t.Fatal("empty album should have empty service name")
	}
	got := types.AlbumServiceName(types.Album{Sources: []types.AlbumSource{{Service: "spotify", ServiceName: "Spotify"}}})
	if got != "Spotify" {
		t.Fatalf("types.AlbumServiceName = %q", got)
	}

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	instr := srv.libraryInstructionsFor("photos")
	if !strings.Contains(instr, "/v1/library/photos") {
		t.Fatalf("photos instructions missing endpoint: %s", instr)
	}

	if !LibraryKindByID("photos").Mentions("what photos do I have?") {
		t.Fatal("expected photo mention")
	}
	if LibraryKindByID("photos").Mentions("play my jazz playlists") {
		t.Fatal("playlists should not trigger photos hint")
	}
}
