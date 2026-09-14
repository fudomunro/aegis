package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePlaylistProvider struct {
	playlists []Playlist
	err       error
}

func (f *fakePlaylistProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (f *fakePlaylistProvider) SystemInstructions(string) string  { return "fake" }
func (f *fakePlaylistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakePlaylistProvider) ListUserPlaylists(context.Context) ([]Playlist, bool, error) {
	return f.playlists, false, f.err
}

func TestHandleLibraryPlaylists_AggregatesServices(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []Playlist{
				{
					Name:       "Jazz Vibes",
					TrackCount: 10,
					Sources:    []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}},
				},
			},
		},
		"youtube-music": &fakePlaylistProvider{
			playlists: []Playlist{
				{
					Name:       "Focus Mix",
					TrackCount: 25,
					Sources:    []PlaylistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt1"}},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2", resp.Total)
	}
	if len(resp.Playlists) != 2 {
		t.Fatalf("got %d playlists, want 2", len(resp.Playlists))
	}
	if resp.ByService["spotify"].Status != LibraryStatusOK || resp.ByService["spotify"].Count != 1 {
		t.Fatalf("spotify status = %+v", resp.ByService["spotify"])
	}
	if resp.ByService["youtube-music"].Status != LibraryStatusOK || resp.ByService["youtube-music"].Count != 1 {
		t.Fatalf("youtube-music status = %+v", resp.ByService["youtube-music"])
	}
	// Synced merge sorts by name.
	if resp.Playlists[0].Name != "Focus Mix" || resp.Playlists[1].Name != "Jazz Vibes" {
		t.Fatalf("order = %s, %s", resp.Playlists[0].Name, resp.Playlists[1].Name)
	}
	if !resp.Synced {
		t.Fatal("expected synced=true on library list")
	}
}

func TestHandleLibraryPlaylists_UnauthorizedService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []Playlist{
				{
					Name:    "Jazz",
					Sources: []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}},
				},
			},
		},
		"youtube-music": &fakePlaylistProvider{err: ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if resp.ByService["youtube-music"].Status != LibraryStatusUnauthorized {
		t.Fatalf("youtube-music status = %+v", resp.ByService["youtube-music"])
	}
	if !strings.Contains(resp.ByService["youtube-music"].Error, "/v1/auth/youtube-music/login") {
		t.Fatalf("error = %q", resp.ByService["youtube-music"].Error)
	}
}

func TestHandleLibraryPlaylists_ErrorService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakePlaylistProvider{err: errors.New("upstream boom")},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != LibraryStatusError {
		t.Fatalf("spotify status = %+v", resp.ByService["spotify"])
	}
	if resp.Playlists == nil || len(resp.Playlists) != 0 {
		t.Fatalf("playlists = %+v", resp.Playlists)
	}
}

func TestPreRequest_PlaylistWithoutServiceName(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": NewSpotifyConnector(srv),
	}

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{RequestID: "req-lib", LoginName: "alice"},
		UserMessage: "What playlists do I have?",
		RequestBody: mustJSON(t, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "What playlists do I have?"}},
		}),
	}

	req := httptest.NewRequest("POST", "/v1/pre-request", strings.NewReader(string(mustJSON(t, payload))))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != ActionModify {
		t.Fatalf("action = %s, want modify", resp.Action)
	}
	if !strings.Contains(string(resp.RequestBody), "/v1/library/playlists") {
		t.Fatalf("request_body missing library endpoint: %s", resp.RequestBody)
	}
}

func TestPreRequest_NoServiceNoPlaylistKeyword(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": NewSpotifyConnector(srv),
	}

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{RequestID: "req-none", LoginName: "alice"},
		UserMessage: "Tell me a joke",
		RequestBody: mustJSON(t, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "Tell me a joke"}},
		}),
	}

	req := httptest.NewRequest("POST", "/v1/pre-request", strings.NewReader(string(mustJSON(t, payload))))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != ActionAllow {
		t.Fatalf("action = %s, want allow", resp.Action)
	}
}

func TestSpotifyConnector_ListUserPlaylists_MockAPI(t *testing.T) {
	var mock *httptest.Server
	mock = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/me/playlists" {
			w.Write([]byte(`{"items":[{"id":"sp1","name":"Jazz","tracks":{"total":5}}],"next":"` + mock.URL + `/page2","total":2}`))
			return
		}
		w.Write([]byte(`{"items":[{"id":"sp2","name":"Coffee","tracks":{"total":8}}],"next":null,"total":2}`))
	}))
	defer mock.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mock.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	})
	conn := NewSpotifyConnector(srv)

	playlists, _, err := conn.ListUserPlaylists(context.Background())
	if err != nil {
		t.Fatalf("ListUserPlaylists: %v", err)
	}
	if len(playlists) != 2 {
		t.Fatalf("got %d playlists, want 2", len(playlists))
	}
	if playlists[0].Name != "Jazz" || playlists[0].TrackCount != 5 {
		t.Fatalf("first = %+v", playlists[0])
	}
	if playlists[0].PrimaryService() != "spotify" {
		t.Fatalf("service = %s", playlists[0].PrimaryService())
	}
	if playlists[1].Name != "Coffee" || playlists[1].TrackCount != 8 {
		t.Fatalf("second = %+v", playlists[1])
	}
}

func TestSpotifyConnector_ListUserPlaylists_Unauthorized(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	_, _, err := conn.ListUserPlaylists(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestHandleLibraryPlaylists_PermissionDenied(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: Permissions{"playlists": AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []Playlist{{Name: "Jazz", Sources: []PlaylistSource{{Service: "spotify", ID: "sp1"}}}},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestHandleLibraryPlaylists_SkipsDeniedService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{
				ID: "youtube-music", Name: "YouTube Music", Action: "inject",
				Permissions: Permissions{"playlists": AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []Playlist{{Name: "Jazz", Sources: []PlaylistSource{{Service: "spotify", ID: "sp1"}}}},
		},
		"youtube-music": &fakePlaylistProvider{
			playlists: []Playlist{{Name: "Focus", Sources: []PlaylistSource{{Service: "youtube-music", ID: "yt1"}}}},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || resp.Playlists[0].Name != "Jazz" {
		t.Fatalf("resp = %+v, want only Jazz", resp)
	}
}

type fakeLikedTrackProvider struct {
	tracks []Track
	err    error
}

func (f *fakeLikedTrackProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (f *fakeLikedTrackProvider) SystemInstructions(string) string  { return "fake" }
func (f *fakeLikedTrackProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeLikedTrackProvider) ListUserLikedTracks(context.Context) ([]Track, bool, error) {
	return f.tracks, false, f.err
}

func TestHandleLibraryTracks_AggregatesAndMerges(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeLikedTrackProvider{
			tracks: []Track{
				{
					Name:    "Blinding Lights",
					Artists: []Artist{newArtist("spotify", "Spotify", "", "The Weeknd")},
					Sources: []TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-bl"}},
				},
				{
					Name:    "Solo Spotify Song",
					Artists: []Artist{newArtist("spotify", "Spotify", "", "Artist A")},
					Sources: []TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-solo"}},
				},
			},
		},
		"youtube-music": &fakeLikedTrackProvider{
			tracks: []Track{
				{
					Name:    "Blinding Lights",
					Artists: []Artist{newArtist("youtube-music", "YouTube Music", "", "The Weeknd")},
					Sources: []TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-bl"}},
				},
				{
					Name:    "YouTube Only Hit",
					Artists: []Artist{newArtist("youtube-music", "YouTube Music", "", "Artist B")},
					Sources: []TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-only"}},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 4 raw tracks merge to 3: Blinding Lights (2 sources) + 2 singles.
	if resp.Total != 3 {
		t.Fatalf("total = %d, want 3; tracks=%+v", resp.Total, resp.Tracks)
	}
	if !resp.Synced {
		t.Fatal("expected synced=true")
	}

	var blinding *Track
	for i := range resp.Tracks {
		if resp.Tracks[i].Name == "Blinding Lights" {
			blinding = &resp.Tracks[i]
			break
		}
	}
	if blinding == nil {
		t.Fatal("Blinding Lights missing from library")
	}
	if len(blinding.Sources) != 2 {
		t.Fatalf("Blinding Lights sources = %+v, want 2", blinding.Sources)
	}
	if blinding.Sources[0].Service != "spotify" || blinding.Sources[1].Service != "youtube-music" {
		t.Fatalf("source order = %+v", blinding.Sources)
	}
	if resp.ByService["spotify"].Status != LibraryStatusOK || resp.ByService["spotify"].Count != 2 {
		t.Fatalf("spotify status = %+v", resp.ByService["spotify"])
	}
	if resp.ByService["youtube-music"].Status != LibraryStatusOK || resp.ByService["youtube-music"].Count != 2 {
		t.Fatalf("youtube-music status = %+v", resp.ByService["youtube-music"])
	}
}

func TestHandleLibraryTracks_UnauthorizedService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeLikedTrackProvider{
			tracks: []Track{
				{Name: "Solo", Sources: []TrackSource{{Service: "spotify", ID: "sp1"}}},
			},
		},
		"youtube-music": &fakeLikedTrackProvider{err: ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if resp.ByService["youtube-music"].Status != LibraryStatusUnauthorized {
		t.Fatalf("youtube-music status = %+v", resp.ByService["youtube-music"])
	}
}

func TestHandleLibraryTracks_PermissionDenied(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: Permissions{"tracks": AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeLikedTrackProvider{
			tracks: []Track{{Name: "Solo", Sources: []TrackSource{{Service: "spotify", ID: "sp1"}}}},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
}

func TestPreRequest_LikedSongsWithoutServiceName(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": NewSpotifyConnector(srv),
	}

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{RequestID: "req-liked", LoginName: "alice"},
		UserMessage: "What songs have I liked?",
		RequestBody: mustJSON(t, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "What songs have I liked?"}},
		}),
	}

	req := httptest.NewRequest("POST", "/v1/pre-request", strings.NewReader(string(mustJSON(t, payload))))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != ActionModify {
		t.Fatalf("action = %s, want modify", resp.Action)
	}
	if !strings.Contains(string(resp.RequestBody), "/v1/library/tracks") {
		t.Fatalf("request_body missing tracks library endpoint: %s", resp.RequestBody)
	}
}

func TestSpotifyConnector_ListUserLikedTracks_MockAPI(t *testing.T) {
	var mock *httptest.Server
	mock = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/me/tracks" {
			w.Write([]byte(`{
				"items":[
					{"track":{"id":"t1","name":"Blinding Lights","uri":"spotify:track:t1",
						"artists":[{"name":"The Weeknd"}],"album":{"id":"al1","name":"After Hours"}}},
					{"track":{"id":"t2","name":"Levitating","uri":"spotify:track:t2",
						"artists":[{"name":"Dua Lipa"}],"album":{"id":"al2","name":"Future Nostalgia"}}}
				],
				"next":null,
				"total":2
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mock.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mock.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	})
	conn := NewSpotifyConnector(srv)

	tracks, _, err := conn.ListUserLikedTracks(context.Background())
	if err != nil {
		t.Fatalf("ListUserLikedTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(tracks))
	}
	if tracks[0].Name != "Blinding Lights" {
		t.Fatalf("first = %+v", tracks[0])
	}
	if len(tracks[0].Artists) == 0 || tracks[0].Artists[0].Name != "The Weeknd" {
		t.Fatalf("artists = %+v", tracks[0].Artists)
	}
	if tracks[0].Album == nil || tracks[0].Album.Name != "After Hours" {
		t.Fatalf("album = %+v", tracks[0].Album)
	}
	if len(tracks[0].Sources) != 1 || tracks[0].Sources[0].Service != "spotify" {
		t.Fatalf("sources = %+v", tracks[0].Sources)
	}
}

func TestYouTubeConnector_ListUserLikedTracks_MockAPI(t *testing.T) {
	var mock *httptest.Server
	mock = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/channels":
			w.Write([]byte(`{"items":[{"id":"ch1","contentDetails":{"relatedPlaylists":{"likes":"LL123"}}}]}`))
		case "/playlistItems":
			w.Write([]byte(`{
				"items":[
					{"snippet":{"title":"Video Song","channelTitle":"Some Artist","publishedAt":"2024-01-01T00:00:00Z",
						"resourceId":{"videoId":"vid1"}},"contentDetails":{"videoId":"vid1"}}
				],
				"nextPageToken":"",
				"pageInfo":{"totalResults":1}
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mock.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mock.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("youtube-music", &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	})
	conn := NewYouTubeMusicConnector(srv)

	tracks, _, err := conn.ListUserLikedTracks(context.Background())
	if err != nil {
		t.Fatalf("ListUserLikedTracks: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0].Name != "Video Song" {
		t.Fatalf("name = %q", tracks[0].Name)
	}
	if len(tracks[0].Artists) == 0 || tracks[0].Artists[0].Name != "Some Artist" {
		t.Fatalf("artists = %+v", tracks[0].Artists)
	}
	if len(tracks[0].Sources) != 1 || tracks[0].Sources[0].ID != "vid1" {
		t.Fatalf("sources = %+v", tracks[0].Sources)
	}
}

func TestMergeTracksSync_DifferentArtistsDoNotMerge(t *testing.T) {
	merged := MergeTracksSync([]Track{
		{
			Name:    "Home",
			Artists: []Artist{newArtist("spotify", "Spotify", "", "Artist A")},
			Sources: []TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "1"}},
		},
		{
			Name:    "Home",
			Artists: []Artist{newArtist("youtube-music", "YouTube Music", "", "Artist B")},
			Sources: []TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "2"}},
		},
	})
	if len(merged) != 2 {
		t.Fatalf("got %d tracks, want 2 (same title, different artists)", len(merged))
	}
}

type fakeAlbumProvider struct {
	albums []Album
	err    error
}

func (f *fakeAlbumProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (f *fakeAlbumProvider) SystemInstructions(string) string  { return "fake" }
func (f *fakeAlbumProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeAlbumProvider) ListUserSavedAlbums(context.Context) ([]Album, bool, error) {
	return f.albums, false, f.err
}

type fakeArtistProvider struct {
	artists []Artist
	err     error
}

func (f *fakeArtistProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (f *fakeArtistProvider) SystemInstructions(string) string  { return "fake" }
func (f *fakeArtistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeArtistProvider) ListUserFollowedArtists(context.Context) ([]Artist, bool, error) {
	return f.artists, false, f.err
}

func TestHandleLibraryAlbums_AggregatesAndMerges(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeAlbumProvider{
			albums: []Album{
				{
					Name:    "After Hours",
					Artists: []Artist{newArtist("spotify", "Spotify", "", "The Weeknd")},
					Sources: []AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "al1"}},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryAlbums)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp AlbumList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || resp.Albums[0].Name != "After Hours" {
		t.Fatalf("resp = %+v", resp)
	}
	if !resp.Synced {
		t.Fatal("expected synced=true")
	}
}

func TestHandleLibraryAlbums_PermissionDenied(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Permissions: Permissions{"albums": AccessNone}},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeAlbumProvider{albums: []Album{{Name: "X", Sources: []AlbumSource{{Service: "spotify", ID: "1"}}}}},
	}

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryAlbums)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandleLibraryArtists_AggregatesAndMerges(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeArtistProvider{
			artists: []Artist{
				newArtist("spotify", "Spotify", "sp1", "The Weeknd"),
			},
		},
		"youtube-music": &fakeArtistProvider{
			artists: []Artist{
				newArtist("youtube-music", "YouTube Music", "ch1", "The Weeknd"),
				newArtist("youtube-music", "YouTube Music", "ch2", "Dua Lipa"),
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryArtists)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp ArtistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The Weeknd merges across services; Dua Lipa is solo → 2 artists.
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2; artists=%+v", resp.Total, resp.Artists)
	}
	var weeknd *Artist
	for i := range resp.Artists {
		if resp.Artists[i].Name == "The Weeknd" {
			weeknd = &resp.Artists[i]
		}
	}
	if weeknd == nil || len(weeknd.Sources) != 2 {
		t.Fatalf("The Weeknd = %+v, want 2 sources", weeknd)
	}
}

func TestPreRequest_AlbumsWithoutServiceName(t *testing.T) {
	cfg := &Config{
		Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{"spotify": NewSpotifyConnector(srv)}

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{RequestID: "req-alb", LoginName: "alice"},
		UserMessage: "What albums have I saved?",
		RequestBody: mustJSON(t, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "What albums have I saved?"}},
		}),
	}
	req := httptest.NewRequest("POST", "/v1/pre-request", strings.NewReader(string(mustJSON(t, payload))))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != ActionModify {
		t.Fatalf("action = %s, want modify", resp.Action)
	}
	if !strings.Contains(string(resp.RequestBody), "/v1/library/albums") {
		t.Fatalf("request_body missing albums library endpoint: %s", resp.RequestBody)
	}
}

func TestSpotifyConnector_ListUserSavedAlbums_MockAPI(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/me/albums" {
			w.Write([]byte(`{"items":[{"album":{"id":"al1","name":"After Hours","uri":"spotify:album:al1",
				"artists":[{"name":"The Weeknd"}]}}],"next":null,"total":1}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mock.Close()

	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mock.URL}}}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	albums, _, err := NewSpotifyConnector(srv).ListUserSavedAlbums(context.Background())
	if err != nil {
		t.Fatalf("ListUserSavedAlbums: %v", err)
	}
	if len(albums) != 1 || albums[0].Name != "After Hours" {
		t.Fatalf("albums = %+v", albums)
	}
	if len(albums[0].Artists) == 0 || albums[0].Artists[0].Name != "The Weeknd" {
		t.Fatalf("artists = %+v", albums[0].Artists)
	}
}

func TestSpotifyConnector_ListUserFollowedArtists_MockAPI(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/me/following" {
			w.Write([]byte(`{"artists":{"items":[{"id":"ar1","name":"The Weeknd"}],"next":null}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mock.Close()

	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mock.URL}}}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	artists, _, err := NewSpotifyConnector(srv).ListUserFollowedArtists(context.Background())
	if err != nil {
		t.Fatalf("ListUserFollowedArtists: %v", err)
	}
	if len(artists) != 1 || artists[0].Name != "The Weeknd" {
		t.Fatalf("artists = %+v", artists)
	}
	if len(artists[0].Sources) != 1 || artists[0].Sources[0].ID != "ar1" {
		t.Fatalf("sources = %+v", artists[0].Sources)
	}
}

func TestYouTubeConnector_ListUserFollowedArtists_MockAPI(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/subscriptions" {
			w.Write([]byte(`{"items":[{"snippet":{"title":"Some Artist",
				"resourceId":{"channelId":"UCabc"}}}],"nextPageToken":""}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mock.Close()

	cfg := &Config{Services: []Service{{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mock.URL}}}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("youtube-music", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	artists, _, err := NewYouTubeMusicConnector(srv).ListUserFollowedArtists(context.Background())
	if err != nil {
		t.Fatalf("ListUserFollowedArtists: %v", err)
	}
	if len(artists) != 1 || artists[0].Name != "Some Artist" {
		t.Fatalf("artists = %+v", artists)
	}
	if artists[0].Sources[0].ID != "UCabc" {
		t.Fatalf("sources = %+v", artists[0].Sources)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
