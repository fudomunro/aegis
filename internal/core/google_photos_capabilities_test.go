package core

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
)

func TestAlbumServiceName_AndPhotosInstructions(t *testing.T) {
	if kinds.AlbumServiceName(kinds.Album{}) != "" {
		t.Fatal("empty album should have empty service name")
	}
	got := kinds.AlbumServiceName(kinds.Album{Sources: []kinds.AlbumSource{{Service: "spotify", ServiceName: "Spotify"}}})
	if got != "Spotify" {
		t.Fatalf("kinds.AlbumServiceName = %q", got)
	}

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	instr := srv.library().LibraryInstructionsFor("photos")
	if !strings.Contains(instr, "/v1/library/photos") {
		t.Fatalf("photos instructions missing endpoint: %s", instr)
	}

	if !library.LibraryKindByID("photos").Mentions("what photos do I have?") {
		t.Fatal("expected photo mention")
	}
	if library.LibraryKindByID("photos").Mentions("play my jazz playlists") {
		t.Fatal("playlists should not trigger photos hint")
	}
}
