package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
)

// hygieneConfig writes a bootstrap config and loads it, for the race tests
// below.
func hygieneConfig(t *testing.T) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "aegis.json")
	body := `{
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"endpoint": "https://api.example.com/spotify",
				"grant_limits": {"tracks": "read"}
			}
		],
		"hook": {"skip_patterns": ["^zzz"]}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg, path
}

// rewriteConfig swaps the file contents so Reload has something new to load.
func rewriteConfig(t *testing.T, path string, i int) {
	t.Helper()
	body := fmt.Sprintf(`{
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"endpoint": "https://api.example.com/spotify",
				"grant_limits": {"tracks": %q}
			}
		],
		"hook": {"skip_patterns": ["^zzz%d"]}
	}`, map[bool]string{true: "read", false: "read-write"}[i%2 == 0], i)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// DataGrants used to hand live map references past RUnlock, so a reload
// racing a read was a data race. It now copies under the lock and the
// materialized rule map is derived from the copy. Run under -race: this is
// the regression test.
func TestEffectiveAccessors_SafeUnderReload(t *testing.T) {
	cfg, path := hygieneConfig(t)
	cfg.SeedDataGrant("tracks", policy.DataGrant{Default: "read"})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = cfg.DataGrants().Levels().Level("tracks")
				_ = cfg.DataGrants().Levels().Level("")
			}
		}()
	}
	// SeedDataGrant mutates typePolicies in place — the one writer that is not
	// replace-on-write. Hammering it concurrently with the readers above is
	// what makes a live map escape observable: if those readers hold the map
	// reference across RUnlock, -race fires here.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
				}
				cfg.SeedDataGrant(fmt.Sprintf("seeded-%d-%d", base, j),
					policy.DataGrant{Default: "read"})
			}
		}(i)
	}

	for i := 0; i < 20; i++ {
		rewriteConfig(t, path, i)
		if err := cfg.Reload(); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Reload(%d): %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

// shouldSkipMatching read hookSkip without the lock Reload writes it under —
// a SIGHUP during live hook traffic. Run under -race.
func TestShouldSkipMatching_SafeUnderReload(t *testing.T) {
	cfg, path := hygieneConfig(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = cfg.shouldSkipMatching("zzz skip this please")
				_ = cfg.shouldSkipMatching("ordinary user question")
				_ = cfg.shouldSkipMatching("here is main.go and a code fence")
			}
		}()
	}

	for i := 0; i < 20; i++ {
		rewriteConfig(t, path, i)
		if err := cfg.Reload(); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Reload(%d): %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

// A reloaded hook config is what matching should see — the lock must not
// make matching observe a stale skip set forever.
func TestShouldSkipMatching_SeesReload(t *testing.T) {
	cfg, path := hygieneConfig(t)
	if reason := cfg.shouldSkipMatching("zzz hello"); reason != "skip_pattern" {
		t.Fatalf("reason = %q, want skip_pattern", reason)
	}
	body := `{
		"services": [
			{"id": "spotify", "name": "Spotify", "action": "inject",
			 "endpoint": "https://api.example.com/spotify"}
		],
		"hook": {"skip_patterns": ["^qqq"]}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if reason := cfg.shouldSkipMatching("zzz hello"); reason != "" {
		t.Fatalf("after reload reason = %q, want empty (pattern changed)", reason)
	}
	if reason := cfg.shouldSkipMatching("qqq hello"); reason != "skip_pattern" {
		t.Fatalf("after reload reason = %q, want skip_pattern", reason)
	}
}

// The tokens-first write order is deliberate (issue #71): a crash between the
// two stores must leave an orphan secret, never an entry pointing at a missing
// one. This pins the observable end state — both stores land together on
// success — and the comment at the call site pins the order.
func TestSetServiceConnection_WritesSecretAndEntry(t *testing.T) {
	cfg, _ := hygieneConfig(t)
	cfg.secrets = mustTokens(t)
	cfg.svcConnPath = filepath.Join(t.TempDir(), "service_connections.json")
	cfg.svcConnOverlay = map[string]serviceConnectionEntry{}

	if err := cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Endpoint: "http://immich.example.com",
		Headers:  map[string]string{"X-API-Key": "hunter2"},
	}); err != nil {
		t.Fatalf("SetServiceConnection: %v", err)
	}

	// The header secret must be in the encrypted store, not the JSON file.
	raw := cfg.secrets.GetSecret(tokens.ServiceHeadersSecretKey("immich"))
	if !strings.Contains(raw, "hunter2") {
		t.Fatalf("secret blob = %q, want it to carry the X-API-Key value", raw)
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("secret blob is not a headers map: %v", err)
	}
	if stored["X-API-Key"] != "hunter2" {
		t.Fatalf("stored header = %q, want hunter2", stored["X-API-Key"])
	}
	fileRaw, err := os.ReadFile(cfg.svcConnPath)
	if err != nil {
		t.Fatalf("read service_connections.json: %v", err)
	}
	if strings.Contains(string(fileRaw), "hunter2") {
		t.Fatal("the plaintext secret must not land in service_connections.json")
	}
	var f serviceConnectionsFile
	if err := json.Unmarshal(fileRaw, &f); err != nil {
		t.Fatalf("service_connections.json is not valid JSON: %v", err)
	}
	if got, ok := f.Services["immich"]; !ok || got.Endpoint != "http://immich.example.com" {
		t.Fatalf("entry = %+v, want the immich endpoint saved", got)
	}
}
