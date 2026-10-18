package connectors

import "testing"

func TestAmazonMusicConnector_Capabilities(t *testing.T) {
	caps := NewAmazonMusicConnector(nil).Capabilities()
	if len(caps.DataTypes) == 0 {
		t.Fatal("expected data types")
	}
	names := map[string]bool{}
	for _, dt := range caps.DataTypes {
		names[dt.Name] = true
	}
	for _, want := range []string{"user", "tracks", "albums", "artists", "playlists", "search", "library", "recommendations"} {
		if !names[want] {
			t.Fatalf("missing data type %q in %+v", want, names)
		}
	}
}
