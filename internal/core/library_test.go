package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

type fakePlaylistProvider struct {
	playlists []kinds.Playlist
	err       error
}

func (f *fakePlaylistProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakePlaylistProvider) SystemInstructions(string) string { return "fake" }
func (f *fakePlaylistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakePlaylistProvider) ListUserPlaylists(context.Context) ([]kinds.Playlist, bool, error) {
	return f.playlists, false, f.err
}

func TestHandleLibraryPlaylists_AggregatesServices(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []kinds.Playlist{
				{
					Name:       "Jazz Vibes",
					TrackCount: 10,
					Sources:    []kinds.PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}},
				},
			},
		},
		"youtube-music": &fakePlaylistProvider{
			playlists: []kinds.Playlist{
				{
					Name:       "Focus Mix",
					TrackCount: 25,
					Sources:    []kinds.PlaylistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt1"}},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("playlists"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp kinds.PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2", resp.Total)
	}
	if len(resp.Playlists) != 2 {
		t.Fatalf("got %d playlists, want 2", len(resp.Playlists))
	}
	if resp.ByService["spotify"].Status != kinds.LibraryStatusOK || resp.ByService["spotify"].Count != 1 {
		t.Fatalf("spotify status = %+v", resp.ByService["spotify"])
	}
	if resp.ByService["youtube-music"].Status != kinds.LibraryStatusOK || resp.ByService["youtube-music"].Count != 1 {
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []kinds.Playlist{
				{
					Name:    "Jazz",
					Sources: []kinds.PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}},
				},
			},
		},
		"youtube-music": &fakePlaylistProvider{err: kinds.ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("playlists"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp kinds.PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if resp.ByService["youtube-music"].Status != kinds.LibraryStatusUnauthorized {
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakePlaylistProvider{err: errors.New("upstream boom")},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("playlists"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp kinds.PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != kinds.LibraryStatusError {
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	})
	conn := connectors.NewSpotifyConnector(srv)

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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	_, _, err := conn.ListUserPlaylists(context.Background())
	if !errors.Is(err, kinds.ErrUnauthorized) {
		t.Fatalf("err = %v, want kinds.ErrUnauthorized", err)
	}
}

func TestHandleLibraryPlaylists_PermissionDenied(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"playlists": {Default: shared.AccessNone}},

		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []kinds.Playlist{{Name: "Jazz", Sources: []kinds.PlaylistSource{{Service: "spotify", ID: "sp1"}}}},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("playlists"))(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "denied: data grant") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

type fakeLikedTrackProvider struct {
	tracks []kinds.Track
	err    error
}

func (f *fakeLikedTrackProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakeLikedTrackProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeLikedTrackProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeLikedTrackProvider) ListUserLikedTracks(context.Context) ([]kinds.Track, bool, error) {
	return f.tracks, false, f.err
}

func TestHandleLibraryTracks_AggregatesAndMerges(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeLikedTrackProvider{
			tracks: []kinds.Track{
				{
					Name:    "Blinding Lights",
					Artists: []kinds.Artist{kinds.NewArtist("spotify", "Spotify", "", "The Weeknd")},
					Sources: []kinds.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-bl"}},
				},
				{
					Name:    "Solo Spotify Song",
					Artists: []kinds.Artist{kinds.NewArtist("spotify", "Spotify", "", "Artist A")},
					Sources: []kinds.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-solo"}},
				},
			},
		},
		"youtube-music": &fakeLikedTrackProvider{
			tracks: []kinds.Track{
				{
					Name:    "Blinding Lights",
					Artists: []kinds.Artist{kinds.NewArtist("youtube-music", "YouTube Music", "", "The Weeknd")},
					Sources: []kinds.TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-bl"}},
				},
				{
					Name:    "YouTube Only Hit",
					Artists: []kinds.Artist{kinds.NewArtist("youtube-music", "YouTube Music", "", "Artist B")},
					Sources: []kinds.TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-only"}},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("tracks"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp kinds.TrackList
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

	var blinding *kinds.Track
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
	if resp.ByService["spotify"].Status != kinds.LibraryStatusOK || resp.ByService["spotify"].Count != 2 {
		t.Fatalf("spotify status = %+v", resp.ByService["spotify"])
	}
	if resp.ByService["youtube-music"].Status != kinds.LibraryStatusOK || resp.ByService["youtube-music"].Count != 2 {
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeLikedTrackProvider{
			tracks: []kinds.Track{
				{Name: "Solo", Sources: []kinds.TrackSource{{Service: "spotify", ID: "sp1"}}},
			},
		},
		"youtube-music": &fakeLikedTrackProvider{err: kinds.ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("tracks"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp kinds.TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if resp.ByService["youtube-music"].Status != kinds.LibraryStatusUnauthorized {
		t.Fatalf("youtube-music status = %+v", resp.ByService["youtube-music"])
	}
}

func TestHandleLibraryTracks_PermissionDenied(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"tracks": {Default: shared.AccessNone}},

		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeLikedTrackProvider{
			tracks: []kinds.Track{{Name: "Solo", Sources: []kinds.TrackSource{{Service: "spotify", ID: "sp1"}}}},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("tracks"))(w, req)

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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
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
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	})
	conn := connectors.NewSpotifyConnector(srv)

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
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		case "/videos":
			w.Write([]byte(`{
				"items":[
					{"id":"vid1","snippet":{"title":"Video Song","channelTitle":"Some Artist","categoryId":"10"}}
				]
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &tokens.OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	})
	conn := connectors.NewYouTubeMusicConnector(srv)

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

func TestYouTubeConnector_ListUserLikedTracks_FiltersNonMusic(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/channels":
			w.Write([]byte(`{"items":[{"id":"ch1","contentDetails":{"relatedPlaylists":{"likes":"LL123"}}}]}`))
		case "/playlistItems":
			w.Write([]byte(`{
				"items":[
					{"snippet":{"title":"So What","channelTitle":"Miles Davis - Topic","publishedAt":"2024-01-01T00:00:00Z",
						"resourceId":{"videoId":"vid_music"}},"contentDetails":{"videoId":"vid_music"}},
					{"snippet":{"title":"Official Music Video","channelTitle":"Some Band","publishedAt":"2024-01-02T00:00:00Z",
						"resourceId":{"videoId":"vid_mvid"}},"contentDetails":{"videoId":"vid_mvid"}},
					{"snippet":{"title":"How to cook pasta","channelTitle":"Cooking Channel","publishedAt":"2024-01-03T00:00:00Z",
						"resourceId":{"videoId":"vid_tutorial"}},"contentDetails":{"videoId":"vid_tutorial"}},
					{"snippet":{"title":"Deleted clip","channelTitle":"Someone","publishedAt":"2024-01-04T00:00:00Z",
						"resourceId":{"videoId":"vid_deleted"}},"contentDetails":{"videoId":"vid_deleted"}}
				],
				"nextPageToken":"",
				"pageInfo":{"totalResults":4}
			}`))
		case "/videos":
			// Deleted/private video id is omitted from videos.list.
			w.Write([]byte(`{
				"items":[
					{"id":"vid_music","snippet":{"title":"So What","channelTitle":"Miles Davis - Topic","categoryId":"24"}},
					{"id":"vid_mvid","snippet":{"title":"Official Music Video","channelTitle":"Some Band","categoryId":"10"}},
					{"id":"vid_tutorial","snippet":{"title":"How to cook pasta","channelTitle":"Cooking Channel","categoryId":"26"}}
				]
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &tokens.OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	})
	conn := connectors.NewYouTubeMusicConnector(srv)

	tracks, _, err := conn.ListUserLikedTracks(context.Background())
	if err != nil {
		t.Fatalf("ListUserLikedTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2 (music + Topic); tracks=%+v", len(tracks), tracks)
	}
	names := map[string]bool{}
	for _, tr := range tracks {
		names[tr.Name] = true
	}
	if !names["So What"] || !names["Official Music Video"] {
		t.Fatalf("track names = %v", names)
	}
	if names["How to cook pasta"] || names["Deleted clip"] {
		t.Fatalf("non-music/deleted leaked into tracks: %v", names)
	}
}

func TestIsMusicShapedYouTubeVideo(t *testing.T) {
	cases := []struct {
		categoryID string
		channel    string
		want       bool
	}{
		{"10", "Some Band", true},
		{"24", "Miles Davis - Topic", true},
		{"26", "Cooking Channel", false},
		{"10", "", true},
		{"", "Artist - Topic", true},
		{"", "Vlogger", false},
	}
	for _, tc := range cases {
		if got := connectors.IsMusicShapedYouTubeVideo(tc.categoryID, tc.channel); got != tc.want {
			t.Fatalf("connectors.IsMusicShapedYouTubeVideo(%q, %q) = %v, want %v", tc.categoryID, tc.channel, got, tc.want)
		}
	}
}

func TestMergeTracksSync_DifferentArtistsDoNotMerge(t *testing.T) {
	merged := kinds.MergeTracksSync([]kinds.Track{
		{
			Name:    "Home",
			Artists: []kinds.Artist{kinds.NewArtist("spotify", "Spotify", "", "Artist A")},
			Sources: []kinds.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "1"}},
		},
		{
			Name:    "Home",
			Artists: []kinds.Artist{kinds.NewArtist("youtube-music", "YouTube Music", "", "Artist B")},
			Sources: []kinds.TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "2"}},
		},
	})
	if len(merged) != 2 {
		t.Fatalf("got %d tracks, want 2 (same title, different artists)", len(merged))
	}
}

type fakeAlbumProvider struct {
	albums []kinds.Album
	err    error
}

func (f *fakeAlbumProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakeAlbumProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeAlbumProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeAlbumProvider) ListUserSavedAlbums(context.Context) ([]kinds.Album, bool, error) {
	return f.albums, false, f.err
}

type fakeArtistProvider struct {
	artists []kinds.Artist
	err     error
}

func (f *fakeArtistProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakeArtistProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeArtistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeArtistProvider) ListUserFollowedArtists(context.Context) ([]kinds.Artist, bool, error) {
	return f.artists, false, f.err
}

func TestHandleLibraryAlbums_AggregatesAndMerges(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeAlbumProvider{
			albums: []kinds.Album{
				{
					Name:    "After Hours",
					Artists: []kinds.Artist{kinds.NewArtist("spotify", "Spotify", "", "The Weeknd")},
					Sources: []kinds.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "al1"}},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("albums"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp kinds.AlbumList
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
		typePolicies: policy.DataGrants{"albums": {Default: shared.AccessNone}},
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeAlbumProvider{albums: []kinds.Album{{Name: "X", Sources: []kinds.AlbumSource{{Service: "spotify", ID: "1"}}}}},
	}

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("albums"))(w, req)
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeArtistProvider{
			artists: []kinds.Artist{
				kinds.NewArtist("spotify", "Spotify", "sp1", "The Weeknd"),
			},
		},
		"youtube-music": &fakeArtistProvider{
			artists: []kinds.Artist{
				kinds.NewArtist("youtube-music", "YouTube Music", "ch1", "The Weeknd"),
				kinds.NewArtist("youtube-music", "YouTube Music", "ch2", "Dua Lipa"),
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("artists"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp kinds.ArtistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The Weeknd merges across services; Dua Lipa is solo → 2 artists.
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2; artists=%+v", resp.Total, resp.Artists)
	}
	var weeknd *kinds.Artist
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"spotify": connectors.NewSpotifyConnector(srv)}

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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	albums, _, err := connectors.NewSpotifyConnector(srv).ListUserSavedAlbums(context.Background())
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	artists, _, err := connectors.NewSpotifyConnector(srv).ListUserFollowedArtists(context.Background())
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
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	artists, _, err := connectors.NewYouTubeMusicConnector(srv).ListUserFollowedArtists(context.Background())
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
