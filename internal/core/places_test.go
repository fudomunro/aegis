package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"

	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

type fakePlaceProvider struct {
	visits    []kinds.PlaceVisit
	truncated bool
	err       error
}

func (f *fakePlaceProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakePlaceProvider) SystemInstructions(string) string { return "" }
func (f *fakePlaceProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}
func (f *fakePlaceProvider) ListUserPlaceVisits(context.Context) ([]kinds.PlaceVisit, bool, error) {
	return f.visits, f.truncated, f.err
}

func samplePlaces() []kinds.PlaceVisit {
	return GenerateSampleLibrary().Places
}

func TestNormalizePlaceVisitKindAndMode(t *testing.T) {
	if got := kinds.NormalizePlaceVisitKind("placeVisit"); got != kinds.PlaceKindVisit {
		t.Fatalf("kind = %q", got)
	}
	if got := kinds.NormalizePlaceVisitKind("activitySegment"); got != kinds.PlaceKindTrip {
		t.Fatalf("kind = %q", got)
	}
	if got := kinds.NormalizeTripMode("IN_PASSENGER_VEHICLE"); got != kinds.TripModeDrive {
		t.Fatalf("mode = %q", got)
	}
	if got := kinds.NormalizeTripMode("FLYING"); got != kinds.TripModeFlight {
		t.Fatalf("mode = %q", got)
	}
}

func TestPlacesLevelForResponse(t *testing.T) {
	cases := map[string]string{
		policy.AccessNone:    policy.AccessNone,
		policy.AccessCity:    policy.AccessCity,
		policy.AccessRead:    policy.AccessRead,
		policy.AccessPrecise: policy.AccessPrecise,
		policy.AccessWrite:   policy.AccessPrecise,
		policy.AccessFull:    policy.AccessPrecise,
	}
	for in, want := range cases {
		if got := policy.PlacesLevelForResponse(in); got != want {
			t.Fatalf("policy.PlacesLevelForResponse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPermissions_PlacesDefaultNone(t *testing.T) {
	var nilPerm policy.Permissions
	if got := nilPerm.For("places"); got != policy.AccessNone {
		t.Fatalf("nil map places = %q, want none", got)
	}
	p := policy.Permissions{"default": policy.AccessFull, "tracks": policy.AccessRead}
	if got := p.For("places"); got != policy.AccessNone {
		t.Fatalf("default key must not enable places: %q", got)
	}
	if p.AllowsRead("places") {
		t.Fatal("places must not be readable by default")
	}
	enabled := policy.Permissions{"places": policy.AccessCity}
	if got := enabled.For("places"); got != policy.AccessCity {
		t.Fatalf("explicit places = %q, want city", got)
	}
	if !enabled.AllowsRead("places") {
		t.Fatal("city should allow reads")
	}
}

func TestValidatePermissions_CityPreciseOnlyForPlaces(t *testing.T) {
	if err := policy.ValidatePermissions(policy.Permissions{"places": policy.AccessCity}); err != nil {
		t.Fatalf("places city: %v", err)
	}
	if err := policy.ValidatePermissions(policy.Permissions{"places": policy.AccessPrecise}); err != nil {
		t.Fatalf("places precise: %v", err)
	}
	if err := policy.ValidatePermissions(policy.Permissions{"events": policy.AccessCity}); err == nil {
		t.Fatal("city on events should be rejected")
	}
	if err := policy.ValidatePermissions(policy.Permissions{"places": policy.AccessFreeBusy}); err == nil {
		t.Fatal("freebusy on places should be rejected")
	}
	if err := policy.ValidatePermissions(policy.Permissions{"places": policy.AccessSummary}); err == nil {
		t.Fatal("summary on places should be rejected")
	}
}

// TestPhotosPlacesJoinDemo is the issue #142 done-when: photos taken during a
// visit work on sample data.
func TestPhotosPlacesJoinDemo(t *testing.T) {
	lib := GenerateSampleLibrary()
	merged := kinds.MergePlaceVisits(lib.Places)
	var harbor, lunenburg *kinds.PlaceVisit
	for i := range merged {
		switch {
		case merged[i].Place.Name == "Halifax Harbour" && merged[i].StartAt == "2023-07-04T16:00:00Z":
			harbor = &merged[i]
		case merged[i].Place.Name == "Lunenburg Beach" && merged[i].Kind == kinds.PlaceKindVisit:
			lunenburg = &merged[i]
		}
	}
	if harbor == nil || lunenburg == nil {
		t.Fatalf("sample visits missing: harbor=%v lunenburg=%v", harbor, lunenburg)
	}
	harborPhotos := PhotosTakenDuring(lib.Photos, *harbor)
	// Fireworks photo (gp-1) and its Immich twin fall inside the harbour window.
	// Beach photo is outside (next day). Office photo is a year later.
	if len(harborPhotos) != 2 {
		t.Fatalf("harbour photos = %d, want 2: %+v", len(harborPhotos), harborPhotos)
	}
	lunenburgPhotos := PhotosTakenDuring(lib.Photos, *lunenburg)
	if len(lunenburgPhotos) != 1 {
		t.Fatalf("lunenburg photos = %d, want 1", len(lunenburgPhotos))
	}
	if lunenburgPhotos[0].Filename != "IMG_0002.jpg" {
		t.Fatalf("joined photo = %q", lunenburgPhotos[0].Filename)
	}
}

func TestHandleLibraryPlaces_Aggregate(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: connectors.TimelineServiceID, Name: "Google Timeline", Action: "inject",
				GrantLimits: policy.Permissions{"places": policy.AccessPrecise}},
			{ID: connectors.OwnTracksServiceID, Name: "OwnTracks", Action: "inject",
				GrantLimits: policy.Permissions{"places": policy.AccessRead}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		connectors.TimelineServiceID:  &fakePlaceProvider{visits: samplePlaces()},
		connectors.OwnTracksServiceID: &fakePlaceProvider{visits: samplePlaces()},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("places"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list kinds.PlaceVisitList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total == 0 {
		t.Fatal("expected visits")
	}
}

func TestHandleLibraryPlaces_DeniedByDefault(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: connectors.TimelineServiceID, Name: "Google Timeline", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		connectors.TimelineServiceID: &fakePlaceProvider{visits: samplePlaces()},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("places"))(w, req)
	// Places is opt-in: with no permission the agent is denied (not empty 200).
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", w.Code, w.Body.String())
	}
}

func TestHandleLibraryPlaces_CityRedaction(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: connectors.TimelineServiceID, Name: "Google Timeline", Action: "inject",
				GrantLimits: policy.Permissions{"places": policy.AccessCity}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		connectors.TimelineServiceID: &fakePlaceProvider{visits: samplePlaces()[:1]},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("places"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list kinds.PlaceVisitList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total == 0 {
		t.Fatal("expected visits")
	}
	for _, v := range list.Visits {
		if v.Place.Name != "" || v.Place.Address != "" || v.Place.Lat != 0 {
			t.Fatalf("city redaction leaked: %+v", v.Place)
		}
		if v.Place.Locality == "" && v.StartAt == "" {
			t.Fatal("city must keep locality and window")
		}
	}
}

func TestHandleLibraryPlaces_ReadOmitsCoords(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: connectors.TimelineServiceID, Name: "Google Timeline", Action: "inject",
				GrantLimits: policy.Permissions{"places": policy.AccessRead}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		connectors.TimelineServiceID: &fakePlaceProvider{visits: samplePlaces()[:1]},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("places"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list kinds.PlaceVisitList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total == 0 {
		t.Fatal("expected visits")
	}
	for _, v := range list.Visits {
		if v.Place.Name == "" {
			t.Fatal("read should keep names")
		}
		if v.Place.Lat != 0 || v.Place.Lng != 0 {
			t.Fatalf("read leaked coords: %+v", v.Place)
		}
	}
}

func TestHandleLibraryPlaces_PreciseKeepsCoords(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: connectors.TimelineServiceID, Name: "Google Timeline", Action: "inject",
				GrantLimits: policy.Permissions{"places": policy.AccessPrecise}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		connectors.TimelineServiceID: &fakePlaceProvider{visits: samplePlaces()[:1]},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("places"))(w, req)
	var list kinds.PlaceVisitList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total == 0 {
		t.Fatal("expected visits")
	}
	found := false
	for _, v := range list.Visits {
		if v.Place.Lat != 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("precise should keep coordinates")
	}
}

func TestLibraryKindPlaces_Registered(t *testing.T) {
	k := LibraryKindByID("places")
	if k == nil {
		t.Fatal("places kind missing")
	}
	if k.PermType != "places" {
		t.Fatalf("PermType = %q", k.PermType)
	}
	if !k.Mentions("where was I last summer?") {
		t.Fatal("keywords should catch location questions")
	}
}
