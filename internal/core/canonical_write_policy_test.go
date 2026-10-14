package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestWritePolicyNotice_HomeAndSecondary(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID:           "spotify",
				Name:         "Spotify",
				Action:       "inject",
				CanonicalFor: []string{"playlists"},
				Permissions:  types.Permissions{"playlists": types.AccessWrite, "tracks": types.AccessWrite},
			},
			{
				ID:          "youtube-music",
				Name:        "YouTube Music",
				Action:      "inject",
				Permissions: types.Permissions{"playlists": types.AccessWrite, "tracks": types.AccessRead},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	home := srv.writePolicyNotice(cfg.Services[0])
	if !strings.Contains(home, "canonical home for playlists") {
		t.Fatalf("home notice missing: %s", home)
	}
	if !strings.Contains(home, "Spotify") {
		t.Fatalf("home notice should name Spotify: %s", home)
	}
	// tracks has no configured home — should not mention tracks.
	if strings.Contains(home, "liked tracks") || strings.Contains(home, "tracks") {
		// "playlists" is fine; bare "tracks" should not appear as a policy line.
		for _, line := range strings.Split(home, "\n") {
			if strings.Contains(line, "tracks") && !strings.Contains(line, "playlists") {
				t.Fatalf("unexpected tracks line: %s", line)
			}
		}
	}

	sec := srv.writePolicyNotice(cfg.Services[1])
	if !strings.Contains(sec, "prefer Spotify") {
		t.Fatalf("secondary should prefer home: %s", sec)
	}
	if !strings.Contains(sec, "not auto-synced") {
		t.Fatalf("secondary should say not auto-synced: %s", sec)
	}
	// youtube tracks is read-only — no tracks mutation line.
	if strings.Contains(sec, "liked tracks mutations") {
		t.Fatalf("read-only tracks should not get write policy: %s", sec)
	}
}

func TestWritePolicyNotice_EmptyWithoutWriteOrHome(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Permissions: types.Permissions{"default": types.AccessRead}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	if got := srv.writePolicyNotice(cfg.Services[0]); got != "" {
		t.Fatalf("expected empty notice, got %q", got)
	}
}

func TestLibraryInstructions_MultiSourceMutationRule(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	instr := srv.libraryInstructionsFor("playlists")
	if !strings.Contains(instr, "Multi-source mutation") {
		t.Fatalf("missing multi-source rule: %s", instr)
	}
	if !strings.Contains(instr, "does not auto-mirror") {
		t.Fatalf("missing auto-mirror disclaimer: %s", instr)
	}
}

func TestPreRequest_InjectsWritePolicy(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID:           "spotify",
				Name:         "Spotify",
				Aliases:      []string{"spotify music"},
				Action:       "inject",
				CanonicalFor: []string{"playlists"},
				Permissions:  types.Permissions{"playlists": types.AccessWrite},
				Endpoint:     "https://api.spotify.com/v1",
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	body, _ := json.Marshal(map[string]any{
		"metadata":     map[string]any{"request_id": "req-wp", "login_name": "alice", "model": "gpt-4"},
		"user_message": "add a song to my Spotify playlist",
		"request_body": map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		},
	})
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandlePreRequest(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != ActionModify {
		t.Fatalf("action = %q, want modify", resp.Action)
	}
	var modified struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(resp.RequestBody, &modified); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(modified.Messages) == 0 {
		t.Fatal("no messages in modified body")
	}
	content := modified.Messages[0].Content
	if !strings.Contains(content, "Write policy") {
		t.Fatalf("expected write policy injection: %s", content)
	}
	if !strings.Contains(content, "canonical home for playlists") {
		t.Fatalf("expected home write hint: %s", content)
	}
}

func TestWritePolicyNotices_DedupesIdentical(t *testing.T) {
	// Two services with the same write relationship produce the same notice
	// text only when they share name/home — build one service twice via matched slice.
	svc := Service{
		ID:          "spotify",
		Name:        "Spotify",
		Action:      "inject",
		Permissions: types.Permissions{"playlists": types.AccessWrite},
	}
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists"}, Permissions: types.Permissions{"playlists": types.AccessWrite}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	notices := srv.writePolicyNotices([]Service{svc, svc})
	if len(notices) != 1 {
		t.Fatalf("expected 1 deduped notice, got %d: %#v", len(notices), notices)
	}
}
