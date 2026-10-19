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

func TestPermissionsOverview_TypeCentricRelevantAccessors(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := writeFile(cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SeedDataGrant("tracks", types.DataGrant{Default: types.AccessRead})
	cfg.SeedDataGrant("events", types.DataGrant{Default: types.AccessFreeBusy})
	cfg.SeedDataGrant("places", types.DataGrant{Default: types.AccessCity})

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
			// Explicit none still counts as relevant for the editor.
			"tracks": types.AccessNone,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := apps.Create(AppCreateRequest{
		ID:     "disabled-app",
		Name:   "Disabled App",
		Scopes: types.Permissions{"photos": types.AccessRead},
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
	if len(overview.DataTypes) == 0 || len(overview.Types) == 0 {
		t.Fatalf("overview = %+v", overview)
	}

	byID := map[string]types.TypeAccess{}
	for _, row := range overview.Types {
		byID[row.ID] = row
	}

	tracks, ok := byID["tracks"]
	if !ok {
		t.Fatalf("missing tracks row: %+v", overview.Types)
	}
	if tracks.AgentLevel != types.AccessRead {
		t.Fatalf("tracks agent = %q, want read", tracks.AgentLevel)
	}
	// Eco named tracks (even as none) — relevant. Mail sync is not.
	// Spotify is a LikedTrackProvider, so it appears as a service peer.
	var sawEco, sawSpotify, sawSync bool
	for _, acc := range tracks.Accessors {
		switch acc.ID {
		case "app:eco-demo":
			sawEco = true
			if acc.Level != types.AccessNone {
				t.Fatalf("eco tracks = %q, want none", acc.Level)
			}
		case "service:spotify":
			sawSpotify = true
			if acc.Kind != types.AccessorService {
				t.Fatalf("spotify kind = %q", acc.Kind)
			}
		default:
			if acc.Kind == types.AccessorSync {
				sawSync = true
			}
		}
	}
	if !sawEco {
		t.Fatalf("tracks accessors = %+v, want eco-demo", tracks.Accessors)
	}
	if !sawSpotify {
		t.Fatalf("tracks accessors = %+v, want service:spotify", tracks.Accessors)
	}
	if sawSync {
		t.Fatalf("tracks should not list sync accessors: %+v", tracks.Accessors)
	}

	events, ok := byID["events"]
	if !ok {
		t.Fatal("missing events row")
	}
	if events.AgentLevel != types.AccessFreeBusy {
		t.Fatalf("events agent = %q", events.AgentLevel)
	}
	if len(events.Accessors) != 1 || events.Accessors[0].ID != "app:eco-demo" {
		t.Fatalf("events accessors = %+v", events.Accessors)
	}
	if events.Accessors[0].Level != types.AccessFreeBusy {
		t.Fatalf("eco events = %q", events.Accessors[0].Level)
	}

	messages, ok := byID["messages"]
	if !ok {
		t.Fatal("missing messages row")
	}
	foundMail := false
	for _, acc := range messages.Accessors {
		if acc.ID == syncMailID {
			foundMail = true
			if acc.Kind != types.AccessorSync || acc.Level != types.AccessRead {
				t.Fatalf("mail row = %+v", acc)
			}
		}
		if acc.ID == "app:eco-demo" {
			t.Fatal("eco should not appear under messages")
		}
	}
	if !foundMail {
		t.Fatalf("messages accessors = %+v, want mail archive", messages.Accessors)
	}

	places, ok := byID["places"]
	if !ok {
		t.Fatal("missing places row")
	}
	if places.AgentLevel != types.AccessCity {
		t.Fatalf("places agent = %q", places.AgentLevel)
	}
	// Disabled app named photos only — not places. Timeline not mounted.
	if len(places.Accessors) != 0 {
		t.Fatalf("places accessors = %+v, want none", places.Accessors)
	}

	photos, ok := byID["photos"]
	if !ok {
		t.Fatal("missing photos row")
	}
	if len(photos.Accessors) != 1 || photos.Accessors[0].ID != "app:disabled-app" {
		t.Fatalf("photos accessors = %+v", photos.Accessors)
	}
	if !photos.Accessors[0].Disabled {
		t.Fatal("disabled app should stay marked disabled")
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
	if len(overview.DataTypes) == 0 || len(overview.Types) == 0 {
		t.Fatalf("overview = %+v", overview)
	}
	sawAgentLevel := false
	for _, row := range overview.Types {
		if row.ID == "places" {
			if row.AgentLevel != types.AccessNone {
				t.Fatalf("agent places = %q, want none", row.AgentLevel)
			}
		}
		if row.ID == "tracks" {
			if row.AgentLevel != types.DefaultAccessType {
				t.Fatalf("agent tracks = %q, want %q", row.AgentLevel, types.DefaultAccessType)
			}
			sawAgentLevel = true
		}
	}
	if !sawAgentLevel {
		t.Fatal("missing tracks row")
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/admin/permissions", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
}

func TestPermissionsOverview_ServicePeersUnderLibraryTypes(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	if err := writeFile(cfgPath, `{"services":[
		{"id":"spotify","name":"Spotify","action":"inject"},
		{"id":"youtube-music","name":"YouTube Music","action":"inject"},
		{"id":"player-only","name":"Player Only","action":"inject"}
	]}`); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
	srv.connectors["youtube-music"] = connectors.NewYouTubeMusicConnector(srv)
	// Proxy-only capability surface must not create a playlists row.
	srv.connectors["player-only"] = stubConnector{
		caps: types.ServiceCapabilities{DataTypes: []types.DataTypeCapability{
			{Name: "player"},
			{Name: "search"},
		}},
	}

	overview := srv.permissionsOverview()
	var playlists *types.TypeAccess
	for i := range overview.Types {
		if overview.Types[i].ID == "playlists" {
			playlists = &overview.Types[i]
			break
		}
	}
	if playlists == nil {
		t.Fatal("missing playlists row")
	}

	services := map[string]types.TypeAccessor{}
	for _, acc := range playlists.Accessors {
		if acc.Kind == types.AccessorService {
			services[acc.ID] = acc
		}
	}
	spotify, ok := services["service:spotify"]
	if !ok {
		t.Fatalf("playlists services = %+v, want spotify", playlists.Accessors)
	}
	if spotify.Label != "Spotify" || spotify.Level != types.AccessRead {
		t.Fatalf("spotify = %+v", spotify)
	}
	yt, ok := services["service:youtube-music"]
	if !ok {
		t.Fatalf("playlists services = %+v, want youtube-music", playlists.Accessors)
	}
	if yt.Label != "YouTube Music" {
		t.Fatalf("youtube = %+v", yt)
	}
	if _, ok := services["service:player-only"]; ok {
		t.Fatal("player-only must not appear under playlists")
	}

	// Player-only stays off every library type row.
	for _, row := range overview.Types {
		for _, acc := range row.Accessors {
			if acc.ID == "service:player-only" {
				t.Fatalf("player-only under %s: %+v", row.ID, acc)
			}
		}
	}
}

type stubConnector struct {
	caps types.ServiceCapabilities
}

func (s stubConnector) Capabilities() types.ServiceCapabilities { return s.caps }
func (s stubConnector) SystemInstructions(string) string        { return "" }
func (s stubConnector) HandleProxy(http.ResponseWriter, *http.Request, string) {
}
