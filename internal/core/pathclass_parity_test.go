package core

import (
	"testing"
)

// TestKindPathRules_Parity pins the ordered rule table to the historical
// KindForPath switch (issue #15). When a kind gains paths, extend both
// the table and this map — do not reintroduce a switch.
func TestKindPathRules_Parity(t *testing.T) {
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
		if got := KindForCanonicalPath(path); got != want {
			t.Errorf("KindForCanonicalPath(%q) = %q, want %q", path, got, want)
		}
		if got := KindForPath(path); got != want {
			t.Errorf("KindForPath(%q) = %q, want %q", path, got, want)
		}
	}
}
