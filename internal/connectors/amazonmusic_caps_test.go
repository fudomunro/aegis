package connectors

import "testing"

func TestAmazonMusicConnector_Capabilities(t *testing.T) {
	caps := NewAmazonMusicConnector(nil).Capabilities()
	if len(caps.Kinds) == 0 {
		t.Fatal("expected kinds")
	}
	names := map[string]bool{}
	for _, dt := range caps.Kinds {
		names[dt.Name] = true
	}
	for _, want := range []string{"user", "tracks", "albums", "artists", "playlists", "search", "library", "recommendations"} {
		if !names[want] {
			t.Fatalf("missing kind %q in %+v", want, names)
		}
	}
}
