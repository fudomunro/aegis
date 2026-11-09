package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegisterRoutes_AdminAuthPaths verifies every admin/auth path resolves
// through the pattern registrations with the same auth wrapper as before
// the switch-based routers were removed (issue #274).
func TestRegisterRoutes_AdminAuthPaths(t *testing.T) {
	for _, tc := range adminAuthPathTests() {
		t.Run(tc.name, func(t *testing.T) {
			srv := adminSplitServer(t)

			// Without credentials.
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			tc.dispatch(srv, w, req)

			switch {
			case tc.public:
				// OAuth login/callback have no auth wrapper; must not 401.
				if w.Code == http.StatusUnauthorized {
					t.Fatalf("public route %s %s returned 401 without credentials", tc.method, tc.path)
				}
			case tc.auth:
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("protected route %s %s = %d without credentials, want 401; body=%s",
						tc.method, tc.path, w.Code, w.Body.String())
				}
			}

			// With admin credentials — must get past auth (not 401).
			req = httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Authorization", "Bearer admin-key")
			w = httptest.NewRecorder()
			tc.dispatch(srv, w, req)

			if w.Code == http.StatusUnauthorized {
				t.Fatalf("route %s %s = 401 even with admin key; body=%s",
					tc.method, tc.path, w.Body.String())
			}
		})
	}
}

// TestAPIAllowlist_MatchesRoutes enumerates the registered admin/auth
// patterns and asserts none of them leak onto the API listener allowlist.
// The allowlist is the durable boundary (docs/external-access.md): admin
// and OAuth surfaces must never be reachable on the published listener.
func TestAPIAllowlist_MatchesRoutes(t *testing.T) {
	// Collect concrete (method, path) probes that would hit admin/auth.
	probes := []struct {
		method string
		path   string
	}{
		{"GET", "/v1/admin/summary"},
		{"GET", "/v1/admin/flow"},
		{"POST", "/v1/admin/cache/clear"},
		{"GET", "/v1/admin/data-grants"},
		{"PUT", "/v1/admin/data-grants"},
		{"GET", "/v1/admin/apps"},
		{"POST", "/v1/admin/apps"},
		{"GET", "/v1/admin/setup/immich"},
		{"POST", "/v1/admin/setup/immich/save"},
		{"GET", "/v1/admin/quicken"},
		{"GET", "/v1/admin/timeline"},
		{"GET", "/v1/admin/signal"},
		{"GET", "/v1/admin/udt"},
		{"GET", "/v1/admin/library/playlists"},
		{"GET", "/v1/admin/permissions"},
		{"GET", "/v1/admin/sync"},
		{"POST", "/v1/admin/sync/pause"},
		{"GET", "/v1/admin/mail/status"},
		{"POST", "/v1/admin/mail/sync"},
		{"GET", "/v1/admin/maintenance/signals"},
		{"GET", "/v1/admin/maintenance/dashboard"},
		{"GET", "/v1/admin/hook-health"},
		{"GET", "/v1/admin/search/status"},
		{"GET", "/v1/admin/services/spotify/grant-limits"},
		{"GET", "/v1/admin/data-scopes"},
		{"GET", "/v1/admin/data-scopes/scope1"},
		{"GET", "/v1/admin/sync/schedules/1/enable"},
		{"POST", "/v1/admin/sync/job1/cancel"},
		{"GET", "/"},
		{"GET", "/home"},
		{"GET", "/library"},
		{"GET", "/services"},
		{"GET", "/advanced"},
		{"GET", "/v1/auth/spotify/login"},
		{"GET", "/v1/auth/spotify/callback"},
		{"DELETE", "/v1/auth/spotify"},
	}

	for _, p := range probes {
		if apiRouteAllowed(p.method, p.path) {
			t.Errorf("admin/auth route %s %s is on the API allowlist — must not be", p.method, p.path)
		}
	}
}

// TestAPIAllowlist_AgentRoutesAllowed asserts the agent-facing routes that
// must be on the API listener allowlist are present. Guards against the
// allowlist drifting closed during route registration refactors.
func TestAPIAllowlist_AgentRoutesAllowed(t *testing.T) {
	probes := []struct {
		method string
		path   string
	}{
		{"GET", "/healthz"},
		{"HEAD", "/healthz"},
		{"GET", "/readyz"},
		{"POST", "/v1/pre-request"},
		{"GET", "/v1/auth/status"},
		{"POST", "/v1/apps/token"},
		{"POST", "/v1/apps/revoke"},
		{"GET", "/v1/library/playlists"},
		{"GET", "/v1/library/tracks"},
		{"GET", "/v1/library/photos"},
		{"GET", "/v1/mail/inbox"},
		{"GET", "/v1/services/spotify/v1/me"},
	}
	for _, p := range probes {
		if !apiRouteAllowed(p.method, p.path) {
			t.Errorf("agent route %s %s is not on the API allowlist — must be", p.method, p.path)
		}
	}
}

// TestRegisterRoutes_UnknownAdminPath asserts unlisted /v1/admin/* paths
// still answer 404 (the former handleAdminRoutes default case).
func TestRegisterRoutes_UnknownAdminPath(t *testing.T) {
	srv := adminSplitServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/definitely-not-a-route", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	w := httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// TestRegisterRoutes_UnknownAuthPath asserts unlisted /v1/auth/* paths
// still answer 404 JSON (the former handleAuthRoutes default case).
func TestRegisterRoutes_UnknownAuthPath(t *testing.T) {
	srv := adminSplitServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/nope", nil)
	w := httptest.NewRecorder()
	dispatchAuth(srv, w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown auth endpoint") {
		t.Fatalf("body = %s, want unknown auth endpoint", w.Body.String())
	}
}

// TestRegisterRoutes_FullMuxNoPanic builds the complete mux — built-ins
// plus a user-defined kind fixture — and asserts ServeMux registration
// does not panic. Go's mux panics on conflicting patterns at registration,
// which is the real shadow check behind registry composition
// (kinds.MergeUserDefined; issue #312).
func TestRegisterRoutes_FullMuxNoPanic(t *testing.T) {
	dir := t.TempDir()
	writeSchemaFile(t, dir, "schema.json", `{
		"id": "widgets",
		"fields": {"title": {"type": "string"}},
		"identity": {"fields": ["title"]},
		"sources": [{"id": "s", "kind": "file_json", "path": "widgets.json", "field_map": {"title": "t"}}]
	}`)
	writeSchemaFile(t, dir, "widgets.json", `{"t": "hi"}`)
	reg, _ := LoadUserDefinedTypes(dir, "", nil)

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.udts = reg
	mux := http.NewServeMux()
	registerRoutes(mux, srv, nil)

	for _, path := range []string{
		"/v1/library/playlists",
		"/v1/library/freebusy",
		"/v1/library/documents/svc/abc/text",
		"/v1/library/widgets",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("no mux route matches %s", path)
		}
	}
}
