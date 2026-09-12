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

func (f *fakePlaylistProvider) ListUserPlaylists(context.Context) ([]Playlist, error) {
	return f.playlists, f.err
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

	playlists, err := conn.ListUserPlaylists(context.Background())
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

	_, err := conn.ListUserPlaylists(context.Background())
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

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
