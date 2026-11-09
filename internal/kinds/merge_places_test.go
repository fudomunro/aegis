package kinds

import "testing"

func TestMergePlaceVisits_WindowAndDurableID(t *testing.T) {
	merged := MergePlaceVisits(samplePlaceVisits())
	// Harbor evening merges across Timeline + OwnTracks (window+place).
	// Lunenburg visit, drive trip, and a different-year harbour visit stay distinct.
	if len(merged) != 4 {
		t.Fatalf("merged = %d, want 4: %+v", len(merged), merged)
	}
	var harbor *PlaceVisit
	for i := range merged {
		if merged[i].EntityID == "tl-harbor-20230704" || placeVisitHasService(merged[i], "google-timeline") {
			if merged[i].Place.Name == "Halifax Harbour" && merged[i].StartAt == "2023-07-04T16:00:00Z" {
				harbor = &merged[i]
			}
		}
	}
	if harbor == nil {
		// Find by place name + date
		for i := range merged {
			if merged[i].Place.Name == "Halifax Harbour" && len(merged[i].Sources) > 1 {
				harbor = &merged[i]
			}
		}
	}
	if harbor == nil {
		t.Fatalf("harbour multi-source visit not found: %+v", merged)
	}
	if len(harbor.Sources) < 2 {
		t.Fatalf("harbour sources = %d, want >= 2", len(harbor.Sources))
	}
	if harbor.Match != PlaceVisitMatchWindow && harbor.Match != PlaceVisitMatchProviderID {
		// Cross-provider merge on window+place.
		if harbor.Match != PlaceVisitMatchWindow {
			t.Fatalf("match = %q", harbor.Match)
		}
	}
}

func placeVisitHasService(v PlaceVisit, id string) bool {
	for _, s := range v.Sources {
		if s.Service == id {
			return true
		}
	}
	return false
}

// samplePlaceVisits mirrors the core sample set: a harbor evening reported by
// two providers (merge pair on window+place), a Lunenburg visit, a drive trip,
// and a name-only near-miss at a different time that must not merge.
func samplePlaceVisits() []PlaceVisit {
	return []PlaceVisit{
		{
			StartAt:    "2023-07-04T16:00:00Z",
			EndAt:      "2023-07-04T21:00:00Z",
			Kind:       PlaceKindVisit,
			Confidence: "high",
			EntityID:   "tl-harbor-20230704",
			Place: Place{
				Name:     "Halifax Harbour",
				Address:  "123 Harbour Way, Halifax, Nova Scotia, Canada",
				Locality: "Halifax",
				Country:  "Canada",
				Lat:      44.6488,
				Lng:      -63.5752,
			},
			Sources: []PlaceVisitSource{
				{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-harbor-20230704", Canonical: true, PointIDs: []string{"tl-harbor-20230704"}},
			},
		},
		{
			// Same harbour evening from OwnTracks — merge pair (window+place).
			StartAt: "2023-07-04T16:05:00Z",
			EndAt:   "2023-07-04T20:55:00Z",
			Kind:    PlaceKindVisit,
			Place: Place{
				Name:     "Halifax Harbour",
				Locality: "Halifax",
				Country:  "Canada",
				Lat:      44.6490,
				Lng:      -63.5750,
			},
			Sources: []PlaceVisitSource{
				{Service: "owntracks", ServiceName: "OwnTracks", ID: "otc-20230704-1605"},
			},
		},
		{
			StartAt:    "2023-07-05T14:00:00Z",
			EndAt:      "2023-07-05T17:00:00Z",
			Kind:       PlaceKindVisit,
			Confidence: "medium",
			EntityID:   "tl-lunenburg-20230705",
			Place: Place{
				Name:     "Lunenburg Beach",
				Address:  "Beach Rd, Lunenburg, Nova Scotia, Canada",
				Locality: "Lunenburg",
				Country:  "Canada",
				Lat:      44.3764,
				Lng:      -64.3188,
			},
			Sources: []PlaceVisitSource{
				{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-lunenburg-20230705", Canonical: true},
			},
		},
		{
			StartAt: "2023-07-04T21:00:00Z",
			EndAt:   "2023-07-05T14:00:00Z",
			Kind:    PlaceKindTrip,
			Mode:    TripModeDrive,
			Place: Place{
				Name:     "Lunenburg Beach",
				Locality: "Lunenburg",
				Country:  "Canada",
			},
			EntityID: "tl-drive-halifax-lunenburg",
			Sources: []PlaceVisitSource{
				{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-drive-halifax-lunenburg"},
			},
		},
		{
			// Name-only near-miss: different window — must not merge with the
			// harbour evening visit.
			StartAt: "2024-03-11T09:00:00Z",
			EndAt:   "2024-03-11T17:00:00Z",
			Kind:    PlaceKindVisit,
			Place: Place{
				Name:     "Halifax Harbour",
				Locality: "Halifax",
				Country:  "Canada",
			},
			EntityID: "tl-harbor-20240311",
			Sources: []PlaceVisitSource{
				{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-harbor-20240311"},
			},
		},
	}
}

func TestMergePlaceVisits_EmptyAndAnon(t *testing.T) {
	if MergePlaceVisits(nil) != nil {
		t.Fatal("nil in")
	}
	v := PlaceVisit{StartAt: "2023-01-01T00:00:00Z", Kind: PlaceKindVisit}
	// No place / no sources → still returns a distinct slot when key exists.
	out := MergePlaceVisits([]PlaceVisit{v})
	if len(out) != 1 {
		t.Fatalf("out = %+v", out)
	}
}
