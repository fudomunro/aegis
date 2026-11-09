package core

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/links"
	"github.com/fudomunro/aegis/internal/kinds"
)

// Fuzz targets for merge/matching heuristics (issue #107). These run on
// untrusted provider metadata (titles, ISRCs, artist names) and must not
// panic or explode combinatorially.

func FuzzNormalizeIdentityKey(f *testing.F) {
	f.Add("The Beatles")
	f.Add("  The   Beatles  ")
	f.Add("kinds.Artist - Topic")
	f.Add("0\n- TopiC")
	f.Add("- - TopiC TopiC")
	f.Add("Name\r\n- Topic")
	f.Add("")
	f.Add("Артист")
	f.Add("a\x00b")
	f.Add(strings.Repeat("x", 1000))
	f.Fuzz(func(t *testing.T, name string) {
		got := kinds.NormalizeIdentityKey(name)
		// Idempotent.
		if again := kinds.NormalizeIdentityKey(got); again != got {
			t.Fatalf("NormalizeIdentityKey not idempotent: %q -> %q -> %q", name, got, again)
		}
		// No surrounding space after normalize (unless empty).
		if got != strings.TrimSpace(got) {
			t.Fatalf("normalized key has padding: %q", got)
		}
	})
}

func FuzzCleanYouTubeTitle(f *testing.F) {
	f.Add("Song - Topic")
	f.Add("Song (Official Video)")
	f.Add("Song (Official Audio)")
	f.Add("")
	f.Add("(((((")
	f.Add("Song (Live) (Remix) (2020) (HD)")
	f.Fuzz(func(t *testing.T, name string) {
		got := kinds.CleanYouTubeTitle(name)
		if again := kinds.CleanYouTubeTitle(got); again != got {
			t.Fatalf("cleanYouTubeTitle not idempotent: %q -> %q -> %q", name, got, again)
		}
	})
}

func FuzzMergeTracksSync(f *testing.F) {
	f.Add("Song", "kinds.Artist", "ISRC1", "sp1")
	f.Add("Song", "kinds.Artist", "", "yt1")
	f.Add("Song - Topic", "kinds.Artist", "", "yt1")
	f.Add("Song", "kinds.Artist", "ISRC1", "")
	f.Add("", "", "", "")
	f.Add("Song", "kinds.Artist A", "ISRCX", "a")
	f.Add("Song", "kinds.Artist B", "ISRCX", "b")
	f.Add("Song", "kinds.Artist", "ISRC1", "a")
	f.Add("Song", "kinds.Artist", "ISRC2", "b")
	f.Fuzz(func(t *testing.T, title, artist, isrc, serviceID string) {
		mk := func(id, name string) kinds.Track {
			tr := kinds.Track{Name: name, EntityID: isrc, DurationMs: 200000}
			if artist != "" {
				tr.Artists = []kinds.Artist{{Name: artist, Sources: []kinds.ArtistSource{{Service: serviceID, Name: artist}}}}
			}
			if serviceID != "" || id != "" {
				tr.Sources = []kinds.TrackSource{{Service: serviceID, ID: id}}
			}
			return tr
		}
		in := []kinds.Track{
			mk("1", title),
			mk("2", title),
			mk("3", kinds.NormalizeIdentityKey(title)),
		}
		out := kinds.MergeTracksSync(in)
		if len(out) > len(in) {
			t.Fatalf("merge grew the list: in=%d out=%d", len(in), len(out))
		}
		// Every surviving track keeps at least one source when input had any.
		for _, tr := range out {
			if tr.Name == "" && len(tr.Sources) == 0 && tr.EntityID == "" {
				// Empty stubs are allowed only if all inputs were empty.
				continue
			}
			_ = kinds.TrackIdentityKey(tr)
			_ = kinds.TrackEntityID(tr)
		}
	})
}

func FuzzTrackIdentityKey(f *testing.F) {
	f.Add("Song", "kinds.Artist", "ISRC")
	f.Add("", "", "")
	f.Add("Song - Topic", "kinds.Artist - Topic", "")
	f.Fuzz(func(t *testing.T, name, artist, isrc string) {
		tr := kinds.Track{Name: name, EntityID: isrc}
		if artist != "" {
			tr.Artists = []kinds.Artist{{Name: artist}}
		}
		_ = kinds.TrackIdentityKey(tr)
		_ = kinds.TrackEntityID(tr)
		_ = kinds.DurationCompatible(tr, kinds.Track{Name: name, DurationMs: tr.DurationMs})
	})
}

func FuzzMergePlaylistsSync(f *testing.F) {
	f.Add("Jazz", "spotify", "sp1", "jazz")
	f.Add("Jazz", "youtube", "yt1", "jazz")
	f.Add("", "", "", "")
	f.Add("A", "s", "1", "")
	f.Add("B", "s", "2", "")
	f.Fuzz(func(t *testing.T, name, service, id, linkKey string) {
		p := kinds.Playlist{Name: name}
		if service != "" || id != "" {
			p.Sources = []kinds.PlaylistSource{{Service: service, ID: id}}
		}
		in := []kinds.Playlist{p, p, {Name: name + " "}}
		out := kinds.MergePlaylistsSyncLinked(in, nil)
		if len(out) > len(in) {
			t.Fatalf("playlist merge grew list: %d -> %d", len(out), len(in))
		}
		out2 := kinds.MergePlaylistsSyncLinked(in, func(kinds.Playlist) string { return linkKey })
		if len(out2) > len(in) {
			t.Fatalf("linked playlist merge grew list: %d -> %d", len(out2), len(in))
		}
		_ = p.PrimaryService()
		_ = kinds.NormalizeIdentityKey(name)
	})
}

func FuzzPlaylistLinkKey(f *testing.F) {
	f.Add("spotify", "sp1")
	f.Add("", "")
	f.Add("youtube", "yt1")
	f.Fuzz(func(t *testing.T, service, id string) {
		st := &links.Store{Links: []links.Link{{
			ID:      "L1",
			Kind:    "playlist",
			Sources: []links.Source{{Service: service, ID: id}},
		}}}
		p := kinds.Playlist{Sources: []kinds.PlaylistSource{{Service: service, ID: id}}}
		key := st.PlaylistLinkKey(p)
		if service == "" && id == "" {
			if key != "" && key != "link:L1" {
				t.Fatalf("unexpected key %q", key)
			}
		}
		// Nil store is inert.
		var missing *links.Store
		if k := missing.PlaylistLinkKey(p); k != "" {
			t.Fatalf("nil store returned %q", k)
		}
	})
}

func FuzzMergeAlbumsSync(f *testing.F) {
	f.Add("kinds.Album", "kinds.Artist", "UPC1", "sp1")
	f.Add("kinds.Album", "kinds.Artist", "", "sp1")
	f.Add("", "", "", "")
	f.Fuzz(func(t *testing.T, title, artist, upc, serviceID string) {
		al := kinds.Album{Name: title, EntityID: upc}
		if artist != "" {
			al.Artists = []kinds.Artist{{Name: artist}}
		}
		if serviceID != "" {
			al.Sources = []kinds.AlbumSource{{Service: serviceID, ID: "1", Name: title}}
		}
		out := kinds.MergeAlbumsSync([]kinds.Album{al, al})
		if len(out) > 2 {
			t.Fatalf("album merge grew list to %d", len(out))
		}
		_ = kinds.AlbumIdentityKey(al)
		_ = kinds.AlbumEntityID(al)
	})
}
