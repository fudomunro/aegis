package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Google Maps Timeline local import (issue #142). Timeline moved on-device in
// 2024, so this is an import (like mail Takeout), not a live API. Semantic
// Location History JSON (placeVisit / activitySegment) is parsed into a local
// query store and served as a PlaceVisitProvider. Aegis stores visit windows
// and place metadata — never a raw GPS firehose beyond the import archive.

const (
	timelineServiceID   = "google-timeline"
	timelineServiceName = "Google Timeline"
)

// TimelineStore is a local SQLite store of imported Timeline visits/trips.
// Empty path disables the store (like mail-archive / quicken).
type TimelineStore struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
}

// TimelineImportResult reports one import run.
type TimelineImportResult struct {
	Path      string `json:"path"`
	Scanned   int    `json:"scanned"`
	Stored    int    `json:"stored"`
	Skipped   int    `json:"skipped"`
	Errors    int    `json:"errors"`
	Done      bool   `json:"done"`
	Error     string `json:"error,omitempty"`
}

// OpenTimelineStore opens (or creates) the local Timeline SQLite store.
// An empty path returns nil (disabled).
func OpenTimelineStore(path string) (*TimelineStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create timeline store dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	s := &TimelineStore{db: db, path: path}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *TimelineStore) init() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS timeline_visits (
  source_id TEXT PRIMARY KEY,
  file_path TEXT NOT NULL,
  start_at TEXT NOT NULL,
  end_at TEXT,
  kind TEXT NOT NULL,
  mode TEXT,
  confidence TEXT,
  place_name TEXT,
  place_address TEXT,
  place_locality TEXT,
  place_country TEXT,
  place_lat REAL,
  place_lng REAL,
  place_id TEXT,
  raw_json TEXT
);
CREATE INDEX IF NOT EXISTS timeline_visits_start ON timeline_visits(start_at);
CREATE INDEX IF NOT EXISTS timeline_visits_locality ON timeline_visits(place_locality);
`)
	return err
}

func (s *TimelineStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *TimelineStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *TimelineStore) Enabled() bool { return s != nil && s.db != nil }

// Upsert stores or replaces one imported visit keyed by source_id.
func (s *TimelineStore) Upsert(filePath string, v PlaceVisit) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("timeline store disabled")
	}
	id := placeVisitSourceID(v)
	if id == "" {
		return fmt.Errorf("timeline visit missing source id")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var lat, lng sql.NullFloat64
	if v.Place.Lat != 0 || v.Place.Lng != 0 {
		lat = sql.NullFloat64{Float64: v.Place.Lat, Valid: true}
		lng = sql.NullFloat64{Float64: v.Place.Lng, Valid: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`
INSERT INTO timeline_visits(
  source_id, file_path, start_at, end_at, kind, mode, confidence,
  place_name, place_address, place_locality, place_country,
  place_lat, place_lng, place_id, raw_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(source_id) DO UPDATE SET
  file_path=excluded.file_path,
  start_at=excluded.start_at,
  end_at=excluded.end_at,
  kind=excluded.kind,
  mode=excluded.mode,
  confidence=excluded.confidence,
  place_name=excluded.place_name,
  place_address=excluded.place_address,
  place_locality=excluded.place_locality,
  place_country=excluded.place_country,
  place_lat=excluded.place_lat,
  place_lng=excluded.place_lng,
  place_id=excluded.place_id,
  raw_json=excluded.raw_json
`,
		id, filePath, v.StartAt, v.EndAt, normalizePlaceVisitKind(v.Kind),
		normalizeTripMode(v.Mode), v.Confidence,
		v.Place.Name, v.Place.Address, v.Place.Locality, v.Place.Country,
		lat, lng, v.EntityID, string(raw),
	)
	return err
}

func (s *TimelineStore) Has(sourceID string) bool {
	if s == nil || s.db == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM timeline_visits WHERE source_id = ?`, sourceID).Scan(&n)
	return err == nil && n > 0
}

// List returns stored visits (chronological), optional filters.
func (s *TimelineStore) List(f PlaceVisitFilters, limit int) ([]PlaceVisit, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("timeline store disabled")
	}
	if limit <= 0 || limit > libraryPageCap {
		limit = libraryPageCap
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT raw_json FROM timeline_visits ORDER BY start_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlaceVisit
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v PlaceVisit
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			continue
		}
		if f.Empty() || f.matches(v) {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

func (s *TimelineStore) Count() (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("timeline store disabled")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM timeline_visits`).Scan(&n)
	return n, err
}

func placeVisitSourceID(v PlaceVisit) string {
	if id := strings.TrimSpace(v.EntityID); id != "" {
		return "e:" + id
	}
	for _, src := range v.Sources {
		if id := strings.TrimSpace(messageExtraString(src.Extra, "provider_id")); id != "" {
			return "p:" + src.Service + ":" + id
		}
		if id := strings.TrimSpace(src.ID); id != "" {
			return "s:" + src.Service + ":" + id
		}
	}
	return "w:" + placeIdentityKey(v)
}

// --- Google Timeline JSON shapes ---

// timelineExport is the root of a Semantic Location History monthly file
// (Takeout → Location History → Semantic Location History → YYYY → YYYY-MM.json).
type timelineExport struct {
	TimelineObjects []timelineObject `json:"timelineObjects"`
}

type timelineObject struct {
	PlaceVisit      *timelinePlaceVisit      `json:"placeVisit"`
	ActivitySegment *timelineActivitySegment `json:"activitySegment"`
}

type timelinePlaceVisit struct {
	Location       timelineLocation `json:"location"`
	Duration       timelineDuration `json:"duration"`
	PlaceConfidence string          `json:"placeConfidence"`
	VisitConfidence *int            `json:"visitConfidence"`
	PlaceID        string          `json:"placeId"`
}

type timelineActivitySegment struct {
	StartLocation timelineLocation `json:"startLocation"`
	EndLocation   timelineLocation `json:"endLocation"`
	Duration      timelineDuration `json:"duration"`
	ActivityType  string           `json:"activityType"`
	PlaceID       string           `json:"placeId"`
}

type timelineLocation struct {
	LatitudeE7  *int64  `json:"latitudeE7"`
	LongitudeE7 *int64  `json:"longitudeE7"`
	PlaceID     string  `json:"placeId"`
	Address     string  `json:"address"`
	Name        string  `json:"name"`
	SemanticType string `json:"semanticType"`
}

type timelineDuration struct {
	StartTimestamp string `json:"startTimestamp"`
	EndTimestamp   string `json:"endTimestamp"`
	// Legacy numeric forms (unix seconds).
	StartTimestampMs string `json:"startTimestampMs"`
	EndTimestampMs   string `json:"endTimestampMs"`
}

func (d timelineDuration) start() string {
	if d.StartTimestamp != "" {
		return normalizePlaceVisitTime(d.StartTimestamp)
	}
	if d.StartTimestampMs != "" {
		if ms, err := strconv.ParseInt(d.StartTimestampMs, 10, 64); err == nil {
			return time.UnixMilli(ms).UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func (d timelineDuration) end() string {
	if d.EndTimestamp != "" {
		return normalizePlaceVisitTime(d.EndTimestamp)
	}
	if d.EndTimestampMs != "" {
		if ms, err := strconv.ParseInt(d.EndTimestampMs, 10, 64); err == nil {
			return time.UnixMilli(ms).UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func (l timelineLocation) place() Place {
	p := Place{
		Name:    strings.TrimSpace(l.Name),
		Address: strings.TrimSpace(l.Address),
	}
	if l.LatitudeE7 != nil && l.LongitudeE7 != nil {
		p.Lat = float64(*l.LatitudeE7) / 1e7
		p.Lng = float64(*l.LongitudeE7) / 1e7
	}
	// Timeline addresses are free-form "Name, Street, City, Region, Country".
	// Derive locality/country from the trailing CSV parts when present.
	if p.Address != "" {
		parts := strings.Split(p.Address, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) >= 2 {
			p.Country = parts[len(parts)-1]
		}
		if len(parts) >= 3 {
			p.Locality = parts[len(parts)-3]
			if p.Locality == "" {
				p.Locality = parts[len(parts)-2]
			}
		} else if len(parts) == 2 {
			p.Locality = parts[0]
		}
	}
	return p
}

// ParseTimelineJSON parses one Semantic Location History file into visits
// (placeVisit) and trips (activitySegment).
func ParseTimelineJSON(name string, data []byte) ([]PlaceVisit, error) {
	var root timelineExport
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	var out []PlaceVisit
	for _, obj := range root.TimelineObjects {
		if obj.PlaceVisit != nil {
			v := obj.PlaceVisit
			if v.Duration.start() == "" {
				continue
			}
			place := v.Location.place()
			conf := strings.ToLower(strings.TrimSpace(v.PlaceConfidence))
			if conf == "" && v.VisitConfidence != nil {
				switch {
				case *v.VisitConfidence >= 80:
					conf = "high"
				case *v.VisitConfidence >= 40:
					conf = "medium"
				default:
					conf = "low"
				}
			}
			pid := v.Location.PlaceID
			if pid == "" {
				pid = v.PlaceID
			}
			visit := newPlaceVisit(timelineServiceID, timelineServiceName, pid, PlaceVisit{
				StartAt:    v.Duration.start(),
				EndAt:      v.Duration.end(),
				Place:      place,
				Kind:       PlaceKindVisit,
				Confidence: conf,
				EntityID:   pid,
			})
			if pid != "" {
				visit.Sources[0].Extra = map[string]any{"place_id": pid, "provider_id": pid}
			}
			out = append(out, visit)
			continue
		}
		if obj.ActivitySegment != nil {
			a := obj.ActivitySegment
			if a.Duration.start() == "" {
				continue
			}
			// Trips are travel between places. Prefer the destination as the
			// place so "which beach was that?" can use the arrival window.
			place := a.EndLocation.place()
			if place.Name == "" && place.Address == "" {
				place = a.StartLocation.place()
			}
			aid := a.PlaceID
			if aid == "" {
				aid = a.EndLocation.PlaceID
			}
			if aid == "" {
				aid = a.StartLocation.PlaceID
			}
			trip := newPlaceVisit(timelineServiceID, timelineServiceName, "seg:"+a.Duration.start(), PlaceVisit{
				StartAt: a.Duration.start(),
				EndAt:   a.Duration.end(),
				Place:   place,
				Kind:    PlaceKindTrip,
				Mode:    normalizeTripMode(a.ActivityType),
			})
			trip.Sources[0].Extra = map[string]any{"activity_type": a.ActivityType}
			if aid != "" {
				trip.EntityID = "seg:" + aid + ":" + a.Duration.start()
				trip.Sources[0].ID = trip.EntityID
				trip.Sources[0].Extra["place_id"] = aid
			}
			out = append(out, trip)
		}
	}
	return out, nil
}

// ImportTimelinePath imports a Timeline JSON file, a directory of them
// (Takeout Semantic Location History / YYYY), or a Takeout .zip.
func ImportTimelinePath(store *TimelineStore, path string) (TimelineImportResult, error) {
	res := TimelineImportResult{Path: path}
	if store == nil || !store.Enabled() {
		return res, fmt.Errorf("timeline store disabled")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return res, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return res, err
	}
	var files []string
	switch {
	case st.IsDir():
		err = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if isTimelineJSONName(d.Name()) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return res, err
		}
	case strings.EqualFold(filepath.Ext(abs), ".zip"):
		return importTimelineZip(store, abs)
	case isTimelineJSONName(filepath.Base(abs)):
		files = []string{abs}
	default:
		return res, fmt.Errorf("not a Timeline JSON file or directory: %s", path)
	}
	sort.Strings(files)
	for _, f := range files {
		visits, err := parseTimelineFile(f)
		if err != nil {
			res.Errors++
			continue
		}
		for _, v := range visits {
			res.Scanned++
			id := placeVisitSourceID(v)
			if id != "" && store.Has(id) {
				res.Skipped++
				continue
			}
			if err := store.Upsert(f, v); err != nil {
				res.Errors++
				continue
			}
			res.Stored++
		}
	}
	res.Done = true
	return res, nil
}

func isTimelineJSONName(name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".json") {
		return false
	}
	// Semantic monthly files (2023-01.json), Records.json, Timeline.json.
	base := strings.TrimSuffix(lower, ".json")
	if base == "records" || base == "timeline" {
		return true
	}
	// YYYY-MM
	if len(base) == 7 && base[4] == '-' {
		return true
	}
	// Any other Location History JSON (user renamed export).
	return strings.Contains(lower, "timeline") || strings.Contains(lower, "location") ||
		strings.Contains(lower, "semantic")
}

func parseTimelineFile(path string) ([]PlaceVisit, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return nil, err
	}
	return ParseTimelineJSON(filepath.Base(path), data)
}

func importTimelineZip(store *TimelineStore, zipPath string) (TimelineImportResult, error) {
	res := TimelineImportResult{Path: zipPath, Done: true}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return res, err
	}
	defer zr.Close()
	for _, ent := range zr.File {
		if ent.FileInfo().IsDir() {
			continue
		}
		if !isTimelineJSONName(filepath.Base(ent.Name)) {
			continue
		}
		rc, err := ent.Open()
		if err != nil {
			res.Errors++
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		if err != nil {
			res.Errors++
			continue
		}
		visits, err := ParseTimelineJSON(ent.Name, data)
		if err != nil {
			res.Errors++
			continue
		}
		for _, v := range visits {
			res.Scanned++
			id := placeVisitSourceID(v)
			if id != "" && store.Has(id) {
				res.Skipped++
				continue
			}
			if err := store.Upsert(zipPath+"!"+ent.Name, v); err != nil {
				res.Errors++
				continue
			}
			res.Stored++
		}
	}
	return res, nil
}

// TimelineConnector lists imported Google Timeline visits from the local store.
type TimelineConnector struct {
	srv   *Server
	store *TimelineStore
}

// NewTimelineConnector wraps the local Timeline import store.
func NewTimelineConnector(srv *Server, store *TimelineStore) *TimelineConnector {
	return &TimelineConnector{srv: srv, store: store}
}

func (c *TimelineConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "places",
				Description: "Imported Google Maps Timeline visits and trips",
				Operations: []Operation{
					{
						Name:        "list_place_visits",
						Description: "Imported Timeline visits and trips (permission key: places)",
						Method:      "GET",
						Path:        "/v1/library/places",
						Parameters: []Parameter{
							{Name: "after", In: "query", Type: "string", Description: "Visits overlapping this RFC3339 time or later"},
							{Name: "before", In: "query", Type: "string", Description: "Visits starting before this RFC3339 time"},
							{Name: "locality", In: "query", Type: "string", Description: "Filter by city/locality"},
							{Name: "kind", In: "query", Type: "string", Description: "visit or trip"},
						},
						Response: ResponseSpec{
							Description: "Neutral place visits with sources[]",
							Fields:      []string{"visits", "total"},
						},
					},
				},
			},
		},
	}
}

func (c *TimelineConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return "Google Timeline visits imported from a Takeout export are available at " + root + "/v1/library/places (visits and trips with time windows). Import is admin-only (Advanced → Timeline import). Do not request raw GPS points."
}

func (c *TimelineConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	writeJSONError(w, http.StatusNotFound, "google-timeline is import-only; use /v1/library/places")
}

// ListUserPlaceVisits returns imported visits as a PlaceVisitProvider.
func (c *TimelineConnector) ListUserPlaceVisits(ctx context.Context) (visits []PlaceVisit, truncated bool, err error) {
	if c == nil || c.store == nil || !c.store.Enabled() {
		return nil, false, ErrUnauthorized
	}
	logger := slog.With("connector", timelineServiceID)
	items, listErr := c.store.List(PlaceVisitFilters{}, libraryPageCap)
	if listErr != nil {
		logger.Error("timeline store list failed", "error", listErr)
		return nil, false, listErr
	}
	if n, cntErr := c.store.Count(); cntErr == nil && n > len(items) {
		truncated = true
	}
	out := make([]PlaceVisit, 0, len(items))
	for _, v := range items {
		out = append(out, newPlaceVisit(timelineServiceID, timelineServiceName, placeVisitSourceID(v), v))
	}
	return out, truncated, nil
}

// --- Admin import status / run (issue #142) ---

// TimelineImportStatus is GET /v1/admin/timeline/status.
type TimelineImportStatus struct {
	Enabled    bool     `json:"enabled"`
	Path       string   `json:"path,omitempty"`
	Count      int      `json:"count"`
	StoreEmpty bool     `json:"store_empty"`
	LibraryURL string   `json:"library_url,omitempty"`
	SamplePath string   `json:"sample_hint,omitempty"`
	Hints      []string `json:"hints,omitempty"`
}

// TimelineImportRequest is POST /v1/admin/timeline/import.
type TimelineImportRequest struct {
	Path string `json:"path"`
}

// HandleAdminTimeline serves /v1/admin/timeline/*.
func (s *Server) HandleAdminTimeline(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/timeline"), "/")
	switch {
	case path == "" || path == "status":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeTimelineStatus(w)
	case path == "import":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleTimelineImport(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) timelineStatus() TimelineImportStatus {
	store := s.timeline
	st := TimelineImportStatus{
		Enabled:    store != nil && store.Enabled(),
		SamplePath: "~/Downloads/Takeout/Location History/Semantic Location History (JSON files or a Takeout zip)",
	}
	if st.Enabled {
		st.Path = store.Path()
		if n, err := store.Count(); err == nil {
			st.Count = n
		}
		st.StoreEmpty = st.Count == 0
	}
	root := strings.TrimRight(s.baseURL, "/")
	st.LibraryURL = root + "/v1/library/places"
	if !st.Enabled {
		st.Hints = append(st.Hints, "Timeline store is disabled — restart with -timeline-store <path> (default under the data dir).")
	} else if st.StoreEmpty {
		st.Hints = append(st.Hints,
			"Export Location History from Google Takeout (Timeline → Export), then import the Semantic Location History folder or zip below.",
			"Re-import is safe: rows already stored are skipped. places permission defaults to none — enable city, read, or precise to let agents query it.")
	} else {
		st.Hints = append(st.Hints,
			fmt.Sprintf("%d visit/trip window(s) imported. Agents can query them via %s when places permissions allow.", st.Count, st.LibraryURL))
	}
	return st
}

func (s *Server) writeTimelineStatus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.timelineStatus())
}

func (s *Server) handleTimelineImport(w http.ResponseWriter, r *http.Request) {
	store := s.timeline
	if store == nil || !store.Enabled() {
		writeJSONError(w, http.StatusBadRequest, "timeline store disabled — set -timeline-store")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req TimelineImportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		writeJSONError(w, http.StatusBadRequest, "path is required — a Timeline JSON file, directory, or Takeout zip")
		return
	}
	res, err := ImportTimelinePath(store, req.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}
