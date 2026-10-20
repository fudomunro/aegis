package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// extractCSRFToken pulls the per-render consent token from the HTML page.
func extractCSRFToken(t *testing.T, html string) string {
	t.Helper()
	const marker = `name="csrf-token" content="`
	i := strings.Index(html, marker)
	if i < 0 {
		return ""
	}
	rest := html[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func TestOriginMatchesBase(t *testing.T) {
	cases := []struct {
		origin, base string
		want         bool
	}{
		{"https://aegis.example.com", "https://aegis.example.com", true},
		{"https://aegis.example.com", "https://aegis.example.com/", true},
		{"https://aegis.example.com:8443", "https://aegis.example.com:8443", true},
		{"http://localhost:8080", "http://localhost:8080", true},
		{"https://evil.example.com", "https://aegis.example.com", false},
		{"https://aegis.example.com", "https://evil.example.com", false},
		{"http://aegis.example.com", "https://aegis.example.com", false},
		{"https://aegis.example.com:8443", "https://aegis.example.com", false},
		{"null", "https://aegis.example.com", false},
		{"", "https://aegis.example.com", false},
		{"https://aegis.example.com", "", false},
	}
	for _, c := range cases {
		if got := originMatchesBase(c.origin, c.base); got != c.want {
			t.Fatalf("originMatchesBase(%q, %q) = %v, want %v", c.origin, c.base, got, c.want)
		}
	}
}

func TestIsSafeMethod(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !isSafeMethod(m) {
			t.Fatalf("%s should be safe", m)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if isSafeMethod(m) {
			t.Fatalf("%s must not be safe", m)
		}
	}
}

func TestCSRFGuard_TailnetCrossOriginWriteRejected(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.authMode = AuthModeTailnet
	srv.identity = fakeIdentity{login: "user@example.com"}

	inner := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	}

	// Cross-origin form POST (classic CSRF simple request).
	req := httptest.NewRequest("POST", "/v1/admin/cache/clear",
		strings.NewReader("a=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example.com")
	req.RemoteAddr = "100.64.0.1:1234"
	w := httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin form: %d %s", w.Code, w.Body.String())
	}

	// Cross-origin text/plain POST (CORS simple request carrying JSON).
	req = httptest.NewRequest("POST", "/v1/admin/apps",
		strings.NewReader(`{"id":"x","name":"x","scopes":{}}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "https://evil.example.com")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin text/plain: %d %s", w.Code, w.Body.String())
	}

	// No Origin on ambient tailnet identity is also rejected.
	req = httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing origin: %d %s", w.Code, w.Body.String())
	}
}

func TestCSRFGuard_SameOriginAdminWriteAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.authMode = AuthModeTailnet
	srv.identity = fakeIdentity{login: "user@example.com"}

	inner := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	}

	req := httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	req.Header.Set("Origin", "https://aegis.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin admin write: %d %s", w.Code, w.Body.String())
	}

	// Sec-Fetch-Site alone is enough.
	req = httptest.NewRequest("POST", "/v1/admin/mail/sync",
		strings.NewReader(`{"max_messages":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sec-fetch-site write: %d %s", w.Code, w.Body.String())
	}
}

func TestCSRFGuard_APIKeyWithoutOriginAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	inner := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	}

	req := httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("api-key write: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("POST", "/v1/pre-request",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(inner)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("agent write: %d %s", w.Code, w.Body.String())
	}
}

func TestCSRFGuard_TextPlainRejectedOnJSONSurfaces(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	inner := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	}

	for _, path := range []string{
		"/v1/admin/apps",
		"/v1/admin/cache/clear",
		"/v1/admin/mail/sync",
		"/v1/library/mirror",
	} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Api-Key", "test-key")
		w := httptest.NewRecorder()
		srv.AdminMiddleware(inner)(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s text/plain: %d %s", path, w.Code, w.Body.String())
		}
	}

	// Content transport keeps free-form media types (issue #119).
	req := httptest.NewRequest("POST", "/v1/services/immich/photos",
		strings.NewReader("binary"))
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(inner)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("content transport upload: %d %s", w.Code, w.Body.String())
	}
}

func TestCSRFGuard_ContentTypeEmptyBodyAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	inner := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	}

	req := httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bodyless post: %d %s", w.Code, w.Body.String())
	}
}

func TestWriteRoutes_CrossOriginFormAndTextPlain(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.authMode = AuthModeTailnet
	srv.identity = fakeIdentity{login: "user@example.com"}
	srv.apps = testAppStore(t)
	srv.grants = NewGrantStore()

	type writeRoute struct {
		method, path, contentType, body string
		admin                           bool
	}
	routes := []writeRoute{
		{http.MethodPost, "/v1/apps/authorize", "application/x-www-form-urlencoded",
			"client_id=eco&scopes=events:freebusy", true},
		{http.MethodPost, "/v1/admin/apps", "text/plain", `{"id":"eco","name":"eco"}`, true},
		{http.MethodPost, "/v1/admin/cache/clear", "text/plain", `{}`, true},
		{http.MethodPost, "/v1/admin/mail/sync", "text/plain", `{"max_messages":1}`, true},
		{http.MethodPost, "/v1/admin/udt/sources", "text/plain", `{"id":"x"}`, true},
		{http.MethodPost, "/v1/library/mirror", "text/plain", `{}`, true},
		{http.MethodPost, "/v1/mail/sync", "text/plain", `{"max_messages":1}`, true},
		{http.MethodDelete, "/v1/library/links/abc", "application/json", `{}`, true},
	}

	for _, rt := range routes {
		// Cross-site Origin on a simple content type.
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		req.Header.Set("Content-Type", rt.contentType)
		req.Header.Set("Origin", "https://evil.example.com")
		w := httptest.NewRecorder()
		if rt.admin {
			srv.AdminMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))(w, req)
		} else {
			srv.AuthMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))(w, req)
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s cross-origin: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}

		// text/plain is rejected even same-origin / with API key.
		req = httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Api-Key", "test-key")
		req.Header.Set("Origin", "https://aegis.example.com")
		w = httptest.NewRecorder()
		if rt.admin {
			srv.AdminMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))(w, req)
		} else {
			srv.AuthMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))(w, req)
		}
		if rt.contentType == "text/plain" && w.Code != http.StatusForbidden {
			t.Fatalf("%s %s text/plain: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
}

func TestAppAuthorize_FormCSRFRequired(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apps = store
	srv.grants = NewGrantStore()

	// Render consent page and capture the per-render token.
	req := httptest.NewRequest("GET",
		"/v1/apps/authorize?client_id=eco&scopes=events:freebusy&redirect_uri=/demo/third-party",
		nil)
	w := httptest.NewRecorder()
	srv.HandleAppAuthorize(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}
	token := extractCSRFToken(t, w.Body.String())
	if token == "" {
		t.Fatal("missing csrf token in page")
	}

	form := url.Values{
		"client_id":    {"eco"},
		"scopes":       {"events:freebusy"},
		"redirect_uri": {"/demo/third-party"},
	}

	// Missing token.
	req = httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing csrf: %d %s", w.Code, w.Body.String())
	}

	// Wrong token.
	bad := form
	bad.Set("csrf_token", "aeg_csrf_nope")
	req = httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(bad.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("bad csrf: %d %s", w.Code, w.Body.String())
	}

	// Valid token is single-use.
	okForm := form
	okForm.Set("csrf_token", token)
	req = httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(okForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("valid csrf: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(okForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("reused csrf: %d %s", w.Code, w.Body.String())
	}

	if _, ok := store.Get("eco"); !ok {
		t.Fatal("successful grant should create the app")
	}
}

func TestAppAuthorize_JSONGrantStillWorksWithAPIKey(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apps = store
	srv.grants = NewGrantStore()

	body := `{"client_id":"eco","name":"eco","scopes":"events:freebusy","redirect_uri":"/demo/third-party"}`
	req := httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("json grant: %d %s", w.Code, w.Body.String())
	}
}

func TestAppAuthorize_TailnetSameOriginFormWithCSRF(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apps = store
	srv.grants = NewGrantStore()
	srv.authMode = AuthModeTailnet
	srv.identity = fakeIdentity{login: "user@example.com"}

	req := httptest.NewRequest("GET",
		"/v1/apps/authorize?client_id=eco&scopes=events:freebusy&redirect_uri=/demo/third-party",
		nil)
	w := httptest.NewRecorder()
	srv.HandleAppAuthorize(w, req)
	token := extractCSRFToken(t, w.Body.String())
	if token == "" {
		t.Fatal("missing csrf token")
	}

	form := url.Values{
		"client_id":    {"eco"},
		"scopes":       {"events:freebusy"},
		"redirect_uri": {"/demo/third-party"},
		"csrf_token":   {token},
	}
	req = httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://aegis.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("same-origin form grant: %d %s", w.Code, w.Body.String())
	}
}

func TestAllowUnsafeContentType(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	cases := []struct {
		path, ct string
		want     int
	}{
		{"/v1/admin/apps", "application/json", http.StatusOK},
		{"/v1/admin/apps", "application/json; charset=utf-8", http.StatusOK},
		{"/v1/admin/apps", "text/plain", http.StatusForbidden},
		{"/v1/admin/apps", "text/html", http.StatusForbidden},
		{"/v1/apps/authorize", "application/x-www-form-urlencoded", http.StatusOK},
		{"/v1/apps/authorize", "multipart/form-data", http.StatusOK},
		{"/v1/library/mirror", "application/x-www-form-urlencoded", http.StatusForbidden},
		{"/v1/services/immich/photos", "image/png", http.StatusOK},
		{"/v1/admin/cache/clear", "", http.StatusOK},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", c.path, strings.NewReader("{}"))
		if c.ct != "" {
			req.Header.Set("Content-Type", c.ct)
		}
		req.Header.Set("X-Api-Key", "test-key")
		w := httptest.NewRecorder()
		srv.AdminMiddleware(inner)(w, req)
		// Auth may 401/403 first on non-admin paths; only assert Content-Type
		// gate when auth would have allowed the write.
		if c.path == "/v1/services/immich/photos" {
			req = httptest.NewRequest("POST", c.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", c.ct)
			req.Header.Set("X-Api-Key", "test-key")
			w = httptest.NewRecorder()
			srv.AuthMiddleware(inner)(w, req)
		}
		if c.want == http.StatusForbidden && w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: got %d, want 403", c.path, c.ct, w.Code)
		}
		if c.want == http.StatusOK && w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "Content-Type") {
			t.Fatalf("%s %s: unexpected content-type reject: %s", c.path, c.ct, w.Body.String())
		}
	}
}

func TestAuthKindTracking(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	var got authKind
	inner := func(w http.ResponseWriter, r *http.Request) {
		got = authKindFrom(r)
		writeJSON(w, map[string]string{"status": "ok"})
	}

	req := httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if got != authKindAPIKey {
		t.Fatalf("api-key kind = %q", got)
	}

	srv.authMode = AuthModeTailnet
	srv.identity = fakeIdentity{login: "user@example.com"}
	req = httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	req.Header.Set("Origin", "https://aegis.example.com")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(inner)(w, req)
	if got != authKindTailnet {
		t.Fatalf("tailnet kind = %q", got)
	}
}

// fakeIdentity is a stub IdentityResolver for tailnet write tests.
type fakeIdentity struct {
	login string
}

func (f fakeIdentity) Resolve(_ context.Context, _ *http.Request) (*PeerIdentity, error) {
	return &PeerIdentity{LoginName: f.login, Source: "test"}, nil
}

var _ IdentityResolver = fakeIdentity{}
