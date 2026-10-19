package types

import (
	"testing"
)

// TestDataTypePathRules_Parity pins the ordered rule table to the historical
// DataTypeForPath switch (issue #15). When a type gains paths, extend both
// the table and this map — do not reintroduce a switch.
func TestDataTypePathRules_Parity(t *testing.T) {
	cases := map[string]string{
		"/me":                        "user",
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
		"/me/subscriptions":          "artists",
		"/me/playlists":              "playlists",
		"/playlists/abc/items":       "playlists",
		"/me/events":                 "events",
		"/calendars/primary/events":  "events",
		"/me/calendars":              "events",
		"/calendarList":              "events",
		"/users/me/calendarList":     "events",
		"/me/messages":               "messages",
		"/me/threads":                "messages",
		"/users/me/messages":         "messages",
		"/users/me/threads/t1":       "messages",
		"/gmail/users/me/messages":   "messages",
		"/me/photos":                 "photos",
		"/mediaItems/abc":            "photos",
		"/picker/sessions":           "photos",
		"/assets/abc":                "photos",
		"/search/metadata":           "photos",
		"/me/transactions":           "transactions",
		"/budgets/x/accounts":        "transactions",
		"/ofx/bank":                  "transactions",
		"/quicken/import":            "transactions",
		"/me/places":                 "places",
		"/visits/abc":                "places",
		"/api/v0/locations":          "places",
		"/api/v1/points":             "places",
		"/timeline/export":           "places",
		"/semantic":                  "places",
		"/search":                    "search",
		"/channels/abc":              "channels",
		"/me/library":                "library",
		"/me/recommendations":        "recommendations",
		"/unknown/thing":             "",
	}
	for path, want := range cases {
		if got := DataTypeForCanonicalPath(path); got != want {
			t.Errorf("DataTypeForCanonicalPath(%q) = %q, want %q", path, got, want)
		}
		if got := DataTypeForPath(path); got != want {
			t.Errorf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDataGrant_For(t *testing.T) {
	// Empty default preserves pre-permission behavior (DefaultAccessType).
	var empty DataGrant
	if got := empty.For("spotify"); got != DefaultAccessType {
		t.Fatalf("empty default = %q, want %q", got, DefaultAccessType)
	}

	tp := DataGrant{
		Default: AccessRead,
		Providers: map[string]string{
			"spotify":       AccessNone,
			"youtube-music": AccessFull, // cannot grant above type default
		},
	}
	if got := tp.For("spotify"); got != AccessNone {
		t.Fatalf("provider none = %q", got)
	}
	if got := tp.For("youtube-music"); got != AccessRead {
		t.Fatalf("provider cannot loosen type default: %q", got)
	}
	if got := tp.For("immich"); got != AccessRead {
		t.Fatalf("missing provider uses default: %q", got)
	}
	if got := tp.For(""); got != AccessRead {
		t.Fatalf("empty provider uses default: %q", got)
	}
}

func TestTypePolicies_For(t *testing.T) {
	var nilPolicies DataGrants
	if got := nilPolicies.For("tracks"); got.Default != "" || len(got.Providers) != 0 {
		t.Fatalf("nil policies = %+v", got)
	}
	p := DataGrants{"places": {Default: AccessNone}}
	if got := p.For("places").Default; got != AccessNone {
		t.Fatalf("places default = %q", got)
	}
	if got := p.For("tracks").Default; got != "" {
		t.Fatalf("unknown type = %q", got)
	}
}

func TestStricterAccess(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{AccessNone, AccessRead, AccessNone},
		{AccessRead, AccessNone, AccessNone},
		{AccessRead, AccessFull, AccessRead},
		{AccessFull, AccessRead, AccessRead},
		{AccessCity, AccessRead, AccessCity},
		{AccessWrite, AccessFull, AccessWrite},
	}
	for _, c := range cases {
		if got := stricterAccess(c.a, c.b); got != c.want {
			t.Errorf("stricterAccess(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
