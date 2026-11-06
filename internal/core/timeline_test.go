package core

import (
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

func TestOwnTracksClusterVisits(t *testing.T) {
	// Three samples at the same spot (visit) then two far away (new visit + trip).
	base := int64(1700000000)
	locs := []connectors.OwnTracksLocation{
		{Tst: base, Lat: 44.6488, Lon: -63.5752, Desc: "Harbour", TID: "ab"},
		{Tst: base + 600, Lat: 44.6489, Lon: -63.5751, TID: "ab"},
		{Tst: base + 1200, Lat: 44.6488, Lon: -63.5752, TID: "ab"},
		// gap + travel
		{Tst: base + 7200, Lat: 44.3764, Lon: -64.3188, Desc: "Beach", TID: "ab"},
		{Tst: base + 7800, Lat: 44.3765, Lon: -64.3187, TID: "ab"},
		{Tst: base + 8400, Lat: 44.3764, Lon: -64.3188, TID: "ab"},
	}
	visits := connectors.ClusterOwnTracksVisits(locs)
	if len(visits) < 2 {
		t.Fatalf("clusters = %d, want >= 2: %+v", len(visits), visits)
	}
	if visits[0].Kind != types.PlaceKindVisit {
		t.Fatalf("first = %+v", visits[0])
	}
	if visits[0].Place.Name != "Harbour" {
		t.Fatalf("name = %q", visits[0].Place.Name)
	}
}
