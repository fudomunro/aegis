package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/ownership"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestOwnershipStatuses_ConnectedServiceIsUnderManagement(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"playlists"}},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.ownership = mustOwnershipStore(t, "")
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
	srv.connectors["youtube-music"] = connectors.NewYouTubeMusicConnector(srv)
	srv.tokens.Tokens["spotify"] = &tokens.OAuthToken{AccessToken: "at"}

	statuses := srv.admin().OwnershipStatuses()
	byKind := ownershipByKind(t, statuses)

	playlists := byKind["playlists"]
	assertOwnershipSource(t, playlists.UnderManagement, "spotify")
	assertOwnershipRoute(t, playlists.UnusedRoutes, "youtube-music")
	for _, src := range playlists.UnderManagement {
		if src.ServiceID == "spotify" && src.Role != "canonical" {
			t.Fatalf("spotify role = %q, want canonical", src.Role)
		}
	}
	if byKind["playlists"].Confirmed {
		t.Fatal("nothing is confirmed yet")
	}
}

func TestOwnershipStatuses_EmptyArchiveStoreStaysARoute(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: connectors.SignalServiceID, Name: connectors.SignalServiceName, Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.ownership = mustOwnershipStore(t, "")
	store, err := connectors.OpenSignalStore(filepath.Join(t.TempDir(), "signal.db"))
	if err != nil {
		t.Fatalf("open signal store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	srv.signal = store
	srv.connectors[connectors.SignalServiceID] = connectors.NewSignalConnector(store)

	byKind := ownershipByKind(t, srv.admin().OwnershipStatuses())
	assertOwnershipRoute(t, byKind["messages"].UnusedRoutes, connectors.SignalServiceID)
	assertOwnershipRoute(t, byKind["contacts"].UnusedRoutes, connectors.SignalServiceID)
	if len(byKind["messages"].UnderManagement) != 0 {
		t.Fatalf("empty store must not be under management: %+v", byKind["messages"].UnderManagement)
	}

	// A row import flips it to under management for the kinds it feeds.
	if err := store.UpsertMessage("f1", kinds.Message{
		Channel: "signal", Subject: "hi", Sources: []kinds.MessageSource{{Service: connectors.SignalServiceID, ID: "m1"}},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	byKind = ownershipByKind(t, srv.admin().OwnershipStatuses())
	assertOwnershipSource(t, byKind["messages"].UnderManagement, connectors.SignalServiceID)
	if hasRoute(byKind["messages"].UnusedRoutes, connectors.SignalServiceID) {
		t.Fatal("imported signal store must not stay an unused route")
	}
}

func TestOwnershipStatuses_MailEvidence(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: connectors.GoogleGmailServiceID, Name: connectors.GoogleGmailServiceName, Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.ownership = mustOwnershipStore(t, "")
	srv.connectors[connectors.GoogleGmailServiceID] = connectors.NewGoogleGmailConnector(srv)
	mailArch := openTestMailArchive(t)
	srv.mail = mailArch

	// No Takeout path and no rows: Takeout stays a route.
	byKind := ownershipByKind(t, srv.admin().OwnershipStatuses())
	assertOwnershipRoute(t, byKind["messages"].UnusedRoutes, connectors.TakeoutServiceID)

	// Gmail is not connected, so it is a route too — until the archive
	// holds Gmail rows, which is evidence the data is under management.
	if err := mailArch.Upsert(mail.Record{
		Service: connectors.GoogleGmailServiceID, ServiceName: connectors.GoogleGmailServiceName,
		ID: "g1", Subject: "hello",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	byKind = ownershipByKind(t, srv.admin().OwnershipStatuses())
	assertOwnershipSource(t, byKind["messages"].UnderManagement, connectors.GoogleGmailServiceID)

	// Importing Takeout rows moves Takeout from route to source.
	if err := mailArch.Upsert(mail.Record{
		Service: connectors.TakeoutServiceID, ServiceName: connectors.TakeoutServiceName,
		ID: "t1", Subject: "old",
	}); err != nil {
		t.Fatalf("upsert takeout: %v", err)
	}
	byKind = ownershipByKind(t, srv.admin().OwnershipStatuses())
	assertOwnershipSource(t, byKind["messages"].UnderManagement, connectors.TakeoutServiceID)
	if hasRoute(byKind["messages"].UnusedRoutes, connectors.TakeoutServiceID) {
		t.Fatal("takeout with rows must not stay an unused route")
	}
}

func TestOwnershipStatuses_FreshnessPassthrough(t *testing.T) {
	cfg := &Config{Services: []Service{}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.ownership = mustOwnershipStore(t, "")
	srv.copyLedger = mustCopyLedger(t)
	if _, err := srv.copyLedger.Upsert(copies.Record{
		Kind:          "playlists",
		EntityKey:     "playlists:spotify:p1",
		SourceService: "spotify",
		SourceID:      "p1",
		Service:       "youtube-music",
		ExternalID:    "y1",
		Role:          copies.CopyRoleMirror,
		Version:       copies.Version{ItemCount: 1},
	}); err != nil {
		t.Fatalf("upsert copy: %v", err)
	}
	byKind := ownershipByKind(t, srv.admin().OwnershipStatuses())
	f := byKind["playlists"].Freshness
	if f == nil || f.Total != 1 {
		t.Fatalf("freshness = %+v, want a 1-row rollup", f)
	}
}

func TestHandleAdminOwnership_ConfirmReopen(t *testing.T) {
	cfg := &Config{Services: []Service{}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.ownership = mustOwnershipStore(t, filepath.Join(t.TempDir(), "ownership.json"))

	// GET lists every builtin kind.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/ownership", nil)
	req.Header.Set("X-Api-Key", "test-key")
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", w.Code, w.Body.String())
	}
	var list struct {
		Ownership []shared.OwnershipStatus `json:"ownership"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Ownership) != len(kinds.BuiltinLibraryKinds) {
		t.Fatalf("got %d kinds, want %d", len(list.Ownership), len(kinds.BuiltinLibraryKinds))
	}

	// POST confirms one kind.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/admin/ownership/messages", strings.NewReader(`{"note":"all of it"}`))
	req.Header.Set("X-Api-Key", "test-key")
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST status = %d body=%s", w.Code, w.Body.String())
	}
	var st shared.OwnershipStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Kind != "messages" || !st.Confirmed || st.Note != "all of it" {
		t.Fatalf("confirm response = %+v", st)
	}

	// DELETE reopens it.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/v1/admin/ownership/messages", nil)
	req.Header.Set("X-Api-Key", "test-key")
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Confirmed {
		t.Fatalf("still confirmed after reopen: %+v", st)
	}

	// Unknown kinds 404.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/admin/ownership/not-a-kind", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "test-key")
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown kind status = %d, want 404", w.Code)
	}

	// Wrong method 405s.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/admin/ownership", nil)
	req.Header.Set("X-Api-Key", "test-key")
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", w.Code)
	}
}

func TestHandleAdminOwnership_RequiresAuth(t *testing.T) {
	cfg := &Config{Services: []Service{}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.ownership = mustOwnershipStore(t, "")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/ownership", nil)
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", w.Code)
	}
}

func TestOwnershipRoutes_ActionsMatchSeams(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "immich", Name: "Immich", Action: "inject"},
			{ID: connectors.QuickenServiceID, Name: connectors.QuickenServiceName, Action: "inject"},
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.ownership = mustOwnershipStore(t, "")
	srv.connectors["immich"] = connectors.NewImmichConnector(srv)
	srv.connectors[connectors.QuickenServiceID] = connectors.NewQuickenConnector(nil)
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)

	byKind := ownershipByKind(t, srv.admin().OwnershipStatuses())
	actions := map[string]shared.OwnershipAction{}
	for _, rt := range byKind["photos"].UnusedRoutes {
		actions[rt.ServiceID] = rt.Action
	}
	for _, rt := range byKind["transactions"].UnusedRoutes {
		actions[rt.ServiceID] = rt.Action
	}
	for _, rt := range byKind["playlists"].UnusedRoutes {
		actions[rt.ServiceID] = rt.Action
	}
	if a := actions["immich"]; a.Kind != "setup" || a.Target != "immich" {
		t.Fatalf("immich action = %+v, want setup/immich", a)
	}
	if a := actions[connectors.QuickenServiceID]; a.Kind != "import" {
		t.Fatalf("quicken action = %+v, want import", a)
	}
	if a := actions["spotify"]; a.Kind != "connect" {
		t.Fatalf("spotify action = %+v, want connect", a)
	}
}

// --- helpers ---

// TestHandleAdminOwnership_BrowserWritePath pins the tailnet-browser write
// path: ambient identity, same-origin proof, and a JSON content type. The
// API-key path skips the CSRF guard, so a handler test without this path
// cannot see a content-type or origin rejection (issue #165).
func TestHandleAdminOwnership_BrowserWritePath(t *testing.T) {
	cfg := &Config{Services: []Service{}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.ownership = mustOwnershipStore(t, filepath.Join(t.TempDir(), "ownership.json"))
	srv.authMode = AuthModeTailnet
	srv.identity = fakeIdentity{login: "user@example.com"}

	// The browser write: same-origin fetch with a JSON body.
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/ownership/messages",
		strings.NewReader(`{"note":"all of it"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://aegis.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin JSON write: %d %s", w.Code, w.Body.String())
	}

	// The fetch default for a string body is text/plain — the write guard
	// must reject it, which is what a missing Content-Type header causes.
	req = httptest.NewRequest(http.MethodPost, "/v1/admin/ownership/messages",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "https://aegis.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("text/plain write: %d, want 403 (fetch defaults a string body to text/plain)", w.Code)
	}

	// Bodyless DELETE is a safe content type and must stay accepted.
	req = httptest.NewRequest(http.MethodDelete, "/v1/admin/ownership/messages", nil)
	req.Header.Set("Origin", "https://aegis.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminOwnership)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin DELETE: %d %s", w.Code, w.Body.String())
	}
}

func mustOwnershipStore(t *testing.T, path string) *ownership.Store {
	t.Helper()
	s, err := ownership.NewStore(path)
	if err != nil {
		t.Fatalf("ownership store: %v", err)
	}
	return s
}

func ownershipByKind(t *testing.T, statuses []shared.OwnershipStatus) map[string]shared.OwnershipStatus {
	t.Helper()
	out := make(map[string]shared.OwnershipStatus, len(statuses))
	for _, st := range statuses {
		out[st.Kind] = st
	}
	for _, want := range kinds.BuiltinLibraryKinds {
		if _, ok := out[want.ID]; !ok {
			t.Fatalf("missing kind %q in ownership status", want.ID)
		}
	}
	return out
}

func assertOwnershipSource(t *testing.T, sources []shared.OwnershipSource, serviceID string) {
	t.Helper()
	for _, s := range sources {
		if s.ServiceID == serviceID {
			return
		}
	}
	t.Fatalf("service %q not in under_management: %+v", serviceID, sources)
}

func assertOwnershipRoute(t *testing.T, routes []shared.OwnershipRoute, serviceID string) {
	t.Helper()
	for _, r := range routes {
		if r.ServiceID == serviceID {
			return
		}
	}
	t.Fatalf("service %q not in unused_routes: %+v", serviceID, routes)
}

func hasRoute(routes []shared.OwnershipRoute, serviceID string) bool {
	for _, r := range routes {
		if r.ServiceID == serviceID {
			return true
		}
	}
	return false
}
