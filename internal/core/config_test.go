package core

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.json")

	cfg := `{
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"endpoint": "https://api.example.com/spotify",
				"cache_ttl": "5m"
			}
		]
	}`
	os.WriteFile(cfgPath, []byte(cfg), 0644)

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(loaded.Services) != 1 {
		t.Fatalf("got %d services, want 1", len(loaded.Services))
	}
	svc := loaded.Services[0]
	if svc.ID != "spotify" {
		t.Fatalf("ID = %q, want %q", svc.ID, "spotify")
	}
	if svc.Name != "Spotify" {
		t.Fatalf("Name = %q, want %q", svc.Name, "Spotify")
	}
	if svc.CacheTTL.Duration != 5*time.Minute {
		t.Fatalf("cache_ttl = %v, want 5m", svc.CacheTTL.Duration)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.json")
	os.WriteFile(cfgPath, []byte("{bad}"), 0644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFindMatchingServices_ExactName(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}

	matched := cfg.FindMatchingServices("Play some jazz on Spotify")
	if len(matched) != 1 || matched[0].ID != "spotify" {
		t.Fatalf("expected spotify, got %v", matched)
	}
}

func TestFindMatchingServices_CaseInsensitive(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}

	matched := cfg.FindMatchingServices("play some SPOTIFY music")
	if len(matched) != 1 {
		t.Fatalf("expected case-insensitive match, got %d", len(matched))
	}
}

func TestFindMatchingServices_Alias(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "ytm", Name: "YouTube Music", Aliases: []string{"yt music", "ytmusic"}, Action: "inject"},
		},
	}

	matched := cfg.FindMatchingServices("queue up yt music playlist")
	if len(matched) != 1 || matched[0].ID != "ytm" {
		t.Fatalf("expected ytm via alias, got %v", matched)
	}
}

func TestFindMatchingServices_Substring(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}

	// "Spotify" appears as a substring within "Spotify's"
	matched := cfg.FindMatchingServices("check Spotify's top charts")
	if len(matched) != 1 {
		t.Fatalf("expected substring match, got %d", len(matched))
	}
}

func TestFindMatchingServices_NoMatch(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "ytm", Name: "YouTube Music", Action: "inject"},
		},
	}

	matched := cfg.FindMatchingServices("play some local files")
	if len(matched) != 0 {
		t.Fatalf("expected no matches, got %v", matched)
	}
}

func TestFindMatchingServices_MultipleMatches(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "ytm", Name: "YouTube Music", Action: "inject"},
		},
	}

	matched := cfg.FindMatchingServices("compare my Spotify and YouTube Music playlists")
	if len(matched) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matched))
	}
}

func TestGetService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify"},
			{ID: "ytm", Name: "YouTube Music"},
		},
	}

	svc := cfg.GetService("spotify")
	if svc == nil {
		t.Fatal("expected to find spotify")
	}
	if svc.Name != "Spotify" {
		t.Fatalf("Name = %q, want %q", svc.Name, "Spotify")
	}

	svc = cfg.GetService("missing")
	if svc != nil {
		t.Fatal("expected nil for missing service")
	}
}

func TestConfig_Reload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "reload.json")

	initial := `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`
	os.WriteFile(cfgPath, []byte(initial), 0644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].ID != "spotify" {
		t.Fatal("initial config wrong")
	}

	updated := `{"services":[{"id":"ytm","name":"YouTube Music","action":"block"}]}`
	os.WriteFile(cfgPath, []byte(updated), 0644)

	if err := cfg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].ID != "ytm" {
		t.Fatalf("after reload: services = %v", cfg.Services)
	}
}

func TestDuration_UnmarshalJSON(t *testing.T) {
	var d Duration
	if err := d.UnmarshalJSON([]byte(`"10m"`)); err != nil {
		t.Fatal(err)
	}
	if d.Duration != 10*time.Minute {
		t.Fatalf("got %v, want 10m", d.Duration)
	}

	if err := d.UnmarshalJSON([]byte(`"bad"`)); err == nil {
		t.Fatal("expected error for bad duration")
	}
}

func TestDuration_MarshalJSON(t *testing.T) {
	d := Duration{3 * time.Hour}
	b, err := d.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"3h0m0s"` {
		t.Fatalf("got %s, want %q", string(b), "3h0m0s")
	}
}

func TestGetService_ReturnsSnapshot(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Permissions: types.Permissions{"player": types.AccessNone}},
		},
	}

	svc := cfg.GetService("spotify")
	if svc == nil {
		t.Fatal("expected service")
	}
	// Mutating the snapshot must not touch the live store.
	svc.Endpoint = "https://mutated.example"
	svc.Permissions["player"] = types.AccessFull

	live := cfg.GetService("spotify")
	if live.Endpoint != "" {
		t.Fatalf("Endpoint leaked to store: %q", live.Endpoint)
	}
	if live.Permissions.For("player") != types.AccessNone {
		t.Fatalf("Permissions mutated via snapshot: %q", live.Permissions.For("player"))
	}

	if !cfg.updateService("spotify", func(s *Service) { s.Endpoint = "https://mock.example" }) {
		t.Fatal("updateService should find spotify")
	}
	if got := cfg.GetService("spotify").Endpoint; got != "https://mock.example" {
		t.Fatalf("Endpoint after updateService = %q", got)
	}
	if cfg.updateService("missing", func(*Service) {}) {
		t.Fatal("updateService should report missing id")
	}
}

// Issue #95: concurrent admin permission writes must not race proxy/library
// readers that snapshot services.
func TestConfig_ConcurrentSetServicePermissionsAndReads(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Permissions: types.Permissions{types.PermissionDefaultKey: types.AccessRead, "player": types.AccessNone}},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Permissions: types.Permissions{types.PermissionDefaultKey: types.AccessRead}},
		},
	}
	permsPath := filepath.Join(t.TempDir(), "permissions.json")
	if err := cfg.InitPermissionsStore(permsPath); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			p := types.Permissions{types.PermissionDefaultKey: types.AccessRead, "player": types.AccessNone, "tracks": types.AccessNone}
			if i%2 == 0 {
				p = types.Permissions{"player": types.AccessNone, "default": types.AccessRead, "playlists": types.AccessRead}
			}
			if err := cfg.SetServicePermissions("spotify", p); err != nil {
				t.Errorf("SetServicePermissions: %v", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			svc := cfg.GetService("spotify")
			if svc == nil {
				t.Error("GetService returned nil")
				return
			}
			_ = svc.Permissions.AllowsRead("tracks")
			_ = svc.Permissions.AccessForDataType("")
			_ = svc.Endpoint
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			for _, svc := range cfg.ServiceSnapshots() {
				_ = svc.Permissions.AllowsRead("tracks")
				_ = svc.IsCanonicalFor("playlists")
			}
			_ = srv.libraryReadableFor("tracks")
			_ = srv.canonicalServiceName("playlists")
			_ = srv.defaultMirrorSource("playlists")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			matched := cfg.FindMatchingServices("play something on spotify")
			for _, svc := range matched {
				_ = svc.Permissions.For("player")
			}
			_ = cfg.ServiceCount()
		}
	}()

	wg.Wait()
}
