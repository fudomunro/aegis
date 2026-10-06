package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseScopeList(t *testing.T) {
	entries, err := parseScopeList("events:freebusy, contacts:read")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 || entries[0].Type != "events" || entries[0].Level != AccessFreeBusy {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := parseScopeList("tracks:freebusy"); err == nil {
		t.Fatal("expected freebusy-on-tracks rejected")
	}
	if _, err := parseScopeList(""); err == nil {
		t.Fatal("expected empty scopes rejected")
	}
	// bare type means read
	entries, err = parseScopeList("events")
	if err != nil || entries[0].Level != AccessRead {
		t.Fatalf("bare = %+v err=%v", entries, err)
	}
}

func TestAppAuthorize_TokenExchangeFlow(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{Services: []Service{}}, "https://aegis.example.com")
	srv.apps = store
	srv.grants = NewGrantStore()

	// GET consent page is public.
	req := httptest.NewRequest("GET",
		"/v1/apps/authorize?client_id=eco-demo&name=Event%20Coordinator&scopes=events:freebusy&redirect_uri=/demo/third-party&state=abc",
		nil)
	w := httptest.NewRecorder()
	srv.HandleAppAuthorize(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authorize get: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Aegis") {
		t.Fatalf("consent page missing branding: %s", w.Body.String()[:200])
	}

	// POST grant requires auth.
	form := url.Values{
		"client_id":    {"eco-demo"},
		"name":         {"Event Coordinator"},
		"scopes":       {"events:freebusy"},
		"redirect_uri": {"/demo/third-party"},
		"state":        {"abc"},
	}
	req = httptest.NewRequest("POST", "/v1/apps/authorize",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated grant: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("POST", "/v1/apps/authorize",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "code=aeg_grant_") || !strings.Contains(loc, "state=abc") {
		t.Fatalf("redirect = %s", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse loc: %v", err)
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatal("missing code")
	}

	// App exists with freebusy scope.
	view, ok := store.Get("eco-demo")
	if !ok || view.Scopes["events"] != AccessFreeBusy {
		t.Fatalf("app = %+v ok=%v", view, ok)
	}

	// Exchange code for token (public endpoint).
	req = httptest.NewRequest("POST", "/v1/apps/token",
		strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.HandleAppToken(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(tok.AccessToken, appTokenPrefix) {
		t.Fatalf("token = %q", tok.AccessToken)
	}

	// Code is single-use.
	req = httptest.NewRequest("POST", "/v1/apps/token",
		strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.HandleAppToken(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reuse: %d %s", w.Code, w.Body.String())
	}

	// Token works on freebusy library route.
	svc := &Service{
		ID:          "google-calendar",
		Name:        "Google Calendar",
		Action:      "inject",
		Permissions: Permissions{"events": AccessRead},
	}
	srv.config = &Config{Services: []Service{*svc}}
	srv.connectors["google-calendar"] = &fakeCalendarEventProvider{events: freeBusyTestEvents()}
	req = httptest.NewRequest("GET", "/v1/library/freebusy", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryFreeBusy)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("freebusy with granted token: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Doctor") {
		t.Fatalf("leaked title: %s", w.Body.String())
	}
}

func TestAppAuthorize_RejectsBadRedirect(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apps = store
	srv.grants = NewGrantStore()

	req := httptest.NewRequest("GET",
		"/v1/apps/authorize?client_id=eco&scopes=events:freebusy&redirect_uri=https://evil.example.com/",
		nil)
	w := httptest.NewRecorder()
	srv.HandleAppAuthorize(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleThirdPartyDemo(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/demo/third-party", nil)
	w := httptest.NewRecorder()
	srv.HandleThirdPartyDemo(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Continue with Aegis") || !strings.Contains(body, "/v1/apps/authorize") {
		t.Fatalf("demo page missing button/flow: %s", body[:300])
	}
}

func TestAppAuthorize_JSONGrantAndAppTokenDenied(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apps = store
	srv.grants = NewGrantStore()

	body := `{"client_id":"eco-demo","name":"Event Coordinator","scopes":"events:freebusy","redirect_uri":"/demo/third-party","state":"s1"}`
	req := httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("json grant: %d %s", w.Code, w.Body.String())
	}
	var grantResp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &grantResp); err != nil || grantResp.Code == "" {
		t.Fatalf("grant resp: %s", w.Body.String())
	}

	// Second grant updates the same app and mints another code.
	req = httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("re-grant: %d %s", w.Code, w.Body.String())
	}

	// App token must not call authorize (cannot mint more grants).
	_, token := createTestApp(t, store, "other", Permissions{"events": AccessRead})
	req = httptest.NewRequest("POST", "/v1/apps/authorize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAppAuthorize)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("app token authorize: %d %s", w.Code, w.Body.String())
	}
}

func TestHandleAppToken_Errors(t *testing.T) {
	store := testAppStore(t)
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apps = store
	srv.grants = NewGrantStore()

	req := httptest.NewRequest("POST", "/v1/apps/token", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.HandleAppToken(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing code: %d", w.Code)
	}

	req = httptest.NewRequest("POST", "/v1/apps/token", strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.HandleAppToken(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", w.Code)
	}

	req = httptest.NewRequest("POST", "/v1/apps/token", strings.NewReader(`{"code":"aeg_grant_missing"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.HandleAppToken(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown code: %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/v1/apps/token", nil)
	w = httptest.NewRecorder()
	srv.HandleAppToken(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get token: %d", w.Code)
	}
}

func TestGrantStore_Expires(t *testing.T) {
	g := NewGrantStore()
	code, err := g.issue(GrantCode{AppID: "eco", Scopes: Permissions{"events": AccessRead}})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Force expiry.
	g.mu.Lock()
	c := g.byCode[code]
	c.Expires = c.Expires.Add(-2 * grantCodeTTL)
	g.byCode[code] = c
	g.mu.Unlock()
	if _, ok := g.consume(code); ok {
		t.Fatal("expired code must not consume")
	}
}
