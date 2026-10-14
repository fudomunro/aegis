package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func testAppStore(t *testing.T) *AppStore {
	t.Helper()
	store, err := NewAppStore(filepath.Join(t.TempDir(), "apps.json"))
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	return store
}

func createTestApp(t *testing.T, store *AppStore, id string, scopes types.Permissions) (view AppView, token string) {
	t.Helper()
	view, token, err := store.Create(AppCreateRequest{ID: id, Name: id, Scopes: scopes})
	if err != nil {
		t.Fatalf("Create(%s): %v", id, err)
	}
	return view, token
}

func TestAppStore_CreateReturnsTokenOnce(t *testing.T) {
	store := testAppStore(t)
	view, token, err := store.Create(AppCreateRequest{
		ID:     "eco",
		Name:   "Event Coordinator",
		Scopes: types.Permissions{"events": types.AccessFreeBusy},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(token, appTokenPrefix) {
		t.Fatalf("token prefix = %q", token)
	}
	if view.TokenHint == "" || view.TokenHint == token {
		t.Fatalf("hint should be short and non-secret: %q", view.TokenHint)
	}
	if strings.Contains(view.TokenHint, token[len(appTokenPrefix):]) {
		t.Fatalf("hint too long: %q", view.TokenHint)
	}
	// List/get never return plaintext.
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if list[0].TokenHint != view.TokenHint {
		t.Fatalf("list hint mismatch")
	}
	got, ok := store.Get("eco")
	if !ok || got.Scopes["events"] != types.AccessFreeBusy {
		t.Fatalf("get = %+v ok=%v", got, ok)
	}
	// Scopes default to deny-all when not listed.
	if got.Scopes[types.PermissionDefaultKey] != types.AccessNone {
		t.Fatalf("default scope = %q, want none", got.Scopes[types.PermissionDefaultKey])
	}
}

func TestAppStore_FindByTokenAndRotate(t *testing.T) {
	store := testAppStore(t)
	_, token := createTestApp(t, store, "eco", types.Permissions{"events": types.AccessRead})
	if store.FindByToken(token) == nil {
		t.Fatal("expected token to resolve")
	}
	if store.FindByToken(appTokenPrefix+"nope") != nil {
		t.Fatal("unknown token should not resolve")
	}

	_, newToken, err := store.Rotate("eco")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if store.FindByToken(token) != nil {
		t.Fatal("old token should be invalid after rotate")
	}
	if store.FindByToken(newToken) == nil {
		t.Fatal("new token should resolve")
	}
}

func TestAppStore_DeleteAndDisable(t *testing.T) {
	store := testAppStore(t)
	_, token := createTestApp(t, store, "eco", types.Permissions{"events": types.AccessRead})
	disabled := true
	if _, err := store.Update("eco", AppUpdateRequest{Disabled: &disabled}); err != nil {
		t.Fatalf("Update disabled: %v", err)
	}
	if store.FindByToken(token) != nil {
		t.Fatal("disabled app must not authenticate")
	}
	if err := store.Delete("eco"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := store.Get("eco"); ok {
		t.Fatal("app should be gone")
	}
}

func TestAppStore_CreateRejectsBadIDAndScopes(t *testing.T) {
	store := testAppStore(t)
	if _, _, err := store.Create(AppCreateRequest{ID: "bad id", Scopes: types.Permissions{"events": types.AccessRead}}); err == nil {
		t.Fatal("expected bad id rejected")
	}
	if _, _, err := store.Create(AppCreateRequest{ID: "ok", Scopes: types.Permissions{"tracks": types.AccessFreeBusy}}); err == nil {
		t.Fatal("expected freebusy on tracks rejected")
	}
	if _, _, err := store.Create(AppCreateRequest{ID: "ok", Scopes: types.Permissions{"events": "write-only-oops"}}); err == nil {
		t.Fatal("expected unknown level rejected")
	}
}

func TestAuthMiddleware_AppTokenLibraryOnly(t *testing.T) {
	store := testAppStore(t)
	_, token := createTestApp(t, store, "eco", types.Permissions{
		"events":   types.AccessRead,
		"contacts": types.AccessNone,
	})
	svc := &Service{
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: types.Permissions{"events": types.AccessRead},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.apps = store
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	// Allowed: library events with app token.
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
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
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)
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
	srv.AuthMiddleware(srv.HandleServiceProxy)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("proxy status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddleware_AppTokenFreeBusyRedactsEvents(t *testing.T) {
	store := testAppStore(t)
	_, token := createTestApp(t, store, "eco", types.Permissions{"events": types.AccessFreeBusy})
	svc := &Service{
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: types.Permissions{"events": types.AccessRead},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.apps = store
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	// events endpoint must be redacted when app scope is freebusy.
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("events status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "Doctor") || strings.Contains(body, "appointment") || strings.Contains(body, "Ada") {
		t.Fatalf("freebusy app leaked content: %s", body)
	}
	if !strings.Contains(body, "start_at") {
		t.Fatalf("expected timing fields: %s", body)
	}

	// freebusy endpoint allowed.
	req = httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryFreeBusy)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("freebusy status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestLibraryCache_AppScopeIsolated(t *testing.T) {
	store := testAppStore(t)
	_, fbToken := createTestApp(t, store, "eco-fb", types.Permissions{"events": types.AccessFreeBusy})
	_, roToken := createTestApp(t, store, "eco-ro", types.Permissions{"events": types.AccessRead})
	svc := &Service{
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: types.Permissions{"events": types.AccessRead},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")
	srv.apps = store
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}

	// Prime cache with a full-read app response.
	req := httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer "+roToken)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Doctor") {
		t.Fatalf("read app: %d %s", w.Code, w.Body.String())
	}

	// Freebusy app must not receive the cached full response.
	req = httptest.NewRequest("GET", "/v1/library/events", nil)
	req.Header.Set("Authorization", "Bearer "+fbToken)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryEvents)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("fb app: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Doctor") {
		t.Fatalf("cache leaked full events to freebusy app: %s", w.Body.String())
	}
}

func TestAdminApps_CRUD(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{Services: []Service{}}, "https://aegis.example.com")
	srv.apps = store

	// Create
	body := `{"id":"eco","name":"Event Coordinator","scopes":{"events":"freebusy"}}`
	req := httptest.NewRequest("POST", "/v1/admin/apps", strings.NewReader(body))
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminApps)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}
	var created struct {
		App   AppView `json:"app"`
		Token string  `json:"token"`
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
		"/v1/mail/messages",
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
	req = withAppIdentity(req, &App{ID: "eco", Name: "E Co", Scopes: types.Permissions{"events": types.AccessRead}})
	if got := agentForRequest(req); got != "app:eco" {
		t.Fatalf("agent = %q", got)
	}
}
