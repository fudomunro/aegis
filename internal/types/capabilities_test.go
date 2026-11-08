package types

import (
	"net/http"
	"testing"
)

func TestCapabilityHelpers(t *testing.T) {
	caps := ServiceCapabilities{Kinds: []KindCapability{
		{Name: "tracks", Operations: []Operation{
			{Name: "list", Method: "GET"},
			{Name: "save", Method: "POST"},
		}},
		{Name: "playlists", Operations: []Operation{{Name: "list", Method: "GET"}}},
	}}
	if !KindKnown(caps, "tracks") || KindKnown(caps, "nope") {
		t.Fatal("KindKnown")
	}
	if KindHasWriteOp(caps, "playlists") || !KindHasWriteOp(caps, "tracks") {
		t.Fatal("KindHasWriteOp")
	}
	if KindHasAgentWriteOp(caps, "playlists") || !KindHasAgentWriteOp(caps, "tracks") {
		t.Fatal("KindHasAgentWriteOp")
	}
	if !IsMutatingMethod(http.MethodPost) || IsMutatingMethod(http.MethodGet) {
		t.Fatal("IsMutatingMethod")
	}
	writable := WritableKindsFromCapabilities(caps)
	if len(writable) != 1 || writable[0] != "tracks" {
		t.Fatalf("writable = %v", writable)
	}
}

func TestIsAgentDataWritePath(t *testing.T) {
	if IsAgentDataWritePath("/picker/sessions") || IsAgentDataWritePath("/search") {
		t.Fatal("picker/search are not agent data writes")
	}
	if !IsAgentDataWritePath("/me/tracks") {
		t.Fatal("library write path should count")
	}
}

func TestLibraryKind_MentionsInstructionsCacheKey(t *testing.T) {
	k := LibraryKind{
		ID: "widgets", Path: "/v1/library/widgets", Label: "widgets",
		Usage: "widgets", Summary: "widgets", SyncRule: "merge",
		ExampleQuestion: "what widgets?",
		Keywords:        []string{"my widgets"},
	}
	if !k.Mentions("show my widgets please") || k.Mentions("play some jazz") {
		t.Fatal("Mentions")
	}
	s := k.Instructions("https://aegis.example.com/")
	if s == "" {
		t.Fatal("Instructions empty")
	}
	if k.CacheKey() != "library:widgets" {
		t.Fatalf("CacheKey = %q", k.CacheKey())
	}
}

func TestMergeHelpers(t *testing.T) {
	a := NewArtist("spotify", "Spotify", "id1", "  A  ")
	if a.Name != "A" || a.Sources[0].ID != "id1" {
		t.Fatalf("NewArtist = %+v", a)
	}
	if NewAlbum("spotify", "Spotify", "1", "  ") != nil {
		t.Fatal("NewAlbum empty name")
	}
	src := PrimaryPlaylistSource("spotify", "Spotify", "p1", "https://x", map[string]any{"k": 1})
	if src.Service != "spotify" || src.ID != "p1" || src.URL != "https://x" {
		t.Fatalf("PrimaryPlaylistSource = %+v", src)
	}
	p := Playlist{Sources: []PlaylistSource{{Service: "a", ID: "1"}}}
	if FirstSourceID(p) != "1" {
		t.Fatalf("FirstSourceID = %q", FirstSourceID(p))
	}
	if got := ArtistEntityID(Artist{EntityID: "mb"}); got != "mb" {
		t.Fatalf("ArtistEntityID = %q", got)
	}
	if got := MergeArtistLibrary([]Artist{{Name: "A", Sources: []ArtistSource{{Service: "s", ID: "1"}}}}); len(got) != 1 {
		t.Fatalf("MergeArtistLibrary = %+v", got)
	}
	sources := []TrackSource{
		{Service: "b", ServiceName: "B"},
		{Service: "a", ServiceName: "A", Canonical: true},
	}
	SortTrackSourcesCanonicalFirst(sources)
	if !sources[0].Canonical || sources[0].Service != "a" {
		t.Fatalf("SortTrackSourcesCanonicalFirst = %+v", sources)
	}
	sources2 := []PlaylistSource{{Service: "b"}, {Service: "a", Canonical: true}}
	SortPlaylistSourcesCanonicalFirst(sources2)
	if !sources2[0].Canonical {
		t.Fatalf("SortPlaylistSourcesCanonicalFirst = %+v", sources2)
	}
	sources3 := []ContactSource{{Service: "b"}, {Service: "a", Canonical: true}}
	SortContactSourcesCanonicalFirst(sources3)
	if !sources3[0].Canonical {
		t.Fatalf("SortContactSourcesCanonicalFirst = %+v", sources3)
	}
}

func TestCanonicalService(t *testing.T) {
	p := Playlist{Sources: []PlaylistSource{{Service: "other"}, {Service: "home", Canonical: true}}}
	if p.CanonicalService() != "home" {
		t.Fatalf("playlist canonical = %q", p.CanonicalService())
	}
	tr := Track{Sources: []TrackSource{{Service: "only"}}}
	if tr.CanonicalService() != "only" {
		t.Fatalf("track primary = %q", tr.CanonicalService())
	}
	c := Contact{Sources: []ContactSource{{Service: "x"}, {Service: "y", Canonical: true}}}
	if c.CanonicalService() != "y" {
		t.Fatalf("contact = %q", c.CanonicalService())
	}
	ph := Photo{Sources: []PhotoSource{{Service: "p", Canonical: true}}}
	if ph.CanonicalService() != "p" {
		t.Fatalf("photo = %q", ph.CanonicalService())
	}
	m := Message{Sources: []MessageSource{{Service: "m"}}}
	if m.CanonicalService() != "m" {
		t.Fatalf("message = %q", m.CanonicalService())
	}
	e := CalendarEvent{Sources: []CalendarEventSource{{Service: "e", Canonical: true}}}
	if e.CanonicalService() != "e" {
		t.Fatalf("event = %q", e.CanonicalService())
	}
}

func TestServiceNameHelpersAndPrimary(t *testing.T) {
	p := Playlist{Sources: []PlaylistSource{{ServiceName: "Spotify"}}}
	if PlaylistServiceName(p) != "Spotify" {
		t.Fatalf("PlaylistServiceName = %q", PlaylistServiceName(p))
	}
	if PlaylistServiceName(Playlist{}) != "" {
		t.Fatal("empty playlist")
	}
	tr := Track{Sources: []TrackSource{{ServiceName: "YT"}}}
	if TrackServiceName(tr) != "YT" || TrackServiceName(Track{}) != "" {
		t.Fatal("TrackServiceName")
	}
	al := Album{Sources: []AlbumSource{{ServiceName: "AM"}}}
	if AlbumServiceName(al) != "AM" || AlbumServiceName(Album{}) != "" {
		t.Fatal("AlbumServiceName")
	}
	ar := Artist{Sources: []ArtistSource{{ServiceName: "SP"}}}
	if ArtistServiceName(ar) != "SP" || ArtistServiceName(Artist{}) != "" {
		t.Fatal("ArtistServiceName")
	}
	pl := Playlist{Sources: []PlaylistSource{{Service: "b", ID: "2"}, {Service: "a", ID: "1", Canonical: true}}}
	if pl.PrimaryService() != "b" {
		t.Fatalf("PrimaryService = %q", pl.PrimaryService())
	}
}

func TestMergeArtistLibrary_NameMerge(t *testing.T) {
	merged := MergeArtistLibrary([]Artist{
		{Name: "A", Sources: []ArtistSource{{Service: "s1", ID: "1"}}},
		{Name: "a", Sources: []ArtistSource{{Service: "s2", ID: "2"}}},
		{Name: "B", Sources: []ArtistSource{{Service: "s1", ID: "3"}}},
	})
	if len(merged) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestKindForPath_QueryAndSlash(t *testing.T) {
	if KindForPath("/me/tracks?limit=1") != "tracks" {
		t.Fatal("query strip")
	}
	if KindForPath("   ") != "" {
		t.Fatal("blank")
	}
	if KindForPath("/me/../secret") != "" {
		t.Fatal("dotdot must be non-canonical")
	}
}
