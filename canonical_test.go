package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceIsCanonicalFor(t *testing.T) {
	svc := Service{ID: "spotify", Action: "inject", CanonicalFor: []string{"playlists", "Tracks"}}
	if !svc.IsCanonicalFor("playlists") {
		t.Fatal("expected playlists")
	}
	if !svc.IsCanonicalFor("tracks") {
		t.Fatal("expected case-insensitive tracks")
	}
	if svc.IsCanonicalFor("photos") {
		t.Fatal("photos should not be claimed")
	}
	blocked := Service{ID: "tidal", Action: "block", CanonicalFor: []string{"playlists"}}
	if blocked.IsCanonicalFor("playlists") {
		t.Fatal("blocked services must not be canonical")
	}
}

func TestValidateCanonicalFor_DuplicateAndUnknown(t *testing.T) {
	err := validateCanonicalFor([]Service{
		{ID: "spotify", Action: "inject", CanonicalFor: []string{"playlists"}},
		{ID: "youtube-music", Action: "inject", CanonicalFor: []string{"playlists"}},
	})
	if err == nil || !strings.Contains(err.Error(), "playlists") {
		t.Fatalf("expected duplicate claim error, got %v", err)
	}

	err = validateCanonicalFor([]Service{
		{ID: "spotify", Action: "inject", CanonicalFor: []string{"not-a-kind"}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown canonical_for") {
		t.Fatalf("expected unknown kind error, got %v", err)
	}

	// Blocked second claim is fine.
	err = validateCanonicalFor([]Service{
		{ID: "spotify", Action: "inject", CanonicalFor: []string{"playlists"}},
		{ID: "tidal", Action: "block", CanonicalFor: []string{"playlists"}},
	})
	if err != nil {
		t.Fatalf("blocked claim should be allowed: %v", err)
	}
}

func TestLoadConfig_NormalizesCanonicalFor(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.json")
	body := `{
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"canonical_for": [" playlists ", "playlists", "", "tracks"]
			}
		]
	}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got := cfg.Services[0].CanonicalFor
	if len(got) != 2 || got[0] != "playlists" || got[1] != "tracks" {
		t.Fatalf("canonical_for = %v", got)
	}
}

func TestCanonicalServiceHelpers(t *testing.T) {
	p := Playlist{Sources: []PlaylistSource{
		{Service: "youtube-music"},
		{Service: "spotify", Canonical: true},
	}}
	if p.CanonicalService() != "spotify" {
		t.Fatalf("playlist CanonicalService = %q", p.CanonicalService())
	}
	tr := Track{Sources: []TrackSource{{Service: "a"}, {Service: "b", Canonical: true}}}
	if tr.CanonicalService() != "b" {
		t.Fatalf("track CanonicalService = %q", tr.CanonicalService())
	}
	c := Contact{Sources: []ContactSource{{Service: "google"}, {Service: "outlook", Canonical: true}}}
	if c.CanonicalService() != "outlook" {
		t.Fatalf("contact CanonicalService = %q", c.CanonicalService())
	}
	ph := Photo{Sources: []PhotoSource{{Service: "immich"}, {Service: "google-photos", Canonical: true}}}
	if ph.CanonicalService() != "google-photos" {
		t.Fatalf("photo CanonicalService = %q", ph.CanonicalService())
	}
	// Fallback to first source when none marked.
	plain := Playlist{Sources: []PlaylistSource{{Service: "spotify"}}}
	if plain.CanonicalService() != "spotify" {
		t.Fatalf("fallback CanonicalService = %q", plain.CanonicalService())
	}
}

func TestMergePlaylistsSync_CanonicalSortsFirst(t *testing.T) {
	merged := MergePlaylistsSync([]Playlist{
		{Name: "Mix", Sources: []PlaylistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "y1"}}},
		{Name: "Mix", Sources: []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "s1", Canonical: true}}},
	})
	if len(merged) != 1 {
		t.Fatalf("merged len = %d", len(merged))
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
	if merged[0].Sources[0].Service != "spotify" || !merged[0].Sources[0].Canonical {
		t.Fatalf("canonical should sort first: %+v", merged[0].Sources)
	}
}

func TestMergeTracksSync_CanonicalSortsFirst(t *testing.T) {
	merged := MergeTracksSync([]Track{
		{Name: "Song", Artists: []Artist{{Name: "A"}}, Sources: []TrackSource{{Service: "youtube-music", ServiceName: "YT"}}},
		{Name: "Song", Artists: []Artist{{Name: "A"}}, Sources: []TrackSource{{Service: "spotify", ServiceName: "Spotify", Canonical: true}}},
	})
	if len(merged) != 1 || len(merged[0].Sources) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged[0].Sources[0].Service != "spotify" || !merged[0].Sources[0].Canonical {
		t.Fatalf("canonical track source should sort first: %+v", merged[0].Sources)
	}
}

func TestMergeContacts_CanonicalSortsFirst(t *testing.T) {
	merged := MergeContacts([]Contact{
		{Name: "Alex", Emails: []string{"alex@example.com"},
			Sources: []ContactSource{{Service: "outlook", ServiceName: "Outlook"}}},
		{Name: "Alex", Emails: []string{"alex@example.com"},
			Sources: []ContactSource{{Service: "google", ServiceName: "Google Contacts", Canonical: true}}},
	})
	if len(merged) != 1 || len(merged[0].Sources) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged[0].Sources[0].Service != "google" || !merged[0].Sources[0].Canonical {
		t.Fatalf("canonical contact source should sort first: %+v", merged[0].Sources)
	}
}

type twoSourcePlaylistProvider struct{}

func (twoSourcePlaylistProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (twoSourcePlaylistProvider) SystemInstructions(string) string  { return "fake" }
func (twoSourcePlaylistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}
func (twoSourcePlaylistProvider) ListUserPlaylists(context.Context) ([]Playlist, bool, error) {
	return []Playlist{{
		Name:       "Shared Mix",
		TrackCount: 3,
		Sources:    []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}},
	}}, false, nil
}

type youtubePlaylistProvider struct{}

func (youtubePlaylistProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (youtubePlaylistProvider) SystemInstructions(string) string  { return "fake" }
func (youtubePlaylistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}
func (youtubePlaylistProvider) ListUserPlaylists(context.Context) ([]Playlist, bool, error) {
	return []Playlist{{
		Name:       "Shared Mix",
		TrackCount: 5,
		Sources:    []PlaylistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt1"}},
	}}, false, nil
}

func TestLibraryPlaylists_ConfigCanonicalForStampsAndSorts(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists"}},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify":       twoSourcePlaylistProvider{},
		"youtube-music": youtubePlaylistProvider{},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1 merged playlist", resp.Total)
	}
	p := resp.Playlists[0]
	if p.CanonicalService() != "spotify" {
		t.Fatalf("CanonicalService = %q, sources=%+v", p.CanonicalService(), p.Sources)
	}
	if len(p.Sources) != 2 || !p.Sources[0].Canonical || p.Sources[0].Service != "spotify" {
		t.Fatalf("expected spotify canonical first: %+v", p.Sources)
	}
	if p.Sources[1].Canonical {
		t.Fatalf("youtube should not be canonical: %+v", p.Sources[1])
	}
}

func TestLibraryInstructions_MentionCanonical(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"tracks"}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	instr := srv.libraryInstructionsFor("tracks")
	if !strings.Contains(instr, "canonical") {
		t.Fatalf("instructions should mention canonical: %s", instr)
	}
	if !strings.Contains(instr, "Spotify") || !strings.Contains(instr, "home copy") {
		t.Fatalf("instructions should name configured home service: %s", instr)
	}
}

func TestAdminSummary_IncludesCanonicalFor(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists"}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	w := httptest.NewRecorder()
	srv.HandleAdminSummary(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var summary AdminSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(summary.Services) != 1 {
		t.Fatalf("services = %d", len(summary.Services))
	}
	if len(summary.Services[0].CanonicalFor) != 1 || summary.Services[0].CanonicalFor[0] != "playlists" {
		t.Fatalf("canonical_for = %v", summary.Services[0].CanonicalFor)
	}
}

func TestAdminHTML_ShowsCanonicalBadges(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	body := w.Body.String()
	for _, want := range []string{"canonical_for", "hasCanonicalSource", "Authoritative non-Aegis location"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin HTML missing %q", want)
		}
	}
}

func TestFlowTopology_NotesConfiguredCanonical(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists"}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin/flow", nil)
	w := httptest.NewRecorder()
	srv.HandleAdminFlow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Nodes []FlowNode `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, n := range payload.Nodes {
		if n.ID != "svc:spotify" {
			continue
		}
		for _, note := range n.Notes {
			if strings.Contains(note, "Configured canonical for playlists") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected canonical note on spotify node: %+v", payload.Nodes)
	}
}
