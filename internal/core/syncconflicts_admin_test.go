package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/syncjobs"
)

// stubConflictResolver answers ResolveConflict from the test's plan.
type stubConflictResolver struct {
	accept *bool
	err    error
}

func (s *stubConflictResolver) ResolveConflict(_ context.Context, id string, accept bool) (syncjobs.SyncConflict, error) {
	s.accept = &accept
	if s.err != nil {
		return syncjobs.SyncConflict{}, s.err
	}
	status := syncjobs.ConflictStatusRejected
	if accept {
		status = syncjobs.ConflictStatusAccepted
	}
	return syncjobs.SyncConflict{ID: id, Status: status}, nil
}

func testConflictStore(t *testing.T) *syncjobs.ConflictStore {
	t.Helper()
	s, err := syncjobs.OpenConflictStore(nil, filepath.Join(t.TempDir(), "sync_jobs.db"))
	if err != nil {
		t.Fatalf("OpenConflictStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func syncConflictsServer(t *testing.T) (*Server, *syncjobs.ConflictStore, *stubConflictResolver) {
	t.Helper()
	srv := syncAdminServer(t)
	store := testConflictStore(t)
	stub := &stubConflictResolver{}
	h := srv.admin()
	h.SyncConflicts = store
	h.Conflicts = stub
	return srv, store, stub
}

func TestHandleAdminSyncConflicts_List(t *testing.T) {
	srv, store, _ := syncConflictsServer(t)
	if _, err := store.Upsert(syncjobs.SyncConflict{
		Kind: "playlists", Service: "icloud", EntityKey: "playlists:google:P1",
		Subject: "Road trip", Field: syncjobs.ConflictFieldTitle, MemberValue: "Mine",
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.Upsert(syncjobs.SyncConflict{
		Kind: "contacts", Service: "icloud", EntityKey: "contacts:Ada",
		Field: syncjobs.ConflictFieldRecord, MemberValue: "changed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(resolved.ID, syncjobs.ConflictStatusAccepted, "applied"); err != nil {
		t.Fatal(err)
	}

	req := adminRequest(http.MethodGet, "/v1/admin/sync/conflicts", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Conflicts []syncjobs.SyncConflict `json:"conflicts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0].Subject != "Road trip" {
		t.Fatalf("conflicts = %+v, want the one open row", got.Conflicts)
	}

	// Status filter reaches the resolved row.
	req = adminRequest(http.MethodGet, "/v1/admin/sync/conflicts?status=accepted", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != "contacts" {
		t.Fatalf("accepted = %+v", got.Conflicts)
	}
}

func TestHandleAdminSyncConflicts_AcceptReject(t *testing.T) {
	srv, store, stub := syncConflictsServer(t)
	rec, err := store.Upsert(syncjobs.SyncConflict{
		Kind: "playlists", Service: "icloud", EntityKey: "playlists:google:P1",
		Field: syncjobs.ConflictFieldTitle, MemberValue: "Mine",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := adminRequest(http.MethodPost, "/v1/admin/sync/conflicts/"+rec.ID+"/accept", "admin-key", "{}")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("accept status = %d, body %s", w.Code, w.Body.String())
	}
	if stub.accept == nil || !*stub.accept {
		t.Fatal("accept must reach the resolver")
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync/conflicts/"+rec.ID+"/reject", "admin-key", "{}")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reject status = %d, body %s", w.Code, w.Body.String())
	}
	if stub.accept == nil || *stub.accept {
		t.Fatal("reject must reach the resolver")
	}
}

func TestHandleAdminSyncConflicts_Errors(t *testing.T) {
	srv, _, stub := syncConflictsServer(t)

	req := adminRequest(http.MethodPost, "/v1/admin/sync/conflicts/%20%20/accept", "admin-key", "{}")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty id = %d, want 400", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync/conflicts/1/frob", "admin-key", "{}")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown action = %d, want 404", w.Code)
	}

	req = adminRequest(http.MethodDelete, "/v1/admin/sync/conflicts", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method = %d, want 405", w.Code)
	}

	// Terminal resolver failures (already resolved, refused deletes) map to
	// 400 so the panel can show the explanation; provider trouble is 502.
	stub.err = syncjobs.ErrSyncTerminal
	req = adminRequest(http.MethodPost, "/v1/admin/sync/conflicts/1/accept", "admin-key", "{}")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("terminal = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "#3") {
		t.Fatalf("user-facing error must not cite issues: %s", w.Body.String())
	}
	stub.err = context.DeadlineExceeded
	req = adminRequest(http.MethodPost, "/v1/admin/sync/conflicts/1/accept", "admin-key", "{}")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.admin().HandleAdminSyncConflicts)(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("provider failure = %d, want 502", w.Code)
	}
}
