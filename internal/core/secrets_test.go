package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSecret_LiteralUnchanged(t *testing.T) {
	for _, v := range []string{"", "plain-secret", "$ENV", "ENV:FOO", "$env:FOO", "${FOO", "prefix-$ENV:FOO"} {
		if got := resolveSecret("field", v); got != v {
			t.Fatalf("resolveSecret(%q) = %q, want literal %q", v, got, v)
		}
	}
}

func TestResolveSecret_EnvColonForm(t *testing.T) {
	t.Setenv("AEGIS_TEST_SECRET_COLON", "from-env")
	if got := resolveSecret("field", "$ENV:AEGIS_TEST_SECRET_COLON"); got != "from-env" {
		t.Fatalf("got %q", got)
	}
	if got := resolveSecret("field", "$ENV: AEGIS_TEST_SECRET_COLON "); got != "from-env" {
		t.Fatalf("whitespace name got %q", got)
	}
}

func TestResolveSecret_EnvBraceForm(t *testing.T) {
	t.Setenv("AEGIS_TEST_SECRET_BRACE", "brace-secret")
	if got := resolveSecret("field", "${AEGIS_TEST_SECRET_BRACE}"); got != "brace-secret" {
		t.Fatalf("got %q", got)
	}
	if got := resolveSecret("field", "${ AEGIS_TEST_SECRET_BRACE }"); got != "brace-secret" {
		t.Fatalf("whitespace name got %q", got)
	}
}

func TestResolveSecret_MissingEnvYieldsEmpty(t *testing.T) {
	if got := resolveSecret("connections.x.client_secret", "$ENV:AEGIS_TEST_SECRET_MISSING"); got != "" {
		t.Fatalf("missing env got %q, want empty", got)
	}
	if got := resolveSecret("connections.x.client_secret", "${AEGIS_TEST_SECRET_MISSING}"); got != "" {
		t.Fatalf("missing brace env got %q, want empty", got)
	}
}

func TestResolveSecret_EmptyEnvYieldsEmpty(t *testing.T) {
	t.Setenv("AEGIS_TEST_SECRET_EMPTY", "")
	if got := resolveSecret("field", "$ENV:AEGIS_TEST_SECRET_EMPTY"); got != "" {
		t.Fatalf("empty env got %q, want empty", got)
	}
}

func TestResolveSecret_EmptyNameIsLiteral(t *testing.T) {
	if got := resolveSecret("field", "$ENV:"); got != "$ENV:" {
		t.Fatalf("got %q", got)
	}
	if got := resolveSecret("field", "${}"); got != "${}" {
		t.Fatalf("got %q", got)
	}
}

func TestSecretRefName(t *testing.T) {
	name, ok := secretRefName("$ENV:FOO")
	if !ok || name != "FOO" {
		t.Fatalf("colon form = %q %v", name, ok)
	}
	name, ok = secretRefName("${FOO}")
	if !ok || name != "FOO" {
		t.Fatalf("brace form = %q %v", name, ok)
	}
	if _, ok := secretRefName("literal"); ok {
		t.Fatal("literal must not be a ref")
	}
	if _, ok := secretRefName("$ENV:"); ok {
		t.Fatal("empty name must not be a ref")
	}
	if _, ok := secretRefName("${}"); ok {
		t.Fatal("empty brace name must not be a ref")
	}
}

func TestResolveConfigSecrets_ConnectionsAndHeaders(t *testing.T) {
	t.Setenv("AEGIS_TEST_SPOTIFY_SECRET", "spot-from-env")
	t.Setenv("AEGIS_TEST_IMMICH_KEY", "immich-from-env")

	cfg := &Config{
		Connections: map[string]Connection{
			"spotify": {
				ClientID:     "cid",
				ClientSecret: "$ENV:AEGIS_TEST_SPOTIFY_SECRET",
			},
			"google": {
				ClientID:     "gid",
				ClientSecret: "literal-google-secret",
			},
		},
		Services: []Service{
			{
				ID: "immich",
				Headers: map[string]string{
					"X-API-Key": "${AEGIS_TEST_IMMICH_KEY}",
					"X-Other":   "literal-header",
				},
			},
		},
	}
	resolveConfigSecrets(cfg)

	if cfg.Connections["spotify"].ClientSecret != "spot-from-env" {
		t.Fatalf("spotify secret = %q", cfg.Connections["spotify"].ClientSecret)
	}
	if cfg.Connections["google"].ClientSecret != "literal-google-secret" {
		t.Fatalf("google secret = %q", cfg.Connections["google"].ClientSecret)
	}
	if cfg.Services[0].Headers["X-API-Key"] != "immich-from-env" {
		t.Fatalf("immich key = %q", cfg.Services[0].Headers["X-API-Key"])
	}
	if cfg.Services[0].Headers["X-Other"] != "literal-header" {
		t.Fatalf("literal header = %q", cfg.Services[0].Headers["X-Other"])
	}
}

func TestResolveServiceConnectionSecrets(t *testing.T) {
	t.Setenv("AEGIS_TEST_SVC_CONN_KEY", "overlay-key")
	services := map[string]serviceConnectionEntry{
		"immich": {
			Headers: map[string]string{
				"X-API-Key": "$ENV:AEGIS_TEST_SVC_CONN_KEY",
				"X-Literal": "keep",
			},
		},
	}
	resolveServiceConnectionSecrets(services)
	if services["immich"].Headers["X-API-Key"] != "overlay-key" {
		t.Fatalf("overlay key = %q", services["immich"].Headers["X-API-Key"])
	}
	if services["immich"].Headers["X-Literal"] != "keep" {
		t.Fatalf("literal = %q", services["immich"].Headers["X-Literal"])
	}
}

func TestLoadConfig_ResolvesSecretIndirection(t *testing.T) {
	t.Setenv("AEGIS_TEST_CFG_SECRET", "resolved-at-load")
	dir := t.TempDir()
	path := filepath.Join(dir, "aegis.json")
	writeJSONFile(t, path, map[string]any{
		"connections": map[string]any{
			"spotify": map[string]any{
				"name":          "Spotify",
				"client_id":     "cid",
				"client_secret": "$ENV:AEGIS_TEST_CFG_SECRET",
				"auth_url":      "https://example.com/auth",
				"token_url":     "https://example.com/token",
			},
		},
		"services": []map[string]any{
			{
				"id":         "spotify",
				"name":       "Spotify",
				"action":     "inject",
				"endpoint":   "https://api.example.com",
				"cache_ttl":  "1m",
				"connection": "spotify",
				"headers": map[string]any{
					"X-API-Key": "${AEGIS_TEST_CFG_SECRET}",
				},
			},
		},
	})

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	conn := cfg.GetConnection("spotify")
	if conn == nil || conn.ClientSecret != "resolved-at-load" {
		t.Fatalf("connection secret = %+v", conn)
	}
	svc := cfg.GetService("spotify")
	if svc == nil || svc.Headers["X-API-Key"] != "resolved-at-load" {
		t.Fatalf("service header = %+v", svc)
	}
}

func TestLoadConfig_MissingEnvLeavesEmptySecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aegis.json")
	writeJSONFile(t, path, map[string]any{
		"connections": map[string]any{
			"spotify": map[string]any{
				"client_id":     "cid",
				"client_secret": "$ENV:AEGIS_TEST_CFG_MISSING",
				"auth_url":      "https://example.com/auth",
				"token_url":     "https://example.com/token",
			},
		},
		"services": []map[string]any{
			{
				"id":         "spotify",
				"name":       "Spotify",
				"action":     "inject",
				"endpoint":   "https://api.example.com",
				"cache_ttl":  "1m",
				"connection": "spotify",
			},
		},
	})
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	conn := cfg.GetConnection("spotify")
	if conn == nil || conn.ClientSecret != "" {
		t.Fatalf("missing env should yield empty secret, got %+v", conn)
	}
}

func TestInitServiceConnectionsStore_ResolvesHeaderSecrets(t *testing.T) {
	t.Setenv("AEGIS_TEST_OVERLAY_KEY", "overlay-resolved")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	svcConnPath := filepath.Join(dir, "service_connections.json")
	writeJSONFile(t, cfgPath, map[string]any{
		"services": []map[string]any{
			{
				"id":        "immich",
				"name":      "Immich",
				"action":    "inject",
				"endpoint":  "http://immich.local/api",
				"cache_ttl": "1m",
			},
		},
	})
	writeJSONFile(t, svcConnPath, map[string]any{
		"version": 1,
		"services": map[string]any{
			"immich": map[string]any{
				"headers": map[string]any{
					"X-API-Key": "$ENV:AEGIS_TEST_OVERLAY_KEY",
				},
			},
		},
	})

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitServiceConnectionsStore(svcConnPath); err != nil {
		t.Fatal(err)
	}
	svc := cfg.GetService("immich")
	if svc == nil || svc.Headers["X-API-Key"] != "overlay-resolved" {
		t.Fatalf("overlay header = %+v", svc)
	}
}

func TestConfigReload_ResolvesSecretIndirection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aegis.json")
	writeJSONFile(t, path, map[string]any{
		"connections": map[string]any{
			"spotify": map[string]any{
				"client_id":     "cid",
				"client_secret": "$ENV:AEGIS_TEST_RELOAD_SECRET",
				"auth_url":      "https://example.com/auth",
				"token_url":     "https://example.com/token",
			},
		},
		"services": []map[string]any{
			{
				"id":         "spotify",
				"name":       "Spotify",
				"action":     "inject",
				"endpoint":   "https://api.example.com",
				"cache_ttl":  "1m",
				"connection": "spotify",
			},
		},
	})

	t.Setenv("AEGIS_TEST_RELOAD_SECRET", "before-reload")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetConnection("spotify").ClientSecret != "before-reload" {
		t.Fatal("pre-reload resolve failed")
	}

	t.Setenv("AEGIS_TEST_RELOAD_SECRET", "after-reload")
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if cfg.GetConnection("spotify").ClientSecret != "after-reload" {
		t.Fatal("reload must re-resolve env secrets")
	}
}

// Ensure the example config documents the env form for at least one secret.
func TestExampleConfigUsesEnvSecretForm(t *testing.T) {
	data, err := os.ReadFile("../../aegis.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"$ENV:`) {
		t.Fatal("aegis.example.json should show $ENV: secret indirection")
	}
}
