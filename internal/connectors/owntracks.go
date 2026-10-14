package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// OwnTracks live connector (issue #142). OwnTracks Recorder is a local-first
// location history service that fits the Immich/self-hosted audience. Aegis
// models visits and trips from Recorder location samples — not a raw GPS
// firehose on the library surface. Endpoint comes from the configured
// service endpoint (never hardcoded).

const (
	OwnTracksServiceID   = "owntracks"
	OwnTracksServiceName = "OwnTracks"
)

// OwnTracksLocation is one Recorder /api/v0/locations sample.
type OwnTracksLocation struct {
	Type      string   `json:"_type"`
	Lat       float64  `json:"lat"`
	Lon       float64  `json:"lon"`
	Tst       int64    `json:"tst"`
	Acc       float64  `json:"acc"`
	Addr      string   `json:"addr"`
	TID       string   `json:"tid"`
	U         string   `json:"user"`
	Device    string   `json:"device"`
	Desc      string   `json:"desc"`
	Trigger   string   `json:"t"`
	InRegions []string `json:"inregions"`
	Topic     string   `json:"topic"`
}

type ownTracksLocationsResponse struct {
	Type string              `json:"_type"`
	Data []OwnTracksLocation `json:"data"`
}

// ownTracksVisit is a Recorder /api/v0/visit row when available.
type ownTracksVisit struct {
	Type   string  `json:"_type"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Tst    int64   `json:"tst"`
	TstEnd int64   `json:"tst_end"`
	Name   string  `json:"name"`
	Addr   string  `json:"addr"`
	ID     string  `json:"id"`
	Vac    float64 `json:"vac"` // vertical accuracy / confidence proxy
}

type ownTracksVisitsResponse struct {
	Type string           `json:"_type"`
	Data []ownTracksVisit `json:"data"`
}

// OwnTracksConnector talks to an OwnTracks Recorder REST API.
type OwnTracksConnector struct {
	host Host
}

// NewOwnTracksConnector builds the live connector.
func NewOwnTracksConnector(host Host) *OwnTracksConnector {
	return &OwnTracksConnector{host: host}
}

func (c *OwnTracksConnector) OwnTracksUpstreamBase() string {
	if c.host == nil {
		return ""
	}
	svc := c.host.ServiceConfig(OwnTracksServiceID)
	if svc == nil {
		return ""
	}
	return strings.TrimRight(svc.Endpoint, "/")
}

func (c *OwnTracksConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "places",
				Description: "OwnTracks visits and trips derived from Recorder history",
				Operations: []types.Operation{
					{
						Name:        "list_locations",
						Description: "Recorder location samples (internal; library clusters these into visits)",
						Method:      "GET",
						Path:        "/api/v0/locations",
						Parameters: []types.Parameter{
							{Name: "user", In: "query", Type: "string", Description: "OwnTracks user id"},
							{Name: "device", In: "query", Type: "string", Description: "OwnTracks device id"},
							{Name: "from", In: "query", Type: "integer", Description: "Unix seconds"},
							{Name: "to", In: "query", Type: "integer", Description: "Unix seconds"},
						},
						Response: types.ResponseSpec{
							Description: "OwnTracks locations payload",
							Fields:      []string{"data"},
						},
					},
					{
						Name:        "list_visits",
						Description: "Recorder-computed visits when available",
						Method:      "GET",
						Path:        "/api/v0/visit",
						Parameters: []types.Parameter{
							{Name: "user", In: "query", Type: "string", Description: "OwnTracks user id"},
							{Name: "device", In: "query", Type: "string", Description: "OwnTracks device id"},
						},
						Response: types.ResponseSpec{
							Description: "OwnTracks visits payload",
							Fields:      []string{"data"},
						},
					},
				},
			},
		},
	}
}

func (c *OwnTracksConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return "OwnTracks visit history is aggregated at " + root + "/v1/library/places (visit windows with place names; coordinates require the precise permission). Prefer that endpoint over scraping Recorder paths."
}

func (c *OwnTracksConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", OwnTracksServiceID, "method", r.Method, "path", remainingPath)
	token := c.OwnTracksToken(logger)
	if token == "" {
		WriteJSONError(w, http.StatusUnauthorized,
			"no valid OwnTracks token — set Authorization/X-API-Key headers on the owntracks service")
		return
	}
	upstreamBase := c.OwnTracksUpstreamBase()
	upstreamURL := upstreamBase + remainingPath
	if encoded := r.URL.Query().Encode(); encoded != "" {
		upstreamURL += "?" + encoded
	}
	cacheKey := OwnTracksServiceID + ":" + r.URL.RequestURI()
	if r.Method == http.MethodGet {
		if cached, ok := c.host.CacheGet(cacheKey); ok {
			logger.Debug("cache hit")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Aegis-Cache", "hit")
			io.WriteString(w, cached)
			return
		}
	}
	data, status, err := DoUpstreamRequest(r.Context(), r.Method, upstreamURL, r.Body, token)
	if err != nil {
		logger.Error("owntracks request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, OwnTracksServiceName+" API request failed")
		return
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		WriteJSONError(w, http.StatusUnauthorized,
			OwnTracksServiceName+" rejected the request — set an API key in service headers")
		return
	}
	if r.Method == http.MethodGet && status == http.StatusOK {
		c.host.CacheSet(cacheKey, string(data), 60*time.Second)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
}

// OwnTracksToken resolves static config headers (Authorization / X-API-Key)
// or an OAuth token. Self-hosted Recorder typically uses a static token.
func (c *OwnTracksConnector) OwnTracksToken(logger *slog.Logger) string {
	if c.host == nil {
		return ""
	}
	if svc := c.host.ServiceConfig(OwnTracksServiceID); svc != nil {
		for k, v := range svc.Headers {
			if strings.EqualFold(k, "Authorization") && strings.TrimSpace(v) != "" {
				v = strings.TrimSpace(v)
				if strings.HasPrefix(strings.ToLower(v), "bearer ") {
					return strings.TrimSpace(v[len("bearer "):])
				}
				return v
			}
			if (strings.EqualFold(k, "X-API-Key") || strings.EqualFold(k, "X-OwnTracks-Token")) && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return c.host.GetValidToken(OwnTracksServiceID, logger)
}

func (c *OwnTracksConnector) ListUserPlaceVisits(ctx context.Context) (visits []types.PlaceVisit, truncated bool, err error) {
	base := c.OwnTracksUpstreamBase()
	if base == "" {
		return nil, false, types.ErrUnauthorized
	}
	logger := slog.With("connector", OwnTracksServiceID)
	// Prefer explicit visits when the Recorder exposes them.
	if vs, ok, err := c.ListRecorderVisits(ctx, base, logger); err == nil && ok {
		return vs, false, nil
	} else if err != nil {
		// Fail-open: fall back to clustering locations.
		logger.Debug("visit endpoint failed; falling back to locations", "error", err)
	}
	locs, truncated, err := c.ListRecorderLocations(ctx, base, logger)
	if err != nil {
		return nil, truncated, err
	}
	return ClusterOwnTracksVisits(locs), truncated, nil
}

func (c *OwnTracksConnector) ListRecorderVisits(ctx context.Context, base string, logger *slog.Logger) ([]types.PlaceVisit, bool, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(LibraryPageCap*2))
	var parsed ownTracksVisitsResponse
	if err := c.OwnTracksAPIGet(ctx, logger, base+"/api/v0/visit", q, &parsed); err != nil {
		return nil, false, err
	}
	if len(parsed.Data) == 0 {
		return nil, false, fmt.Errorf("no visit rows")
	}
	out := make([]types.PlaceVisit, 0, len(parsed.Data))
	for _, v := range parsed.Data {
		start := time.Unix(v.Tst, 0).UTC().Format(time.RFC3339)
		end := ""
		if v.TstEnd > v.Tst {
			end = time.Unix(v.TstEnd, 0).UTC().Format(time.RFC3339)
		}
		name := strings.TrimSpace(v.Name)
		if name == "" {
			name = strings.TrimSpace(v.Addr)
		}
		id := strings.TrimSpace(v.ID)
		if id == "" {
			id = "ot:" + strconv.FormatInt(v.Tst, 10)
		}
		conf := "high"
		if v.Vac > 200 {
			conf = "low"
		} else if v.Vac > 50 {
			conf = "medium"
		}
		visit := types.NewPlaceVisit(OwnTracksServiceID, OwnTracksServiceName, id, types.PlaceVisit{
			StartAt:    start,
			EndAt:      end,
			Kind:       types.PlaceKindVisit,
			Confidence: conf,
			EntityID:   id,
			Place: types.Place{
				Name: name,
				Lat:  v.Lat,
				Lng:  v.Lon,
			},
		})
		visit.Sources[0].Extra = map[string]any{"provider_id": id, "point_ids": []string{id}}
		out = append(out, visit)
	}
	return out, true, nil
}

func (c *OwnTracksConnector) ListRecorderLocations(ctx context.Context, base string, logger *slog.Logger) ([]OwnTracksLocation, bool, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(LibraryPageCap*4))
	var parsed ownTracksLocationsResponse
	if err := c.OwnTracksAPIGet(ctx, logger, base+"/api/v0/locations", q, &parsed); err != nil {
		return nil, false, err
	}
	truncated := len(parsed.Data) >= LibraryPageCap*4
	return parsed.Data, truncated, nil
}

func (c *OwnTracksConnector) OwnTracksAPIGet(ctx context.Context, logger *slog.Logger, rawURL string, query url.Values, out any) error {
	token := c.OwnTracksToken(logger)
	if token == "" {
		return types.ErrUnauthorized
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return types.ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("owntracks: unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ClusterOwnTracksVisits groups consecutive samples that stay within
// ~200m for at least 15 minutes into a visit. Movement between clusters is
// recorded as a trip when the gap is short enough to be travel (issue #142).
func ClusterOwnTracksVisits(locs []OwnTracksLocation) []types.PlaceVisit {
	if len(locs) == 0 {
		return nil
	}
	// Sort chronologically.
	sorted := append([]OwnTracksLocation(nil), locs...)
	SortOwnTracks(sorted)

	const (
		stayRadiusM   = 200.0
		minDwell      = 15 * time.Minute
		tripMaxGap    = 6 * time.Hour
		sampleTimeout = 30 * time.Minute
	)

	type cluster struct {
		start, end time.Time
		lat, lng   float64
		points     []OwnTracksLocation
	}

	var clusters []cluster
	var cur *cluster
	for _, loc := range sorted {
		ts := time.Unix(loc.Tst, 0).UTC()
		if cur != nil {
			dist := HaversineMeters(cur.lat, cur.lng, loc.Lat, loc.Lon)
			gap := ts.Sub(cur.end)
			if dist <= stayRadiusM && gap <= sampleTimeout {
				cur.end = ts
				cur.points = append(cur.points, loc)
				continue
			}
			clusters = append(clusters, *cur)
			cur = nil
		}
		cur = &cluster{start: ts, end: ts, lat: loc.Lat, lng: loc.Lon, points: []OwnTracksLocation{loc}}
	}
	if cur != nil {
		clusters = append(clusters, *cur)
	}

	out := make([]types.PlaceVisit, 0, len(clusters))
	for i, cl := range clusters {
		dwell := cl.end.Sub(cl.start)
		if dwell < minDwell && len(cl.points) < 2 {
			continue
		}
		name := ""
		addr := ""
		for _, p := range cl.points {
			if name == "" && strings.TrimSpace(p.Desc) != "" {
				name = strings.TrimSpace(p.Desc)
			}
			if name == "" && len(p.InRegions) > 0 {
				name = p.InRegions[0]
			}
			if addr == "" && strings.TrimSpace(p.Addr) != "" {
				addr = strings.TrimSpace(p.Addr)
			}
		}
		id := "otc:" + strconv.FormatInt(cl.start.Unix(), 10) + ":" + strconv.FormatInt(cl.end.Unix(), 10)
		conf := "medium"
		if len(cl.points) >= 3 {
			conf = "high"
		}
		visit := types.NewPlaceVisit(OwnTracksServiceID, OwnTracksServiceName, id, types.PlaceVisit{
			StartAt:    cl.start.UTC().Format(time.RFC3339),
			EndAt:      cl.end.UTC().Format(time.RFC3339),
			Kind:       types.PlaceKindVisit,
			Confidence: conf,
			EntityID:   id,
			Place: types.Place{
				Name:    name,
				Address: addr,
				Lat:     cl.lat,
				Lng:     cl.lng,
			},
		})
		pointIDs := make([]string, 0, len(cl.points))
		for _, p := range cl.points {
			pointIDs = append(pointIDs, strconv.FormatInt(p.Tst, 10)+":"+p.TID)
		}
		visit.Sources[0].Extra = map[string]any{
			"provider_id": id,
			"point_ids":   pointIDs,
		}
		out = append(out, visit)

		// Trip gap to the next cluster.
		if i+1 < len(clusters) {
			next := clusters[i+1]
			gap := next.start.Sub(cl.end)
			if gap > sampleTimeout && gap <= tripMaxGap {
				dist := HaversineMeters(cl.lat, cl.lng, next.lat, next.lng)
				if dist > stayRadiusM {
					tid := "ott:" + strconv.FormatInt(cl.end.Unix(), 10) + ":" + strconv.FormatInt(next.start.Unix(), 10)
					mode := types.TripModeDrive
					if dist < 2000 && gap > 30*time.Minute {
						mode = types.TripModeWalk
					}
					trip := types.NewPlaceVisit(OwnTracksServiceID, OwnTracksServiceName, tid, types.PlaceVisit{
						StartAt:  cl.end.UTC().Format(time.RFC3339),
						EndAt:    next.start.UTC().Format(time.RFC3339),
						Kind:     types.PlaceKindTrip,
						Mode:     mode,
						EntityID: tid,
						Place: types.Place{
							Name: next.points[0].Desc,
							Lat:  next.lat,
							Lng:  next.lng,
						},
					})
					trip.Sources[0].Extra = map[string]any{"provider_id": tid}
					out = append(out, trip)
				}
			}
		}
	}
	return out
}

func SortOwnTracks(locs []OwnTracksLocation) {
	sort.SliceStable(locs, func(i, j int) bool { return locs[i].Tst < locs[j].Tst })
}

// HaversineMeters returns the great-circle distance between two points.
func HaversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	toRad := func(v float64) float64 { return v * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
