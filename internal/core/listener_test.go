package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/fudomunro/aegis/internal/core/scopes"
)

func TestAPIRouteAllowed(t *testing.T) {
	// The API listener is an allowlist (docs/external-access.md "Model"):
	// agent/external surfaces only; admin tier, demo harnesses, and every
	// write stay off it and default to the admin listener.
	allowed := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/healthz"},
		{http.MethodHead, "/readyz"},
		{http.MethodPost, "/v1/pre-request"},
		{http.MethodGet, "/v1/library/playlists"},
		{http.MethodGet, "/v1/library/documents/immich/abc/text"},
		{http.MethodGet, "/v1/library/links"},
		{http.MethodGet, "/v1/library/review-page-of-udt"},
		{http.MethodHead, "/v1/library/contacts"},
		{http.MethodGet, "/v1/services/immich/assets/abc/original"},
		{http.MethodPost, "/v1/services/signal/import"},
		{http.MethodGet, "/v1/mail/search"},
		{http.MethodGet, "/v1/mail/status"},
		{http.MethodGet, "/v1/mail/messages/abc"},
		{http.MethodGet, "/v1/auth/status"},
		{http.MethodPost, "/v1/apps/token"},
		{http.MethodPost, "/v1/apps/revoke"},
	}
	for _, tc := range allowed {
		if !apiRouteAllowed(tc.method, tc.path) {
			t.Errorf("apiRouteAllowed(%s %s) = false, want true", tc.method, tc.path)
		}
	}

	denied := []struct {
		method string
		path   string
	}{
		// Admin tier.
		{http.MethodGet, "/v1/admin/summary"},
		{http.MethodGet, "/v1/admin"},
		{http.MethodGet, "/metrics"},
		{http.MethodHead, "/metrics"},
		{http.MethodGet, "/"},
		{http.MethodGet, "/home"},
		{http.MethodGet, "/library"},
		{http.MethodGet, "/services"},
		{http.MethodGet, "/advanced"},
		{http.MethodGet, "/v1/apps/authorize"},
		{http.MethodPost, "/v1/apps/authorize"},
		{http.MethodGet, "/v1/auth/spotify"},
		{http.MethodGet, "/v1/auth/spotify/callback"},
		// Admin-tier writes sharing agent prefixes.
		{http.MethodGet, "/v1/library/review"},
		{http.MethodGet, "/v1/library/copies"},
		{http.MethodPost, "/v1/library/mirror"},
		{http.MethodPost, "/v1/library/photos/share"},
		{http.MethodPost, "/v1/library/links"},
		{http.MethodDelete, "/v1/library/links/abc"},
		{http.MethodPost, "/v1/mail/sync"},
		{http.MethodGet, "/v1/mail/sync"},
		// Mutations on agent read prefixes are not agent routes.
		{http.MethodPost, "/v1/library/playlists"},
		{http.MethodDelete, "/v1/library/playlists"},
		{http.MethodPut, "/v1/services/immich/assets"},
		{http.MethodPost, "/v1/auth/status"},
		// Demo / dev harnesses never publish.
		{http.MethodGet, "/demo/third-party"},
		{http.MethodGet, "/demo/fake-upstream/"},
		{http.MethodPost, "/demo/fake-upstream/control"},
		// Static brand assets serve the admin SPA.
		{http.MethodGet, "/favicon.ico"},
		{http.MethodGet, "/logo.svg"},
		{http.MethodGet, "/definitely-not-a-route"},
	}
	for _, tc := range denied {
		if apiRouteAllowed(tc.method, tc.path) {
			t.Errorf("apiRouteAllowed(%s %s) = true, want false", tc.method, tc.path)
		}
	}
}

func TestAPIAllowlistGate_UnlistedRoutes404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/admin/summary", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/library/playlists", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	gate := apiAllowlistGate(mux)

	// A route the mux serves is still 404 on the API listener when it is
	// not on the allowlist: registration cannot leak it onto the surface.
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/summary", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("admin route through gate: code=%d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	gate.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/library/playlists", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("allowlisted route through gate: code=%d, want 200", w.Code)
	}
}

func TestAuthMiddleware_ExternalAPI_TokensOnly(t *testing.T) {
	path := writeDataScopes(t, []scopes.Scope{
		{ID: "ext", Dataset: "", APIKey: "YOUR_EXT_KEY", ReadOnly: true},
	})
	scopeStore, err := scopes.NewStore(path)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	srv := &Server{
		apiKey:      "agent-key",
		adminAPIKey: "admin-key",
		authMode:    AuthModeAuto,
		config:      &Config{},
		cache:       NewCache(),
		mbCache:     NewCache(),
		scopes:      scopeStore,
		identity:    ServeHeaderResolver{},
	}

	called := false
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if scopes.From(r) == nil {
			t.Error("expected data scope attached")
		}
	})

	// Invite token works on the published bind.
	req := httptest.NewRequest(http.MethodGet, "/v1/library/contacts", nil)
	req = withExternalAPI(req)
	req.Header.Set("X-Api-Key", "YOUR_EXT_KEY")
	w := httptest.NewRecorder()
	h(w, req)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("invite token: code=%d called=%v body=%s", w.Code, called, w.Body.String())
	}

	// The shared agent key never crosses the public bind.
	called = false
	req = httptest.NewRequest(http.MethodGet, "/v1/library/contacts", nil)
	req = withExternalAPI(req)
	req.Header.Set("Authorization", "Bearer agent-key")
	w = httptest.NewRecorder()
	h(w, req)
	if called || w.Code != http.StatusUnauthorized {
		t.Fatalf("agent key on published bind: code=%d called=%v", w.Code, called)
	}

	// Nor the admin key.
	called = false
	req = httptest.NewRequest(http.MethodGet, "/v1/library/contacts", nil)
	req = withExternalAPI(req)
	req.Header.Set("Authorization", "Bearer admin-key")
	w = httptest.NewRecorder()
	h(w, req)
	if called || w.Code != http.StatusUnauthorized {
		t.Fatalf("admin key on published bind: code=%d called=%v", w.Code, called)
	}

	// Ambient identity is not resolved at all — even a loopback Serve hop
	// cannot authenticate here; tokens only.
	called = false
	req = httptest.NewRequest(http.MethodGet, "/v1/library/contacts", nil)
	req = withExternalAPI(req)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Tailscale-User-Login", "alice@example.com")
	w = httptest.NewRecorder()
	h(w, req)
	if called || w.Code != http.StatusUnauthorized {
		t.Fatalf("ambient identity on published bind: code=%d called=%v", w.Code, called)
	}

	// Nothing at all → 401.
	called = false
	req = withExternalAPI(httptest.NewRequest(http.MethodGet, "/v1/library/contacts", nil))
	w = httptest.NewRecorder()
	h(w, req)
	if called || w.Code != http.StatusUnauthorized {
		t.Fatalf("bare external request: code=%d called=%v", w.Code, called)
	}
}

func TestAdminMiddleware_ExternalAPI_NotFound(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	called := false
	h := srv.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := withExternalAPI(httptest.NewRequest(http.MethodGet, "/v1/admin/summary", nil))
	req.Header.Set("Authorization", "Bearer test-key") // admin key even
	w := httptest.NewRecorder()
	h(w, req)
	if called || w.Code != http.StatusNotFound {
		t.Fatalf("admin surface on published bind must 404: code=%d called=%v", w.Code, called)
	}
}

func TestValidateAdminListenAddr(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1:8081",
		"localhost:8081",
		"[::1]:8081",
		"100.64.0.5:8081",
		"[fd7a:115c:a1e0::1]:8081",
	} {
		if err := validateAdminListenAddr(addr); err != nil {
			t.Errorf("validateAdminListenAddr(%q) = %v, want nil", addr, err)
		}
	}
	for _, addr := range []string{
		":8081",
		"0.0.0.0:8081",
		"[::]:8081",
		"192.168.1.50:8081",
		"10.0.0.5:8081",
		"example.com:8081",
		"no-port",
	} {
		if err := validateAdminListenAddr(addr); err == nil {
			t.Errorf("validateAdminListenAddr(%q) = nil, want refusal", addr)
		}
	}
}

func TestValidateSplitListeners(t *testing.T) {
	// Single-listener mode: no split config is fine.
	if err := validateSplitListeners("", "", false); err != nil {
		t.Fatalf("single-listener: %v", err)
	}
	// External access on a single listener would publish admin surfaces.
	if err := validateSplitListeners("", "", true); err == nil {
		t.Fatal("external access without -admin-addr must be refused")
	}
	// Split requires its own base URL (OAuth redirects, CSRF origin).
	if err := validateSplitListeners("127.0.0.1:8081", "", false); err == nil {
		t.Fatal("split without -admin-base-url must be refused")
	}
	// Admin bind must stay loopback or tailnet.
	if err := validateSplitListeners("0.0.0.0:8081", "https://admin.example.com", true); err == nil {
		t.Fatal("wildcard admin bind must be refused")
	}
	// Happy paths: external and tailnet-only splits.
	if err := validateSplitListeners("127.0.0.1:8081", "https://aegis-admin.tailnet.ts.net", true); err != nil {
		t.Fatalf("external split: %v", err)
	}
	if err := validateSplitListeners("100.64.0.5:8081", "https://aegis-admin.tailnet.ts.net", false); err != nil {
		t.Fatalf("tailnet-only split: %v", err)
	}
}

func TestListenerMiddleware_BaseURLAndExternalMarker(t *testing.T) {
	var gotBase string
	var gotExternal bool
	h := listenerMiddleware("https://api.example.com", true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBase = listenerBaseURLFrom(r)
		gotExternal = externalAPIFrom(r)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if gotBase != "https://api.example.com" {
		t.Fatalf("listener base = %q", gotBase)
	}
	if !gotExternal {
		t.Fatal("expected external API marker")
	}

	h = listenerMiddleware("https://admin.example.com", false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBase = listenerBaseURLFrom(r)
		gotExternal = externalAPIFrom(r)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if gotBase != "https://admin.example.com" {
		t.Fatalf("listener base = %q", gotBase)
	}
	if gotExternal {
		t.Fatal("admin listener must not carry the external marker")
	}

	// No listener wrapper: base falls back to the instance base URL and the
	// external marker is absent (single-listener behavior).
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	if got := srv.baseURLFor(httptest.NewRequest(http.MethodGet, "/", nil)); got != "https://aegis.example.com" {
		t.Fatalf("baseURLFor fallback = %q", got)
	}
	r := withListenerBase(httptest.NewRequest(http.MethodGet, "/", nil), "https://split.example.com")
	if got := srv.baseURLFor(r); got != "https://split.example.com" {
		t.Fatalf("baseURLFor listener = %q", got)
	}
}

// Ensure the per-listener base URL reaches CSRF origin checks: an admin
// write from the admin listener's own origin passes, an API-origin write
// does not (and vice versa).
func TestSameOriginRequest_PerListenerBase(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://api.example.com")

	r := withListenerBase(httptest.NewRequest(http.MethodPost, "/v1/admin/data-grants", nil), "https://admin.example.com")
	r.Header.Set("Origin", "https://admin.example.com")
	if !srv.sameOriginRequest(r) {
		t.Fatal("admin origin on admin listener must pass")
	}
	r.Header.Set("Origin", "https://api.example.com")
	if srv.sameOriginRequest(r) {
		t.Fatal("API origin on admin listener must not pass")
	}

	// Single-listener fallback keeps the instance base URL behavior.
	r = httptest.NewRequest(http.MethodPost, "/v1/admin/data-grants", nil)
	r.Header.Set("Origin", "https://api.example.com")
	if !srv.sameOriginRequest(r) {
		t.Fatal("instance origin must pass without a listener wrapper")
	}
}

var _ = context.Background
