package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

// adminWriteCases are the admin surfaces an LLM agent credential must never
// reach (issue #181). Includes the writes named in the issue plus the other
// mutating admin paths that share the same trust boundary.
var adminWriteCases = []struct {
	name   string
	method string
	path   string
	// body is optional; handlers may reject empty bodies after auth.
	body string
}{
	{"data-grants", http.MethodPut, "/v1/admin/data-grants", `{"data_grants":{}}`},
	{"apps-create", http.MethodPost, "/v1/admin/apps", `{"name":"evil","scopes":{"tracks":"read"}}`},
	{"udt-source", http.MethodPut, "/v1/admin/udt/kinds/k/sources/s", `{"path":"/etc/passwd"}`},
	{"udt-extra", http.MethodPost, "/v1/admin/udt/kinds/k/extra", `{"path":"/etc/passwd"}`},
	{"mirror", http.MethodPost, "/v1/library/mirror", `{"kind":"tracks"}`},
	{"photos-share", http.MethodPost, "/v1/library/photos/share", `{"service":"immich","id":"p1"}`},
	{"links-create", http.MethodPost, "/v1/library/links", `{"kind":"playlist","sources":[]}`},
	{"mail-sync", http.MethodPost, "/v1/mail/sync", `{}`},
	{"apps-authorize", http.MethodPost, "/v1/apps/authorize", `{"client_id":"x","redirect_uri":"https://example.com/cb","scopes":{"tracks":"read"}}`},
	{"auth-revoke", http.MethodDelete, "/v1/auth/spotify", ""},
	{"service-grant-limits", http.MethodPut, "/v1/admin/services/spotify/grant-limits", `{"tracks":"read"}`},
	{"cache-clear", http.MethodPost, "/v1/admin/cache/clear", `{}`},
}

func adminSplitServer(t *testing.T) *Server {
	t.Helper()
	srv := testServerWithBase(&Config{Services: []Service{}}, "https://aegis.example.com")
	// Separate agent vs admin keys so the split is observable.
	srv.apiKey = "agent-key"
	srv.adminAPIKey = "admin-key"
	return srv
}

func adminRequest(method, path, key, body string) *http.Request {
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestAdminMiddleware_AgentKeyForbiddenOnAdminWrites(t *testing.T) {
	for _, tc := range adminWriteCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := adminSplitServer(t)
			// Route through AdminMiddleware the same way production does.
			var handler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
				// Stand-in for the real handler: success only if auth passed.
				w.WriteHeader(http.StatusOK)
			}
			w := httptest.NewRecorder()
			req := adminRequest(tc.method, tc.path, "agent-key", tc.body)
			srv.AdminMiddleware(handler)(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("agent key on %s %s = %d, want 403; body=%s",
					tc.method, tc.path, w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminMiddleware_AdminKeyAllowedOnAdminWrites(t *testing.T) {
	for _, tc := range adminWriteCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := adminSplitServer(t)
			handler := func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}
			w := httptest.NewRecorder()
			req := adminRequest(tc.method, tc.path, "admin-key", tc.body)
			srv.AdminMiddleware(handler)(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("admin key on %s %s = %d, want 200; body=%s",
					tc.method, tc.path, w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminMiddleware_UnauthenticatedIs401(t *testing.T) {
	srv := adminSplitServer(t)
	handler := func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}
	for _, tc := range adminWriteCases {
		w := httptest.NewRecorder()
		req := adminRequest(tc.method, tc.path, "", tc.body)
		srv.AdminMiddleware(handler)(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("no credential on %s %s = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

func TestAdminMiddleware_WrongKeyIs401(t *testing.T) {
	srv := adminSplitServer(t)
	handler := func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}
	w := httptest.NewRecorder()
	req := adminRequest(http.MethodPut, "/v1/admin/data-grants", "wrong-key", `{}`)
	srv.AdminMiddleware(handler)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key = %d, want 401", w.Code)
	}
}

func TestAdminMiddleware_AppTokenNeverAdmin(t *testing.T) {
	srv := adminSplitServer(t)
	srv.apps = mustAppStore(t)
	_, token, err := srv.apps.Create(AppCreateRequest{
		ID:     "evil-app",
		Name:   "evil-app",
		Scopes: types.Permissions{"tracks": types.AccessRead},
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if token == "" {
		t.Fatal("expected app token")
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}
	w := httptest.NewRecorder()
	req := adminRequest(http.MethodPut, "/v1/admin/data-grants", token, `{"data_grants":{}}`)
	srv.AdminMiddleware(handler)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("app token = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestAdminMiddleware_TailnetUserLoginIsAdminByDefault(t *testing.T) {
	srv := adminSplitServer(t)
	srv.authMode = AuthModeTailnet
	srv.identity = stubResolver{id: &PeerIdentity{LoginName: "alice@example.com", Source: "whois"}}
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	w := httptest.NewRecorder()
	req := adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`)
	srv.AdminMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("user login = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestAdminMiddleware_TaggedDeviceIsNotAdminByDefault(t *testing.T) {
	srv := adminSplitServer(t)
	srv.authMode = AuthModeTailnet
	srv.identity = stubResolver{id: &PeerIdentity{
		LoginName: "tag:aperture,tag:ci",
		Tags:      []string{"tag:aperture", "tag:ci"},
		Source:    "whois",
	}}
	handler := func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}
	w := httptest.NewRecorder()
	req := adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`)
	srv.AdminMiddleware(handler)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("tagged device = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestAdminMiddleware_AdminLoginsAllowlist(t *testing.T) {
	srv := adminSplitServer(t)
	srv.authMode = AuthModeTailnet
	srv.adminLogins = []string{"alice@example.com"}
	srv.identity = stubResolver{id: &PeerIdentity{LoginName: "bob@example.com", Source: "whois"}}
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}

	w := httptest.NewRecorder()
	req := adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`)
	srv.AdminMiddleware(handler)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("bob not in admin-logins = %d, want 403", w.Code)
	}

	srv.identity = stubResolver{id: &PeerIdentity{LoginName: "alice@example.com", Source: "whois"}}
	w = httptest.NewRecorder()
	req = adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`)
	srv.AdminMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("alice in admin-logins = %d, want 200", w.Code)
	}
}

func TestAdminMiddleware_AdminLoginsCanGrantTaggedIdentity(t *testing.T) {
	srv := adminSplitServer(t)
	srv.authMode = AuthModeTailnet
	srv.adminLogins = []string{"tag:aegis-admin"}
	srv.identity = stubResolver{id: &PeerIdentity{
		LoginName: "tag:aegis-admin",
		Tags:      []string{"tag:aegis-admin"},
		Source:    "whois",
	}}
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	w := httptest.NewRecorder()
	req := adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`)
	srv.AdminMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tagged admin login = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestAuthMiddleware_AgentKeyStillReadsLibrary(t *testing.T) {
	srv := adminSplitServer(t)
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	w := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/v1/library/playlists", "agent-key", "")
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("agent key library read = %d, want 200", w.Code)
	}
}

func TestAuthMiddleware_AdminKeyStillReadsLibrary(t *testing.T) {
	srv := adminSplitServer(t)
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	w := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/v1/library/playlists", "admin-key", "")
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin key library read = %d, want 200", w.Code)
	}
}

func TestAuthMiddleware_SoloAdminKeyWithoutAgentKey(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apiKey = ""
	srv.adminAPIKey = "admin-key"
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	w := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/v1/library/playlists", "admin-key", "")
	srv.AuthMiddleware(handler)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin-only key on library = %d, want 200", w.Code)
	}
	w = httptest.NewRecorder()
	req = adminRequest(http.MethodPut, "/v1/admin/data-grants", "admin-key", `{}`)
	srv.AdminMiddleware(func(http.ResponseWriter, *http.Request) {
		w.WriteHeader(http.StatusOK)
	})(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin-only key on admin = %d, want 200", w.Code)
	}
}

func TestIsTagIdentity(t *testing.T) {
	if isTagIdentity(nil) {
		t.Fatal("nil should not be tag")
	}
	if isTagIdentity(&PeerIdentity{LoginName: "alice@example.com"}) {
		t.Fatal("user login is not a tag identity")
	}
	if !isTagIdentity(&PeerIdentity{LoginName: "tag:ci"}) {
		t.Fatal("tag: prefix should be tag identity")
	}
	if !isTagIdentity(&PeerIdentity{
		LoginName: "tag:a,tag:b",
		Tags:      []string{"tag:a", "tag:b"},
	}) {
		t.Fatal("joined tags should be tag identity")
	}
	// User login that also carries tags is still a user principal.
	if isTagIdentity(&PeerIdentity{
		LoginName: "alice@example.com",
		Tags:      []string{"tag:laptop"},
	}) {
		t.Fatal("user login with tags is not a tag identity")
	}
}

func TestIsAdminIdentity_DefaultAndAllowlist(t *testing.T) {
	user := &PeerIdentity{LoginName: "alice@example.com"}
	tagged := &PeerIdentity{LoginName: "tag:ci", Tags: []string{"tag:ci"}}

	s := &Server{}
	if !s.isAdminIdentity(user) {
		t.Fatal("user login should be admin by default")
	}
	if s.isAdminIdentity(tagged) {
		t.Fatal("tagged device should not be admin by default")
	}
	if s.isAdminIdentity(nil) {
		t.Fatal("nil should not be admin")
	}

	s.allowedLogins = []string{"bob@example.com"}
	if s.isAdminIdentity(user) {
		t.Fatal("user not in allowed-logins should not be admin by default")
	}

	s.adminLogins = []string{"alice@example.com"}
	if !s.isAdminIdentity(user) {
		t.Fatal("admin-logins should grant admin")
	}
	if s.isAdminIdentity(tagged) {
		t.Fatal("tagged still not admin when not listed")
	}
	s.adminLogins = []string{"tag:ci"}
	if !s.isAdminIdentity(tagged) {
		t.Fatal("admin-logins should grant tagged identity")
	}
}

func mustAppStore(t *testing.T) *AppStore {
	t.Helper()
	st, err := NewAppStore(t.TempDir() + "/apps.json")
	if err != nil {
		t.Fatalf("app store: %v", err)
	}
	return st
}

func TestAdminMiddleware_ModeMatrix(t *testing.T) {
	okHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	noHandler := func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		t.Fatal("handler must not run")
	}

	t.Run("tailnet-no-identity-401", func(t *testing.T) {
		srv := adminSplitServer(t)
		srv.authMode = AuthModeTailnet
		srv.identity = stubResolver{}
		w := httptest.NewRecorder()
		srv.AdminMiddleware(noHandler)(w, adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", w.Code)
		}
	})

	t.Run("tailnet-identity-error-401", func(t *testing.T) {
		srv := adminSplitServer(t)
		srv.authMode = AuthModeTailnet
		srv.identity = stubResolver{err: context.DeadlineExceeded}
		w := httptest.NewRecorder()
		srv.AdminMiddleware(noHandler)(w, adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", w.Code)
		}
	})

	t.Run("auto-admin-login-200", func(t *testing.T) {
		srv := adminSplitServer(t)
		srv.authMode = AuthModeAuto
		srv.identity = stubResolver{id: &PeerIdentity{LoginName: "alice@example.com", Source: "whois"}}
		w := httptest.NewRecorder()
		srv.AdminMiddleware(okHandler)(w, adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`))
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("auto-agent-key-403", func(t *testing.T) {
		srv := adminSplitServer(t)
		srv.authMode = AuthModeAuto
		w := httptest.NewRecorder()
		srv.AdminMiddleware(noHandler)(w, adminRequest(http.MethodPut, "/v1/admin/data-grants", "agent-key", `{}`))
		if w.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403", w.Code)
		}
	})

	t.Run("auto-tagged-with-agent-key-403", func(t *testing.T) {
		srv := adminSplitServer(t)
		srv.authMode = AuthModeAuto
		srv.identity = stubResolver{id: &PeerIdentity{
			LoginName: "tag:ci",
			Tags:      []string{"tag:ci"},
			Source:    "whois",
		}}
		w := httptest.NewRecorder()
		srv.AdminMiddleware(noHandler)(w, adminRequest(http.MethodPut, "/v1/admin/data-grants", "agent-key", `{}`))
		if w.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403", w.Code)
		}
	})

	t.Run("auto-no-credential-401", func(t *testing.T) {
		srv := adminSplitServer(t)
		srv.authMode = AuthModeAuto
		w := httptest.NewRecorder()
		srv.AdminMiddleware(noHandler)(w, adminRequest(http.MethodPut, "/v1/admin/data-grants", "", `{}`))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", w.Code)
		}
	})

	t.Run("api-key-identity-error-still-denied", func(t *testing.T) {
		srv := adminSplitServer(t)
		srv.authMode = AuthModeAPIKey
		srv.identity = stubResolver{err: context.DeadlineExceeded}
		w := httptest.NewRecorder()
		srv.AdminMiddleware(noHandler)(w, adminRequest(http.MethodPut, "/v1/admin/data-grants", "wrong", `{}`))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", w.Code)
		}
	})

	t.Run("x-api-key-header-admin", func(t *testing.T) {
		srv := adminSplitServer(t)
		req := httptest.NewRequest(http.MethodPut, "/v1/admin/data-grants", strings.NewReader(`{}`))
		req.Header.Set("X-Api-Key", "admin-key")
		w := httptest.NewRecorder()
		srv.AdminMiddleware(okHandler)(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", w.Code)
		}
	})
}

func TestCheckAdminAPIKey_EmptyDisabled(t *testing.T) {
	srv := &Server{adminAPIKey: ""}
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/summary", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	if srv.checkAdminAPIKey(req) {
		t.Fatal("empty admin key must not authenticate")
	}
	srv.adminAPIKey = "secret"
	req.Header.Set("Authorization", "Bearer secret")
	if !srv.checkAdminAPIKey(req) {
		t.Fatal("matching bearer should authenticate")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/v1/admin/summary", nil)
	req2.Header.Set("X-Api-Key", "secret")
	if !srv.checkAdminAPIKey(req2) {
		t.Fatal("matching X-Api-Key should authenticate")
	}
	req3 := httptest.NewRequest(http.MethodGet, "/v1/admin/summary", nil)
	if srv.checkAdminAPIKey(req3) {
		t.Fatal("missing credential must not authenticate")
	}
}

func TestMatchAPIKey_EmptyKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer x")
	if matchAPIKey(req, "") {
		t.Fatal("empty key must not match")
	}
}

func TestValidateAuthCredentials(t *testing.T) {
	if err := validateAuthCredentials("nope", "k", ""); err == nil {
		t.Fatal("invalid auth-mode must error")
	}
	if err := validateAuthCredentials(AuthModeAPIKey, "", ""); err == nil {
		t.Fatal("api-key mode without any key must error")
	}
	if err := validateAuthCredentials(AuthModeAPIKey, "agent", ""); err != nil {
		t.Fatalf("agent key alone is valid for agents: %v", err)
	}
	if err := validateAuthCredentials(AuthModeAPIKey, "", "admin"); err != nil {
		t.Fatalf("admin key alone is valid: %v", err)
	}
	if err := validateAuthCredentials(AuthModeAuto, "", ""); err != nil {
		t.Fatalf("auto without keys is a warning, not fatal: %v", err)
	}
	if err := validateAuthCredentials(AuthModeAuto, "same", "same"); err != nil {
		t.Fatalf("equal keys warn but are valid: %v", err)
	}
	if err := validateAuthCredentials(AuthModeTailnet, "", ""); err != nil {
		t.Fatalf("tailnet mode needs no key: %v", err)
	}
}

func TestHandleAdminRoutes_Unknown(t *testing.T) {
	srv := adminSplitServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/definitely-not-a-route", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.handleAdminRoutes)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleAuthRoutes_UnknownMethod(t *testing.T) {
	srv := adminSplitServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/spotify", nil)
	w := httptest.NewRecorder()
	srv.handleAuthRoutes(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}

func TestIsTagIdentity_JoinedTagsOnly(t *testing.T) {
	// LoginName equals joined tags even without a tag: prefix on the first field.
	id := &PeerIdentity{LoginName: "a,b", Tags: []string{"a", "b"}}
	if !isTagIdentity(id) {
		t.Fatal("joined tags should count as tag identity")
	}
}

func TestRequestMethodAndHost_NilSafe(t *testing.T) {
	if requestMethod(nil) != "" {
		t.Fatal("nil request method should be empty")
	}
	if requestHost(nil) != "" {
		t.Fatal("nil request host should be empty")
	}
	req := httptest.NewRequest(http.MethodPost, "http://aegis.example/x", nil)
	if requestMethod(req) != http.MethodPost {
		t.Fatalf("got %q", requestMethod(req))
	}
	if requestHost(req) == "" {
		t.Fatal("expected host from URL")
	}
}

func TestSmallHelpers_Coverage(t *testing.T) {
	if !looksLikeMboxName("inbox.mbox") {
		t.Fatal("inbox.mbox should look like mbox")
	}
	if looksLikeMboxName("notes.txt") {
		t.Fatal("notes.txt is not mbox")
	}
	if !looksLikeMboxName("All Mail") {
		t.Fatal("All Mail should look like mbox")
	}
	cfg := &Config{}
	cfg.EnsureService(Service{ID: "x", Name: "X", Action: "inject"})
	if cfg.GetService("x") == nil {
		t.Fatal("EnsureService should register service")
	}
	cfg.EnsureService(Service{ID: "x", Name: "Y", Action: "inject"})
	if cfg.GetService("x").Name != "X" {
		t.Fatal("EnsureService must not overwrite existing")
	}
	cfg.EnsureService(Service{ID: "  ", Name: "Z", Action: "inject"})
	if len(cfg.Services) != 1 {
		t.Fatalf("blank id must not insert, got %d", len(cfg.Services))
	}
}
