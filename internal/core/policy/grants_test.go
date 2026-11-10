package policy

import "testing"

func TestLevel_DenyByDefault(t *testing.T) {
	cases := []struct {
		name string
		p    Permissions
		kind string
		want string
	}{
		{"nil map denies", nil, "playlists", AccessNone},
		{"empty map denies", Permissions{}, "playlists", AccessNone},
		{"unlisted kind denies", Permissions{"tracks": AccessRead}, "playlists", AccessNone},
		{"explicit entry wins", Permissions{"playlists": "rw"}, "playlists", AccessFull},
		{"explicit none", Permissions{"playlists": AccessNone}, "playlists", AccessNone},
		{"default opens unlisted", Permissions{PermissionDefaultKey: AccessRead}, "playlists", AccessRead},
		{"explicit beats default", Permissions{PermissionDefaultKey: AccessRead, "playlists": AccessWrite}, "playlists", AccessWrite},
		{"opt-in kind not opened by default", Permissions{PermissionDefaultKey: AccessFull}, "places", AccessNone},
		{"opt-in kind explicit entry", Permissions{PermissionDefaultKey: AccessFull, "places": AccessCity}, "places", AccessCity},
		{"empty kind is unlisted", Permissions{PermissionDefaultKey: AccessRead}, "", AccessRead},
		{"malformed entry denies", Permissions{"playlists": "bogus"}, "playlists", AccessNone},
		{"malformed default denies", Permissions{PermissionDefaultKey: "bogus"}, "playlists", AccessNone},
		{"empty entry denies", Permissions{"playlists": ""}, "playlists", AccessNone},
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
	p := Permissions{PermissionDefaultKey: AccessRead, "playlists": AccessFull, "places": AccessNone}
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

func TestLevels_MaterializesRuleMap(t *testing.T) {
	legacy := DataGrants{
		"tracks":             {Default: "rw"},
		"playlists":          {Default: "ro", Providers: map[string]string{"spotify": AccessNone}},
		"places":             {Default: ""},
		"events":             {},
		PermissionDefaultKey: {Default: "read"},
	}
	got := legacy.Levels()
	want := Permissions{
		"tracks":             AccessFull,
		"playlists":          AccessRead,
		"events":             DefaultAccessType,
		PermissionDefaultKey: AccessRead,
	}
	if len(got) != len(want) {
		t.Fatalf("Levels() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("Levels()[%q] = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["places"]; ok {
		t.Fatal("opt-in kind with empty level must stay unlisted")
	}
	if level := got.Level("playlists"); level != AccessRead {
		t.Fatalf("materialized map must drop provider overrides, got %q", level)
	}
}

func TestLevels_EmptyIsDenyAll(t *testing.T) {
	if got := (DataGrants{}).Levels(); got != nil {
		t.Fatalf("empty DataGrants must materialize to nil, got %v", got)
	}
	if got := (DataGrants{"places": {}}).Levels(); got != nil {
		t.Fatalf("only-empty opt-in entries must materialize to nil, got %v", got)
	}
	if got := ((DataGrants)(nil)).Levels(); got != nil {
		t.Fatalf("nil DataGrants must materialize to nil, got %v", got)
	}
}
