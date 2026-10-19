package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestPermissions_ForAndAllows(t *testing.T) {
	var nilPerms types.Permissions
	if nilPerms.For("tracks") != types.AccessFull {
		t.Fatalf("nil permissions For(tracks) = %q, want %q", nilPerms.For("tracks"), types.AccessFull)
	}
	if !nilPerms.Allows("tracks", http.MethodGet) || !nilPerms.Allows("tracks", http.MethodPost) {
		t.Fatal("nil permissions should allow all methods")
	}

	p := types.Permissions{
		types.PermissionDefaultKey: types.AccessRead,
		"playlists":                types.AccessFull,
		"player":                   types.AccessNone,
		"contacts":                 types.AccessWrite,
	}

	if p.For("tracks") != types.AccessRead {
		t.Fatalf("default For(tracks) = %q, want read", p.For("tracks"))
	}
	if p.For("playlists") != types.AccessFull {
		t.Fatalf("For(playlists) = %q, want read-write", p.For("playlists"))
	}
	if p.For("player") != types.AccessNone {
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
	p := types.Permissions{"a": "READ", "b": "deny", "c": "read_write"}
	if p.For("a") != types.AccessRead {
		t.Fatalf("a = %q", p.For("a"))
	}
	if p.For("b") != types.AccessNone {
		t.Fatalf("b = %q", p.For("b"))
	}
	if p.For("c") != types.AccessFull {
		t.Fatalf("c = %q", p.For("c"))
	}
}

func TestPermissions_DeniedDataTypes(t *testing.T) {
	p := types.Permissions{
		types.PermissionDefaultKey: types.AccessNone,
		"player":                   "none",
		"albums":                   types.AccessRead,
		"tracks":                   "deny",
	}
	denied := p.DeniedDataTypes()
	if len(denied) != 2 || denied[0] != "player" || denied[1] != "tracks" {
		t.Fatalf("denied = %v, want [player tracks]", denied)
	}
}

func TestValidatePermissions(t *testing.T) {
	if err := types.ValidatePermissions(nil); err != nil {
		t.Fatalf("nil should be valid: %v", err)
	}
	if err := types.ValidatePermissions(types.Permissions{types.PermissionDefaultKey: types.AccessRead, "tracks": "nope"}); err == nil {
		t.Fatal("expected error for unknown level")
	}
	if err := types.ValidatePermissions(types.Permissions{"": types.AccessRead}); err == nil {
		t.Fatal("expected error for empty key")
	}
	if err := types.ValidatePermissions(types.Permissions{"tracks": types.AccessFull}); err != nil {
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
		"/me/events":                 "events",
		"/events/abc":                "events",
		"/calendars/primary/events":  "events",
		"/me/calendars":              "events",
		"/calendarList":              "events",
		"/me/messages":               "messages",
		"/messages/abc":              "messages",
		"/me/threads":                "messages",
		"/threads/abc":               "messages",
		"/users/me/messages":         "messages",
		"/users/me/threads/t1":       "messages",
		"/search":                    "search",
		"/search?q=x":                "search",
		"/channels/abc":              "channels",
		"/me/subscriptions":          "artists",
		"/me/library":                "library",
		"/me/recommendations":        "recommendations",
		"/unknown/thing":             "",
		"":                           "",
		// Issue #94: non-canonical forms must not change classification.
		"//me/player":      "player",
		"///me//player":    "player",
		"/me/player/":      "player",
		"/me/./player":     "",
		"/me/../player":    "",
		"/./me/player":     "",
		"/../me/player":    "",
		"/me/player/../x":  "",
		"/me\\player":      "",
		"/unknown/./thing": "",
	}
	for path, want := range cases {
		if got := types.DataTypeForPath(path); got != want {
			t.Errorf("types.DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCanonicalProxyPath(t *testing.T) {
	ok := map[string]string{
		"":            "",
		"/":           "/",
		"//":          "/",
		"/me/player":  "/me/player",
		"//me/player": "/me/player",
		"/me//player": "/me/player",
		"/me/player/": "/me/player",
		"///a//b/":    "/a/b",
	}
	for in, want := range ok {
		got, err := types.CanonicalProxyPath(in)
		if err != nil {
			t.Errorf("types.CanonicalProxyPath(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("types.CanonicalProxyPath(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"/me/./player",
		"/me/../player",
		"/.",
		"/..",
		"/a/..",
		"/a/.",
		"/me\\player",
		"/me\x00player",
	}
	for _, in := range bad {
		if got, err := types.CanonicalProxyPath(in); err == nil {
			t.Errorf("types.CanonicalProxyPath(%q) = %q, want error", in, got)
		}
	}
}

func TestPermissions_Strictest(t *testing.T) {
	cases := []struct {
		p    types.Permissions
		want string
	}{
		{nil, types.DefaultAccessType},
		{types.Permissions{}, types.DefaultAccessType},
		{types.Permissions{"player": types.AccessNone}, types.AccessNone},
		{types.Permissions{types.PermissionDefaultKey: types.AccessRead, "player": types.AccessNone}, types.AccessNone},
		{types.Permissions{types.PermissionDefaultKey: types.AccessRead, "player": types.AccessRead}, types.AccessRead},
		{types.Permissions{"tracks": types.AccessRead, "playlists": types.AccessFull}, types.AccessRead},
		{types.Permissions{"tracks": types.AccessWrite, "playlists": types.AccessFull}, types.AccessWrite},
		{types.Permissions{"tracks": types.AccessRead, "playlists": types.AccessWrite}, types.AccessNone},
		{types.Permissions{"tracks": types.AccessFull, "playlists": types.AccessFull}, types.AccessFull},
	}
	for _, tc := range cases {
		if got := tc.p.Strictest(); got != tc.want {
			t.Errorf("Strictest(%v) = %q, want %q", map[string]string(tc.p), got, tc.want)
		}
	}
}

func TestAccessForDataType(t *testing.T) {
	p := types.Permissions{types.PermissionDefaultKey: types.AccessRead, "player": types.AccessNone, "playlists": types.AccessFull}
	if got := p.AccessForDataType("player"); got != types.AccessNone {
		t.Fatalf("player = %q", got)
	}
	if got := p.AccessForDataType("playlists"); got != types.AccessFull {
		t.Fatalf("playlists = %q", got)
	}
	// Unclassified must not inherit default=read while player is none.
	if got := p.AccessForDataType(""); got != types.AccessNone {
		t.Fatalf("unclassified = %q, want none", got)
	}
}

func TestAllowDataPermission_DeniesWrite(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				GrantLimits: types.Permissions{"default": types.AccessRead, "playlists": types.AccessFull},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	svc := cfg.GetService("spotify")

	req := httptest.NewRequest("POST", "/v1/services/spotify/me/tracks", nil)
	w := httptest.NewRecorder()
	if srv.AllowDataPermission(w, req, svc, "/me/tracks") {
		t.Fatal("expected POST /me/tracks denied under default read")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "denied: data grant") {
		t.Fatalf("body = %s", w.Body.String())
	}

	req = httptest.NewRequest("GET", "/v1/services/spotify/me/playlists", nil)
	w = httptest.NewRecorder()
	if !srv.AllowDataPermission(w, req, svc, "/me/playlists") {
		t.Fatal("expected GET playlists allowed")
	}
}

func TestAllDataDenied(t *testing.T) {
	if allDataDenied(nil) {
		t.Fatal("nil should not be all-denied")
	}
	if allDataDenied(types.Permissions{"default": types.AccessRead, "player": types.AccessNone}) {
		t.Fatal("default read should not be all-denied")
	}
	if !allDataDenied(types.Permissions{"default": types.AccessNone, "player": types.AccessNone, "tracks": "deny"}) {
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
			"grant_limits": {"tracks": "maybe"}
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
			"grant_limits": {"default": "read", "playlists": "read-write"}
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
	if svc.GrantLimits.For("playlists") != types.AccessFull {
		t.Fatalf("playlists = %q", svc.GrantLimits.For("playlists"))
	}
	if svc.GrantLimits.For("tracks") != types.AccessRead {
		t.Fatalf("tracks default = %q", svc.GrantLimits.For("tracks"))
	}
}

func TestClampPermissionsWithWritable(t *testing.T) {
	caps := types.ServiceCapabilities{DataTypes: []types.DataTypeCapability{
		{Name: "tracks", Operations: []types.Operation{{Method: "GET", Path: "/me/tracks"}}},
		{Name: "photos", Operations: []types.Operation{
			{Method: "GET", Path: "/photos"},
			{Method: "POST", Path: "/picker/sessions"},
		}},
		{Name: "player", Operations: []types.Operation{{Method: "GET", Path: "/me/player"}}},
		{Name: "playlists", Operations: []types.Operation{{Method: "GET", Path: "/me/playlists"}}},
		{Name: "contacts", Operations: []types.Operation{
			{Method: "GET", Path: "/me/contacts"},
			{Method: "POST", Path: "/people/merge"},
		}},
	}}
	// Picker POST is not agent-writable; contacts POST (data write) is.
	writable := types.WritableDataTypesFromCapabilities(caps)
	if len(writable) != 1 || writable[0] != "contacts" {
		t.Fatalf("writable from caps = %v, want [contacts]", writable)
	}
	wset := map[string]bool{"contacts": true}
	p := types.Permissions{
		types.PermissionDefaultKey: types.AccessFull,
		"tracks":                   types.AccessFull,
		"photos":                   types.AccessFull,
		"playlists":                types.AccessFull,
		"contacts":                 types.AccessFull,
		"player":                   types.AccessWrite,
		"custom":                   types.AccessFull,
	}
	got := clampPermissionsWithWritable(p, caps, wset)
	if got["tracks"] != types.AccessRead || got["playlists"] != types.AccessRead || got["photos"] != types.AccessRead || got["player"] != types.AccessRead {
		t.Fatalf("non-agent-writable types should clamp to read: tracks=%q playlists=%q photos=%q player=%q",
			got["tracks"], got["playlists"], got["photos"], got["player"])
	}
	if got["contacts"] != types.AccessFull {
		t.Fatalf("contacts = %q, want read-write (true data write op)", got["contacts"])
	}
	if got["custom"] != types.AccessFull || got[types.PermissionDefaultKey] != types.AccessFull {
		t.Fatalf("unknown/default should be preserved: custom=%q default=%q", got["custom"], got[types.PermissionDefaultKey])
	}
}

func TestAllowDataPermission_DeniesWriteWhenConnectorReadOnly(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:   "spotify",
		Name: "Spotify",
		// Legacy write grant — must not unlock proxy writes the connector
		// does not implement (mirror write is a separate path).
		GrantLimits: types.Permissions{"tracks": types.AccessFull, "playlists": types.AccessFull},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
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

	// Mirror does not make core types Editable in the admin form.
	writable := srv.writableDataSet("spotify", connectors.NewSpotifyConnector(srv).Capabilities())
	if len(writable) != 0 {
		t.Fatalf("spotify writable set = %v, want empty (no agent data write ops)", writable)
	}
}

func TestWritableDataTypes_ExcludesPickerAndSearch(t *testing.T) {
	caps := types.ServiceCapabilities{DataTypes: []types.DataTypeCapability{
		{Name: "photos", Operations: []types.Operation{
			{Method: "GET", Path: "/photos"},
			{Method: "POST", Path: "/picker/sessions"},
		}},
		{Name: "photos2", Operations: []types.Operation{
			{Method: "POST", Path: "/search/metadata"},
		}},
	}}
	if got := types.WritableDataTypesFromCapabilities(caps); len(got) != 0 {
		t.Fatalf("picker/search POSTs must not mark types writable, got %v", got)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
