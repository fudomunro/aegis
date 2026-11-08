package core

import (
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

func TestCanonicalServiceIDs_AndStampTrackContact(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists", "tracks"}},
		{ID: "google", Name: "Google", Action: "inject", CanonicalFor: []string{"contacts"}},
		{ID: "tidal", Name: "Tidal", Action: "block", CanonicalFor: []string{"tracks"}},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

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
	markTracks, _ := kinds.Stamps(srv, "tracks", forEachTrackSource)
	markTracks(items, "spotify")
	if !items[0].Sources[0].Canonical || items[0].Sources[1].Canonical || items[1].Sources[0].Canonical {
		t.Fatalf("track stamp = %+v", items)
	}
	if items[0].CanonicalService() != "spotify" {
		t.Fatalf("CanonicalService = %q", items[0].CanonicalService())
	}

	contactsIn := []types.Contact{
		{Name: "Ada", Sources: []types.ContactSource{{Service: "google", ID: "c1"}, {Service: "immich", ID: "c2"}}},
	}
	markContacts, _ := kinds.Stamps(srv, "contacts", forEachContactSource)
	markContacts(contactsIn, "google")
	if !contactsIn[0].Sources[0].Canonical || contactsIn[0].Sources[1].Canonical {
		t.Fatalf("contact stamp = %+v", contactsIn[0].Sources)
	}
	if contactsIn[0].CanonicalService() != "google" {
		t.Fatalf("CanonicalService = %q", contactsIn[0].CanonicalService())
	}

	// No-op when service does not appear or is not canonical for the kind.
	markTracks(items, "amazon-music")
	markContacts(contactsIn, "spotify")
}

func TestStampTransactionCanonical_OnlyWhenConfigured(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "ynab", Name: "YNAB", Action: "inject", CanonicalFor: []string{"transactions"}},
		{ID: "quicken", Name: "Quicken", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	items := []types.Transaction{
		{
			Amount:      "-12.00",
			Description: "Coffee",
			Sources: []types.TransactionSource{
				{Service: "ynab", ID: "y1"},
				{Service: "quicken", ID: "q1"},
			},
		},
	}
	mark, _ := kinds.Stamps(srv, "transactions", forEachTransactionSource)
	mark(items, "ynab")
	mark(items, "quicken")
	mark(items, "missing")

	if !items[0].Sources[0].Canonical {
		t.Fatal("ynab source should be canonical")
	}
	if items[0].Sources[1].Canonical {
		t.Fatal("quicken source must stay non-canonical")
	}
}

func TestStampPlaceVisitCanonical_OnlyWhenConfigured(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-timeline", Name: "Timeline", Action: "inject", CanonicalFor: []string{"places"}},
		{ID: "owntracks", Name: "OwnTracks", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	items := []types.PlaceVisit{
		{
			StartAt: "2026-01-01T10:00:00Z",
			EndAt:   "2026-01-01T11:00:00Z",
			Kind:    types.PlaceKindVisit,
			Place:   types.Place{Name: "Home"},
			Sources: []types.PlaceVisitSource{
				{Service: "google-timeline", ID: "p1"},
				{Service: "owntracks", ID: "p2"},
			},
		},
	}
	mark, _ := kinds.Stamps(srv, "places", forEachPlaceVisitSource)
	mark(items, "google-timeline")
	mark(items, "owntracks")

	if !items[0].Sources[0].Canonical {
		t.Fatal("timeline source should be canonical")
	}
	if items[0].Sources[1].Canonical {
		t.Fatal("owntracks source must stay non-canonical")
	}
}
