package types

import "testing"

// TestDataTypeForPath_Contacts pins the contacts proxy paths (issue #254 —
// these cases lived in core's contacts_test.go).
func TestDataTypeForPath_Contacts(t *testing.T) {
	cases := map[string]string{
		"/me/contacts":      "contacts",
		"/contacts":         "contacts",
		"/contacts/abc":     "contacts",
		"/contacts/abc/x":   "contacts",
		"/me/contacts/":     "contacts",
		"/contact":          "", // singular is not a known type
		"/me/contact-lists": "",
	}
	for path, want := range cases {
		if got := DataTypeForPath(path); got != want {
			t.Errorf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestDataTypeForPath_Documents pins the document-proxy paths, including the
// Paperless and Google Drive shapes.
func TestDataTypeForPath_Documents(t *testing.T) {
	cases := map[string]string{
		"/me/documents":          "documents",
		"/documents/11":          "documents",
		"/documents/11/download": "documents",
		"/api/documents/11":      "documents",
		"/correspondents/3":      "documents",
		"/document_types/4":      "documents",
		"/api/tags/7":            "documents",
		"/files":                 "documents",
		"/files/gdoc-1":          "documents",
		"/files/gdoc-1/export":   "documents",
		"/drive/files/gdoc-1":    "documents",
	}
	for path, want := range cases {
		if got := DataTypeForPath(path); got != want {
			t.Fatalf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
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
		if got := DataTypeForPath(path); got != want {
			t.Errorf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDataTypeForPath_Photos(t *testing.T) {
	cases := map[string]string{
		"/me/photos":           "photos",
		"/photos":              "photos",
		"/photos/abc":          "photos",
		"/mediaItems":          "photos",
		"/mediaItems/abc":      "photos",
		"/mediaItems/abc/x":    "photos",
		"/me/photos/":          "photos",
		"/mediaItems?pageSize": "photos",
	}
	for path, want := range cases {
		if got := DataTypeForPath(path); got != want {
			t.Errorf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDataTypeForPath_Picker(t *testing.T) {
	cases := map[string]string{
		"/picker/sessions":          "photos",
		"/picker/sessions/abc":      "photos",
		"/picker/mediaItems":        "photos",
		"/mediaItems?sessionId=s-1": "photos",
	}
	for path, want := range cases {
		if got := DataTypeForPath(path); got != want {
			t.Errorf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDataTypeForPath_Places(t *testing.T) {
	cases := map[string]string{
		"/me/places":        "places",
		"/visits":           "places",
		"/api/v0/locations": "places",
		"/api/v0/visit":     "places",
		"/api/v1/points":    "places",
		"/timeline/2023-01": "places",
	}
	for p, want := range cases {
		if got := DataTypeForPath(p); got != want {
			t.Fatalf("DataTypeForPath(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestDataTypeForPath_Transactions(t *testing.T) {
	for _, p := range []string{
		"/budgets", "/budgets/x/transactions", "/accounts", "/transactions",
		"/me/transactions", "/categories", "/payees", "/quicken/import",
	} {
		if got := DataTypeForPath(p); got != "transactions" {
			t.Fatalf("DataTypeForPath(%q) = %q", p, got)
		}
	}
}

func TestDataTypeForPath_PeopleAPI(t *testing.T) {
	cases := map[string]string{
		"/people/me/connections": "contacts",
		"/people/c123":           "contacts",
		"/people:searchContacts": "contacts",
	}
	for path, want := range cases {
		if got := DataTypeForPath(path); got != want {
			t.Errorf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDataTypeForPath_ImmichAssets(t *testing.T) {
	cases := map[string]string{
		"/search/metadata":      "photos",
		"/assets/abc/original":  "photos",
		"/assets/abc/thumbnail": "photos",
		"/albums":               "albums", // music albums win; Immich albums via connector proxy still work under default
	}
	for path, want := range cases {
		if got := DataTypeForPath(path); got != want {
			t.Errorf("DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}
