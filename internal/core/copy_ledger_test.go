package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/types"
)

func TestServer_RecordMirrorCopyAndStamp(t *testing.T) {
	c := mustCopyLedger(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.copyLedger = c

	srv.recordMirrorCopy(MirrorKindPlaylists, "spotify", "abc", "youtube-music", "xyz", "Road Trip", 3, "snap1")
	info := srv.copyInfoFor("playlists", "youtube-music", "xyz")
	if info == nil {
		t.Fatal("expected copy stamp")
	}
	if info.Role != copies.CopyRoleMirror || info.SyncMode != copies.CopySyncModeFull || info.SyncStatus != copies.CopyStatusInSync {
		t.Fatalf("info = %+v", info)
	}
	if info.AsOf == "" {
		t.Fatal("expected as_of")
	}

	// Unknown locations stay unstamped (fail-open).
	if srv.copyInfoFor("playlists", "spotify", "abc") != nil {
		t.Fatal("origin should not be stamped")
	}

	items := []types.Playlist{{
		Name: "Road Trip",
		Sources: []types.PlaylistSource{
			{Service: "spotify", ID: "abc"},
			{Service: "youtube-music", ID: "xyz"},
		},
	}}
	srv.stampPlaylistCopies(items)
	if items[0].Sources[0].Copy != nil {
		t.Fatal("source origin should remain unstamped")
	}
	if items[0].Sources[1].Copy == nil || items[0].Sources[1].Copy.SyncStatus != copies.CopyStatusInSync {
		t.Fatalf("target stamp = %+v", items[0].Sources[1].Copy)
	}
}

func TestServer_RecordTakeoutSnapshot(t *testing.T) {
	c := mustCopyLedger(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.copyLedger = c

	srv.recordTakeoutSnapshot("/tmp/takeout-export", 10, 8, 2, true)
	rows := c.List(copies.Filter{Kind: "messages"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	rec := rows[0]
	if rec.SyncMode != copies.CopySyncModeOptimistic {
		t.Fatalf("sync_mode = %q", rec.SyncMode)
	}
	if rec.Provenance != copies.CopyProvenanceTakeout {
		t.Fatalf("provenance = %q", rec.Provenance)
	}
	if rec.Watermark != "complete" {
		t.Fatalf("watermark = %q", rec.Watermark)
	}
	if rec.Service != connectors.TakeoutServiceID {
		t.Fatalf("service = %q", rec.Service)
	}
}

func TestMirrorEntityKey(t *testing.T) {
	if got := mirrorEntityKey(MirrorKindTracks, "spotify", ""); got != "liked:spotify" {
		t.Fatalf("tracks key = %q", got)
	}
	if got := mirrorEntityKey(MirrorKindPlaylists, "spotify", "abc"); got != "playlists:spotify:abc" {
		t.Fatalf("playlist key = %q", got)
	}
}

func TestHandleLibraryCopies_ListAndFilter(t *testing.T) {
	c := mustCopyLedger(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.copyLedger = c

	if _, err := c.Upsert(copies.Record{
		Kind: "playlists", EntityKey: "playlists:spotify:abc",
		Service: "youtube-music", ExternalID: "xyz",
		Role: copies.CopyRoleMirror, Status: copies.CopyStatusInSync,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(copies.Record{
		Kind: "messages", EntityKey: "snapshot:google-takeout:/tmp/x",
		Service: connectors.TakeoutServiceID, ExternalID: "/tmp/x",
		Role: copies.CopyRoleImport, SyncMode: copies.CopySyncModeOptimistic, Status: copies.CopyStatusStale,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/library/copies", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryCopies)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp CopyListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d", resp.Total)
	}
	if resp.ByStatus[copies.CopyStatusInSync] != 1 || resp.ByStatus[copies.CopyStatusStale] != 1 {
		t.Fatalf("by_status = %v", resp.ByStatus)
	}
	if resp.SyncModes[copies.CopySyncModeOptimistic] != 1 {
		t.Fatalf("sync_modes = %v", resp.SyncModes)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/library/copies?kind=playlists", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryCopies)(w2, req2)
	var filtered CopyListResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || filtered.Copies[0].Kind != "playlists" {
		t.Fatalf("filtered = %+v", filtered)
	}
}

func TestHandleLibraryCopies_MethodAndDisabled(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest(http.MethodPost, "/v1/library/copies", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryCopies)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/library/copies", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryCopies)(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("disabled status = %d", w2.Code)
	}
	var resp CopyListResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 0 {
		t.Fatalf("total = %d", resp.Total)
	}
}

func TestStampOtherKinds(t *testing.T) {
	c := mustCopyLedger(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.copyLedger = c

	if _, err := c.Upsert(copies.Record{
		Kind: "photos", EntityKey: "photos:x", Service: "immich", ExternalID: "img1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(copies.Record{
		Kind: "messages", EntityKey: "msg:x", Service: "google-gmail", ExternalID: "m1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(copies.Record{
		Kind: "contacts", EntityKey: "c:x", Service: "google-contacts", ExternalID: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(copies.Record{
		Kind: "events", EntityKey: "e:x", Service: "google-calendar", ExternalID: "e1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(copies.Record{
		Kind: "tracks", EntityKey: "t:x", Service: "spotify", ExternalID: "t1",
	}); err != nil {
		t.Fatal(err)
	}

	photos := []types.Photo{{Sources: []types.PhotoSource{{Service: "immich", ID: "img1"}}}}
	srv.stampPhotoCopies(photos)
	if photos[0].Sources[0].Copy == nil {
		t.Fatal("photo copy stamp missing")
	}

	msgs := []types.Message{{Sources: []types.MessageSource{{Service: "google-gmail", ID: "m1"}}}}
	srv.stampMessageCopies(msgs)
	if msgs[0].Sources[0].Copy == nil {
		t.Fatal("message copy stamp missing")
	}

	contacts := []types.Contact{{Sources: []types.ContactSource{{Service: "google-contacts", ID: "c1"}}}}
	srv.stampContactCopies(contacts)
	if contacts[0].Sources[0].Copy == nil {
		t.Fatal("contact copy stamp missing")
	}

	events := []types.CalendarEvent{{Sources: []types.CalendarEventSource{{Service: "google-calendar", ID: "e1"}}}}
	srv.stampEventCopies(events)
	if events[0].Sources[0].Copy == nil {
		t.Fatal("event copy stamp missing")
	}

	tracks := []types.Track{{Sources: []types.TrackSource{{Service: "spotify", ID: "t1"}}}}
	srv.stampTrackCopies(tracks)
	if tracks[0].Sources[0].Copy == nil {
		t.Fatal("track copy stamp missing")
	}
}
