package syncjobs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

// Cross-connector mirror runs against mock upstreams (issue #144): Apple
// Music on either side of Spotify and YouTube Music, through the real
// mirror runner and the real placement adapters. The ISRC ladder is what
// these exercise — Apple's catalog filter[isrc] lookup on the way in, and
// Apple-sourced ISRCs on the way out.

// appleMusicPEM returns a throwaway MusicKit-style .p8 key.
func appleMusicPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// mirrorFixture wires two real connectors against their mock upstreams into
// one Runners, with the mirror adapter lookup left to the real
// type-assertion path (no MirrorSourceFn/MirrorTargetFn stubs).
type mirrorFixture struct {
	run     *Runners
	store   *JobStore
	worker  *SyncWorker
	ledger  *copies.Ledger
	writes  *callLog
	h       *fixtureHost
	service map[string]*httptest.Server
}

// callLog records mutating upstream calls per service so dry-run tests can
// prove nothing was written.
type callLog struct {
	byService map[string][]string
}

func newCallLog() *callLog { return &callLog{byService: map[string][]string{}} }

func (l *callLog) add(service, entry string) {
	if l == nil {
		return
	}
	l.byService[service] = append(l.byService[service], entry)
}

func (l *callLog) writes(service string) []string {
	if l == nil {
		return nil
	}
	return l.byService[service]
}

// mirrorFixtureFor wires Apple Music against one other music connector.
// appleUpstream serves the MusicKit API; otherUpstream the partner's.
func mirrorFixtureFor(t *testing.T, otherID string, appleUpstream, otherUpstream http.Handler) *mirrorFixture {
	t.Helper()
	appleUp := httptest.NewServer(appleUpstream)
	t.Cleanup(appleUp.Close)
	otherUp := httptest.NewServer(otherUpstream)
	t.Cleanup(otherUp.Close)

	h := newFixtureHost("https://aegis.example.com")
	h.grants = shared.GrantMap{
		"playlists": shared.AccessFull,
		"tracks":    shared.AccessFull,
	}
	h.services[connectors.AppleMusicServiceID] = fixtureService{
		Name: "Apple Music", Action: "inject", Endpoint: appleUp.URL,
		Headers: map[string]string{
			"X-Apple-Music-Key-Id":      "KEY1",
			"X-Apple-Music-Issuer-Id":   "issuer-1",
			"X-Apple-Music-Private-Key": appleMusicPEM(t),
			"Music-User-Token":          "mut-1",
		},
	}
	h.services[otherID] = fixtureService{Name: otherID, Action: "inject", Endpoint: otherUp.URL}
	h.tokens[otherID] = "tok"
	h.connectors[connectors.AppleMusicServiceID] = connectors.NewAppleMusicConnector(h)
	switch otherID {
	case "spotify":
		h.connectors[otherID] = connectors.NewSpotifyConnector(h)
	case "youtube-music":
		h.connectors[otherID] = connectors.NewYouTubeMusicConnector(h)
	default:
		t.Fatalf("no connector fixture for %s", otherID)
	}

	ledger, err := copies.Open(t.TempDir() + "/copy_ledger.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	// Real adapter lookup: no MirrorSourceFn / MirrorTargetFn overrides, so
	// MirrorSourceFor type-asserts the connectors registered above.
	lib := &library.Handlers{Host: h, Connectors: h.connectors, Copies: ledger}
	run := &Runners{
		Host:       h,
		Mirror:     lib,
		Copies:     ledger,
		Connectors: h.connectors,
	}
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	registerMirrorRunners(reg, run)
	return &mirrorFixture{
		run:    run,
		store:  store,
		worker: NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1}),
		ledger: ledger,
		writes: newCallLog(),
		h:      h,
		service: map[string]*httptest.Server{
			connectors.AppleMusicServiceID: appleUp,
			otherID:                        otherUp,
		},
	}
}

// --- mock upstreams ---

// spotifySourceUpstream serves one playlist with ISRC-bearing tracks.
func spotifySourceUpstream(log *callLog) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/src1":
			writeJSON(w, map[string]any{
				"name":          "Road Trip",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/src1"},
				"snapshot_id":   "snap-1",
				"tracks":        map[string]any{"total": 2},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/src1/tracks":
			writeJSON(w, map[string]any{
				"items": []any{
					spotifyTrackItem("sp1", "Entropy", "Beach Bunny", "USQE92100257", 221000),
					spotifyTrackItem("sp2", "Ascolta", "Ludovico Einaudi", "ITBLB0600018", 291680),
				},
				"total": 2,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/me/tracks":
			writeJSON(w, map[string]any{
				"items": []any{spotifyTrackItem("sp1", "Entropy", "Beach Bunny", "USQE92100257", 221000)},
				"total": 1,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/me":
			writeJSON(w, map[string]any{"id": "user1"})
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			q := r.URL.Query().Get("q")
			log.add("spotify", "search:"+q)
			if strings.HasPrefix(q, "isrc:") {
				writeJSON(w, map[string]any{
					"tracks": map[string]any{"items": []any{
						map[string]any{"id": strings.TrimPrefix(q, "isrc:"), "uri": "spotify:track:" + strings.TrimPrefix(q, "isrc:"), "duration_ms": 221000},
					}},
				})
				return
			}
			writeJSON(w, map[string]any{"tracks": map[string]any{"items": []any{}}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/playlists"):
			log.add("spotify", "create-playlist")
			writeJSON(w, map[string]any{"id": "sp-copy", "external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/sp-copy"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tracks"):
			log.add("spotify", "add-tracks")
			writeJSON(w, map[string]any{"snapshot_id": "snap-2"})
		case r.Method == http.MethodPut:
			log.add("spotify", "put:"+r.URL.Path)
			writeJSON(w, map[string]any{})
		default:
			writeJSON(w, map[string]any{})
		}
	})
}

func spotifyTrackItem(id, name, artist, isrc string, durationMs int) map[string]any {
	return map[string]any{"track": map[string]any{
		"id": id, "name": name, "uri": "spotify:track:" + id, "duration_ms": durationMs,
		"artists":      []any{map[string]any{"name": artist}},
		"album":        map[string]any{"name": "Album", "id": "al1"},
		"external_ids": map[string]any{"isrc": isrc},
	}}
}

// appleTargetUpstream accepts placements and serves catalog ISRC lookups.
// It records writes on log for dry-run assertions.
func appleTargetUpstream(log *callLog) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/me/storefront":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "us"}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/catalog/us/songs"):
			q := r.URL.Query()
			data := []any{}
			if isrc := q.Get("filter[isrc]"); isrc != "" {
				data = append(data, appleSong("cat-"+isrc, isrc, 221000))
			} else if term := q.Get("term"); term != "" {
				data = append(data, appleSong("cat-name", "USQE92100257", 221000))
			}
			writeJSON(w, map[string]any{"data": data})
		case r.Method == http.MethodPost && r.URL.Path == "/me/library/playlists":
			log.add("apple-music", "create-playlist")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			writeJSON(w, map[string]any{"data": []any{applePlaylist("p.copy", body)}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tracks"):
			log.add("apple-music", "add-tracks:"+r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/me/library":
			log.add("apple-music", "save-liked:"+r.URL.RawQuery)
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/me/library/playlists/"):
			id := strings.TrimPrefix(r.URL.Path, "/me/library/playlists/")
			if strings.HasSuffix(id, "/tracks") {
				writeJSON(w, map[string]any{"data": []any{}, "meta": map[string]any{"total": 0}})
				return
			}
			writeJSON(w, map[string]any{"data": []any{applePlaylist(id, nil)}})
		default:
			writeJSON(w, map[string]any{"data": []any{}})
		}
	})
}

// appleSourceUpstream serves one library playlist with catalog-id tracks.
func appleSourceUpstream() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/me/storefront":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "us"}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/catalog/us/songs"):
			ids := strings.Split(r.URL.Query().Get("ids"), ",")
			data := []any{}
			for _, id := range ids {
				if id == "" {
					continue
				}
				data = append(data, appleSong(id, testISRCFor(id), 221000))
			}
			writeJSON(w, map[string]any{"data": data})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tracks"):
			writeJSON(w, map[string]any{
				"data": []any{
					appleLibrarySong("a.1", "Entropy", "Beach Bunny", "1613600188"),
					appleLibrarySong("a.2", "Ascolta", "Ludovico Einaudi", "1559739419"),
				},
				"meta": map[string]any{"total": 2},
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/me/library/playlists/"):
			writeJSON(w, map[string]any{"data": []any{applePlaylist("p.src", nil)}})
		case r.Method == http.MethodGet && r.URL.Path == "/me/library/songs":
			writeJSON(w, map[string]any{
				"data": []any{appleLibrarySong("a.1", "Entropy", "Beach Bunny", "1613600188")},
				"meta": map[string]any{"total": 1},
			})
		default:
			writeJSON(w, map[string]any{"data": []any{}})
		}
	})
}

// youtubeTargetUpstream accepts playlist creation and video placement.
func youtubeTargetUpstream(log *callLog) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/playlists":
			log.add("youtube-music", "create-playlist")
			writeJSON(w, map[string]any{"id": "yt-copy"})
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			writeJSON(w, map[string]any{"items": []any{
				map[string]any{"id": map[string]any{"videoId": "vid1"}},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/playlistItems":
			log.add("youtube-music", "add-item")
			writeJSON(w, map[string]any{"id": "item1"})
		default:
			writeJSON(w, map[string]any{"items": []any{}})
		}
	})
}

// youtubeSourceUpstream serves one playlist of music videos.
func youtubeSourceUpstream() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/playlists":
			writeJSON(w, map[string]any{"items": []any{
				map[string]any{
					"snippet":        map[string]any{"title": "Videos"},
					"contentDetails": map[string]any{"itemCount": 1},
				},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/playlistItems":
			writeJSON(w, map[string]any{"items": []any{
				map[string]any{"snippet": map[string]any{
					"title":                  "Entropy",
					"videoOwnerChannelTitle": "Beach Bunny",
					"resourceId":             map[string]any{"videoId": "vid1"},
				}},
			}})
		default:
			writeJSON(w, map[string]any{"items": []any{}})
		}
	})
}

func appleSong(id, isrc string, durationMs int) map[string]any {
	return map[string]any{
		"id": id, "type": "songs",
		"attributes": map[string]any{
			"name": "Entropy", "artistName": "Beach Bunny",
			"durationInMillis": durationMs, "isrc": isrc,
		},
	}
}

func appleLibrarySong(id, name, artist, catalogID string) map[string]any {
	return map[string]any{
		"id": id, "type": "library-songs",
		"attributes": map[string]any{
			"name": name, "artistName": artist, "durationInMillis": 221000,
			"playParams": map[string]any{"id": id, "kind": "song", "isLibrary": true, "catalogId": catalogID},
		},
	}
}

func applePlaylist(id string, createBody map[string]any) map[string]any {
	name := "Copy"
	if createBody != nil {
		if attrs, ok := createBody["attributes"].(map[string]any); ok {
			if n, ok := attrs["name"].(string); ok {
				name = n
			}
		}
	}
	return map[string]any{
		"id": id, "type": "library-playlists",
		"attributes": map[string]any{"name": name, "canEdit": true},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- runs ---

func TestAppleMusicMirror_SpotifyToApple_ISRCMatch(t *testing.T) {
	f := mirrorFixtureFor(t, "spotify", appleTargetUpstream(nil), spotifySourceUpstream(newCallLog()))
	mustEnqueue(t, f.store, SyncEnqueue{
		Kind: library.MirrorKindPlaylists, JobType: SyncJobTypeMirror, Service: "apple-music",
		Payload: map[string]any{
			"kind": "playlists", "source_service": "spotify", "source_id": "src1",
			"target_service": "apple-music",
		},
	})
	if !f.worker.RunOnce(t.Context(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := f.store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s err=%q", job.State, job.LastError)
	}
	// The copy ledger records the finished placement.
	if _, ok := f.ledger.Get(library.MirrorKindPlaylists, "apple-music", "p.copy"); !ok {
		t.Fatal("copy ledger missing the mirrored playlist row")
	}
}

func TestAppleMusicMirror_AppleToSpotify_ISRCSourced(t *testing.T) {
	log := newCallLog()
	f := mirrorFixtureFor(t, "spotify", appleSourceUpstream(), spotifySourceUpstream(log))
	mustEnqueue(t, f.store, SyncEnqueue{
		Kind: library.MirrorKindPlaylists, JobType: SyncJobTypeMirror, Service: "spotify",
		Payload: map[string]any{
			"kind": "playlists", "source_service": "apple-music", "source_id": "p.src",
			"target_service": "spotify",
		},
	})
	if !f.worker.RunOnce(t.Context(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := f.store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s err=%q", job.State, job.LastError)
	}
	if _, ok := f.ledger.Get(library.MirrorKindPlaylists, "spotify", "sp-copy"); !ok {
		t.Fatal("copy ledger missing the mirrored playlist row")
	}
	// Apple-sourced tracks carry ISRCs after catalog enrichment, so the
	// Spotify placement must search isrc: first — the issue's headline ask.
	searches := log.writes("spotify")
	isrcSearch := false
	for _, w := range searches {
		if strings.HasPrefix(w, "search:isrc:") {
			isrcSearch = true
		}
	}
	if !isrcSearch {
		t.Fatalf("spotify writes = %v, want an isrc: catalog search", searches)
	}
}

func TestAppleMusicMirror_YouTubePairing_BothDirections(t *testing.T) {
	t.Run("youtube to apple", func(t *testing.T) {
		f := mirrorFixtureFor(t, "youtube-music", appleTargetUpstream(nil), youtubeSourceUpstream())
		mustEnqueue(t, f.store, SyncEnqueue{
			Kind: library.MirrorKindPlaylists, JobType: SyncJobTypeMirror, Service: "apple-music",
			Payload: map[string]any{
				"kind": "playlists", "source_service": "youtube-music", "source_id": "yt1",
				"target_service": "apple-music",
			},
		})
		if !f.worker.RunOnce(t.Context(), "w") {
			t.Fatal("RunOnce must claim")
		}
		if job := f.store.List(SyncFilter{})[0]; job.State != SyncJobSucceeded {
			t.Fatalf("state = %s err=%q", job.State, job.LastError)
		}
	})
	t.Run("apple to youtube", func(t *testing.T) {
		log := newCallLog()
		f := mirrorFixtureFor(t, "youtube-music", appleSourceUpstream(), youtubeTargetUpstream(log))
		mustEnqueue(t, f.store, SyncEnqueue{
			Kind: library.MirrorKindPlaylists, JobType: SyncJobTypeMirror, Service: "youtube-music",
			Payload: map[string]any{
				"kind": "playlists", "source_service": "apple-music", "source_id": "p.src",
				"target_service": "youtube-music",
			},
		})
		if !f.worker.RunOnce(t.Context(), "w") {
			t.Fatal("RunOnce must claim")
		}
		if job := f.store.List(SyncFilter{})[0]; job.State != SyncJobSucceeded {
			t.Fatalf("state = %s err=%q", job.State, job.LastError)
		}
		if len(log.writes("youtube-music")) == 0 {
			t.Fatal("expected playlist writes on youtube-music")
		}
	})
}

func TestAppleMusicMirror_LikedTracksSpotifyToApple(t *testing.T) {
	log := newCallLog()
	f := mirrorFixtureFor(t, "spotify", appleTargetUpstream(log), spotifySourceUpstream(newCallLog()))
	mustEnqueue(t, f.store, SyncEnqueue{
		Kind: library.MirrorKindTracks, JobType: SyncJobTypeMirror, Service: "apple-music",
		Payload: map[string]any{
			"kind": "tracks", "source_service": "spotify",
			"target_service": "apple-music",
		},
	})
	if !f.worker.RunOnce(t.Context(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := f.store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s err=%q", job.State, job.LastError)
	}
	saves := 0
	for _, w := range log.writes("apple-music") {
		if strings.HasPrefix(w, "save-liked:") {
			saves++
			if !strings.Contains(w, "ids%5Bsongs%5D") {
				t.Fatalf("save-liked query = %q, want ids[songs]", w)
			}
		}
	}
	if saves == 0 {
		t.Fatalf("no library save on apple-music: %v", log.writes("apple-music"))
	}
	if _, ok := f.ledger.Get(library.MirrorKindTracks, "apple-music", ""); !ok {
		t.Fatal("copy ledger missing the liked-tracks row")
	}
}

func TestAppleMusicMirror_DryRunWritesNothing(t *testing.T) {
	log := newCallLog()
	f := mirrorFixtureFor(t, "spotify", appleTargetUpstream(log), spotifySourceUpstream(newCallLog()))
	mustEnqueue(t, f.store, SyncEnqueue{
		Kind: library.MirrorKindPlaylists, JobType: SyncJobTypeMirror, Service: "apple-music",
		Payload: map[string]any{
			"kind": "playlists", "source_service": "spotify", "source_id": "src1",
			"target_service": "apple-music", "dry_run": true,
		},
	})
	if !f.worker.RunOnce(t.Context(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := f.store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s err=%q", job.State, job.LastError)
	}
	if got := log.writes("apple-music"); len(got) != 0 {
		t.Fatalf("dry-run wrote to apple-music: %v", got)
	}
	if _, ok := f.ledger.Get(library.MirrorKindPlaylists, "apple-music", "p.copy"); ok {
		t.Fatal("dry-run must not stamp a copy row")
	}
}

// testISRCFor maps a catalog id to a well-formed ISRC so the placement
// ladder's ISRC rung is exercised (TrackMatchKey validates the shape).
func testISRCFor(id string) string {
	switch id {
	case "1613600188":
		return "USQE92100257"
	case "1559739419":
		return "ITBLB0600018"
	default:
		return "USQE92100257"
	}
}
