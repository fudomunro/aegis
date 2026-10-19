package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func testMirrorServer(t *testing.T, upstream http.Handler, targetWrite bool) (*Server, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(upstream)
	t.Cleanup(ts.Close)

	perms := types.Permissions{"default": types.AccessRead}
	if targetWrite {
		perms = types.Permissions{"default": types.AccessRead, "playlists": types.AccessFull, "tracks": types.AccessFull}
	}
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: ts.URL, CanonicalFor: []string{"playlists"}, GrantLimits: perms},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: ts.URL, GrantLimits: perms},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "sp-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.tokens.Save("youtube-music", &OAuthToken{
		AccessToken: "yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors["youtube-music"] = connectors.NewYouTubeMusicConnector(srv)
	if ledger, err := OpenCopyLedger(filepath.Join(t.TempDir(), "copy_ledger.db")); err == nil {
		t.Cleanup(func() { ledger.Close() })
		srv.copies = ledger
	}
	return srv, ts
}

func TestHandleLibraryMirror_RejectsBadInput(t *testing.T) {
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), true)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"bad kind", map[string]any{"kind": "albums", "target_service": "youtube-music", "source_id": "x"}, http.StatusBadRequest},
		{"missing target", map[string]any{"kind": "playlists", "source_service": "spotify", "source_id": "p1"}, http.StatusBadRequest},
		{"missing playlist id", map[string]any{"kind": "playlists", "source_service": "spotify", "target_service": "youtube-music"}, http.StatusBadRequest},
		{"same service", map[string]any{"kind": "playlists", "source_service": "spotify", "source_id": "p1", "target_service": "spotify"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("POST", "/v1/library/mirror", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-key")
			w := httptest.NewRecorder()
			srv.AuthMiddleware(srv.HandleLibraryMirror)(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleLibraryMirror_DeniesReadOnlyTarget(t *testing.T) {
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), false)
	body, _ := json.Marshal(map[string]any{
		"kind": "playlists", "source_service": "spotify", "source_id": "p1", "target_service": "youtube-music",
	})
	req := httptest.NewRequest("POST", "/v1/library/mirror", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMirror)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "write access") {
		t.Fatalf("expected write denial: %s", w.Body.String())
	}
}

func TestHandleLibraryMirror_DryRunDoesNotWrite(t *testing.T) {
	var writes atomic.Int32
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		switch {
		case r.URL.Path == "/playlists/p1":
			json.NewEncoder(w).Encode(map[string]any{
				"id": "p1", "name": "Jazz Vibes",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/p1"},
			})
		case r.URL.Path == "/playlists/p1/tracks":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{
					map[string]any{"track": map[string]any{
						"id": "t1", "name": "So What", "uri": "spotify:track:t1",
						"artists": []any{map[string]any{"name": "Miles Davis"}},
						"album":   map[string]any{"name": "Kind of Blue"},
					}},
					map[string]any{"track": map[string]any{
						"id": "t2", "name": "Blue in Green", "uri": "spotify:track:t2",
						"artists": []any{map[string]any{"name": "Miles Davis"}},
					}},
				},
				"next": nil,
			})
		default:
			http.NotFound(w, r)
		}
	}), true)

	body, _ := json.Marshal(map[string]any{
		"kind": "playlists", "source_service": "spotify", "source_id": "p1",
		"target_service": "youtube-music", "dry_run": true,
	})
	req := httptest.NewRequest("POST", "/v1/library/mirror", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMirror)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var res MirrorResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.DryRun || res.Planned != 2 || res.Copied != 0 {
		t.Fatalf("dry-run result = %+v", res)
	}
	if res.Source.Name != "Jazz Vibes" || res.Target.Service != "youtube-music" {
		t.Fatalf("refs = %+v / %+v", res.Source, res.Target)
	}
	if writes.Load() != 0 {
		t.Fatalf("dry-run performed %d writes", writes.Load())
	}
	if res.IdentityNote == "" || !strings.Contains(res.IdentityNote, "issue #4") {
		t.Fatalf("missing identity note: %q", res.IdentityNote)
	}
}

func TestHandleLibraryMirror_SpotifyToYouTubePlaylist(t *testing.T) {
	var playlistItemsCreated atomic.Int32
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/p1":
			json.NewEncoder(w).Encode(map[string]any{
				"id": "p1", "name": "Jazz Vibes",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/p1"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/p1/tracks":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{
					map[string]any{"track": map[string]any{
						"id": "t1", "name": "So What", "uri": "spotify:track:t1",
						"artists": []any{map[string]any{"name": "Miles Davis"}},
					}},
					map[string]any{"track": map[string]any{
						"id": "t2", "name": "Missing Song", "uri": "spotify:track:t2",
						"artists": []any{map[string]any{"name": "Nobody"}},
					}},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/playlists":
			// YouTube create playlist
			json.NewEncoder(w).Encode(map[string]any{"id": "yt-pl-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/playlistItems":
			if q.Get("part") != "snippet" {
				t.Errorf("playlistItems part = %q", q.Get("part"))
			}
			playlistItemsCreated.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"id": "item-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			// YouTube search — match only the first track.
			if strings.Contains(q.Get("q"), "So What") {
				json.NewEncoder(w).Encode(map[string]any{
					"items": []any{map[string]any{"id": map[string]any{"videoId": "vid1"}}},
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}), true)

	body, _ := json.Marshal(map[string]any{
		"kind": "playlists", "source_service": "spotify", "source_id": "p1",
		"target_service": "youtube-music", "name": "Jazz Backup",
	})
	req := httptest.NewRequest("POST", "/v1/library/mirror", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMirror)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var res MirrorResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Target.ID != "yt-pl-1" || res.Copied != 1 || res.Skipped != 1 {
		t.Fatalf("result = %+v", res)
	}
	if playlistItemsCreated.Load() != 1 {
		t.Fatalf("playlistItems created = %d", playlistItemsCreated.Load())
	}
	if len(res.Skips) != 1 || !strings.Contains(res.Skips[0].Name, "Missing Song") {
		t.Fatalf("skips = %+v", res.Skips)
	}
	// Successful mirror records a copy-ledger row (issue #76).
	if srv.copies == nil || !srv.copies.Enabled() {
		t.Fatal("expected copy ledger on mirror server")
	}
	rec, ok := srv.copies.Get("playlists", "youtube-music", "yt-pl-1")
	if !ok {
		t.Fatal("expected copy ledger row for mirrored playlist")
	}
	if rec.SourceService != "spotify" || rec.SourceID != "p1" {
		t.Fatalf("source = %s:%s", rec.SourceService, rec.SourceID)
	}
	if rec.Role != CopyRoleMirror || rec.Status != CopyStatusInSync {
		t.Fatalf("record = %+v", rec)
	}
	info := srv.copyInfoFor("playlists", "youtube-music", "yt-pl-1")
	if info == nil || info.SyncMode != CopySyncModeFull {
		t.Fatalf("stamp = %+v", info)
	}
}

func TestHandleLibraryMirror_DefaultsSourceToCanonical(t *testing.T) {
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/playlists/p1" {
			json.NewEncoder(w).Encode(map[string]any{"id": "p1", "name": "Home Mix"})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/playlists/p1/tracks" {
			json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			return
		}
		http.NotFound(w, r)
	}), true)

	body, _ := json.Marshal(map[string]any{
		"kind": "playlists", "source_id": "p1", "target_service": "youtube-music", "dry_run": true,
	})
	req := httptest.NewRequest("POST", "/v1/library/mirror", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMirror)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var res MirrorResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Source.Service != "spotify" {
		t.Fatalf("expected canonical source spotify, got %+v", res.Source)
	}
}

func TestLibraryInstructions_MentionMirrorEndpoint(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	instr := srv.libraryInstructionsFor("playlists")
	if !strings.Contains(instr, "/v1/library/mirror") {
		t.Fatalf("instructions should mention mirror endpoint: %s", instr)
	}
}
