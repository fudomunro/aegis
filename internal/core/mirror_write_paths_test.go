package core

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
)

func writeMirrorJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestWriteMirrorUpstreamError_AndUnauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	logger := slog.Default()

	t.Run("unauthorized sentinel", func(t *testing.T) {
		w := httptest.NewRecorder()
		srv.library().WriteMirrorUpstreamError(w, logger, "spotify", kinds.ErrUnauthorized)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "/v1/auth/spotify/login") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})

	t.Run("string unauthorized", func(t *testing.T) {
		w := httptest.NewRecorder()
		srv.library().WriteMirrorUpstreamError(w, logger, "youtube-music", errUnauthorizedString())
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("code = %d", w.Code)
		}
	})

	t.Run("generic upstream failure", func(t *testing.T) {
		w := httptest.NewRecorder()
		srv.library().WriteMirrorUpstreamError(w, logger, "spotify", errBoom())
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "mirror failed on spotify") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
}

func TestErrorsIsUnauthorized(t *testing.T) {
	if !library.ErrorsIsUnauthorized(kinds.ErrUnauthorized) {
		t.Fatal("sentinel should match")
	}
	if !library.ErrorsIsUnauthorized(errUnauthorizedString()) {
		t.Fatal("string unauthorized should match")
	}
	if library.ErrorsIsUnauthorized(nil) || library.ErrorsIsUnauthorized(errBoom()) {
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
	srv.AuthMiddleware(srv.library().HandleLibraryMirror)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var res library.MirrorResult
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
	srv.AuthMiddleware(srv.library().HandleLibraryMirror)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func errUnauthorizedString() error { return errString("upstream returned unauthorized") }
func errBoom() error               { return errString("upstream exploded") }

type simpleError string

func (e simpleError) Error() string { return string(e) }

func errString(s string) error { return simpleError(s) }
