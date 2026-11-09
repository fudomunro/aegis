package core

import (
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
)

// TestLibrarySourceDisplayName pins the resolution order on the Host seam:
// the connector registry's canonical name, then the configured service
// name, then the raw ID. Kind-owned sources (the messages archive) override
// through StatusHooks instead of the generic path (issue #312).
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
		if got := srv.SourceName(tc.id); got != tc.want {
			t.Errorf("SourceName(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}

	// The messages kind labels its local archive via its declared hooks,
	// not the generic seam (issue #312).
	hooks := kinds.MessagesStatusHooks(srv, mailArchive{srv})
	if got, ok := hooks.SourceName("mail-archive"); !ok || got != "Mail archive" {
		t.Errorf("messages SourceName(mail-archive) = %q, %v; want %q, true", got, ok, "Mail archive")
	}
	if _, ok := hooks.SourceName("spotify"); ok {
		t.Error("messages SourceName must not claim sources it does not own")
	}
}
