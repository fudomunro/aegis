package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/tokens"
)

func TestSpotifyConnector_HandleProxy_NoToken(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/spotify/playlists/pl1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestSpotifyConnector_HandleProxy_WithMockSpotify(t *testing.T) {
	// Mock Spotify API.
	mockSpotify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/playlists/pl1":
			io.WriteString(w, `{"id":"pl1","name":"Jazz","tracks":{"total":5,"items":[{"track":{"id":"t1","name":"So What","artists":[{"name":"Miles Davis"}]}}]},"uri":"spotify:playlist:pl1"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockSpotify.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mockSpotify.URL + "/v1"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	// Store a mock token.
	srv.tokens.Save("spotify", &tokens.OAuthToken{
		AccessToken: "mock-token",
		Expiry:      futureTime(),
	})

	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
	}

	// Playlist content path — allowed under issue #119.
	req := httptest.NewRequest("GET", "/v1/services/spotify/playlists/pl1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		body := w.Body.String()
		t.Fatalf("status = %d, want 200, body: %s", w.Code, body)
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	if result["id"] != "pl1" && result["name"] != "Jazz" {
		t.Fatalf("playlist transform missing fields: %v", result)
	}
}

func futureTime() time.Time {
	return time.Now().Add(time.Hour)
}
