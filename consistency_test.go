package main

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestWithPeerLog_IncludesIdentity(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	r = withPeerIdentity(r, &PeerIdentity{
		LoginName: "alice@example.com",
		Source:    "serve-headers",
	})

	logger := withPeerLog(slog.Default(), r)
	if logger == nil {
		t.Fatal("expected logger")
	}
	// slog.Logger does not expose attrs; ensure no panic and that a non-nil
	// identity path is exercised. Mismatch helper is tested separately.
}

func TestWithPeerLog_NoIdentityIsNoop(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	if withPeerLog(slog.Default(), r) == nil {
		t.Fatal("expected logger")
	}
}

func TestWarnIdentityMismatch_LogsWhenDifferent(t *testing.T) {
	// Ensure the comparison branches without requiring log capture:
	// equal → no action; different → warn path runs (no panic).
	r := httptest.NewRequest("POST", "/v1/pre-request", nil)
	r = withPeerIdentity(r, &PeerIdentity{LoginName: "bob@example.com", Source: "whois"})
	warnIdentityMismatch(slog.Default(), "alice@example.com", r)
	warnIdentityMismatch(slog.Default(), "bob@example.com", r)
	warnIdentityMismatch(slog.Default(), "", r)
}

func TestConnectorInstructions_DoNotRequireAPIKey(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	connectors := []struct {
		name string
		svc  ServiceConnector
	}{
		{"spotify", NewSpotifyConnector(srv)},
		{"youtube-music", NewYouTubeMusicConnector(srv)},
		{"amazon-music", NewAmazonMusicConnector(srv)},
	}
	for _, c := range connectors {
		instr := c.svc.SystemInstructions("https://aegis.example.com")
		if strings.Contains(instr, "all require Authorization header") {
			t.Errorf("%s: still claims Authorization header is required", c.name)
		}
		if !strings.Contains(instr, "Aegis authenticates you") {
			t.Errorf("%s: missing Aegis-authenticates wording", c.name)
		}
		if !strings.Contains(instr, "/v1/library/playlists") {
			t.Errorf("%s: missing library endpoint mention", c.name)
		}
	}
}

func TestCapabilities_PlaylistFieldsUseSources(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	check := func(name string, caps types.ServiceCapabilities) {
		t.Helper()
		for _, dt := range caps.DataTypes {
			if dt.Name != "playlists" {
				continue
			}
			for _, op := range dt.Operations {
				desc := strings.ToLower(op.Response.Description)
				if strings.Contains(desc, "id, name") && !strings.Contains(desc, "sources") {
					t.Errorf("%s.%s: description still lists top-level id/service: %s", name, op.Name, op.Response.Description)
				}
				for _, f := range op.Response.Fields {
					if f == "service" || f == "id" {
						t.Errorf("%s.%s: field %q is service-specific; expect sources[]", name, op.Name, f)
					}
				}
			}
			return
		}
		t.Errorf("%s: no playlists data type", name)
	}
	check("spotify", NewSpotifyConnector(srv).Capabilities())
	check("youtube-music", NewYouTubeMusicConnector(srv).Capabilities())
}
