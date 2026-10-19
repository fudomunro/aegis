package types

import "testing"

func TestIsAgentContentPath(t *testing.T) {
	allowed := []string{
		"/import",
		"/import/status",
		"/import/file",
		"/picker",
		"/picker/sessions",
		"/picker/sessions/abc",
		"/picker/mediaItems",
		"/assets/asset-1/original",
		"/assets/asset-1/thumbnail",
		"/xrpc/com.atproto.sync.getBlob",
		"/messages/msg-1",
		"/threads/t-1",
		"/playlists/pl-1",
		"/playlists/pl-1/items",
		"/documents/doc-1/download",
		"/documents/doc-1/preview",
		"/documents/doc-1/thumb",
		"/api/documents/doc-1/download",
	}
	for _, p := range allowed {
		if !IsAgentContentPath(p) {
			t.Errorf("IsAgentContentPath(%q) = false, want true", p)
		}
	}

	denied := []string{
		"",
		"/",
		"/me",
		"/me/playlists",
		"/me/tracks",
		"/me/albums",
		"/me/artists",
		"/me/player",
		"/me/player/recently-played",
		"/me/messages",
		"/me/threads",
		"/me/events",
		"/me/calendars",
		"/me/following",
		"/me/subscriptions",
		"/search",
		"/search/metadata",
		"/tracks",
		"/tracks/t-1",
		"/albums",
		"/albums/a-1",
		"/artists",
		"/playlists",
		"/messages",
		"/threads",
		"/events",
		"/events/ev-1",
		"/contacts",
		"/contacts/c-1",
		"/budgets",
		"/budgets/b1/transactions",
		"/transactions",
		"/capabilities",
		"/albums",
		"/videos/v-1",
		"/channels/c-1",
		// Document detail includes OCR text — not content transport; use the
		// gated library text endpoint instead (issue #143).
		"/documents",
		"/documents/doc-1",
		"/api/documents/doc-1",
	}
	for _, p := range denied {
		if IsAgentContentPath(p) {
			t.Errorf("IsAgentContentPath(%q) = true, want false", p)
		}
	}
}
