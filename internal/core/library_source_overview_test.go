package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

func TestEnrichLibraryServiceStatuses_NameCanonicalRole(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists"}},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	bySvc := map[string]types.LibraryServiceStatus{
		"spotify":       {Status: types.LibraryStatusOK, Count: 12},
		"youtube-music": {Status: types.LibraryStatusOK, Count: 8},
	}
	enrichLibraryServiceStatuses(srv, "playlists", bySvc)

	if bySvc["spotify"].Name != "Spotify" {
		t.Fatalf("spotify name = %q", bySvc["spotify"].Name)
	}
	if !bySvc["spotify"].Canonical || bySvc["spotify"].Role != "canonical" {
		t.Fatalf("spotify = %+v, want canonical role", bySvc["spotify"])
	}
	if bySvc["youtube-music"].Role != "mirror" {
		t.Fatalf("youtube role = %q, want mirror", bySvc["youtube-music"].Role)
	}
	if bySvc["youtube-music"].Canonical {
		t.Fatal("youtube must not be canonical")
	}
}

func TestEnrichLibraryServiceStatuses_MessagesSplitsArchiveSources(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: connectors.GoogleGmailServiceID, Name: connectors.GoogleGmailServiceName, Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	mail := openTestMailArchive(t)
	srv.mail = mail

	// Inventory: 3 Gmail-synced + 5 Takeout rows in the archive.
	for i := 0; i < 3; i++ {
		rec := MailArchiveRecord{
			Service:     connectors.GoogleGmailServiceID,
			ServiceName: connectors.GoogleGmailServiceName,
			ID:          "g" + string(rune('a'+i)),
			Subject:     "Gmail " + string(rune('a'+i)),
		}
		if err := mail.Upsert(rec); err != nil {
			t.Fatalf("upsert gmail: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		rec := MailArchiveRecord{
			Service:     takeoutServiceID,
			ServiceName: takeoutServiceName,
			ID:          "t" + string(rune('a'+i)),
			Subject:     "Takeout " + string(rune('a'+i)),
		}
		if err := mail.Upsert(rec); err != nil {
			t.Fatalf("upsert takeout: %v", err)
		}
	}
	if err := mail.SaveSyncCursor(connectors.GoogleGmailServiceID, MailSyncCursor{
		LastSyncAt: "2026-10-11T00:00:00Z",
		Done:       true,
	}); err != nil {
		t.Fatalf("save gmail cursor: %v", err)
	}
	if err := mail.SaveSyncCursor(takeoutServiceID, MailSyncCursor{
		LastSyncAt: "2026-08-24T00:00:00Z",
		Done:       true,
	}); err != nil {
		t.Fatalf("save takeout cursor: %v", err)
	}

	bySvc := map[string]types.LibraryServiceStatus{
		"mail-archive":                  {Status: types.LibraryStatusOK, Count: 3},
		connectors.GoogleGmailServiceID: {Status: types.LibraryStatusOK, Count: 3},
	}
	enrichLibraryServiceStatuses(srv, "messages", bySvc)

	to, ok := bySvc[takeoutServiceID]
	if !ok {
		t.Fatalf("missing takeout source: %+v", bySvc)
	}
	if to.Count != 5 {
		t.Fatalf("takeout count = %d, want 5 (archive inventory)", to.Count)
	}
	if to.Parent != connectors.GoogleGmailServiceID {
		t.Fatalf("takeout parent = %q, want %q", to.Parent, connectors.GoogleGmailServiceID)
	}
	if to.LastSyncedAt != "2026-08-24T00:00:00Z" {
		t.Fatalf("takeout last_synced_at = %q", to.LastSyncedAt)
	}
	if to.Name != takeoutServiceName {
		t.Fatalf("takeout name = %q", to.Name)
	}
	gm := bySvc[connectors.GoogleGmailServiceID]
	if gm.LastSyncedAt != "2026-10-11T00:00:00Z" {
		t.Fatalf("gmail last_synced_at = %q", gm.LastSyncedAt)
	}
	if gm.Name != connectors.GoogleGmailServiceName {
		t.Fatalf("gmail name = %q", gm.Name)
	}
	// mail-archive rollup stays for by_service compatibility.
	if bySvc["mail-archive"].Status != types.LibraryStatusOK {
		t.Fatalf("mail-archive = %+v", bySvc["mail-archive"])
	}
}

func TestHandleLibraryPlaylists_SourceOverviewFields(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists"}},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakePlaylistProvider{
			playlists: []types.Playlist{
				{Name: "Jazz", TrackCount: 3, Sources: []types.PlaylistSource{{Service: "spotify", ID: "sp1"}}},
			},
		},
		"youtube-music": &fakePlaylistProvider{
			playlists: []types.Playlist{
				{Name: "Focus", TrackCount: 9, Sources: []types.PlaylistSource{{Service: "youtube-music", ID: "yt1"}}},
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryPlaylists)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	var resp types.PlaylistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sp := resp.ByService["spotify"]
	if sp.Name != "Spotify" || !sp.Canonical || sp.Role != "canonical" {
		t.Fatalf("spotify overview = %+v", sp)
	}
	yt := resp.ByService["youtube-music"]
	if yt.Name != "YouTube Music" || yt.Role != "mirror" {
		t.Fatalf("youtube overview = %+v", yt)
	}
}

func TestAdminHTML_LibrarySourceOverview(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="library-sources"`,
		"renderLibrarySourceOverview",
		"source-overview",
		"source-box",
		"source-role-canonical",
		"source-role-mirror",
		"data-source",
		// Icons for data types + services/files (issue #162 follow-up).
		"sourceIconHTML",
		"sourceIconPath",
		"dataTypeIconHTML",
		"FLOW_ICONS",
		`"google-gmail"`,
		`"google-takeout"`,
		`"mail-archive"`,
		"source-box-name-label",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin HTML missing %q", want)
		}
	}
}
