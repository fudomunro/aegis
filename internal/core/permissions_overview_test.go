package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

func TestPermissionsOverview_AgentsAppsAndSync(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := writeFile(cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SeedTypePolicy("tracks", types.TypePolicy{Default: types.AccessRead})
	cfg.SeedTypePolicy("events", types.TypePolicy{Default: types.AccessFreeBusy})
	cfg.SeedTypePolicy("places", types.TypePolicy{Default: types.AccessCity})

	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)

	apps, err := NewAppStore(filepath.Join(dir, "apps.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := apps.Create(AppCreateRequest{
		ID:   "eco-demo",
		Name: "Event Coordinator",
		Scopes: types.Permissions{
			"events": types.AccessFreeBusy,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := apps.Create(AppCreateRequest{
		ID:     "disabled-app",
		Name:   "Disabled App",
		Scopes: types.Permissions{"tracks": types.AccessRead},
	}); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if _, err := apps.Update("disabled-app", AppUpdateRequest{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	srv.apps = apps

	mail, err := OpenMailArchive(filepath.Join(dir, "mail-archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv.mail = mail
	copies, err := OpenCopyLedger(filepath.Join(dir, "copy_ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv.copies = copies

	overview := srv.permissionsOverview()

	if len(overview.DataTypes) == 0 {
		t.Fatal("expected data types")
	}
	found := map[string]types.Accessor{}
	for _, a := range overview.Accessors {
		found[a.ID] = a
	}

	agent, ok := found[agentAccessorID]
	if !ok {
		t.Fatalf("missing agent accessor: %+v", found)
	}
	if agent.Kind != types.AccessorAgent || !agent.Editable {
		t.Fatalf("agent = %+v", agent)
	}
	if agent.Levels["tracks"] != types.AccessRead {
		t.Fatalf("agent tracks = %q, want read", agent.Levels["tracks"])
	}
	if agent.Levels["events"] != types.AccessFreeBusy {
		t.Fatalf("agent events = %q, want freebusy", agent.Levels["events"])
	}
	if agent.Levels["places"] != types.AccessCity {
		t.Fatalf("agent places = %q, want city", agent.Levels["places"])
	}

	app, ok := found["app:eco-demo"]
	if !ok {
		t.Fatalf("missing app accessor: %+v", found)
	}
	if app.Kind != types.AccessorApp || app.Disabled {
		t.Fatalf("app = %+v", app)
	}
	if app.Levels["events"] != types.AccessFreeBusy {
		t.Fatalf("app events = %q, want freebusy", app.Levels["events"])
	}
	if app.Levels["tracks"] != types.AccessNone {
		t.Fatalf("app tracks = %q, want none", app.Levels["tracks"])
	}
	if app.Levels["places"] != types.AccessNone {
		t.Fatalf("app places = %q, want none (opt-in unnamed)", app.Levels["places"])
	}

	disabledApp, ok := found["app:disabled-app"]
	if !ok {
		t.Fatal("missing disabled app")
	}
	if !disabledApp.Disabled {
		t.Fatal("disabled app should be marked disabled")
	}

	mailRow, ok := found[syncMailID]
	if !ok {
		t.Fatalf("missing mail sync accessor: %+v", found)
	}
	if mailRow.Kind != types.AccessorSync {
		t.Fatalf("mail kind = %q", mailRow.Kind)
	}
	if mailRow.Levels["messages"] != types.AccessRead {
		t.Fatalf("mail messages = %q, want read", mailRow.Levels["messages"])
	}
	if mailRow.Levels["tracks"] != types.AccessNone {
		t.Fatalf("mail tracks = %q, want none", mailRow.Levels["tracks"])
	}

	if _, ok := found[syncCopyID]; !ok {
		t.Fatalf("missing copy-ledger accessor: %+v", found)
	}
	if _, ok := found[syncTimelineID]; ok {
		t.Fatal("timeline should be absent when store is not mounted")
	}
}

func TestHandleAdminPermissionsOverview(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := writeFile(cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)

	// Unauthenticated.
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/permissions", nil)
	w := httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/admin/permissions", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	var overview types.PermissionsOverview
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(overview.DataTypes) == 0 || len(overview.Accessors) == 0 {
		t.Fatalf("overview = %+v", overview)
	}
	// Place is opt-in and stays off for agents when unnamed.
	sawAgent := false
	for _, a := range overview.Accessors {
		if a.ID != agentAccessorID {
			continue
		}
		sawAgent = true
		if a.Levels["places"] != types.AccessNone {
			t.Fatalf("agent places = %q, want none", a.Levels["places"])
		}
		if a.Levels["tracks"] != types.DefaultAccessType {
			t.Fatalf("agent tracks = %q, want %q", a.Levels["tracks"], types.DefaultAccessType)
		}
	}
	if !sawAgent {
		t.Fatal("missing agent accessor")
	}

	// POST is rejected.
	req = httptest.NewRequest(http.MethodPost, "/v1/admin/permissions", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
}
