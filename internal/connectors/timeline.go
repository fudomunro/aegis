package connectors

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
	"time"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

// Google Maps Timeline local import (issue #142). Timeline moved on-device in
// 2024, so this is an import (like mail Takeout), not a live API. Semantic
// Location History JSON (placeVisit / activitySegment) is parsed into a local
// query store and served as a PlaceVisitProvider. Aegis stores visit windows
// and place metadata — never a raw GPS firehose beyond the import archive.

const (
	TimelineServiceID   = "google-timeline"
	TimelineServiceName = "Google Timeline"
)

// TimelineService is the service card the local Timeline import store injects
// into config (issue #266).
func TimelineService() LocalStoreService {
	return LocalStoreService{
		ID:          TimelineServiceID,
		Name:        TimelineServiceName,
		Action:      "inject",
		Description: "Locally imported Google Maps Timeline exports (Advanced → Timeline import).",
		GrantLimits: policy.Permissions{"places": policy.AccessRead},
	}
}

// Admin declares the Timeline import admin surface (issue #254).
func (s *TimelineStore) Admin() ImportAdmin {
	return ImportAdmin{
		FlagName:    "-timeline-store",
		PathHint:    "a Timeline JSON file, directory, or Takeout zip",
		SamplePath:  "~/Downloads/Takeout/Location History/Semantic Location History (JSON files or a Takeout zip)",
		LibraryPath: "/v1/library/places",
		BareResult:  true,
		Counts: func() map[string]int {
			if s == nil || !s.Enabled() {
				return nil
			}
			n, err := s.Count()
			if err != nil {
				return nil
			}
			return map[string]int{"count": n}
		},
		Hints: func(state ImportHintState) []string {
			switch {
			case !state.Enabled:
				return []string{"Timeline store is disabled — restart with -timeline-store <path> (default under the data dir)."}
			case state.Counts["count"] == 0:
				return []string{
					"Export Location History from Google Takeout (Timeline → Export), then import the Semantic Location History folder or zip below.",
					"Re-import is safe: rows already stored are skipped. places permission defaults to none — enable city, read, or precise to let agents query it.",
				}
			default:
				return []string{fmt.Sprintf("%d visit/trip window(s) imported. Agents can query them via %s when places permissions allow.",
					state.Counts["count"], state.LibraryURL)}
			}
		},
		Import: func(path string) (ImportOutcome, error) {
			return ImportTimelinePath(s, path)
		},
	}
}

// TimelineStore is a local SQLite store of imported Timeline visits/trips.
// Empty path disables the store (like mail-archive / quicken).
type TimelineStore struct {
	localStore
}

// TimelineImportResult reports one import run.
type TimelineImportResult struct {
	Path    string `json:"path"`
	Scanned int    `json:"scanned"`
	Stored  int    `json:"stored"`
	Skipped int    `json:"skipped"`
	Errors  int    `json:"errors"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
}

// LogAttrs returns slog keyvals for one finished import run.
func (r TimelineImportResult) LogAttrs() []any {
	return []any{"scanned", r.Scanned, "stored", r.Stored,
		"skipped", r.Skipped, "errors", r.Errors, "done", r.Done}
}

// Failed reports a run that recorded errors but stored nothing.
func (r TimelineImportResult) Failed() bool { return r.Errors > 0 && r.Stored == 0 }

var _ ImportStore = (*TimelineStore)(nil)
var _ ImportOutcome = TimelineImportResult{}

// OpenTimelineStore opens (or creates) the local Timeline SQLite store.
// An empty path returns nil (disabled).
func OpenTimelineStore(path string) (*TimelineStore, error) {
	s := &TimelineStore{}
	if err := s.openLocalStore(path, "timeline", timelineSchema); err != nil {
		return nil, err
	}
	if !s.Enabled() {
		return nil, nil
	}
	return s, nil
}

// timelineSchema installs the imported visit/trip table.
const timelineSchema = `

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
`

// upsertTimelineTx stores or replaces one imported visit keyed by source_id.
func upsertTimelineTx(tx *sql.Tx, filePath string, v kinds.PlaceVisit) error {
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
	_, err = tx.Exec(`
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
		id, filePath, v.StartAt, v.EndAt, kinds.NormalizePlaceVisitKind(v.Kind),
		kinds.NormalizeTripMode(v.Mode), v.Confidence,
		v.Place.Name, v.Place.Address, v.Place.Locality, v.Place.Country,
		lat, lng, v.EntityID, string(raw),
	)
	return err
}

// Upsert stores or replaces one imported visit keyed by source_id.
func (s *TimelineStore) Upsert(filePath string, v kinds.PlaceVisit) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("timeline store disabled")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := upsertTimelineTx(tx, filePath, v); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

const timelineHasQuery = `SELECT COUNT(1) FROM timeline_visits WHERE source_id = ?`

// Has reports whether source_id is already stored.
func (s *TimelineStore) Has(sourceID string) bool {
	return s.hasRow(timelineHasQuery, sourceID)
}

// List returns stored visits (chronological), optional filters.
func (s *TimelineStore) List(f kinds.PlaceVisitFilters, limit int) ([]kinds.PlaceVisit, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("timeline store disabled")
	}
	if limit <= 0 || limit > LibraryItemCap {
		limit = LibraryItemCap
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT raw_json FROM timeline_visits ORDER BY start_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kinds.PlaceVisit
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v kinds.PlaceVisit
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			continue
		}
		if f.Empty() || f.Matches(v) {
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

func placeVisitSourceID(v kinds.PlaceVisit) string {
	if id := strings.TrimSpace(v.EntityID); id != "" {
		return "e:" + id
	}
	for _, src := range v.Sources {
		if id := strings.TrimSpace(kinds.ExtraString(src.Extra, "provider_id")); id != "" {
			return "p:" + src.Service + ":" + id
		}
		if id := strings.TrimSpace(src.ID); id != "" {
			return "s:" + src.Service + ":" + id
		}
	}
	return "w:" + kinds.PlaceIdentityKey(v)
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
	Location        timelineLocation `json:"location"`
	Duration        timelineDuration `json:"duration"`
	PlaceConfidence string           `json:"placeConfidence"`
	VisitConfidence *int             `json:"visitConfidence"`
	PlaceID         string           `json:"placeId"`
}

type timelineActivitySegment struct {
	StartLocation timelineLocation `json:"startLocation"`
	EndLocation   timelineLocation `json:"endLocation"`
	Duration      timelineDuration `json:"duration"`
	ActivityType  string           `json:"activityType"`
	PlaceID       string           `json:"placeId"`
}

type timelineLocation struct {
	LatitudeE7   *int64 `json:"latitudeE7"`
	LongitudeE7  *int64 `json:"longitudeE7"`
	PlaceID      string `json:"placeId"`
	Address      string `json:"address"`
	Name         string `json:"name"`
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
		return kinds.NormalizePlaceVisitTime(d.StartTimestamp)
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
		return kinds.NormalizePlaceVisitTime(d.EndTimestamp)
	}
	if d.EndTimestampMs != "" {
		if ms, err := strconv.ParseInt(d.EndTimestampMs, 10, 64); err == nil {
			return time.UnixMilli(ms).UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func (l timelineLocation) place() kinds.Place {
	p := kinds.Place{
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
func ParseTimelineJSON(name string, data []byte) ([]kinds.PlaceVisit, error) {
	var root timelineExport
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	var out []kinds.PlaceVisit
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
			visit := kinds.NewPlaceVisit(TimelineServiceID, TimelineServiceName, pid, kinds.PlaceVisit{
				StartAt:    v.Duration.start(),
				EndAt:      v.Duration.end(),
				Place:      place,
				Kind:       kinds.PlaceKindVisit,
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
			trip := kinds.NewPlaceVisit(TimelineServiceID, TimelineServiceName, "seg:"+a.Duration.start(), kinds.PlaceVisit{
				StartAt: a.Duration.start(),
				EndAt:   a.Duration.end(),
				Place:   place,
				Kind:    kinds.PlaceKindTrip,
				Mode:    kinds.NormalizeTripMode(a.ActivityType),
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
		var st ImportStats
		upsertRows(&store.localStore, &st, visits, placeVisitSourceID, timelineHasQuery,
			func(tx *sql.Tx, v kinds.PlaceVisit) error { return upsertTimelineTx(tx, f, v) })
		res.Scanned += st.Scanned
		res.Stored += st.Stored
		res.Skipped += st.Skipped
		res.Errors += st.Errors
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

func parseTimelineFile(path string) ([]kinds.PlaceVisit, error) {
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
	store *TimelineStore
}

// NewTimelineConnector wraps the local Timeline import store.
func NewTimelineConnector(store *TimelineStore) *TimelineConnector {
	return &TimelineConnector{store: store}
}

func (c *TimelineConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		Kinds: []types.KindCapability{
			{
				Name:        "places",
				Description: "Imported Google Maps Timeline visits and trips",
				Operations: []types.Operation{
					{
						Name:        "list_place_visits",
						Description: "Imported Timeline visits and trips (permission key: places)",
						Method:      "GET",
						Path:        "/v1/library/places",
						Parameters: []types.Parameter{
							{Name: "after", In: "query", Type: "string", Description: "Visits overlapping this RFC3339 time or later"},
							{Name: "before", In: "query", Type: "string", Description: "Visits starting before this RFC3339 time"},
							{Name: "locality", In: "query", Type: "string", Description: "Filter by city/locality"},
							{Name: "kind", In: "query", Type: "string", Description: "visit or trip"},
						},
						Response: types.ResponseSpec{
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
	shared.WriteJSONError(w, http.StatusNotFound, "google-timeline is import-only; use /v1/library/places")
}

// ListUserPlaceVisits returns imported visits as a PlaceVisitProvider.
func (c *TimelineConnector) ListUserPlaceVisits(ctx context.Context) (visits []kinds.PlaceVisit, truncated bool, err error) {
	if c == nil || c.store == nil || !c.store.Enabled() {
		return nil, false, kinds.ErrUnauthorized
	}
	logger := slog.With("connector", TimelineServiceID)
	items, listErr := c.store.List(kinds.PlaceVisitFilters{}, LibraryItemCap)
	if listErr != nil {
		logger.Error("timeline store list failed", "error", listErr)
		return nil, false, listErr
	}
	if n, cntErr := c.store.Count(); cntErr == nil && n > len(items) {
		truncated = true
	}
	out := make([]kinds.PlaceVisit, 0, len(items))
	for _, v := range items {
		out = append(out, kinds.NewPlaceVisit(TimelineServiceID, TimelineServiceName, placeVisitSourceID(v), v))
	}
	return out, truncated, nil
}
