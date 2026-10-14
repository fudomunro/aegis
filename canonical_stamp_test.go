package main

import (
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestCanonicalServiceIDs_AndStampTrackContact(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists", "tracks"}},
		{ID: "google", Name: "Google", Action: "inject", CanonicalFor: []string{"contacts"}},
		{ID: "tidal", Name: "Tidal", Action: "block", CanonicalFor: []string{"tracks"}},
	}}

	tracks := cfg.canonicalServiceIDs("tracks")
	if !tracks["spotify"] || tracks["tidal"] || len(tracks) != 1 {
		t.Fatalf("track canonical ids = %v", tracks)
	}
	contacts := cfg.canonicalServiceIDs("contacts")
	if !contacts["google"] || len(contacts) != 1 {
		t.Fatalf("contact canonical ids = %v", contacts)
	}
	if photos := cfg.canonicalServiceIDs("photos"); len(photos) != 0 {
		t.Fatalf("photos canonical ids = %v", photos)
	}

	items := []types.Track{
		{Name: "A", Sources: []types.TrackSource{{Service: "spotify", ID: "1"}, {Service: "youtube-music", ID: "2"}}},
		{Name: "B", Sources: []types.TrackSource{{Service: "youtube-music", ID: "3"}}},
	}
	markTrackSourcesCanonical(items, "spotify")
	if !items[0].Sources[0].Canonical || items[0].Sources[1].Canonical || items[1].Sources[0].Canonical {
		t.Fatalf("track stamp = %+v", items)
	}
	if items[0].CanonicalService() != "spotify" {
		t.Fatalf("CanonicalService = %q", items[0].CanonicalService())
	}

	contactsIn := []types.Contact{
		{Name: "Ada", Sources: []types.ContactSource{{Service: "google", ID: "c1"}, {Service: "immich", ID: "c2"}}},
	}
	markContactSourcesCanonical(contactsIn, "google")
	if !contactsIn[0].Sources[0].Canonical || contactsIn[0].Sources[1].Canonical {
		t.Fatalf("contact stamp = %+v", contactsIn[0].Sources)
	}
	if contactsIn[0].CanonicalService() != "google" {
		t.Fatalf("CanonicalService = %q", contactsIn[0].CanonicalService())
	}

	// No-op when service does not appear.
	markTrackSourcesCanonical(items, "amazon-music")
	markContactSourcesCanonical(contactsIn, "spotify")
}
