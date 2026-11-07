package core

import (
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
)

// TestLibrarySourceDisplayName pins the resolution order: core-local store
// labels, then the connector registry's canonical name, then the configured
// service name, then the raw ID.
func TestLibrarySourceDisplayName(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "My Music", Action: "inject"},
			{ID: "google-gmail", Name: "Work Mail", Action: "inject"},
			{ID: "tidal", Name: "Tidal", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	cases := []struct {
		id   string
		want string
	}{
		{"mail-archive", "Mail archive"},
		{connectors.TakeoutServiceID, connectors.TakeoutServiceName},
		// Canonical registry names win over configured names.
		{"spotify", "Spotify"},
		{"google-gmail", "Gmail"},
		{connectors.ImmichServiceID, connectors.ImmichServiceName},
		// Services without a connector fall back to config, then raw ID.
		{"tidal", "Tidal"},
		{"unknown-service", "unknown-service"},
	}
	for _, tc := range cases {
		if got := librarySourceDisplayName(srv, tc.id); got != tc.want {
			t.Errorf("librarySourceDisplayName(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
