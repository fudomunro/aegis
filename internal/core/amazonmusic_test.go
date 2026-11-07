package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"encoding/json"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAmazonMusicConnector_HandleProxy_NoToken(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "amazon-music", Name: "Amazon Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"amazon-music": connectors.NewAmazonMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/amazon-music/playlists/pl1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAmazonMusicConnector_HandleProxy_WithMockAPI(t *testing.T) {
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-amz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/me":
			io.WriteString(w, `{"id":"amz1","name":"Alice","customer_id":"C1"}`)
		case "/v1/search":
			io.WriteString(w, `{"tracks":{"items":[{"id":"t1","name":"So What","artist":"Miles Davis"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockAPI.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "amazon-music", Name: "Amazon Music", Action: "inject", Endpoint: mockAPI.URL + "/v1"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("amazon-music", &tokens.OAuthToken{
		AccessToken: "mock-amz-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"amazon-music": connectors.NewAmazonMusicConnector(srv),
	}

	// Connector-level /me and /search (not agent API under issue #119).
	req := httptest.NewRequest("GET", "/v1/services/amazon-music/me", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.connectors["amazon-music"].HandleProxy(w, req, "/me")

	if w.Code != http.StatusOK {
		body := w.Body.String()
		t.Fatalf("status = %d, want 200, body: %s", w.Code, body)
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	if result["id"] != "amz1" {
		t.Fatalf("id = %v", result["id"])
	}
	if result["name"] != "Alice" {
		t.Fatalf("name = %v", result["name"])
	}

	// Test /search via connector.
	req = httptest.NewRequest("GET", "/v1/services/amazon-music/search?q=miles+davis", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()

	srv.connectors["amazon-music"].HandleProxy(w, req, "/search")

	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d, want 200", w.Code)
	}

	var searchResult map[string]any
	json.NewDecoder(w.Body).Decode(&searchResult)

	tracks, ok := searchResult["tracks"].([]any)
	if !ok || len(tracks) == 0 {
		t.Fatal("expected tracks in search response")
	}
}

func TestAmazonMusicConnector_HandleProxy_UpstreamDown(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "amazon-music", Name: "Amazon Music", Action: "inject", Endpoint: "http://127.0.0.1:1"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("amazon-music", &tokens.OAuthToken{
		AccessToken: "token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"amazon-music": connectors.NewAmazonMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/amazon-music/playlists/pl1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}
