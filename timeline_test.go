package main

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

const sampleTimelineJSON = `{
  "timelineObjects": [
    {
      "placeVisit": {
        "location": {
          "latitudeE7": 374220000,
          "longitudeE7": -1220840000,
          "placeId": "ChIJ-googleplex",
          "address": "Googleplex, 1600 Amphitheatre Pkwy, Mountain View, CA, USA",
          "name": "Googleplex"
        },
        "duration": {
          "startTimestamp": "2023-01-15T10:00:00Z",
          "endTimestamp": "2023-01-15T18:00:00Z"
        },
        "placeConfidence": "HIGH",
        "visitConfidence": 90
      }
    },
    {
      "activitySegment": {
        "startLocation": {
          "latitudeE7": 374220000,
          "longitudeE7": -1220840000
        },
        "endLocation": {
          "latitudeE7": 377749000,
          "longitudeE7": -1224194000,
          "placeId": "ChIJ-sf",
          "address": "San Francisco, CA, USA",
          "name": "San Francisco"
        },
        "duration": {
          "startTimestamp": "2023-01-15T18:00:00Z",
          "endTimestamp": "2023-01-15T19:30:00Z"
        },
        "activityType": "IN_PASSENGER_VEHICLE"
      }
    },
    {
      "placeVisit": {
        "location": {
          "latitudeE7": 377749000,
          "longitudeE7": -1224194000,
          "placeId": "ChIJ-sf",
          "address": "San Francisco, CA, USA",
          "name": "San Francisco"
        },
        "duration": {
          "startTimestamp": "2023-01-15T19:30:00Z",
          "endTimestamp": "2023-01-16T09:00:00Z"
        },
        "placeConfidence": "MEDIUM"
      }
    }
  ]
}`

func TestParseTimelineJSON(t *testing.T) {
	visits, err := ParseTimelineJSON("2023-01.json", []byte(sampleTimelineJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(visits) != 3 {
		t.Fatalf("got %d objects, want 3", len(visits))
	}
	if visits[0].Kind != types.PlaceKindVisit || visits[0].Place.Name != "Googleplex" {
		t.Fatalf("visit[0] = %+v", visits[0])
	}
	if visits[0].Place.Lat == 0 {
		t.Fatal("expected coordinates on precise model")
	}
	if visits[0].Place.Locality == "" {
		t.Fatalf("expected locality from address: %+v", visits[0].Place)
	}
	if visits[1].Kind != types.PlaceKindTrip || visits[1].Mode != types.TripModeDrive {
		t.Fatalf("trip[1] = %+v", visits[1])
	}
	if visits[2].Place.Name != "San Francisco" {
		t.Fatalf("visit[2] = %+v", visits[2])
	}
}

func TestImportTimelinePath_FileAndStore(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenTimelineStore(filepath.Join(dir, "timeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(dir, "2023-01.json")
	if err := os.WriteFile(path, []byte(sampleTimelineJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportTimelinePath(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored != 3 {
		t.Fatalf("stored = %d, want 3 (scanned=%d errors=%d)", res.Stored, res.Scanned, res.Errors)
	}
	// Re-import skips.
	res2, err := ImportTimelinePath(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Skipped != 3 || res2.Stored != 0 {
		t.Fatalf("reimport = %+v", res2)
	}
	n, err := store.Count()
	if err != nil || n != 3 {
		t.Fatalf("count = %d err=%v", n, err)
	}
	conn := NewTimelineConnector(store)
	visits, truncated, err := conn.ListUserPlaceVisits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("small store should not truncate")
	}
	if len(visits) != 3 {
		t.Fatalf("list = %d", len(visits))
	}
}

func TestImportTimelinePath_Directory(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenTimelineStore(filepath.Join(dir, "timeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Takeout-like layout.
	monthDir := filepath.Join(dir, "Semantic Location History", "2023")
	if err := os.MkdirAll(monthDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(monthDir, "2023-01.json"), []byte(sampleTimelineJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportTimelinePath(store, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored != 3 {
		t.Fatalf("dir import = %+v", res)
	}
}

func TestImportTimelineZip(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenTimelineStore(filepath.Join(dir, "timeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Build a tiny zip via the zip writer.
	zipPath := filepath.Join(dir, "takeout.zip")
	if err := writeSampleTimelineZip(zipPath, "Takeout/Location History/Semantic Location History/2023/2023-01.json", sampleTimelineJSON); err != nil {
		t.Fatal(err)
	}
	res, err := ImportTimelinePath(store, zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored != 3 {
		t.Fatalf("zip import = %+v", res)
	}
}

func TestOpenTimelineStore_EmptyPathDisabled(t *testing.T) {
	s, err := OpenTimelineStore("")
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatal("empty path should disable store")
	}
}

func TestOwnTracksClusterVisits(t *testing.T) {
	// Three samples at the same spot (visit) then two far away (new visit + trip).
	base := int64(1700000000)
	locs := []ownTracksLocation{
		{Tst: base, Lat: 44.6488, Lon: -63.5752, Desc: "Harbour", TID: "ab"},
		{Tst: base + 600, Lat: 44.6489, Lon: -63.5751, TID: "ab"},
		{Tst: base + 1200, Lat: 44.6488, Lon: -63.5752, TID: "ab"},
		// gap + travel
		{Tst: base + 7200, Lat: 44.3764, Lon: -64.3188, Desc: "Beach", TID: "ab"},
		{Tst: base + 7800, Lat: 44.3765, Lon: -64.3187, TID: "ab"},
		{Tst: base + 8400, Lat: 44.3764, Lon: -64.3188, TID: "ab"},
	}
	visits := clusterOwnTracksVisits(locs)
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

func TestHaversineMeters(t *testing.T) {
	// ~111m per 0.001° latitude.
	d := haversineMeters(44.0, -63.0, 44.001, -63.0)
	if d < 100 || d > 130 {
		t.Fatalf("haversine = %f", d)
	}
}

func writeSampleTimelineZip(zipPath, entryName, content string) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create(entryName)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(content)); err != nil {
		return err
	}
	return zw.Close()
}
