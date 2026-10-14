package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func mustCopyLedger(t *testing.T) *CopyLedger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "copy_ledger.db")
	c, err := OpenCopyLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCopyLedger_UpsertGetListDelete(t *testing.T) {
	c := mustCopyLedger(t)
	rec, err := c.Upsert(CopyRecord{
		Kind:          "playlists",
		EntityKey:     "playlists:spotify:abc",
		SourceService: "spotify",
		SourceID:      "abc",
		Service:       "youtube-music",
		ExternalID:    "xyz",
		Role:          CopyRoleMirror,
		Version:       CopyVersion{ItemCount: 12, Name: "Road Trip"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != CopyStatusInSync {
		t.Fatalf("status = %q, want %q", rec.Status, CopyStatusInSync)
	}
	if rec.SyncMode != CopySyncModeFull {
		t.Fatalf("sync_mode = %q, want full", rec.SyncMode)
	}
	if rec.LastSyncedAt == "" {
		t.Fatal("expected last_synced_at on upsert")
	}

	got, ok := c.Get("playlists", "youtube-music", "xyz")
	if !ok {
		t.Fatal("expected row")
	}
	if got.Version.ItemCount != 12 || got.Version.Name != "Road Trip" {
		t.Fatalf("version = %+v", got.Version)
	}
	if got.SourceService != "spotify" || got.SourceID != "abc" {
		t.Fatalf("source = %s:%s", got.SourceService, got.SourceID)
	}

	// Upsert again is an update, not a second row.
	if _, err := c.Upsert(CopyRecord{
		Kind:       "playlists",
		EntityKey:  "playlists:spotify:abc",
		Service:    "youtube-music",
		ExternalID: "xyz",
		Version:    CopyVersion{ItemCount: 15, Name: "Road Trip"},
	}); err != nil {
		t.Fatal(err)
	}
	rows := c.List(CopyFilter{Kind: "playlists"})
	if len(rows) != 1 {
		t.Fatalf("list len = %d, want 1", len(rows))
	}
	if rows[0].Version.ItemCount != 15 {
		t.Fatalf("item_count = %d, want 15", rows[0].Version.ItemCount)
	}

	byEntity := c.GetByEntity("playlists", "playlists:spotify:abc")
	if len(byEntity) != 1 {
		t.Fatalf("entity rows = %d", len(byEntity))
	}

	if !c.Delete("playlists", "playlists:spotify:abc", "youtube-music", "xyz") {
		t.Fatal("delete should succeed")
	}
	if _, ok := c.Get("playlists", "youtube-music", "xyz"); ok {
		t.Fatal("row should be gone")
	}
}

func TestCopyLedger_ValidationAndDisabled(t *testing.T) {
	var disabled CopyLedger
	if _, err := disabled.Upsert(CopyRecord{Kind: "playlists", EntityKey: "k", Service: "spotify"}); err != nil {
		t.Fatalf("disabled upsert should fail-open: %v", err)
	}
	if _, err := (&CopyLedger{}).Upsert(CopyRecord{Kind: "playlists", EntityKey: "k"}); err == nil {
		t.Fatal("expected error when service missing")
	}
	empty := &CopyLedger{}
	if empty.Enabled() {
		t.Fatal("empty ledger should be disabled")
	}
	rec, err := empty.Upsert(CopyRecord{Kind: "playlists", EntityKey: "k", Service: "spotify"})
	if err != nil {
		t.Fatalf("disabled upsert should fail-open: %v", err)
	}
	if rec.EntityKey != "k" {
		t.Fatalf("entity = %q", rec.EntityKey)
	}
	if empty.List(CopyFilter{}) != nil {
		t.Fatal("disabled list should be nil")
	}
	if empty.Delete("a", "b", "c", "d") {
		t.Fatal("disabled delete should be false")
	}
}

func TestCopySyncModeForKind(t *testing.T) {
	if copySyncModeForKind("messages") != CopySyncModeOptimistic {
		t.Fatal("messages should be optimistic")
	}
	if copySyncModeForKind("playlists") != CopySyncModeFull {
		t.Fatal("playlists should be full")
	}
	if copySyncModeForKind("photos") != CopySyncModeFull {
		t.Fatal("photos should be full")
	}
}

func TestCopyLedger_MarkChecked(t *testing.T) {
	c := mustCopyLedger(t)
	if _, err := c.Upsert(CopyRecord{
		Kind: "photos", EntityKey: "photos:immich:1", Service: "google-photos", ExternalID: "p1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkChecked("photos", "photos:immich:1", "google-photos", "p1", CopyStatusStale); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("photos", "google-photos", "p1")
	if !ok || got.Status != CopyStatusStale {
		t.Fatalf("got %+v", got)
	}
	if got.LastCheckedAt == "" {
		t.Fatal("expected last_checked_at")
	}
}

func TestServer_RecordMirrorCopyAndStamp(t *testing.T) {
	c := mustCopyLedger(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.copies = c

	srv.recordMirrorCopy(MirrorKindPlaylists, "spotify", "abc", "youtube-music", "xyz", "Road Trip", 3, "snap1")
	info := srv.copyInfoFor("playlists", "youtube-music", "xyz")
	if info == nil {
		t.Fatal("expected copy stamp")
	}
	if info.Role != CopyRoleMirror || info.SyncMode != CopySyncModeFull || info.SyncStatus != CopyStatusInSync {
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
	if items[0].Sources[1].Copy == nil || items[0].Sources[1].Copy.SyncStatus != CopyStatusInSync {
		t.Fatalf("target stamp = %+v", items[0].Sources[1].Copy)
	}
}

func TestServer_RecordTakeoutSnapshot(t *testing.T) {
	c := mustCopyLedger(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.copies = c

	srv.recordTakeoutSnapshot("/tmp/takeout-export", 10, 8, 2, true)
	rows := c.List(CopyFilter{Kind: "messages"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	rec := rows[0]
	if rec.SyncMode != CopySyncModeOptimistic {
		t.Fatalf("sync_mode = %q", rec.SyncMode)
	}
	if rec.Provenance != CopyProvenanceTakeout {
		t.Fatalf("provenance = %q", rec.Provenance)
	}
	if rec.Watermark != "complete" {
		t.Fatalf("watermark = %q", rec.Watermark)
	}
	if rec.Service != takeoutServiceID {
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
	srv.copies = c

	if _, err := c.Upsert(CopyRecord{
		Kind: "playlists", EntityKey: "playlists:spotify:abc",
		Service: "youtube-music", ExternalID: "xyz",
		Role: CopyRoleMirror, Status: CopyStatusInSync,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(CopyRecord{
		Kind: "messages", EntityKey: "snapshot:google-takeout:/tmp/x",
		Service: takeoutServiceID, ExternalID: "/tmp/x",
		Role: CopyRoleImport, SyncMode: CopySyncModeOptimistic, Status: CopyStatusStale,
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
	if resp.ByStatus[CopyStatusInSync] != 1 || resp.ByStatus[CopyStatusStale] != 1 {
		t.Fatalf("by_status = %v", resp.ByStatus)
	}
	if resp.SyncModes[CopySyncModeOptimistic] != 1 {
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
	srv.copies = c

	if _, err := c.Upsert(CopyRecord{
		Kind: "photos", EntityKey: "photos:x", Service: "immich", ExternalID: "img1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(CopyRecord{
		Kind: "messages", EntityKey: "msg:x", Service: "google-gmail", ExternalID: "m1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(CopyRecord{
		Kind: "contacts", EntityKey: "c:x", Service: "google-contacts", ExternalID: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(CopyRecord{
		Kind: "events", EntityKey: "e:x", Service: "google-calendar", ExternalID: "e1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(CopyRecord{
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

func TestOpenCopyLedger_EmptyPathDisables(t *testing.T) {
	c, err := OpenCopyLedger("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled() {
		t.Fatal("empty path should disable")
	}
	if c.Path() != "" {
		t.Fatalf("path = %q", c.Path())
	}
}
