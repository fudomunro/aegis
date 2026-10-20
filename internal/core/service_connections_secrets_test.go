package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenStore_SecretsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	ts, err := newTestTokenStore(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.SetSecret(ServiceHeadersSecretKey("immich"), `{"X-API-Key":"round-trip"}`); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetSecret(TailscaleAPIKeySecretKey(), "tskey-api-round-trip"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := newTestTokenStore(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.GetSecret(ServiceHeadersSecretKey("immich")); got != `{"X-API-Key":"round-trip"}` {
		t.Fatalf("headers secret = %q", got)
	}
	if got := reloaded.GetSecret(TailscaleAPIKeySecretKey()); got != "tskey-api-round-trip" {
		t.Fatalf("tailscale secret = %q", got)
	}

	// Ciphertext at rest must not contain the plaintext values (issue #184).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "round-trip") || strings.Contains(string(raw), "tskey-api-round-trip") {
		t.Fatal("secrets written in plaintext to tokens.json")
	}
}

func TestTokenStore_SecretsIncludedInRekey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	ts, err := newTestTokenStore(path, "old-pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.SetSecret(ServiceHeadersSecretKey("immich"), "stay-secret"); err != nil {
		t.Fatal(err)
	}
	if err := ts.ChangePassphrase("new-pass"); err != nil {
		t.Fatal(err)
	}

	if _, err := newTestTokenStore(path, "old-pass"); err == nil {
		t.Fatal("old passphrase should not unlock rekeyed store")
	}
	reloaded, err := newTestTokenStore(path, "new-pass")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.GetSecret(ServiceHeadersSecretKey("immich")); got != "stay-secret" {
		t.Fatalf("secret after rekey = %q", got)
	}
}

func TestServiceConnections_MigratesPlaintextSecrets(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	svcConnPath := filepath.Join(dir, "service_connections.json")
	tokensPath := filepath.Join(dir, "tokens.json")
	if err := os.WriteFile(cfgPath, []byte(`{"services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Legacy plaintext overlay (version 1) with secrets inline.
	legacy := `{
  "version": 1,
  "services": {
    "immich": {
      "endpoint": "http://immich.local/api",
      "headers": {"X-API-Key": "plain-key"},
      "canonical_for": ["photos"]
    }
  },
  "tailscale": {
    "api_key": "tskey-api-plain",
    "tailnet": "-"
  }
}`
	if err := os.WriteFile(svcConnPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	secrets := mustSecretStore(t, tokensPath)
	if err := cfg.InitServiceConnectionsStore(svcConnPath, secrets); err != nil {
		t.Fatal(err)
	}

	// In-memory credentials still work.
	svc := cfg.GetService("immich")
	if svc == nil || svc.Headers["X-API-Key"] != "plain-key" {
		t.Fatalf("migrated headers = %+v", svc)
	}
	if cfg.TailscaleAdminAPIKey() != "tskey-api-plain" {
		t.Fatalf("tailscale key = %q", cfg.TailscaleAdminAPIKey())
	}

	// Plaintext is gone from service_connections.json.
	raw, err := os.ReadFile(svcConnPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "plain-key") || strings.Contains(string(raw), "tskey-api-plain") {
		t.Fatalf("plaintext secrets remain after migration: %s", raw)
	}

	// Encrypted store holds them.
	if secrets.GetSecret(ServiceHeadersSecretKey("immich")) == "" {
		t.Fatal("headers not in token store")
	}
	if secrets.GetSecret(TailscaleAPIKeySecretKey()) != "tskey-api-plain" {
		t.Fatal("tailscale key not in token store")
	}

	// Reload with a fresh store at the same path + passphrase restores secrets.
	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.InitServiceConnectionsStore(svcConnPath, mustSecretStore(t, tokensPath)); err != nil {
		t.Fatal(err)
	}
	if got := fresh.GetService("immich"); got == nil || got.Headers["X-API-Key"] != "plain-key" {
		t.Fatalf("reload headers = %+v", got)
	}
	if fresh.TailscaleAdminAPIKey() != "tskey-api-plain" {
		t.Fatal("reload lost tailscale key")
	}
}

func TestServiceConnections_WrongPassphraseRefuses(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	svcConnPath := filepath.Join(dir, "service_connections.json")
	tokensPath := filepath.Join(dir, "tokens.json")
	if err := os.WriteFile(cfgPath, []byte(`{"services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Seed an encrypted secret under the correct passphrase.
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	secrets := mustSecretStore(t, tokensPath)
	if err := cfg.InitServiceConnectionsStore(svcConnPath, secrets); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Endpoint: "http://immich.local/api",
		Headers:  map[string]string{"X-API-Key": "locked"},
	}); err != nil {
		t.Fatal(err)
	}

	// Wrong passphrase must refuse to load the token store (matches tokens.json).
	if _, err := newTokenStore(tokensPath, "wrong-pass", 1_000); err == nil {
		t.Fatal("wrong passphrase must refuse to open token store")
	}

	// Correct passphrase still works.
	ok, err := newTestTokenStore(tokensPath, "test-pass")
	if err != nil {
		t.Fatal(err)
	}
	if ok.GetSecret(ServiceHeadersSecretKey("immich")) == "" {
		t.Fatal("correct passphrase lost secrets")
	}
}

func TestSetServiceConnection_RequiresSecretStoreForHeaders(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := os.WriteFile(cfgPath, []byte(`{"services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json"), nil); err != nil {
		t.Fatal(err)
	}
	err = cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Endpoint: "http://immich.local/api",
		Headers:  map[string]string{"X-API-Key": "nope"},
	})
	if err == nil {
		t.Fatal("saving headers without an encrypted store must fail")
	}

	// Non-secret fields still work without a store.
	if err := cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Endpoint: "http://immich.local/api",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveServiceConnections_StripsSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service_connections.json")
	ov := &serviceConnectionsFile{
		Version: serviceConnectionsFileVersion,
		Services: map[string]serviceConnectionEntry{
			"immich": {
				Endpoint: "http://x/api",
				Headers:  map[string]string{"X-API-Key": "never-disk"},
			},
		},
		Tailscale: &tailscaleAdminEntry{APIKey: "tskey-api-never", Tailnet: "-"},
	}
	if err := saveServiceConnections(path, ov); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "never-disk") || strings.Contains(string(raw), "tskey-api-never") {
		t.Fatalf("saveServiceConnections wrote secrets: %s", raw)
	}
	var disk serviceConnectionsFile
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Version != serviceConnectionsFileVersion {
		t.Fatalf("version = %d", disk.Version)
	}
	if disk.Services["immich"].Endpoint != "http://x/api" {
		t.Fatalf("endpoint lost: %+v", disk.Services["immich"])
	}
	if disk.Tailscale == nil || disk.Tailscale.Tailnet != "-" {
		t.Fatalf("tailscale non-secret lost: %+v", disk.Tailscale)
	}
}

func TestResolveStatePaths_ServiceConnections(t *testing.T) {
	dir := t.TempDir()
	paths, err := ResolveStatePaths(StatePathOverrides{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "service_connections.json")
	if paths.ServiceConnections != want {
		t.Fatalf("ServiceConnections = %q, want %q", paths.ServiceConnections, want)
	}

	custom := filepath.Join(dir, "custom-svc.json")
	paths, err = ResolveStatePaths(StatePathOverrides{Dir: dir, ServiceConnections: custom})
	if err != nil {
		t.Fatal(err)
	}
	if paths.ServiceConnections != custom {
		t.Fatalf("ServiceConnections override = %q", paths.ServiceConnections)
	}
}
