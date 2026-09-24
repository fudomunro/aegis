package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func writeMirrorJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestSpotifyMirror_LoadPlaylistAndLikedTracks(t *testing.T) {
	var likedPages atomic.Int32
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/p1":
			writeMirrorJSON(w, map[string]any{
				"id": "p1", "name": "Jazz",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/p1"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/p1/tracks":
			writeMirrorJSON(w, map[string]any{
				"items": []any{
					map[string]any{"track": map[string]any{
						"id": "t1", "name": "So What", "uri": "spotify:track:t1",
						"artists":     []any{map[string]any{"name": "Miles Davis"}},
						"album":       map[string]any{"name": "Kind of Blue"},
						"duration_ms": 562000,
					}},
				},
				"next": nil,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/me/tracks":
			likedPages.Add(1)
			if r.URL.Query().Get("limit") != "50" {
				t.Errorf("liked tracks limit = %q", r.URL.Query().Get("limit"))
			}
			if likedPages.Load() == 1 {
				writeMirrorJSON(w, map[string]any{
					"items": []any{
						map[string]any{"track": map[string]any{
							"id": "L1", "name": "Liked One", "uri": "spotify:track:L1",
							"artists": []any{map[string]any{"name": "Artist One"}},
						}},
					},
					"next": "https://api.spotify.com/v1/me/tracks?offset=1&limit=50",
				})
				return
			}
			writeMirrorJSON(w, map[string]any{
				"items": []any{
					map[string]any{"track": map[string]any{
						"id": "L2", "name": "Liked Two", "uri": "spotify:track:L2",
						"artists": []any{map[string]any{"name": "Artist Two"}},
					}},
				},
				"next": nil,
			})
		default:
			http.NotFound(w, r)
		}
	}), true)

	m := &spotifyMirror{s: srv}
	ctx := context.Background()

	ref, tracks, truncated, err := m.loadPlaylist(ctx, "p1")
	if err != nil {
		t.Fatalf("loadPlaylist: %v", err)
	}
	if ref.ID != "p1" || ref.Name != "Jazz" || ref.URL == "" || ref.Count != 1 {
		t.Fatalf("playlist ref = %+v", ref)
	}
	if truncated || len(tracks) != 1 || tracks[0].SourceID != "t1" || tracks[0].DurationMs != 562000 {
		t.Fatalf("playlist tracks = %+v truncated=%v", tracks, truncated)
	}

	liked, truncated, err := m.loadLikedTracks(ctx)
	if err != nil {
		t.Fatalf("loadLikedTracks: %v", err)
	}
	if truncated || len(liked) != 2 {
		t.Fatalf("liked = %+v truncated=%v pages=%d", liked, truncated, likedPages.Load())
	}
	if liked[0].SourceID != "L1" || liked[1].SourceID != "L2" {
		t.Fatalf("liked ids = %v / %v", liked[0].SourceID, liked[1].SourceID)
	}
	if likedPages.Load() < 2 {
		t.Fatalf("expected pagination on liked tracks, pages=%d", likedPages.Load())
	}
}

func TestSpotifyMirror_CreatePlaylistSearchFallbackAndSkips(t *testing.T) {
	var addCalls atomic.Int32
	var addURIs [][]string
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/me":
			writeMirrorJSON(w, map[string]any{"id": "user-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/users/user-1/playlists":
			writeMirrorJSON(w, map[string]any{
				"id":            "pl-created",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/pl-created"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			if strings.Contains(q.Get("q"), "Found Song") {
				writeMirrorJSON(w, map[string]any{
					"tracks": map[string]any{
						"items": []any{map[string]any{"uri": "spotify:track:found1"}},
					},
				})
				return
			}
			if strings.Contains(q.Get("q"), "Search Error") {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			writeMirrorJSON(w, map[string]any{
				"tracks": map[string]any{"items": []any{}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/playlists/pl-created/tracks":
			addCalls.Add(1)
			var body struct {
				URIs []string `json:"uris"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addURIs = append(addURIs, body.URIs)
			writeMirrorJSON(w, map[string]any{"snapshot_id": "snap"})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}), true)

	m := &spotifyMirror{s: srv}
	tracks := []mirrorTrack{
		{Name: "Direct", SourceURI: "spotify:track:direct"},
		{Name: "FromID", SourceID: "from-id"},
		{Name: "Found Song", Artists: []string{"Finder"}},
		{Name: "Missing Song", Artists: []string{"Nobody"}},
		{Name: "Search Error", Artists: []string{"Broken"}},
	}
	id, link, copied, skips, err := m.createPlaylistWithTracks(context.Background(), "Backup", tracks)
	if err != nil {
		t.Fatalf("createPlaylistWithTracks: %v", err)
	}
	if id != "pl-created" || link == "" {
		t.Fatalf("id/link = %q %q", id, link)
	}
	if copied != 3 {
		t.Fatalf("copied = %d skips=%+v", copied, skips)
	}
	if addCalls.Load() != 1 || len(addURIs) != 1 {
		t.Fatalf("addCalls=%d uris=%v", addCalls.Load(), addURIs)
	}
	got := strings.Join(addURIs[0], ",")
	for _, want := range []string{"spotify:track:direct", "spotify:track:from-id", "spotify:track:found1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %v", want, addURIs[0])
		}
	}
	if len(skips) != 2 {
		t.Fatalf("skips = %+v", skips)
	}
	reasons := map[string]string{}
	for _, s := range skips {
		reasons[s.Name] = s.Reason
	}
	if !strings.Contains(reasons["Missing Song — Nobody"], "no match on Spotify") {
		t.Fatalf("missing skip reason: %+v", reasons)
	}
	if reasons["Search Error — Broken"] == "" {
		t.Fatalf("search error skip missing: %+v", reasons)
	}
}

func TestSpotifyMirror_CreatePlaylist_UpstreamErrors(t *testing.T) {
	t.Run("no user id", func(t *testing.T) {
		srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeMirrorJSON(w, map[string]any{})
		}), true)
		m := &spotifyMirror{s: srv}
		_, _, _, _, err := m.createPlaylistWithTracks(context.Background(), "x", nil)
		if err == nil || !strings.Contains(err.Error(), "user id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("create playlist missing id", func(t *testing.T) {
		srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/me":
				writeMirrorJSON(w, map[string]any{"id": "u"})
			default:
				writeMirrorJSON(w, map[string]any{"name": "no-id"})
			}
		}), true)
		m := &spotifyMirror{s: srv}
		_, _, _, _, err := m.createPlaylistWithTracks(context.Background(), "x", nil)
		if err == nil || !strings.Contains(err.Error(), "no id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("tracks add fails mid-copy", func(t *testing.T) {
		srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/me":
				writeMirrorJSON(w, map[string]any{"id": "u"})
			case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/users/"):
				writeMirrorJSON(w, map[string]any{"id": "pl"})
			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/tracks"):
				http.Error(w, "nope", http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}), true)
		m := &spotifyMirror{s: srv}
		_, _, copied, _, err := m.createPlaylistWithTracks(context.Background(), "x", []mirrorTrack{
			{Name: "T", SourceURI: "spotify:track:t"},
		})
		if err == nil {
			t.Fatal("expected add error")
		}
		if copied != 0 {
			t.Fatalf("copied = %d", copied)
		}
	})
}

func TestSpotifyMirror_SaveLikedTracks(t *testing.T) {
	var puts atomic.Int32
	var putIDs []string
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			if strings.Contains(r.URL.Query().Get("q"), "Found") {
				writeMirrorJSON(w, map[string]any{
					"tracks": map[string]any{
						"items": []any{map[string]any{"uri": "spotify:track:found-liked"}},
					},
				})
				return
			}
			writeMirrorJSON(w, map[string]any{"tracks": map[string]any{"items": []any{}}})
		case r.Method == http.MethodPut && r.URL.Path == "/me/tracks":
			puts.Add(1)
			putIDs = append(putIDs, r.URL.Query().Get("ids"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}), true)

	m := &spotifyMirror{s: srv}
	tracks := []mirrorTrack{
		{Name: "HasID", SourceID: "id-1"},
		{Name: "HasURI", SourceURI: "spotify:track:uri-1"},
		{Name: "Found Song", Artists: []string{"A"}},
		{Name: "Missing Song", Artists: []string{"B"}},
	}
	copied, skips, err := m.saveLikedTracks(context.Background(), tracks)
	if err != nil {
		t.Fatalf("saveLikedTracks: %v", err)
	}
	if copied != 3 {
		t.Fatalf("copied = %d skips=%+v", copied, skips)
	}
	if puts.Load() != 1 || len(putIDs) != 1 {
		t.Fatalf("puts=%d ids=%v", puts.Load(), putIDs)
	}
	joined := putIDs[0]
	for _, want := range []string{"id-1", "uri-1", "found-liked"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %q", want, joined)
		}
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "no match on Spotify") {
		t.Fatalf("skips = %+v", skips)
	}
}

func TestSpotifyMirror_SaveLikedTracks_PutError(t *testing.T) {
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/me/tracks" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	}), true)
	m := &spotifyMirror{s: srv}
	_, _, err := m.saveLikedTracks(context.Background(), []mirrorTrack{
		{Name: "T", SourceID: "id"},
	})
	if err == nil {
		t.Fatal("expected put error")
	}
}

func TestSpotifyMirror_SearchTrackURI_EdgeCases(t *testing.T) {
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		switch {
		case strings.Contains(q, "BadItem"):
			writeMirrorJSON(w, map[string]any{
				"tracks": map[string]any{"items": []any{"not-a-map"}},
			})
		case strings.Contains(q, "EmptyURI"):
			writeMirrorJSON(w, map[string]any{
				"tracks": map[string]any{"items": []any{map[string]any{"id": "x"}}},
			})
		default:
			writeMirrorJSON(w, map[string]any{"tracks": map[string]any{"items": []any{}}})
		}
	}), true)
	m := &spotifyMirror{s: srv}
	ctx := context.Background()

	uri, err := m.searchTrackURI(ctx, mirrorTrack{Name: "Nope", Artists: []string{"X"}})
	if err != nil || uri != "" {
		t.Fatalf("empty result = %q %v", uri, err)
	}
	uri, err = m.searchTrackURI(ctx, mirrorTrack{Name: "BadItem"})
	if err != nil || uri != "" {
		t.Fatalf("bad item = %q %v", uri, err)
	}
	uri, err = m.searchTrackURI(ctx, mirrorTrack{Name: "EmptyURI"})
	if err != nil || uri != "" {
		t.Fatalf("empty uri = %q %v", uri, err)
	}
}

func TestYouTubeMirror_LoadPlaylistAndItems(t *testing.T) {
	var itemPages atomic.Int32
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/playlists":
			if r.URL.Query().Get("id") != "PL1" {
				http.NotFound(w, r)
				return
			}
			writeMirrorJSON(w, map[string]any{
				"items": []any{map[string]any{
					"id":      "PL1",
					"snippet": map[string]any{"title": "Road Trip Mix"},
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/playlistItems":
			itemPages.Add(1)
			if itemPages.Load() == 1 {
				writeMirrorJSON(w, map[string]any{
					"items": []any{
						map[string]any{"snippet": map[string]any{
							"title":                  "Song One",
							"videoOwnerChannelTitle": "Artist One - Topic",
							"resourceId":             map[string]any{"videoId": "vid1"},
						}},
						map[string]any{"snippet": map[string]any{
							"title":      "No Video",
							"resourceId": map[string]any{},
						}},
					},
					"nextPageToken": "page2",
				})
				return
			}
			writeMirrorJSON(w, map[string]any{
				"items": []any{
					map[string]any{"snippet": map[string]any{
						"title":                  "Song Two",
						"videoOwnerChannelTitle": "Artist Two",
						"resourceId":             map[string]any{"videoId": "vid2"},
					}},
				},
				"nextPageToken": "",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/missing":
			writeMirrorJSON(w, map[string]any{"items": []any{}})
		default:
			http.NotFound(w, r)
		}
	}), true)

	m := &youtubeMirror{s: srv}
	ctx := context.Background()

	ref, tracks, truncated, err := m.loadPlaylist(ctx, "PL1")
	if err != nil {
		t.Fatalf("loadPlaylist: %v", err)
	}
	if ref.ID != "PL1" || ref.Name != "Road Trip Mix" ||
		ref.URL != "https://www.youtube.com/playlist?list=PL1" {
		t.Fatalf("ref = %+v", ref)
	}
	if truncated || len(tracks) != 2 {
		t.Fatalf("tracks=%+v truncated=%v pages=%d", tracks, truncated, itemPages.Load())
	}
	if tracks[0].SourceID != "vid1" || tracks[0].Artists[0] != "Artist One - Topic" {
		t.Fatalf("track0 = %+v", tracks[0])
	}
	if tracks[1].SourceID != "vid2" {
		t.Fatalf("track1 = %+v", tracks[1])
	}

	_, _, _, err = m.loadPlaylist(ctx, "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing playlist err = %v", err)
	}
}

func TestYouTubeMirror_CreatePlaylistSearchAndSkips(t *testing.T) {
	var itemsAdded atomic.Int32
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/playlists":
			writeMirrorJSON(w, map[string]any{"id": "yt-pl"})
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			switch {
			case strings.Contains(q.Get("q"), "Found"):
				writeMirrorJSON(w, map[string]any{
					"items": []any{map[string]any{"id": map[string]any{"videoId": "vid-found"}}},
				})
			case strings.Contains(q.Get("q"), "SearchError"):
				http.Error(w, "bad", http.StatusBadGateway)
			case strings.Contains(q.Get("q"), "BadShape"):
				writeMirrorJSON(w, map[string]any{
					"items": []any{"nope"},
				})
			default:
				writeMirrorJSON(w, map[string]any{"items": []any{}})
			}
		case r.Method == http.MethodPost && r.URL.Path == "/playlistItems":
			itemsAdded.Add(1)
			if q.Get("part") != "snippet" {
				t.Errorf("part = %q", q.Get("part"))
			}
			writeMirrorJSON(w, map[string]any{"id": "item"})
		default:
			http.NotFound(w, r)
		}
	}), true)

	m := &youtubeMirror{s: srv}
	tracks := []mirrorTrack{
		{Name: "HasID", SourceID: "vid-direct"},
		{Name: "Found Song", Artists: []string{"A"}},
		{Name: "Missing Song", Artists: []string{"B"}},
		{Name: "SearchError Song", Artists: []string{"C"}},
		{Name: "BadShape Song", Artists: []string{"D"}},
	}
	id, link, copied, skips, err := m.createPlaylistWithTracks(context.Background(), "Backup", tracks)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "yt-pl" || link != "https://www.youtube.com/playlist?list=yt-pl" {
		t.Fatalf("id/link = %q %q", id, link)
	}
	if copied != 2 || itemsAdded.Load() != 2 {
		t.Fatalf("copied=%d added=%d skips=%+v", copied, itemsAdded.Load(), skips)
	}
	if len(skips) != 3 {
		t.Fatalf("skips = %+v", skips)
	}

	t.Run("missing playlist id", func(t *testing.T) {
		srv2, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeMirrorJSON(w, map[string]any{"snippet": map[string]any{"title": "x"}})
		}), true)
		m2 := &youtubeMirror{s: srv2}
		_, _, _, _, err := m2.createPlaylistWithTracks(context.Background(), "x", nil)
		if err == nil || !strings.Contains(err.Error(), "no id") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("playlistItems fails", func(t *testing.T) {
		srv2, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/playlists":
				writeMirrorJSON(w, map[string]any{"id": "pl"})
			case "/playlistItems":
				http.Error(w, "no", http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}), true)
		m2 := &youtubeMirror{s: srv2}
		_, _, copied, _, err := m2.createPlaylistWithTracks(context.Background(), "x", []mirrorTrack{
			{Name: "T", SourceID: "v1"},
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if copied != 0 {
			t.Fatalf("copied = %d", copied)
		}
	})
}

func TestYouTubeMirror_SaveLikedTracks(t *testing.T) {
	var rates atomic.Int32
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			if strings.Contains(r.URL.Query().Get("q"), "Found") {
				writeMirrorJSON(w, map[string]any{
					"items": []any{map[string]any{"id": map[string]any{"videoId": "vid-found"}}},
				})
				return
			}
			writeMirrorJSON(w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/videos/rate":
			rates.Add(1)
			if r.URL.Query().Get("rating") != "like" {
				t.Errorf("rating = %q", r.URL.Query().Get("rating"))
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}), true)

	m := &youtubeMirror{s: srv}
	copied, skips, err := m.saveLikedTracks(context.Background(), []mirrorTrack{
		{Name: "HasID", SourceID: "vid1"},
		{Name: "Found Song", Artists: []string{"A"}},
		{Name: "Missing Song", Artists: []string{"B"}},
	})
	if err != nil {
		t.Fatalf("saveLikedTracks: %v", err)
	}
	if copied != 2 || rates.Load() != 2 {
		t.Fatalf("copied=%d rates=%d skips=%+v", copied, rates.Load(), skips)
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "no match on YouTube Music") {
		t.Fatalf("skips = %+v", skips)
	}
}

func TestYouTubeMirror_SaveLikedTracks_RateError(t *testing.T) {
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}), true)
	m := &youtubeMirror{s: srv}
	_, _, err := m.saveLikedTracks(context.Background(), []mirrorTrack{
		{Name: "T", SourceID: "v1"},
	})
	if err == nil {
		t.Fatal("expected rate error")
	}
}

func TestYouTubeMirror_LoadLikedTracks(t *testing.T) {
	t.Run("connector not registered", func(t *testing.T) {
		srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), true)
		delete(srv.connectors, "youtube-music")
		m := &youtubeMirror{s: srv}
		_, _, err := m.loadLikedTracks(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("maps liked tracks through connector", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer yt-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch {
			case r.URL.Path == "/channels":
				writeMirrorJSON(w, map[string]any{
					"items": []any{map[string]any{
						"contentDetails": map[string]any{
							"relatedPlaylists": map[string]any{"likes": "LL1"},
						},
					}},
				})
			case r.URL.Path == "/playlistItems":
				writeMirrorJSON(w, map[string]any{
					"items": []any{
						map[string]any{
							"contentDetails": map[string]any{"videoId": "vid-liked"},
							"snippet": map[string]any{
								"title":                  "Liked Track",
								"videoOwnerChannelTitle": "Liked Artist - Topic",
								"resourceId":             map[string]any{"videoId": "vid-liked"},
							},
						},
					},
				})
			case r.URL.Path == "/videos":
				writeMirrorJSON(w, map[string]any{
					"items": []any{map[string]any{
						"id": "vid-liked",
						"snippet": map[string]any{
							"title":                  "Liked Track",
							"channelTitle":           "Liked Artist - Topic",
							"categoryId":             "10",
							"videoOwnerChannelTitle": "Liked Artist - Topic",
						},
						"contentDetails": map[string]any{"duration": "PT3M"},
					}},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(upstream.Close)

		srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), true)
		srv.config.GetService("youtube-music").Endpoint = upstream.URL
		srv.connectors["youtube-music"] = NewYouTubeMusicConnector(srv)
		m := &youtubeMirror{s: srv}
		tracks, truncated, err := m.loadLikedTracks(context.Background())
		if err != nil {
			t.Fatalf("loadLikedTracks: %v", err)
		}
		if truncated {
			t.Fatal("unexpected truncated")
		}
		if len(tracks) != 1 {
			t.Fatalf("tracks = %+v", tracks)
		}
		if tracks[0].SourceID != "vid-liked" || tracks[0].Name != "Liked Track" {
			t.Fatalf("track = %+v", tracks[0])
		}
	})
}

func TestMirrorTrackFromSpotify_PartialShapes(t *testing.T) {
	got := mirrorTrackFromSpotify(map[string]any{
		"name": "Partial",
		"artists": []any{
			map[string]any{"name": "A"},
			"not-a-map",
			map[string]any{},
		},
		"duration_ms": float64(120000),
	})
	if got.Name != "Partial" || len(got.Artists) != 1 || got.DurationMs != 120000 {
		t.Fatalf("got = %+v", got)
	}
}

func TestWriteMirrorUpstreamError_AndUnauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	logger := slog.Default()

	t.Run("unauthorized sentinel", func(t *testing.T) {
		w := httptest.NewRecorder()
		srv.writeMirrorUpstreamError(w, logger, "spotify", ErrUnauthorized)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "/v1/auth/spotify/login") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})

	t.Run("string unauthorized", func(t *testing.T) {
		w := httptest.NewRecorder()
		srv.writeMirrorUpstreamError(w, logger, "youtube-music", errUnauthorizedString())
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("code = %d", w.Code)
		}
	})

	t.Run("generic upstream failure", func(t *testing.T) {
		w := httptest.NewRecorder()
		srv.writeMirrorUpstreamError(w, logger, "spotify", errBoom())
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "mirror failed on spotify") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
}

func TestErrorsIsUnauthorized(t *testing.T) {
	if !errorsIsUnauthorized(ErrUnauthorized) {
		t.Fatal("sentinel should match")
	}
	if !errorsIsUnauthorized(errUnauthorizedString()) {
		t.Fatal("string unauthorized should match")
	}
	if errorsIsUnauthorized(nil) || errorsIsUnauthorized(errBoom()) {
		t.Fatal("nil/generic should not match")
	}
}

func TestHandleLibraryMirror_TracksKindWrites(t *testing.T) {
	var likedPUT, yRate atomic.Int32
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/me/tracks":
			writeMirrorJSON(w, map[string]any{
				"items": []any{
					map[string]any{"track": map[string]any{
						"id": "sp1", "name": "Shared Hit", "uri": "spotify:track:sp1",
						"artists": []any{map[string]any{"name": "Artist"}},
					}},
				},
				"next": nil,
			})
		case r.Method == http.MethodPut && r.URL.Path == "/me/tracks":
			likedPUT.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			if strings.Contains(r.URL.Query().Get("q"), "Shared Hit") {
				writeMirrorJSON(w, map[string]any{
					"items": []any{map[string]any{"id": map[string]any{"videoId": "yt1"}}},
				})
				return
			}
			writeMirrorJSON(w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/videos/rate":
			yRate.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}), true)

	body, _ := json.Marshal(map[string]any{
		"kind": "tracks", "source_service": "spotify", "target_service": "youtube-music",
	})
	req := httptest.NewRequest("POST", "/v1/library/mirror", strings.NewReader(string(body)))
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
	if res.Copied != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v", res)
	}
	if yRate.Load() != 1 || likedPUT.Load() != 0 {
		t.Fatalf("yRate=%d likedPUT=%d", yRate.Load(), likedPUT.Load())
	}
}

func TestHandleLibraryMirror_SourceUpstreamUnauthorized(t *testing.T) {
	srv, _ := testMirrorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}), true)
	srv.tokens.Tokens["spotify"] = nil
	delete(srv.tokens.Tokens, "spotify")

	body, _ := json.Marshal(map[string]any{
		"kind": "playlists", "source_service": "spotify", "source_id": "p1",
		"target_service": "youtube-music",
	})
	req := httptest.NewRequest("POST", "/v1/library/mirror", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMirror)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func errUnauthorizedString() error { return errString("upstream returned unauthorized") }
func errBoom() error               { return errString("upstream exploded") }

type simpleError string

func (e simpleError) Error() string { return string(e) }

func errString(s string) error { return simpleError(s) }
