package shared

import (
	"net/http"
	"testing"
)

func TestPermissions_AllowsAndClone(t *testing.T) {
	p := GrantMap{"tracks": AccessRead, "playlists": AccessNone, "default": AccessWrite}
	if !p.AllowsRead("tracks") || p.AllowsWrite("tracks") {
		t.Fatalf("tracks read-only: %+v", p)
	}
	if p.AllowsRead("playlists") || p.AllowsWrite("playlists") {
		t.Fatalf("playlists none: %+v", p)
	}
	if !p.AllowsWrite("search") {
		t.Fatalf("default write should allow search: %+v", p)
	}
	if !p.Allows("tracks", http.MethodGet) || p.Allows("tracks", http.MethodPost) {
		t.Fatalf("Allows method: %+v", p)
	}
	c := p.Clone()
	if c["tracks"] != AccessRead || len(c) != 3 {
		t.Fatalf("clone = %+v", c)
	}
	c["tracks"] = AccessNone
	if p["tracks"] != AccessRead {
		t.Fatal("clone must be deep enough to not alias levels")
	}
	if got := p.DeniedKinds(); len(got) != 1 || got[0] != "playlists" {
		t.Fatalf("DeniedKinds = %v", got)
	}
}

func TestAllowsMethod_Levels(t *testing.T) {
	if !AllowsMethod(AccessRead, http.MethodGet) || AllowsMethod(AccessRead, http.MethodPost) {
		t.Fatal("read")
	}
	if !AllowsMethod(AccessWrite, http.MethodPost) || AllowsMethod(AccessWrite, http.MethodGet) {
		t.Fatal("write")
	}
	if !AllowsMethod(AccessFull, http.MethodDelete) || !AllowsMethod(AccessFull, http.MethodGet) {
		t.Fatal("full")
	}
	if AllowsMethod(AccessNone, http.MethodGet) {
		t.Fatal("none must deny")
	}
}

func TestNormalizeAccess(t *testing.T) {
	if NormalizeAccess("READ") != AccessRead {
		t.Fatal("NormalizeAccess READ")
	}
	if NormalizeAccess("") != "" {
		t.Fatal("empty stays empty")
	}
}

func TestLevel_DenyByDefault(t *testing.T) {
	RegisterKindRule("places", KindRule{DefaultNone: true})
	cases := []struct {
		name string
		p    GrantMap
		kind string
		want string
	}{
		{"nil map denies", nil, "playlists", AccessNone},
		{"empty map denies", GrantMap{}, "playlists", AccessNone},
		{"unlisted kind denies", GrantMap{"tracks": AccessRead}, "playlists", AccessNone},
		{"explicit entry wins", GrantMap{"playlists": "rw"}, "playlists", AccessFull},
		{"explicit none", GrantMap{"playlists": AccessNone}, "playlists", AccessNone},
		{"default opens unlisted", GrantMap{DefaultKey: AccessRead}, "playlists", AccessRead},
		{"explicit beats default", GrantMap{DefaultKey: AccessRead, "playlists": AccessWrite}, "playlists", AccessWrite},
		{"opt-in kind not opened by default", GrantMap{DefaultKey: AccessFull}, "places", AccessNone},
		{"opt-in kind explicit entry", GrantMap{DefaultKey: AccessFull, "places": AccessRead}, "places", AccessRead},
		{"empty kind is unlisted", GrantMap{DefaultKey: AccessRead}, "", AccessRead},
		{"malformed entry denies", GrantMap{"playlists": "bogus"}, "playlists", AccessNone},
		{"malformed default denies", GrantMap{DefaultKey: "bogus"}, "playlists", AccessNone},
		{"empty entry denies", GrantMap{"playlists": ""}, "playlists", AccessNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Level(tc.kind); got != tc.want {
				t.Fatalf("Level(%q) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

func TestAllowsKindMethod(t *testing.T) {
	p := GrantMap{DefaultKey: AccessRead, "playlists": AccessFull, "places": AccessNone}
	if !p.AllowsKindMethod("tracks", "GET") {
		t.Fatal("default read must allow GET on unlisted kind")
	}
	if p.AllowsKindMethod("tracks", "POST") {
		t.Fatal("default read must deny POST on unlisted kind")
	}
	if !p.AllowsKindMethod("playlists", "POST") {
		t.Fatal("full must allow POST")
	}
	if p.AllowsKindMethod("places", "GET") {
		t.Fatal("explicit none must deny GET")
	}
}

func TestPrefillDefault_OnlyWhenUnconfigured(t *testing.T) {
	empty := (GrantMap{}).PrefillDefault()
	if empty.Level("tracks") != AccessFull {
		t.Fatalf("empty map must prefill a permissive default, got %q", empty.Level("tracks"))
	}
	// A map with any entry is the operator's explicit allow list — an
	// unlisted kind stays none (the freebusy-only key premise, issue #348).
	partial := GrantMap{"freebusy": AccessRead}.PrefillDefault()
	if partial.Level("events") != AccessNone {
		t.Fatalf("partial map must not prefill, got %q", partial.Level("events"))
	}
	if partial.Level("freebusy") != AccessRead {
		t.Fatalf("listed entry must survive, got %q", partial.Level("freebusy"))
	}
}
