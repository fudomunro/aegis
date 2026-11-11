package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/apps"
	"github.com/fudomunro/aegis/internal/shared"
)

func testAppStore(t *testing.T) *apps.Store {
	t.Helper()
	store, err := apps.NewStore(filepath.Join(t.TempDir(), "apps.json"))
	if err != nil {
		t.Fatalf("apps.NewStore: %v", err)
	}
	return store
}

func createTestApp(t *testing.T, store *apps.Store, id string, scopes shared.GrantMap) (view apps.AppView, token string) {
	t.Helper()
	view, token, err := store.Create(apps.AppCreateRequest{ID: id, Name: id, Scopes: scopes})
	if err != nil {
		t.Fatalf("Create(%s): %v", id, err)
	}
	return view, token
}

func TestAuthMiddleware_AppTokenLibraryOnly(t *testing.T) {
	store := testAppStore(t)
	_, token := createTestApp(t, store, "eco", shared.GrantMap{
		"events":   shared.AccessRead,
		"contacts": shared.AccessNone,
	})
	svc := &Service{
		ID:     "google-calendar",
		Name:   "Google Calendar",
		Action: "inject",
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.apps = store
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	// Allowed: library events with app token.
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("events"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("events status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Doctor appointment") {
		t.Fatalf("expected full event title for read scope: %s", w.Body.String())
	}

	// Denied: contacts outside scopes.
	req = httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("contacts"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("contacts status = %d body = %s", w.Code, w.Body.String())
	}

	// Denied: admin surface.
	req = httptest.NewRequest("GET", "/v1/admin/apps", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminApps)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("admin status = %d body = %s", w.Code, w.Body.String())
	}

	// Denied: service proxy.
	req = httptest.NewRequest("GET", "/v1/services/google-calendar/me/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().HandleServiceProxy)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("proxy status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddleware_AppTokenFreeBusyRouteOnly(t *testing.T) {
	store := testAppStore(t)
	_, token := createTestApp(t, store, "eco", shared.GrantMap{"freebusy": shared.AccessRead})
	svc := &Service{
		ID:     "google-calendar",
		Name:   "Google Calendar",
		Action: "inject",
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.apps = store
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	// The events route is a different grant key — denied for this app
	// (issue #348).
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("events"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("events status = %d body = %s, want 403", w.Code, w.Body.String())
	}

	// The freebusy route serves availability only.
	req = httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/v1/library/freebusy"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("freebusy status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "Doctor") || strings.Contains(body, "appointment") || strings.Contains(body, "Ada") {
		t.Fatalf("freebusy route leaked content: %s", body)
	}
	if !strings.Contains(body, "start_at") {
		t.Fatalf("expected timing fields: %s", body)
	}
}

// Route-fixed disclosure shapes isolate cache entries by construction
// (issue #348): each route has its own cache key, and two keys reaching the
// same route receive identical shapes.

func TestAdminApps_CRUD(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{Services: []Service{}}, "https://aegis.example.com")
	srv.apps = store

	// Create
	body := `{"id":"eco","name":"Event Coordinator","scopes":{"freebusy":"read"}}`
	req := httptest.NewRequest("POST", "/v1/admin/apps", strings.NewReader(body))
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminApps)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}
	var created struct {
		App   apps.AppView `json:"app"`
		Token string       `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Token == "" || created.App.ID != "eco" {
		t.Fatalf("create resp: %+v", created)
	}

	// List
	req = httptest.NewRequest("GET", "/v1/admin/apps", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminApps)(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "eco") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), created.Token) {
		t.Fatal("list must not include plaintext token")
	}

	// Patch scopes
	req = httptest.NewRequest("PATCH", "/v1/admin/apps/eco", strings.NewReader(`{"scopes":{"events":"read"}}`))
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminApps)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}

	// Rotate
	req = httptest.NewRequest("POST", "/v1/admin/apps/eco/rotate", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminApps)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	var rotated struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rotated); err != nil || rotated.Token == "" {
		t.Fatalf("rotate resp: %s", w.Body.String())
	}

	// Delete
	req = httptest.NewRequest("DELETE", "/v1/admin/apps/eco", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminApps)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
}

func TestAppPathAllowed(t *testing.T) {
	allowed := []string{
		"/v1/library/events",
		"/v1/library/freebusy",
		"/v1/library/events/",
		"/v1/library/tracks",
	}
	for _, p := range allowed {
		if !appPathAllowed(p) {
			t.Fatalf("expected allow %s", p)
		}
	}
	denied := []string{
		"/v1/admin/apps",
		"/v1/library/mirror",
		"/v1/library/review",
		"/v1/library/links",
		"/v1/services/google-calendar/me/events",
		"/v1/pre-request",
		"/v1/library/messages/google-gmail/m1",
		"/v1/auth/status",
	}
	for _, p := range denied {
		if appPathAllowed(p) {
			t.Fatalf("expected deny %s", p)
		}
	}
}

func TestAgentForRequest_AppIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req = apps.WithAppIdentity(req, &apps.App{ID: "eco", Name: "E Co", Scopes: shared.GrantMap{"events": shared.AccessRead}})
	if got := agentForRequest(req); got != "app:eco" {
		t.Fatalf("agent = %q", got)
	}
}
