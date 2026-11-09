package kinds

import (
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
)

func testPlace(name, locality string, lat, lng float64, start string, trip bool) PlaceVisit {
	kind := PlaceKindVisit
	if trip {
		kind = PlaceKindTrip
	}
	return PlaceVisit{
		Kind:    kind,
		StartAt: start,
		EndAt:   start,
		Place: Place{
			Name:     name,
			Address:  name + " Street",
			Locality: locality,
			Country:  "Canada",
			Lat:      lat,
			Lng:      lng,
		},
		Sources: []PlaceVisitSource{{
			Service:  "google-timeline",
			ID:       name,
			PointIDs: []string{"p1"},
		}},
	}
}

func TestFilterPlaceVisits(t *testing.T) {
	visits := MergePlaceVisits([]PlaceVisit{
		testPlace("Home", "Halifax", 44.6, -63.6, "2024-06-01T10:00:00Z", false),
		testPlace("Cottage", "Lunenburg", 44.3, -64.3, "2024-07-01T10:00:00Z", false),
		testPlace("Drive", "Lunenburg", 44.3, -64.3, "2024-08-01T10:00:00Z", true),
	})
	if got := filterPlaceVisits(visits, PlaceVisitFilters{Locality: "Lunenburg"}); len(got) != 2 {
		t.Fatalf("locality filter = %d, want 2", len(got))
	}
	if got := filterPlaceVisits(visits, PlaceVisitFilters{After: "2024-07-15T00:00:00Z"}); len(got) != 1 {
		t.Fatalf("after filter = %d, want 1", len(got))
	}
	if got := filterPlaceVisits(visits, PlaceVisitFilters{Kind: PlaceKindTrip}); len(got) != 1 {
		t.Fatalf("kind=trip filter = %d, want 1", len(got))
	}
}

func TestRedactPlaceVisitCity(t *testing.T) {
	v := testPlace("Home", "Halifax", 44.6, -63.6, "2024-06-01T10:00:00Z", false)
	city := redactPlaceVisitCity(v)
	if city.Place.Name != "" || city.Place.Address != "" {
		t.Fatalf("city leaked name/address: %+v", city.Place)
	}
	if city.Place.Lat != 0 || city.Place.Lng != 0 {
		t.Fatalf("city leaked coordinates: %+v", city.Place)
	}
	if city.Place.Locality != "Halifax" || city.Place.Country != "Canada" {
		t.Fatalf("city should keep locality/country: %+v", city.Place)
	}
	if city.StartAt == "" {
		t.Fatal("city must keep the time window")
	}
	for _, s := range city.Sources {
		if len(s.PointIDs) != 0 || s.ID != "" {
			t.Fatalf("city leaked source identity: %+v", s)
		}
	}
}

func TestRedactPlaceVisitRead_OmitsCoordinates(t *testing.T) {
	v := testPlace("Home", "Halifax", 44.6, -63.6, "2024-06-01T10:00:00Z", false)
	r := redactPlaceVisitRead(v)
	if r.Place.Name == "" {
		t.Fatal("read should keep place name")
	}
	if r.Place.Lat != 0 || r.Place.Lng != 0 {
		t.Fatalf("read leaked coordinates: %+v", r.Place)
	}
}

func TestTighterPlacesLevel(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{policy.AccessCity, policy.AccessPrecise, policy.AccessCity},
		{policy.AccessPrecise, policy.AccessCity, policy.AccessCity},
		{policy.AccessRead, policy.AccessCity, policy.AccessCity},
		{policy.AccessPrecise, policy.AccessRead, policy.AccessRead},
		{policy.AccessNone, policy.AccessRead, policy.AccessNone},
		{"", policy.AccessRead, policy.AccessNone},
	}
	for _, c := range cases {
		if got := tighterPlacesLevel(c.a, c.b); got != c.want {
			t.Fatalf("tighter(%q,%q)=%q want %q", c.a, c.b, got, c.want)
		}
	}
}
