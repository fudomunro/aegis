package core

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestPermissions_LevelAndAllows(t *testing.T) {
	var nilPerms policy.Permissions
	if nilPerms.Level("tracks") != policy.AccessNone {
		t.Fatalf("nil permissions Level(tracks) = %q, want %q", nilPerms.Level("tracks"), policy.AccessNone)
	}
	if nilPerms.Allows("tracks", http.MethodGet) || nilPerms.Allows("tracks", http.MethodPost) {
		t.Fatal("nil permissions deny all methods")
	}

	p := policy.Permissions{
		policy.PermissionDefaultKey: policy.AccessRead,
		"playlists":                 policy.AccessFull,
		"player":                    policy.AccessNone,
		"contacts":                  policy.AccessWrite,
	}

	if p.Level("tracks") != policy.AccessRead {
		t.Fatalf("default Level(tracks) = %q, want read", p.Level("tracks"))
	}
	if p.Level("playlists") != policy.AccessFull {
		t.Fatalf("Level(playlists) = %q, want read-write", p.Level("playlists"))
	}
	if p.Level("player") != policy.AccessNone {
		t.Fatalf("Level(player) = %q, want none", p.Level("player"))
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
	p := policy.Permissions{"a": "READ", "b": "deny", "c": "read_write"}
	if p.Level("a") != policy.AccessRead {
		t.Fatalf("a = %q", p.Level("a"))
	}
	if p.Level("b") != policy.AccessNone {
		t.Fatalf("b = %q", p.Level("b"))
	}
	if p.Level("c") != policy.AccessFull {
		t.Fatalf("c = %q", p.Level("c"))
	}
}

func TestPermissions_DeniedKinds(t *testing.T) {
	p := policy.Permissions{
		policy.PermissionDefaultKey: policy.AccessNone,
		"player":                    "none",
		"albums":                    policy.AccessRead,
		"tracks":                    "deny",
	}
	denied := p.DeniedKinds()
	if len(denied) != 2 || denied[0] != "player" || denied[1] != "tracks" {
		t.Fatalf("denied = %v, want [player tracks]", denied)
	}
}

func TestValidatePermissions(t *testing.T) {
	if err := policy.ValidatePermissions(nil); err != nil {
		t.Fatalf("nil should be valid: %v", err)
	}
	if err := policy.ValidatePermissions(policy.Permissions{policy.PermissionDefaultKey: policy.AccessRead, "tracks": "nope"}); err == nil {
		t.Fatal("expected error for unknown level")
	}
	if err := policy.ValidatePermissions(policy.Permissions{"": policy.AccessRead}); err == nil {
		t.Fatal("expected error for empty key")
	}
	if err := policy.ValidatePermissions(policy.Permissions{"tracks": policy.AccessFull}); err != nil {
		t.Fatalf("valid map rejected: %v", err)
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
		got, err := CanonicalProxyPath(in)
		if err != nil {
			t.Errorf("CanonicalProxyPath(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("CanonicalProxyPath(%q) = %q, want %q", in, got, want)
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
		if got, err := CanonicalProxyPath(in); err == nil {
			t.Errorf("CanonicalProxyPath(%q) = %q, want error", in, got)
		}
	}
}

func TestAllowDataPermission_DeniesWrite(t *testing.T) {
	cfg := &Config{
		Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}},
		typePolicies: policy.DataGrants{
			policy.PermissionDefaultKey: {Default: policy.AccessRead},
			"playlists":                 {Default: policy.AccessFull},
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
	if !allDataDenied(nil) {
		t.Fatal("nil grants deny everything")
	}
	if allDataDenied(policy.Permissions{"default": policy.AccessRead, "player": policy.AccessNone}) {
		t.Fatal("default read should not be all-denied")
	}
	if !allDataDenied(policy.Permissions{"default": policy.AccessNone, "player": policy.AccessNone, "tracks": "deny"}) {
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
	if svc.GrantLimits.Level("playlists") != policy.AccessFull {
		t.Fatalf("playlists = %q", svc.GrantLimits.Level("playlists"))
	}
	if svc.GrantLimits.Level("tracks") != policy.AccessRead {
		t.Fatalf("tracks default = %q", svc.GrantLimits.Level("tracks"))
	}
}

func TestClampPermissionsWithWritable(t *testing.T) {
	caps := shared.ServiceCapabilities{Kinds: []shared.KindCapability{
		{Name: "tracks", Operations: []shared.Operation{{Method: "GET", Path: "/me/tracks"}}},
		{Name: "photos", Operations: []shared.Operation{
			{Method: "GET", Path: "/photos"},
			{Method: "POST", Path: "/picker/sessions"},
		}},
		{Name: "player", Operations: []shared.Operation{{Method: "GET", Path: "/me/player"}}},
		{Name: "playlists", Operations: []shared.Operation{{Method: "GET", Path: "/me/playlists"}}},
		{Name: "contacts", Operations: []shared.Operation{
			{Method: "GET", Path: "/me/contacts"},
			{Method: "POST", Path: "/people/merge"},
		}},
	}}
	// Picker POST is not agent-writable; contacts POST (data write) is.
	writable := shared.WritableKindsFromCapabilities(caps)
	if len(writable) != 1 || writable[0] != "contacts" {
		t.Fatalf("writable from caps = %v, want [contacts]", writable)
	}
	wset := map[string]bool{"contacts": true}
	p := policy.Permissions{
		policy.PermissionDefaultKey: policy.AccessFull,
		"tracks":                    policy.AccessFull,
		"photos":                    policy.AccessFull,
		"playlists":                 policy.AccessFull,
		"contacts":                  policy.AccessFull,
		"player":                    policy.AccessWrite,
		"custom":                    policy.AccessFull,
	}
	got := clampPermissionsWithWritable(p, caps, wset)
	if got["tracks"] != policy.AccessRead || got["playlists"] != policy.AccessRead || got["photos"] != policy.AccessRead || got["player"] != policy.AccessRead {
		t.Fatalf("non-agent-writable types should clamp to read: tracks=%q playlists=%q photos=%q player=%q",
			got["tracks"], got["playlists"], got["photos"], got["player"])
	}
	if got["contacts"] != policy.AccessFull {
		t.Fatalf("contacts = %q, want read-write (true data write op)", got["contacts"])
	}
	if got["custom"] != policy.AccessFull || got[policy.PermissionDefaultKey] != policy.AccessFull {
		t.Fatalf("unknown/default should be preserved: custom=%q default=%q", got["custom"], got[policy.PermissionDefaultKey])
	}
}

func TestAllowDataPermission_DeniesWriteWhenConnectorReadOnly(t *testing.T) {
	cfg := &Config{
		Services: []Service{{ID: "spotify", Name: "Spotify"}},
		// A write grant must not unlock proxy writes the connector does
		// not implement (mirror write is a separate path).
		typePolicies: policy.DataGrants{
			"tracks":    {Default: policy.AccessFull},
			"playlists": {Default: policy.AccessFull},
		},
	}
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

func TestWritableKinds_ExcludesPickerAndSearch(t *testing.T) {
	caps := shared.ServiceCapabilities{Kinds: []shared.KindCapability{
		{Name: "photos", Operations: []shared.Operation{
			{Method: "GET", Path: "/photos"},
			{Method: "POST", Path: "/picker/sessions"},
		}},
		{Name: "photos2", Operations: []shared.Operation{
			{Method: "POST", Path: "/search/metadata"},
		}},
	}}
	if got := shared.WritableKindsFromCapabilities(caps); len(got) != 0 {
		t.Fatalf("picker/search POSTs must not mark types writable, got %v", got)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
