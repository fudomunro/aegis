package core

import (
	"github.com/fudomunro/aegis/internal/core/tokens"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cover core's connection write seams and the starter-env doc pin.
// Immich's setup contract lives beside the connector
// (internal/connectors/immich_onboarding_test.go); the VIP-Service wizard is
// covered in immich_tailnet_guide_test.go (issue #265).

func TestServiceConnectionsOverlay_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	bootstrap := `{
  "services": [
    {
      "id": "google-photos",
      "name": "Google Photos",
      "action": "inject",
      "canonical_for": ["photos"]
    }
  ]
}`
	if err := os.WriteFile(cfgPath, []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	svcConnPath := filepath.Join(dir, "service_connections.json")
	secrets := mustSecretStore(t, filepath.Join(dir, "tokens.json"))
	if err := cfg.InitServiceConnectionsStore(svcConnPath, secrets); err != nil {
		t.Fatalf("InitServiceConnectionsStore: %v", err)
	}

	err = cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Name:         "Immich",
		Action:       "inject",
		Endpoint:     "http://photos.test:2283/api",
		Headers:      map[string]string{"X-API-Key": "secret-key"},
		CanonicalFor: []string{"photos"},
		CacheTTL:     "5m",
	})
	if err != nil {
		t.Fatalf("SetServiceConnection: %v", err)
	}

	// Claim moves to Immich; Google Photos loses photos.
	for _, svc := range cfg.ServiceSnapshots() {
		if svc.ID == "immich" {
			if !svc.IsCanonicalFor("photos") {
				t.Fatalf("immich should own photos: %+v", svc.CanonicalFor)
			}
			if svc.Endpoint != "http://photos.test:2283/api" {
				t.Fatalf("endpoint = %q", svc.Endpoint)
			}
			if !svc.HasStaticAPIKey() {
				t.Fatal("expected static API key")
			}
		}
		if svc.ID == "google-photos" && svc.IsCanonicalFor("photos") {
			t.Fatal("google-photos should not still claim photos")
		}
	}

	// Overlay file must not contain the API key (issue #184).
	raw, err := os.ReadFile(svcConnPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-key") {
		t.Fatal("service_connections.json must not contain plaintext credentials")
	}
	if !secrets.HasSecret(tokens.ServiceHeadersSecretKey("immich")) {
		t.Fatal("expected encrypted header secret in token store")
	}

	// Reload from disk keeps the overlay; secrets come from the token store.
	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig reload: %v", err)
	}
	if err := fresh.InitServiceConnectionsStore(svcConnPath, mustSecretStore(t, filepath.Join(dir, "tokens.json"))); err != nil {
		t.Fatalf("reload InitServiceConnectionsStore: %v", err)
	}
	immich := fresh.GetService("immich")
	if immich == nil {
		t.Fatal("immich missing after reload")
	}
	if immich.Endpoint != "http://photos.test:2283/api" {
		t.Fatalf("reload endpoint = %q", immich.Endpoint)
	}
	if !immich.IsCanonicalFor("photos") {
		t.Fatal("reload lost canonical claim")
	}
	if gp := fresh.GetService("google-photos"); gp != nil && gp.IsCanonicalFor("photos") {
		t.Fatal("reload restored google-photos claim")
	}
	if immich.Headers["X-API-Key"] != "secret-key" {
		t.Fatal("reload lost API key header")
	}
}

func TestSetServiceConnection_ClearCanonical(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := os.WriteFile(cfgPath, []byte(`{"services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json"), mustSecretStore(t, filepath.Join(dir, "tokens.json"))); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Endpoint:     "http://x/api",
		CanonicalFor: []string{"photos"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetServiceConnection("immich", serviceConnectionEntry{
		Endpoint:          "http://x/api",
		ClearCanonicalFor: true,
	}); err != nil {
		t.Fatal(err)
	}
	svc := cfg.GetService("immich")
	if svc == nil || svc.IsCanonicalFor("photos") {
		t.Fatalf("claim should be cleared: %+v", svc)
	}
}

func TestImmichStarterEnv_File(t *testing.T) {
	data, err := os.ReadFile("../../docs/immich-starter.env")
	if err != nil {
		t.Fatalf("read starter env: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"UPLOAD_LOCATION=./library",
		"DB_DATA_LOCATION=./postgres",
		"IMMICH_VERSION=v3",
		"DB_PASSWORD=postgres",
		"DB_USERNAME=postgres",
		"DB_DATABASE_NAME=immich",
		"named .env",
		"ERR_CONNECTION_REFUSED",
		"CREATE DATABASE immich",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("starter env missing %q", want)
		}
	}
	if strings.Count(s, "DB_DATABASE_NAME=") != 1 {
		t.Fatal("DB_DATABASE_NAME should appear once")
	}
}
