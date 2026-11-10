package policy

import "testing"

func TestLevel_DenyByDefault(t *testing.T) {
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
		{"opt-in kind explicit entry", GrantMap{DefaultKey: AccessFull, "places": AccessCity}, "places", AccessCity},
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

func TestLevels_MaterializesRuleMap(t *testing.T) {
	legacy := DataGrants{
		"tracks":    {Default: "rw"},
		"playlists": {Default: "ro", Providers: map[string]string{"spotify": AccessNone}},
		"places":    {Default: ""},
		"events":    {},
		DefaultKey:  {Default: "read"},
	}
	got := legacy.Levels()
	want := GrantMap{
		"tracks":    AccessFull,
		"playlists": AccessRead,
		"events":    AccessNone,
		"places":    AccessNone,
		DefaultKey:  AccessRead,
	}
	if len(got) != len(want) {
		t.Fatalf("Levels() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("Levels()[%q] = %q, want %q", k, got[k], v)
		}
	}
	if level := got.Level("playlists"); level != AccessRead {
		t.Fatalf("materialized map must drop provider overrides, got %q", level)
	}
}

func TestLevels_EmptyIsDenyAll(t *testing.T) {
	if got := (DataGrants{}).Levels(); got != nil {
		t.Fatalf("empty DataGrants must materialize to nil, got %v", got)
	}
	if got := ((DataGrants)(nil)).Levels(); got != nil {
		t.Fatalf("nil DataGrants must materialize to nil, got %v", got)
	}
	// Listed kinds without a stated level resolve to none.
	empty := DataGrants{"places": {}, "tracks": {Default: ""}}.Levels()
	for _, kind := range []string{"places", "tracks"} {
		if empty.Level(kind) != AccessNone {
			t.Fatalf("empty level for %s = %q, want none", kind, empty.Level(kind))
		}
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
