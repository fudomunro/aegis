package connectors

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// FakeUpstream is an injectable provider stand-in for driving the sync
// maintenance loop without real drift (issue #86). It serves a Spotify
// saved-tracks shape at */me/tracks and takes failure-mode orders at
// */control, so a canary or mirror job pointed at it can be pushed from
// healthy into a forced contract drift (or an error status) and back.
//
// Dev/test only. The Aegis server mounts it behind -fake-upstream; it never
// ships on a real instance's route table.

// Failure modes served by the fake.
const (
	// FakeModeHealthy answers with a normal saved-tracks page.
	FakeModeHealthy = "healthy"
	// FakeModeEmpty answers with a coherent empty library (total 0).
	FakeModeEmpty = "empty"
	// FakeModeHTTP500 answers 500.
	FakeModeHTTP500 = "http-500"
	// FakeModeHTTP401 answers 401.
	FakeModeHTTP401 = "http-401"
	// FakeModeGarbage answers 200 with a body that is not JSON — the parse
	// layer must fail loudly.
	FakeModeGarbage = "garbage"
	// FakeModeDriftItems is a contract drift the adapter has since learned:
	// valid JSON, total intact, items renamed to entries. Kept as the
	// regression shape for issue #240.
	FakeModeDriftItems = "drift-items"
	// FakeModeDriftUnmapped is a contract change no adapter knows: items
	// renamed to a name outside the known migrations. Parsers go silently
	// empty while pagination metadata still claims contents — the
	// future-drift shape for the canary's incoherence guard and the loop's
	// silent-degradation path.
	FakeModeDriftUnmapped = "drift-unmapped"
)

// fakeDriftField is the renamed array the adapter learned to read (#240).
const fakeDriftField = "entries"

// fakeUnmappedField is a rename no adapter knows.
const fakeUnmappedField = "saved_tracks"

// fakeUpstreamTracks is the synthetic library. One item carries a null
// track (removed local file) so parsers that skip nulls stay honest.
var fakeUpstreamTracks = []map[string]any{
	fakeUpstreamTrack("t1", "Fixture Track One", "USFAK1234567", 200000),
	fakeUpstreamTrack("t2", "Fixture Track Two", "USFAK2234567", 185000),
	fakeUpstreamTrack("t3", "Fixture Track Three", "USFAK3234567", 210000),
}

func fakeUpstreamTrack(id, name, isrc string, durationMs int) map[string]any {
	return map[string]any{
		"added_at": "2026-01-01T00:00:00Z",
		"track": map[string]any{
			"album": map[string]any{
				"album_type": "album",
				"id":         "al1",
				"name":       "Fixture Album",
			},
			"artists": []any{
				map[string]any{"id": "ar1", "name": "Fixture Artist"},
			},
			"duration_ms": durationMs,
			"explicit":    false,
			"external_ids": map[string]any{
				"isrc": isrc,
			},
			"external_urls": map[string]any{
				"spotify": "https://open.spotify.com/track/" + id,
			},
			"id":       id,
			"is_local": false,
			"name":     name,
			"uri":      "spotify:track:" + id,
		},
	}
}

// FakeUpstream is an http.Handler; mount it at any prefix and point a
// service endpoint at that prefix. Control lives under */control.
type FakeUpstream struct {
	mu   sync.RWMutex
	mode string
}

// NewFakeUpstream returns a fake provider in healthy mode.
func NewFakeUpstream() *FakeUpstream {
	return &FakeUpstream{mode: FakeModeHealthy}
}

// Mode returns the current failure mode.
func (f *FakeUpstream) Mode() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.mode
}

// SetMode switches the failure mode. Unknown modes are rejected so typos
// cannot silently simulate health.
func (f *FakeUpstream) SetMode(mode string) error {
	switch mode {
	case FakeModeHealthy, FakeModeEmpty, FakeModeHTTP500, FakeModeHTTP401,
		FakeModeGarbage, FakeModeDriftItems, FakeModeDriftUnmapped:
	default:
		return fmt.Errorf("unknown fake upstream mode %q", mode)
	}
	f.mu.Lock()
	f.mode = mode
	f.mu.Unlock()
	return nil
}

// Modes lists the failure modes the control surface accepts.
func Modes() []string {
	return []string{FakeModeHealthy, FakeModeEmpty, FakeModeHTTP500, FakeModeHTTP401,
		FakeModeGarbage, FakeModeDriftItems, FakeModeDriftUnmapped}
}

// ServeHTTP routes by path suffix so the handler works at any mount prefix.
func (f *FakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/control"):
		f.ServeControl(w, r)
	case strings.HasSuffix(r.URL.Path, "/me/tracks"):
		f.serveSavedTracks(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ServeControl answers GET (current mode) and POST {"mode":"…"}. It is
// exported so the Aegis mount can wrap it in admin auth while the data paths
// stay open to the connector.
func (f *FakeUpstream) ServeControl(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": f.Mode(), "modes": Modes()})
	case http.MethodPost:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad control body"})
			return
		}
		if err := f.SetMode(req.Mode); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": f.Mode()})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// serveSavedTracks answers one saved-tracks page under the current mode.
func (f *FakeUpstream) serveSavedTracks(w http.ResponseWriter, r *http.Request) {
	mode := f.Mode()
	w.Header().Set("Content-Type", "application/json")
	switch mode {
	case FakeModeHTTP500:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"status":500,"message":"injected failure"}}`))
		return
	case FakeModeHTTP401:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"status":401,"message":"injected failure"}}`))
		return
	case FakeModeGarbage:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{this is not json`))
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	items := []any{}
	total := len(fakeUpstreamTracks)
	if mode == FakeModeEmpty {
		total = 0
	} else {
		for _, t := range fakeUpstreamTracks {
			items = append(items, t)
		}
		if offset > len(items) {
			offset = len(items)
		}
		if offset+limit < len(items) {
			items = items[offset : offset+limit]
		} else {
			items = items[offset:]
		}
	}

	page := map[string]any{
		"href":     "https://api.spotify.com/v1/me/tracks",
		"limit":    limit,
		"offset":   offset,
		"previous": nil,
		"next":     nil,
		"total":    total,
	}
	switch mode {
	case FakeModeDriftItems:
		// Learned migration (#240): the payload is valid and complete
		// except the items array has the renamed key.
		page[fakeDriftField] = items
	case FakeModeDriftUnmapped:
		// Unmapped contract change: renamed to a name no adapter reads.
		page[fakeUnmappedField] = items
	default:
		page["items"] = items
	}
	_ = json.NewEncoder(w).Encode(page)
}
