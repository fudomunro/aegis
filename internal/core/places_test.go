package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

type fakePlaceProvider struct {
	visits    []types.PlaceVisit
	truncated bool
	err       error
}

func (f *fakePlaceProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakePlaceProvider) SystemInstructions(string) string { return "" }
func (f *fakePlaceProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}
func (f *fakePlaceProvider) ListUserPlaceVisits(context.Context) ([]types.PlaceVisit, bool, error) {
	return f.visits, f.truncated, f.err
}

func samplePlaces() []types.PlaceVisit {
	return GenerateSampleLibrary().Places
}

func TestNormalizePlaceVisitKindAndMode(t *testing.T) {
	if got := types.NormalizePlaceVisitKind("placeVisit"); got != types.PlaceKindVisit {
		t.Fatalf("kind = %q", got)
	}
	if got := types.NormalizePlaceVisitKind("activitySegment"); got != types.PlaceKindTrip {
		t.Fatalf("kind = %q", got)
	}
	if got := types.NormalizeTripMode("IN_PASSENGER_VEHICLE"); got != types.TripModeDrive {
		t.Fatalf("mode = %q", got)
	}
	if got := types.NormalizeTripMode("FLYING"); got != types.TripModeFlight {
		t.Fatalf("mode = %q", got)
	}
}

func TestMergePlaceVisits_WindowAndDurableID(t *testing.T) {
	merged := MergePlaceVisits(samplePlaces())
	// Harbor evening merges across Timeline + OwnTracks (window+place).
	// Lunenburg visit, drive trip, and a different-year harbour visit stay distinct.
	if len(merged) != 4 {
		t.Fatalf("merged = %d, want 4: %+v", len(merged), merged)
	}
	var harbor *types.PlaceVisit
	for i := range merged {
		if merged[i].EntityID == "tl-harbor-20230704" || containsService(merged[i], timelineServiceID) {
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

func containsService(v types.PlaceVisit, id string) bool {
	for _, s := range v.Sources {
		if s.Service == id {
			return true
		}
	}
	return false
}

func TestPlaceVisitFilters(t *testing.T) {
	visits := MergePlaceVisits(samplePlaces())
	f := PlaceVisitFilters{Locality: "Lunenburg"}
	got := filterPlaceVisits(visits, f)
	if len(got) != 2 { // visit + drive trip to Lunenburg
		t.Fatalf("locality filter = %d, want 2", len(got))
	}
	f2 := PlaceVisitFilters{After: "2024-01-01T00:00:00Z"}
	got2 := filterPlaceVisits(visits, f2)
	if len(got2) != 1 {
		t.Fatalf("after filter = %d, want 1", len(got2))
	}
	f3 := PlaceVisitFilters{Kind: types.PlaceKindTrip}
	got3 := filterPlaceVisits(visits, f3)
	if len(got3) != 1 {
		t.Fatalf("kind=trip filter = %d, want 1", len(got3))
	}
}

func TestRedactPlaceVisitCity(t *testing.T) {
	v := samplePlaces()[0]
	city := RedactPlaceVisitCity(v)
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
	v := samplePlaces()[0]
	r := RedactPlaceVisitRead(v)
	if r.Place.Name == "" {
		t.Fatal("read should keep place name")
	}
	if r.Place.Lat != 0 || r.Place.Lng != 0 {
		t.Fatalf("read leaked coordinates: %+v", r.Place)
	}
}

func TestPlacesLevelForResponse(t *testing.T) {
	cases := map[string]string{
		types.AccessNone:    types.AccessNone,
		types.AccessCity:    types.AccessCity,
		types.AccessRead:    types.AccessRead,
		types.AccessPrecise: types.AccessPrecise,
		types.AccessWrite:   types.AccessPrecise,
		types.AccessFull:    types.AccessPrecise,
	}
	for in, want := range cases {
		if got := types.PlacesLevelForResponse(in); got != want {
			t.Fatalf("types.PlacesLevelForResponse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPermissions_PlacesDefaultNone(t *testing.T) {
	var nilPerm types.Permissions
	if got := nilPerm.For("places"); got != types.AccessNone {
		t.Fatalf("nil map places = %q, want none", got)
	}
	p := types.Permissions{"default": types.AccessFull, "tracks": types.AccessRead}
	if got := p.For("places"); got != types.AccessNone {
		t.Fatalf("default key must not enable places: %q", got)
	}
	if p.AllowsRead("places") {
		t.Fatal("places must not be readable by default")
	}
	enabled := types.Permissions{"places": types.AccessCity}
	if got := enabled.For("places"); got != types.AccessCity {
		t.Fatalf("explicit places = %q, want city", got)
	}
	if !enabled.AllowsRead("places") {
		t.Fatal("city should allow reads")
	}
}

func TestValidatePermissions_CityPreciseOnlyForPlaces(t *testing.T) {
	if err := types.ValidatePermissions(types.Permissions{"places": types.AccessCity}); err != nil {
		t.Fatalf("places city: %v", err)
	}
	if err := types.ValidatePermissions(types.Permissions{"places": types.AccessPrecise}); err != nil {
		t.Fatalf("places precise: %v", err)
	}
	if err := types.ValidatePermissions(types.Permissions{"events": types.AccessCity}); err == nil {
		t.Fatal("city on events should be rejected")
	}
	if err := types.ValidatePermissions(types.Permissions{"places": types.AccessFreeBusy}); err == nil {
		t.Fatal("freebusy on places should be rejected")
	}
	if err := types.ValidatePermissions(types.Permissions{"places": types.AccessSummary}); err == nil {
		t.Fatal("summary on places should be rejected")
	}
}

func TestIsCityAndPreciseAccess(t *testing.T) {
	if !types.IsCityAccess("city") || types.IsCityAccess("read") {
		t.Fatal("types.IsCityAccess")
	}
	if !types.IsPreciseAccess("precise") || types.IsPreciseAccess("city") {
		t.Fatal("types.IsPreciseAccess")
	}
}

// TestPhotosPlacesJoinDemo is the issue #142 done-when: photos taken during a
// visit work on sample data.
func TestPhotosPlacesJoinDemo(t *testing.T) {
	lib := GenerateSampleLibrary()
	merged := MergePlaceVisits(lib.Places)
	var harbor, lunenburg *types.PlaceVisit
	for i := range merged {
		switch {
		case merged[i].Place.Name == "Halifax Harbour" && merged[i].StartAt == "2023-07-04T16:00:00Z":
			harbor = &merged[i]
		case merged[i].Place.Name == "Lunenburg Beach" && merged[i].Kind == types.PlaceKindVisit:
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
			{ID: timelineServiceID, Name: "Google Timeline", Action: "inject",
				GrantLimits: types.Permissions{"places": types.AccessPrecise}},
			{ID: connectors.OwnTracksServiceID, Name: "OwnTracks", Action: "inject",
				GrantLimits: types.Permissions{"places": types.AccessRead}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		timelineServiceID:             &fakePlaceProvider{visits: samplePlaces()},
		connectors.OwnTracksServiceID: &fakePlaceProvider{visits: samplePlaces()},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaceVisits)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list types.PlaceVisitList
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
			{ID: timelineServiceID, Name: "Google Timeline", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		timelineServiceID: &fakePlaceProvider{visits: samplePlaces()},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaceVisits)(w, req)
	// Places is opt-in: with no permission the agent is denied (not empty 200).
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", w.Code, w.Body.String())
	}
}

func TestHandleLibraryPlaces_CityRedaction(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: timelineServiceID, Name: "Google Timeline", Action: "inject",
				GrantLimits: types.Permissions{"places": types.AccessCity}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		timelineServiceID: &fakePlaceProvider{visits: samplePlaces()[:1]},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaceVisits)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list types.PlaceVisitList
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
			{ID: timelineServiceID, Name: "Google Timeline", Action: "inject",
				GrantLimits: types.Permissions{"places": types.AccessRead}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		timelineServiceID: &fakePlaceProvider{visits: samplePlaces()[:1]},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaceVisits)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list types.PlaceVisitList
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
			{ID: timelineServiceID, Name: "Google Timeline", Action: "inject",
				GrantLimits: types.Permissions{"places": types.AccessPrecise}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		timelineServiceID: &fakePlaceProvider{visits: samplePlaces()[:1]},
	}
	req := httptest.NewRequest("GET", "/v1/library/places", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaceVisits)(w, req)
	var list types.PlaceVisitList
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

func TestDataTypeForPath_Places(t *testing.T) {
	cases := map[string]string{
		"/me/places":        "places",
		"/visits":           "places",
		"/api/v0/locations": "places",
		"/api/v0/visit":     "places",
		"/api/v1/points":    "places",
		"/timeline/2023-01": "places",
	}
	for p, want := range cases {
		if got := types.DataTypeForPath(p); got != want {
			t.Fatalf("types.DataTypeForPath(%q) = %q, want %q", p, got, want)
		}
	}
}
