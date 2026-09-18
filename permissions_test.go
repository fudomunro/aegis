package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPermissions_ForAndAllows(t *testing.T) {
	var nilPerms Permissions
	if nilPerms.For("tracks") != AccessFull {
		t.Fatalf("nil permissions For(tracks) = %q, want %q", nilPerms.For("tracks"), AccessFull)
	}
	if !nilPerms.Allows("tracks", http.MethodGet) || !nilPerms.Allows("tracks", http.MethodPost) {
		t.Fatal("nil permissions should allow all methods")
	}

	p := Permissions{
		PermissionDefaultKey: AccessRead,
		"playlists":          AccessFull,
		"player":             AccessNone,
		"contacts":           AccessWrite,
	}

	if p.For("tracks") != AccessRead {
		t.Fatalf("default For(tracks) = %q, want read", p.For("tracks"))
	}
	if p.For("playlists") != AccessFull {
		t.Fatalf("For(playlists) = %q, want read-write", p.For("playlists"))
	}
	if p.For("player") != AccessNone {
		t.Fatalf("For(player) = %q, want none", p.For("player"))
	}

	if !p.Allows("tracks", http.MethodGet) {
		t.Fatal("tracks should allow GET under default read")
	}
	if p.Allows("tracks", http.MethodPost) {
		t.Fatal("tracks should deny POST under default read")
	}
	if !p.Allows("playlists", http.MethodGet) || !p.Allows("playlists", http.MethodDelete) {
		t.Fatal("playlists read-write should allow GET and DELETE")
	}
	if p.Allows("player", http.MethodGet) {
		t.Fatal("player none should deny GET")
	}
	if !p.Allows("contacts", http.MethodPost) {
		t.Fatal("contacts write should allow POST")
	}
	if p.Allows("contacts", http.MethodGet) {
		t.Fatal("contacts write should deny GET")
	}
	if !p.AllowsRead("playlists") || !p.AllowsWrite("playlists") {
		t.Fatal("playlists should allow read and write helpers")
	}
	if p.AllowsRead("player") || p.AllowsWrite("player") {
		t.Fatal("player none should deny read and write helpers")
	}
}

func TestPermissions_NormalizeAliases(t *testing.T) {
	p := Permissions{"a": "READ", "b": "deny", "c": "read_write"}
	if p.For("a") != AccessRead {
		t.Fatalf("a = %q", p.For("a"))
	}
	if p.For("b") != AccessNone {
		t.Fatalf("b = %q", p.For("b"))
	}
	if p.For("c") != AccessFull {
		t.Fatalf("c = %q", p.For("c"))
	}
}

func TestPermissions_DeniedDataTypes(t *testing.T) {
	p := Permissions{
		PermissionDefaultKey: AccessNone,
		"player":             "none",
		"albums":             AccessRead,
		"tracks":             "deny",
	}
	denied := p.DeniedDataTypes()
	if len(denied) != 2 || denied[0] != "player" || denied[1] != "tracks" {
		t.Fatalf("denied = %v, want [player tracks]", denied)
	}
}

func TestValidatePermissions(t *testing.T) {
	if err := ValidatePermissions(nil); err != nil {
		t.Fatalf("nil should be valid: %v", err)
	}
	if err := ValidatePermissions(Permissions{PermissionDefaultKey: AccessRead, "tracks": "nope"}); err == nil {
		t.Fatal("expected error for unknown level")
	}
	if err := ValidatePermissions(Permissions{"": AccessRead}); err == nil {
		t.Fatal("expected error for empty key")
	}
	if err := ValidatePermissions(Permissions{"tracks": AccessFull}); err != nil {
		t.Fatalf("valid map rejected: %v", err)
	}
}

func TestDataTypeForPath(t *testing.T) {
	cases := map[string]string{
		"/me":                        "user",
		"/me/":                       "user",
		"/me/player":                 "player",
		"/me/player/recently-played": "player",
		"/me/tracks":                 "tracks",
		"/tracks/abc":                "tracks",
		"/me/liked":                  "tracks",
		"/me/top/tracks":             "tracks",
		"/videos/xyz":                "tracks",
		"/me/albums":                 "albums",
		"/albums/abc":                "albums",
		"/artists/abc":               "artists",
		"/artists/abc/top-tracks":    "artists",
		"/me/artists":                "artists",
		"/me/top/artists":            "artists",
		"/me/following":              "artists",
		"/me/following?type=artist":  "artists",
		"/me/playlists":              "playlists",
		"/playlists/abc":             "playlists",
		"/playlists/abc/items":       "playlists",
		"/search":                    "search",
		"/search?q=x":                "search",
		"/channels/abc":              "channels",
		"/me/subscriptions":          "artists",
		"/me/library":                "library",
		"/me/recommendations":        "recommendations",
		"/unknown/thing":             "",
		"":                           "",
	}
	for path, want := range cases {
		if got := DataTypeForPath(path); got != want {
			t.Errorf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFilterCapabilities(t *testing.T) {
	caps := ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{Name: "playlists", Operations: []Operation{
				{Name: "read_list", Method: "GET", Path: "/me/playlists"},
				{Name: "create", Method: "POST", Path: "/me/playlists"},
			}},
			{Name: "player", Operations: []Operation{
				{Name: "read_state", Method: "GET", Path: "/me/player"},
			}},
		},
	}

	filtered := FilterCapabilities(caps, Permissions{
		"playlists": AccessRead,
		"player":    AccessNone,
	})
	if len(filtered.DataTypes) != 1 {
		t.Fatalf("got %d data types, want 1", len(filtered.DataTypes))
	}
	if filtered.DataTypes[0].Name != "playlists" {
		t.Fatalf("got %q, want playlists", filtered.DataTypes[0].Name)
	}
	if len(filtered.DataTypes[0].Operations) != 1 || filtered.DataTypes[0].Operations[0].Method != "GET" {
		t.Fatalf("playlists ops = %+v, want only GET", filtered.DataTypes[0].Operations)
	}

	unfiltered := FilterCapabilities(caps, nil)
	if len(unfiltered.DataTypes) != 2 {
		t.Fatalf("nil perms should keep both types, got %d", len(unfiltered.DataTypes))
	}
}

func TestAllowDataPermission_DeniesWrite(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: Permissions{"default": AccessRead, "playlists": AccessFull},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	svc := cfg.GetService("spotify")

	req := httptest.NewRequest("POST", "/v1/services/spotify/me/tracks", nil)
	w := httptest.NewRecorder()
	if srv.AllowDataPermission(w, req, svc, "/me/tracks") {
		t.Fatal("expected POST /me/tracks denied under default read")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("body = %s", w.Body.String())
	}

	req = httptest.NewRequest("GET", "/v1/services/spotify/me/playlists", nil)
	w = httptest.NewRecorder()
	if !srv.AllowDataPermission(w, req, svc, "/me/playlists") {
		t.Fatal("expected GET playlists allowed")
	}

	// Capabilities always allowed (filtered separately).
	req = httptest.NewRequest("GET", "/v1/services/spotify/capabilities", nil)
	w = httptest.NewRecorder()
	if !srv.AllowDataPermission(w, req, svc, "/capabilities") {
		t.Fatal("capabilities should always be allowed")
	}
}

func TestAllDataDenied(t *testing.T) {
	if allDataDenied(nil) {
		t.Fatal("nil should not be all-denied")
	}
	if allDataDenied(Permissions{"default": AccessRead, "player": AccessNone}) {
		t.Fatal("default read should not be all-denied")
	}
	if !allDataDenied(Permissions{"default": AccessNone, "player": AccessNone, "tracks": "deny"}) {
		t.Fatal("all none should be all-denied")
	}
}

func TestLoadConfig_ValidatesPermissions(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bad.json"
	body := `{
		"services": [{
			"id": "spotify",
			"name": "Spotify",
			"action": "inject",
			"permissions": {"tracks": "maybe"}
		}]
	}`
	if err := writeFile(path, body); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected invalid permission level to fail load")
	}

	body = `{
		"services": [{
			"id": "spotify",
			"name": "Spotify",
			"action": "inject",
			"permissions": {"default": "read", "playlists": "read-write"}
		}]
	}`
	if err := writeFile(path, body); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("valid permissions failed: %v", err)
	}
	svc := cfg.GetService("spotify")
	if svc.Permissions.For("playlists") != AccessFull {
		t.Fatalf("playlists = %q", svc.Permissions.For("playlists"))
	}
	if svc.Permissions.For("tracks") != AccessRead {
		t.Fatalf("tracks default = %q", svc.Permissions.For("tracks"))
	}
}

func TestClampPermissionsWithWritable(t *testing.T) {
	caps := ServiceCapabilities{DataTypes: []DataTypeCapability{
		{Name: "tracks", Operations: []Operation{{Method: "GET", Path: "/me/tracks"}}},
		{Name: "photos", Operations: []Operation{
			{Method: "GET", Path: "/photos"},
			{Method: "POST", Path: "/picker/sessions"},
		}},
		{Name: "player", Operations: []Operation{{Method: "GET", Path: "/me/player"}}},
		{Name: "playlists", Operations: []Operation{{Method: "GET", Path: "/me/playlists"}}},
	}}
	// Mirror adapters make playlists writable even though proxy ops are GET-only.
	writable := map[string]bool{"photos": true, "playlists": true}
	p := Permissions{
		PermissionDefaultKey: AccessFull,
		"tracks":             AccessFull,
		"photos":             AccessFull,
		"playlists":          AccessFull,
		"player":             AccessWrite,
		"custom":             AccessFull, // unknown to connector — left alone
	}
	got := clampPermissionsWithWritable(p, caps, writable)
	if got["tracks"] != AccessRead {
		t.Fatalf("tracks = %q, want read (clamped; no write path)", got["tracks"])
	}
	if got["photos"] != AccessFull {
		t.Fatalf("photos = %q, want read-write (proxy write op)", got["photos"])
	}
	if got["playlists"] != AccessFull {
		t.Fatalf("playlists = %q, want read-write (mirror target write)", got["playlists"])
	}
	if got["player"] != AccessRead {
		t.Fatalf("player = %q, want read (clamped)", got["player"])
	}
	if got["custom"] != AccessFull {
		t.Fatalf("custom = %q, want preserved (unknown type)", got["custom"])
	}
	if got[PermissionDefaultKey] != AccessFull {
		t.Fatalf("default = %q, want preserved", got[PermissionDefaultKey])
	}
}

func TestAllowDataPermission_DeniesWriteWhenConnectorReadOnly(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:   "spotify",
		Name: "Spotify",
		// Legacy write grant — must not unlock proxy writes the connector
		// does not implement (mirror write is a separate path).
		Permissions: Permissions{"tracks": AccessFull, "playlists": AccessFull},
	}}}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors["spotify"] = NewSpotifyConnector(srv)
	svc := cfg.GetService("spotify")

	req := httptest.NewRequest(http.MethodPost, "/v1/services/spotify/me/tracks", nil)
	w := httptest.NewRecorder()
	if srv.AllowDataPermission(w, req, svc, "/me/tracks") {
		t.Fatal("POST tracks should be denied: connector has no write ops for tracks")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "does not support write") {
		t.Fatalf("body = %s, want connector write-capability denial", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/services/spotify/me/tracks", nil)
	w = httptest.NewRecorder()
	if !srv.AllowDataPermission(w, req, svc, "/me/tracks") {
		t.Fatal("GET tracks should be allowed under read-write permission")
	}

	// Spotify is a mirror target for playlists/tracks — Editable stays available.
	writable := srv.writableDataSet("spotify", NewSpotifyConnector(srv).Capabilities())
	if !writable["playlists"] || !writable["tracks"] {
		t.Fatalf("spotify writable set = %v, want playlists+tracks via mirror", writable)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
