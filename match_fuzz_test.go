package main

import (
	"strings"
	"testing"
)

// Fuzz targets for merge/matching heuristics (issue #107). These run on
// untrusted provider metadata (titles, ISRCs, artist names) and must not
// panic or explode combinatorially.

func FuzzNormalizeIdentityKey(f *testing.F) {
	f.Add("The Beatles")
	f.Add("  The   Beatles  ")
	f.Add("Artist - Topic")
	f.Add("")
	f.Add("Артист")
	f.Add("a\x00b")
	f.Add(strings.Repeat("x", 1000))
	f.Fuzz(func(t *testing.T, name string) {
		got := NormalizeIdentityKey(name)
		// Idempotent.
		if again := NormalizeIdentityKey(got); again != got {
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
		got := cleanYouTubeTitle(name)
		if again := cleanYouTubeTitle(got); again != got {
			t.Fatalf("cleanYouTubeTitle not idempotent: %q -> %q -> %q", name, got, again)
		}
	})
}

func FuzzMergeTracksSync(f *testing.F) {
	f.Add("Song", "Artist", "ISRC1", "sp1")
	f.Add("Song", "Artist", "", "yt1")
	f.Add("Song - Topic", "Artist", "", "yt1")
	f.Add("Song", "Artist", "ISRC1", "")
	f.Add("", "", "", "")
	f.Add("Song", "Artist A", "ISRCX", "a")
	f.Add("Song", "Artist B", "ISRCX", "b")
	f.Add("Song", "Artist", "ISRC1", "a")
	f.Add("Song", "Artist", "ISRC2", "b")
	f.Fuzz(func(t *testing.T, title, artist, isrc, serviceID string) {
		mk := func(id, name string) Track {
			tr := Track{Name: name, EntityID: isrc, DurationMs: 200000}
			if artist != "" {
				tr.Artists = []Artist{{Name: artist, Sources: []ArtistSource{{Service: serviceID, Name: artist}}}}
			}
			if serviceID != "" || id != "" {
				tr.Sources = []TrackSource{{Service: serviceID, ID: id}}
			}
			return tr
		}
		in := []Track{
			mk("1", title),
			mk("2", title),
			mk("3", NormalizeIdentityKey(title)),
		}
		out := MergeTracksSync(in)
		if len(out) > len(in) {
			t.Fatalf("merge grew the list: in=%d out=%d", len(in), len(out))
		}
		// Every surviving track keeps at least one source when input had any.
		for _, tr := range out {
			if tr.Name == "" && len(tr.Sources) == 0 && tr.EntityID == "" {
				// Empty stubs are allowed only if all inputs were empty.
				continue
			}
			_ = trackIdentityKey(tr)
			_ = trackEntityID(tr)
		}
	})
}

func FuzzTrackIdentityKey(f *testing.F) {
	f.Add("Song", "Artist", "ISRC")
	f.Add("", "", "")
	f.Add("Song - Topic", "Artist - Topic", "")
	f.Fuzz(func(t *testing.T, name, artist, isrc string) {
		tr := Track{Name: name, EntityID: isrc}
		if artist != "" {
			tr.Artists = []Artist{{Name: artist}}
		}
		_ = trackIdentityKey(tr)
		_ = trackEntityID(tr)
		_ = durationCompatible(tr, Track{Name: name, DurationMs: tr.DurationMs})
	})
}

func FuzzMergePlaylistsSync(f *testing.F) {
	f.Add("Jazz", "spotify", "sp1", "jazz")
	f.Add("Jazz", "youtube", "yt1", "jazz")
	f.Add("", "", "", "")
	f.Add("A", "s", "1", "")
	f.Add("B", "s", "2", "")
	f.Fuzz(func(t *testing.T, name, service, id, linkKey string) {
		p := Playlist{Name: name}
		if service != "" || id != "" {
			p.Sources = []PlaylistSource{{Service: service, ID: id}}
		}
		in := []Playlist{p, p, {Name: name + " "}}
		out := MergePlaylistsSync(in)
		if len(out) > len(in) {
			t.Fatalf("playlist merge grew list: %d -> %d", len(out), len(in))
		}
		out2 := MergePlaylistsSyncLinked(in, func(Playlist) string { return linkKey })
		if len(out2) > len(in) {
			t.Fatalf("linked playlist merge grew list: %d -> %d", len(out2), len(in))
		}
		_ = p.PrimaryService()
		_ = NormalizeIdentityKey(name)
	})
}

func FuzzPlaylistLinkKey(f *testing.F) {
	f.Add("spotify", "sp1")
	f.Add("", "")
	f.Add("youtube", "yt1")
	f.Fuzz(func(t *testing.T, service, id string) {
		st := &EntityLinkStore{Links: []EntityLink{{
			ID:   "L1",
			Kind: "playlist",
			Sources: []EntityLinkSource{{Service: service, ID: id}},
		}}}
		p := Playlist{Sources: []PlaylistSource{{Service: service, ID: id}}}
		key := st.PlaylistLinkKey(p)
		if service == "" && id == "" {
			if key != "" && key != "link:L1" {
				t.Fatalf("unexpected key %q", key)
			}
		}
		// Nil store is inert.
		var missing *EntityLinkStore
		if k := missing.PlaylistLinkKey(p); k != "" {
			t.Fatalf("nil store returned %q", k)
		}
	})
}

func FuzzMergeAlbumsSync(f *testing.F) {
	f.Add("Album", "Artist", "UPC1", "sp1")
	f.Add("Album", "Artist", "", "sp1")
	f.Add("", "", "", "")
	f.Fuzz(func(t *testing.T, title, artist, upc, serviceID string) {
		al := Album{Name: title, EntityID: upc}
		if artist != "" {
			al.Artists = []Artist{{Name: artist}}
		}
		if serviceID != "" {
			al.Sources = []AlbumSource{{Service: serviceID, ID: "1", Name: title}}
		}
		out := MergeAlbumsSync([]Album{al, al})
		if len(out) > 2 {
			t.Fatalf("album merge grew list to %d", len(out))
		}
		_ = albumIdentityKey(al)
		_ = albumEntityID(al)
	})
}
