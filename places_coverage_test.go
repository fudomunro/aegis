package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestTighterPlacesLevel(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{types.AccessCity, types.AccessPrecise, types.AccessCity},
		{types.AccessPrecise, types.AccessCity, types.AccessCity},
		{types.AccessRead, types.AccessCity, types.AccessCity},
		{types.AccessPrecise, types.AccessRead, types.AccessRead},
		{types.AccessNone, types.AccessRead, types.AccessNone},
		{"", types.AccessRead, types.AccessNone},
	}
	for _, c := range cases {
		if got := tighterPlacesLevel(c.a, c.b); got != c.want {
			t.Fatalf("tighter(%q,%q)=%q want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestPlacesAccessLevels(t *testing.T) {
	p := types.Permissions{"places": types.AccessCity, "tracks": types.AccessRead, "events": types.AccessNone}
	got := p.PlacesAccessLevels()
	if got == nil || got["places"] != types.AccessCity {
		t.Fatalf("got = %v", got)
	}
	if _, ok := got["tracks"]; ok {
		t.Fatal("tracks must not appear")
	}
	if (types.Permissions{}).PlacesAccessLevels() != nil {
		t.Fatal("empty map should be nil")
	}
}

func TestNormalizePlaceVisitTime_Variants(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"2023-01-15T10:00:00Z": "2023-01-15T10:00:00Z",
		"2023-01-15":           "2023-01-15T00:00:00Z",
		"1673776800":           "2023-01-15T10:00:00Z", // unix seconds
		"not-a-time":           "not-a-time",
	}
	for in, want := range cases {
		if got := types.NormalizePlaceVisitTime(in); got != want {
			t.Fatalf("normalize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRoundCoordAndCopysign(t *testing.T) {
	if roundCoord(44.64886) != 44.649 {
		t.Fatalf("round = %v", roundCoord(44.64886))
	}
	if copysign(1.5, -1) != -1.5 || copysign(1.5, 1) != 1.5 {
		t.Fatal("copysign")
	}
}

func TestOwnTracksConnector_ListVisitsAndLocations(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/visit":
			json.NewEncoder(w).Encode(map[string]any{
				"_type": "visits",
				"data": []map[string]any{
					{"_type": "visit", "lat": 44.65, "lon": -63.57, "tst": 1700000000, "tst_end": 1700003600, "name": "Harbour", "id": "v1"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := &Config{Services: []Service{{
		ID:       connectors.OwnTracksServiceID,
		Name:     "OwnTracks",
		Action:   "inject",
		Endpoint: ts.URL,
		Headers:  map[string]string{"X-API-Key": "secret"},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	conn := connectors.NewOwnTracksConnector(srv)
	visits, _, err := conn.ListUserPlaceVisits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(visits) != 1 || visits[0].Place.Name != "Harbour" {
		t.Fatalf("visits = %+v", visits)
	}
	if visits[0].Sources[0].Service != connectors.OwnTracksServiceID {
		t.Fatalf("source = %+v", visits[0].Sources[0])
	}
}

func TestOwnTracksConnector_FallbackToLocations(t *testing.T) {
	base := int64(1700000000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/locations":
			json.NewEncoder(w).Encode(map[string]any{
				"_type": "locations",
				"data": []map[string]any{
					{"_type": "location", "lat": 44.6488, "lon": -63.5752, "tst": base, "tid": "ab", "desc": "Harbour"},
					{"_type": "location", "lat": 44.6489, "lon": -63.5751, "tst": base + 600, "tid": "ab"},
					{"_type": "location", "lat": 44.6488, "lon": -63.5752, "tst": base + 1200, "tid": "ab"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := &Config{Services: []Service{{
		ID:       connectors.OwnTracksServiceID,
		Name:     "OwnTracks",
		Action:   "inject",
		Endpoint: ts.URL,
		Headers:  map[string]string{"Authorization": "Bearer tok"},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	conn := connectors.NewOwnTracksConnector(srv)
	visits, _, err := conn.ListUserPlaceVisits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(visits) == 0 {
		t.Fatal("expected clustered visits")
	}
	if visits[0].Place.Name != "Harbour" {
		t.Fatalf("name = %q", visits[0].Place.Name)
	}
}

func TestOwnTracksConnector_Unauthorized(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: connectors.OwnTracksServiceID, Name: "OwnTracks"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	conn := connectors.NewOwnTracksConnector(srv)
	if _, _, err := conn.ListUserPlaceVisits(context.Background()); err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
}

func TestOwnTracksConnector_HandleProxy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v0/locations", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" && r.Header.Get("X-API-Key") == "" {
			// Bearer is set from config token
		}
		w.Write([]byte(`{"_type":"locations","data":[]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cfg := &Config{Services: []Service{{
		ID:       connectors.OwnTracksServiceID,
		Name:     "OwnTracks",
		Action:   "inject",
		Endpoint: ts.URL,
		Headers:  map[string]string{"X-API-Key": "secret"},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	conn := connectors.NewOwnTracksConnector(srv)
	if caps := conn.Capabilities(); len(caps.DataTypes) == 0 {
		t.Fatal("capabilities empty")
	}
	if s := conn.SystemInstructions("https://aegis.example.com"); s == "" {
		t.Fatal("instructions empty")
	}
	req := httptest.NewRequest("GET", "/api/v0/locations", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/api/v0/locations")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestTimelineAdmin_StatusAndImport(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenTimelineStore(filepath.Join(dir, "timeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := &Config{Services: []Service{{ID: timelineServiceID, Name: "Google Timeline", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.timeline = store

	req := httptest.NewRequest("GET", "/v1/admin/timeline/status", nil)
	w := httptest.NewRecorder()
	srv.HandleAdminTimeline(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	path := filepath.Join(dir, "2023-01.json")
	if err := os.WriteFile(path, []byte(sampleTimelineJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(TimelineImportRequest{Path: path})
	req2 := httptest.NewRequest("POST", "/v1/admin/timeline/import", bytes.NewReader(body))
	w2 := httptest.NewRecorder()
	srv.HandleAdminTimeline(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", w2.Code, w2.Body.String())
	}
	var res TimelineImportResult
	if err := json.Unmarshal(w2.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Stored != 3 {
		t.Fatalf("stored = %d", res.Stored)
	}

	req3 := httptest.NewRequest("PUT", "/v1/admin/timeline/status", nil)
	w3 := httptest.NewRecorder()
	srv.HandleAdminTimeline(w3, req3)
	if w3.Code != http.StatusMethodNotAllowed {
		t.Fatalf("put status = %d", w3.Code)
	}
	req4 := httptest.NewRequest("GET", "/v1/admin/timeline/nope", nil)
	w4 := httptest.NewRecorder()
	srv.HandleAdminTimeline(w4, req4)
	if w4.Code != http.StatusNotFound {
		t.Fatalf("nope = %d", w4.Code)
	}
	req5 := httptest.NewRequest("POST", "/v1/admin/timeline/import", bytes.NewReader([]byte(`{"path":""}`)))
	w5 := httptest.NewRecorder()
	srv.HandleAdminTimeline(w5, req5)
	if w5.Code != http.StatusBadRequest {
		t.Fatalf("empty path = %d", w5.Code)
	}
}

func TestTimelineConnector_CapabilitiesAndDisabled(t *testing.T) {
	conn := NewTimelineConnector(nil)
	if _, _, err := conn.ListUserPlaceVisits(context.Background()); err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if caps := conn.Capabilities(); len(caps.DataTypes) == 0 {
		t.Fatal("caps")
	}
	if s := conn.SystemInstructions("https://x"); s == "" {
		t.Fatal("instructions")
	}
	w := httptest.NewRecorder()
	conn.HandleProxy(w, httptest.NewRequest("GET", "/v1/library/places", nil), "/places")
	if w.Code != http.StatusNotFound {
		t.Fatalf("proxy = %d", w.Code)
	}
}

func TestMergePlaceVisits_EmptyAndAnon(t *testing.T) {
	if MergePlaceVisits(nil) != nil {
		t.Fatal("nil in")
	}
	v := types.PlaceVisit{StartAt: "2023-01-01T00:00:00Z", Kind: types.PlaceKindVisit}
	// No place / no sources → still returns a distinct slot when key exists.
	out := MergePlaceVisits([]types.PlaceVisit{v})
	if len(out) != 1 {
		t.Fatalf("out = %+v", out)
	}
}

func TestPhotosTakenDuring_Edges(t *testing.T) {
	v := types.PlaceVisit{StartAt: "2023-07-04T16:00:00Z"} // no end
	if PhotosTakenDuring(nil, v) != nil && len(PhotosTakenDuring(nil, v)) != 0 {
		t.Fatal("nil photos")
	}
	if len(PhotosTakenDuring([]types.Photo{{TakenAt: "2023-07-04T17:00:00Z"}}, v)) != 1 {
		t.Fatal("open-ended window")
	}
	// Missing start → no join
	if len(PhotosTakenDuring([]types.Photo{{TakenAt: "2023-07-04T17:00:00Z"}}, types.PlaceVisit{})) != 0 {
		t.Fatal("missing start")
	}
}

func TestPlaceVisitFilters_CacheKey(t *testing.T) {
	f := PlaceVisitFilters{After: "2023-01-01T00:00:00Z", Before: "2023-02-01T00:00:00Z", Locality: "Halifax", Kind: types.PlaceKindVisit}
	if f.cacheKeySuffix() == "" {
		t.Fatal("expected cache key")
	}
	if (PlaceVisitFilters{}).cacheKeySuffix() != "" {
		t.Fatal("empty filters")
	}
}
