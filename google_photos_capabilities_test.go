package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestGooglePhotosConnector_DeleteSession_Success(t *testing.T) {
	var deleteAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/sessions/sess-del" {
			http.NotFound(w, r)
			return
		}
		deleteAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "google-photos", Name: "Google Photos", Action: "inject", Endpoint: upstream.URL,
	}}}, "https://aegis.example.com")
	srv.tokens.Save("google-photos", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	conn := NewGooglePhotosConnector(srv)

	req := httptest.NewRequest(http.MethodDelete, "/picker/sessions/sess-del", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/sessions/sess-del")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if deleteAuth != "Bearer tok" {
		t.Fatalf("auth = %q", deleteAuth)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "deleted" || body["session_id"] != "sess-del" {
		t.Fatalf("body = %+v", body)
	}
}

func TestGooglePhotosConnector_DeleteSession_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    403,
				"message": "insufficient scopes",
				"status":  "PERMISSION_DENIED",
			},
		})
	}))
	t.Cleanup(upstream.Close)

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "google-photos", Name: "Google Photos", Action: "inject", Endpoint: upstream.URL,
	}}}, "https://aegis.example.com")
	srv.tokens.Save("google-photos", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	conn := NewGooglePhotosConnector(srv)

	req := httptest.NewRequest(http.MethodDelete, "/picker/sessions/sess-x", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/sessions/sess-x")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "insufficient scopes") ||
		!strings.Contains(w.Body.String(), "re-authorize") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestGooglePhotosConnector_DeleteSession_UpstreamDown(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "google-photos", Name: "Google Photos", Action: "inject", Endpoint: "http://127.0.0.1:1",
	}}}, "https://aegis.example.com")
	srv.tokens.Save("google-photos", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	conn := NewGooglePhotosConnector(srv)

	req := httptest.NewRequest(http.MethodDelete, "/picker/sessions/sess-x", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/sessions/sess-x")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestGooglePhotosConnector_HandleProxy_SessionRouting(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "google-photos", Name: "Google Photos", Action: "inject",
	}}}, "https://aegis.example.com")
	conn := NewGooglePhotosConnector(srv)

	// No token → 401
	req := httptest.NewRequest(http.MethodPost, "/picker/sessions", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/sessions")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d", w.Code)
	}

	srv.tokens.Save("google-photos", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	// Wrong method on create
	req = httptest.NewRequest(http.MethodGet, "/picker/sessions", nil)
	w = httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/sessions")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("create method status = %d", w.Code)
	}

	// Missing session id (trailing slashes only)
	req = httptest.NewRequest(http.MethodDelete, "/picker/sessions//", nil)
	w = httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/sessions//")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing id status = %d body=%s", w.Code, w.Body.String())
	}

	// Nested path in session id
	req = httptest.NewRequest(http.MethodDelete, "/picker/sessions/a/b", nil)
	w = httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/sessions/a/b")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("nested id status = %d", w.Code)
	}

	// Bad method on session
	req = httptest.NewRequest(http.MethodPatch, "/picker/sessions/s1", nil)
	w = httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/sessions/s1")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("session method status = %d", w.Code)
	}

	// mediaItems wrong method
	req = httptest.NewRequest(http.MethodPost, "/picker/mediaItems", nil)
	w = httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/mediaItems")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mediaItems method status = %d", w.Code)
	}

	// mediaItems missing session
	req = httptest.NewRequest(http.MethodGet, "/picker/mediaItems", nil)
	w = httptest.NewRecorder()
	conn.HandleProxy(w, req, "/picker/mediaItems")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("mediaItems missing session status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestWriteGooglePhotosUpstreamError(t *testing.T) {
	t.Run("google error message", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeGooglePhotosUpstreamError(w, []byte(`{"error":{"code":400,"message":"session not ready","status":"FAILED_PRECONDITION"}}`), http.StatusBadRequest)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("code = %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "session not ready") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
	t.Run("scope keyword triggers reauth hint", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeGooglePhotosUpstreamError(w, []byte(`{"error":{"message":"Request had insufficient authentication scopes."}}`), http.StatusUnauthorized)
		if !strings.Contains(w.Body.String(), "re-authorize") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
	t.Run("invalid body uses default", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeGooglePhotosUpstreamError(w, []byte("not-json"), http.StatusBadGateway)
		if !strings.Contains(w.Body.String(), "Google Photos request failed") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
}

func TestGoogleContactsConnector_Capabilities(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := NewGoogleContactsConnector(srv)
	caps := conn.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "contacts" {
		t.Fatalf("caps = %+v", caps)
	}
	ops := caps.DataTypes[0].Operations
	if len(ops) < 2 {
		t.Fatalf("ops = %+v", ops)
	}
	names := map[string]bool{}
	for _, op := range ops {
		names[op.Name] = true
	}
	if !names["read_list"] || !names["get_by_id"] {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestImmichConnector_Capabilities(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := NewImmichConnector(srv)
	caps := conn.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "photos" {
		t.Fatalf("caps = %+v", caps)
	}
	ops := caps.DataTypes[0].Operations
	names := map[string]bool{}
	for _, op := range ops {
		names[op.Name] = true
		if op.Name == "get_asset_original" && op.Path != "/assets/{id}/original" {
			t.Fatalf("path = %q", op.Path)
		}
	}
	for _, want := range []string{"list_assets", "get_asset_original", "get_asset_thumbnail", "list_albums"} {
		if !names[want] {
			t.Fatalf("missing op %s in %+v", want, ops)
		}
	}
}

func TestAlbumServiceName_AndPhotosInstructions(t *testing.T) {
	if types.AlbumServiceName(types.Album{}) != "" {
		t.Fatal("empty album should have empty service name")
	}
	got := types.AlbumServiceName(types.Album{Sources: []types.AlbumSource{{Service: "spotify", ServiceName: "Spotify"}}})
	if got != "Spotify" {
		t.Fatalf("types.AlbumServiceName = %q", got)
	}

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	instr := srv.libraryInstructionsFor("photos")
	if !strings.Contains(instr, "/v1/library/photos") {
		t.Fatalf("photos instructions missing endpoint: %s", instr)
	}

	if !LibraryKindByID("photos").Mentions("what photos do I have?") {
		t.Fatal("expected photo mention")
	}
	if LibraryKindByID("photos").Mentions("play my jazz playlists") {
		t.Fatal("playlists should not trigger photos hint")
	}
}
