package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
)

// TestTailscaleAdminKey_PersistPresenceOnly pins the VIP wizard's write path
// (issue #124, through the onboarding seam of issue #265): the operator's
// Tailscale Admin API key lands in the encrypted store, is never echoed, and
// survives a reload.
func TestTailscaleAdminKey_PersistPresenceOnly(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := os.WriteFile(cfgPath, []byte(`{"services":[{"id":"immich","action":"inject"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(cfg, "https://aegis.example.ts.net")
	srv.adminAPIKey = "admin-key"
	srv.connectors[connectors.ImmichServiceID] = connectors.NewImmichConnector(srv)
	secrets := mustSecretStore(t, filepath.Join(dir, "tokens.json"))
	if err := cfg.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json"), secrets); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"values": map[string]string{
			"ts_api_key":  "tskey-api-secret",
			"host_device": "n1",
		},
	})
	req := adminRequest(http.MethodPost, "/v1/admin/setup/immich/tailnet_save", "admin-key", string(body))
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.handleAdminOnboarding)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("save status = %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "tskey-api-secret") {
		t.Fatal("API key must not be echoed back")
	}

	snap := cfg.TailscaleAdminSnapshot()
	if !snap.HasAPIKey || snap.HostDeviceID != "n1" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if cfg.TailscaleAdminAPIKey() != "tskey-api-secret" {
		t.Fatal("internal key store lost the value")
	}

	// Reload from disk keeps the key.
	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json"),
		mustSecretStore(t, filepath.Join(dir, "tokens.json"))); err != nil {
		t.Fatal(err)
	}
	if fresh.TailscaleAdminAPIKey() != "tskey-api-secret" {
		t.Fatal("reload lost tailscale api key")
	}
}
