package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func oauthConfig(tokenURL, authURL string) *Config {
	return &Config{
		Connections: map[string]Connection{
			"spotify": {
				Name:         "Spotify",
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				AuthURL:      authURL,
				TokenURL:     tokenURL,
				Scopes:       []string{"user-library-read"},
			},
		},
		Services: []Service{
			{
				ID:          "spotify",
				Name:        "Spotify",
				Action:      "inject",
				Connection:  "spotify",
				OAuthScopes: []string{"user-library-read"},
			},
		},
	}
}

func TestHandleAuthLogin_RedirectsWithState(t *testing.T) {
	srv := testServerWithBase(oauthConfig("https://tokens.example.com/token", "https://accounts.example.com/authorize"), "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/auth/spotify/login", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthLogin(w, req)

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://accounts.example.com/authorize?") {
		t.Fatalf("location = %q", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != "client-id" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("redirect_uri") != "https://aegis.example.com/v1/auth/spotify/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	state := q.Get("state")
	if state == "" {
		t.Fatal("expected non-empty state")
	}
	if !srv.oauthStates.Delete("spotify:" + state) {
		t.Fatal("expected state to be stored")
	}
}

func TestHandleAuthLogin_UnknownService(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/auth/unknown/login", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthLogin(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleAuthLogin_NoOAuthConfig(t *testing.T) {
	srv := testServerWithBase(&Config{
		Services: []Service{{ID: "tidal", Name: "Tidal"}},
	}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/auth/tidal/login", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthLogin(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleAuthLogin_GoogleOfflineAccess(t *testing.T) {
	cfg := &Config{
		Connections: map[string]Connection{
			"google": {
				Name:     "Google",
				ClientID: "cid",
				AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL: "https://oauth2.googleapis.com/token",
				Scopes:   []string{"https://www.googleapis.com/auth/youtube.readonly"},
			},
		},
		Services: []Service{{
			ID:          "youtube-music",
			Name:        "YouTube Music",
			Connection:  "google",
			OAuthScopes: []string{"https://www.googleapis.com/auth/youtube.readonly"},
		}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/youtube-music/login", nil)
	rec := httptest.NewRecorder()
	srv.HandleAuthLogin(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://accounts.google.com/o/oauth2/v2/auth?") {
		t.Fatalf("location = %v", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("access_type") != "offline" {
		t.Fatalf("access_type = %q, want offline", q.Get("access_type"))
	}
	if q.Get("include_granted_scopes") != "true" {
		t.Fatalf("include_granted_scopes = %q, want true", q.Get("include_granted_scopes"))
	}
	if q.Get("prompt") != "consent" {
		t.Fatalf("prompt = %q, want consent", q.Get("prompt"))
	}
	if q.Get("redirect_uri") != "https://aegis.example.com/v1/auth/youtube-music/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestHandleAuthLogin_SpotifyOmitsGoogleParams(t *testing.T) {
	cfg := oauthConfig("https://accounts.spotify.com/api/token", "https://accounts.spotify.com/authorize")
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/spotify/login", nil)
	rec := httptest.NewRecorder()
	srv.HandleAuthLogin(rec, req)

	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("access_type") != "" {
		t.Fatalf("spotify should not send access_type, got %q", u.Query().Get("access_type"))
	}
}

func TestHandleAuthCallback_MissingParams(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/auth/spotify/callback", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleAuthCallback_InvalidState(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/auth/spotify/callback?code=abc&state=bad", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleAuthCallback_Success(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code") != "auth-code" {
			t.Errorf("code = %q", r.Form.Get("code"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600,"scope":"user-library-read"}`)
	}))
	defer tokenSrv.Close()

	srv := testServerWithBase(oauthConfig(tokenSrv.URL, "https://accounts.example.com/authorize"), "https://aegis.example.com")
	srv.oauthStates.Set("spotify:good-state", true, time.Minute)

	req := httptest.NewRequest("GET", "/v1/auth/spotify/callback?code=auth-code&state=good-state", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Spotify Authorized") {
		t.Fatalf("body missing authorized page: %s", body)
	}
	// Issue #87 branding on the OAuth success page.
	for _, want := range []string{`rel="icon"`, "/favicon.svg", "/logo.svg"} {
		if !strings.Contains(body, want) {
			t.Fatalf("authorized page missing %q: %s", want, body)
		}
	}
	tok := srv.tokens.Get("spotify")
	if tok == nil || tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Fatalf("stored token = %+v", tok)
	}
	if srv.oauthStates.Delete("spotify:good-state") {
		t.Fatal("expected state to be consumed")
	}
}

func TestHandleAuthCallback_TokenExchangeFails(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer tokenSrv.Close()

	srv := testServerWithBase(oauthConfig(tokenSrv.URL, "https://accounts.example.com/authorize"), "https://aegis.example.com")
	srv.oauthStates.Set("spotify:st", true, time.Minute)

	req := httptest.NewRequest("GET", "/v1/auth/spotify/callback?code=x&state=st", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthCallback(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestHandleAuthStatus_AndRevoke(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.tokens.Tokens["spotify"] = &OAuthToken{
		AccessToken:  "at",
		RefreshToken: "rt",
		Expiry:       time.Now().Add(time.Hour),
	}

	req := httptest.NewRequest("GET", "/v1/auth/status", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var status map[string]TokenStatus
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status["spotify"].HasToken || status["spotify"].Expired {
		t.Fatalf("status = %+v", status["spotify"])
	}

	req = httptest.NewRequest("DELETE", "/v1/auth/spotify", nil)
	w = httptest.NewRecorder()
	srv.HandleAuthRevoke(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("revoke status = %d", w.Code)
	}
	if srv.tokens.Get("spotify") != nil {
		t.Fatal("expected token removed")
	}
}

func TestRefreshToken_Success(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "old-rt" {
			t.Errorf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		fmt.Fprint(w, `{"access_token":"new-at","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenSrv.Close()

	srv := testServerWithBase(oauthConfig(tokenSrv.URL, "https://accounts.example.com/authorize"), "https://aegis.example.com")
	srv.tokens.Tokens["spotify"] = &OAuthToken{
		AccessToken:  "old-at",
		RefreshToken: "old-rt",
		Expiry:       time.Now().Add(-time.Minute),
	}

	tok, err := srv.RefreshToken("spotify")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "new-at" {
		t.Fatalf("access_token = %q", tok.AccessToken)
	}
	// Refresh token preserved when provider omits a new one.
	if tok.RefreshToken != "old-rt" {
		t.Fatalf("refresh_token = %q", tok.RefreshToken)
	}
	if srv.tokens.Get("spotify").AccessToken != "new-at" {
		t.Fatal("expected refreshed token persisted")
	}
}

func TestRefreshToken_NoRefreshToken(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.tokens.Tokens["spotify"] = &OAuthToken{AccessToken: "at"}

	if _, err := srv.RefreshToken("spotify"); err == nil {
		t.Fatal("expected error when no refresh token")
	}
}

func TestRefreshToken_UnconfiguredService(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.tokens.Tokens["unknown"] = &OAuthToken{RefreshToken: "rt"}

	if _, err := srv.RefreshToken("unknown"); err == nil {
		t.Fatal("expected error for unconfigured service")
	}
}

func TestTokenRequest_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "err")
	}))
	defer srv.Close()

	aegis := testServerWithBase(&Config{}, "https://aegis.example.com")
	if _, err := aegis.tokenRequest(srv.URL, url.Values{"grant_type": {"authorization_code"}}); err == nil {
		t.Fatal("expected error from non-200 token endpoint")
	}
}

func TestGenerateState_Unique(t *testing.T) {
	a, err := generateState()
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateState()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) != 32 {
		t.Fatalf("states %q %q", a, b)
	}
}

func TestHandleAuthRoutes_Dispatch(t *testing.T) {
	srv := testServerWithBase(oauthConfig("https://tokens.example.com/token", "https://accounts.example.com/authorize"), "https://aegis.example.com")

	// login is public
	req := httptest.NewRequest("GET", "/v1/auth/spotify/login", nil)
	w := httptest.NewRecorder()
	srv.handleAuthRoutes(w, req)
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("login status = %d", w.Code)
	}

	// status requires auth
	req = httptest.NewRequest("GET", "/v1/auth/status", nil)
	w = httptest.NewRecorder()
	srv.handleAuthRoutes(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status without auth = %d", w.Code)
	}
	req = httptest.NewRequest("GET", "/v1/auth/status", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.handleAuthRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status with auth = %d", w.Code)
	}

	// unknown path
	req = httptest.NewRequest("GET", "/v1/auth/nope", nil)
	w = httptest.NewRecorder()
	srv.handleAuthRoutes(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown = %d", w.Code)
	}

	// revoke requires auth and method
	req = httptest.NewRequest("GET", "/v1/auth/spotify", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.handleAuthRoutes(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET revoke = %d", w.Code)
	}
}

func TestHandleAuthLogin_SharedGoogleConnection(t *testing.T) {
	cfg := googleSharedConfig()
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/google-photos/login", nil)
	rec := httptest.NewRecorder()
	srv.HandleAuthLogin(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rec.Code)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	// Union of all Google surface scopes from the connection block.
	scope := q.Get("scope")
	for _, want := range []string{
		"https://www.googleapis.com/auth/youtube.readonly",
		"https://www.googleapis.com/auth/photospicker.mediaitems.readonly",
		"https://www.googleapis.com/auth/contacts.readonly",
	} {
		if !strings.Contains(scope, want) {
			t.Fatalf("scope %q missing from %q", want, scope)
		}
	}
	if q.Get("client_id") != "cid" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("access_type") != "offline" {
		t.Fatalf("access_type = %q", q.Get("access_type"))
	}
	if q.Get("include_granted_scopes") != "true" {
		t.Fatalf("include_granted_scopes = %q", q.Get("include_granted_scopes"))
	}
	if q.Get("prompt") != "consent" {
		t.Fatalf("prompt = %q, want consent on first connect", q.Get("prompt"))
	}
	if q.Get("redirect_uri") != "https://aegis.example.com/v1/auth/google-photos/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestHandleAuthLogin_SharedConnectionSkipsPromptWhenRefreshExists(t *testing.T) {
	cfg := googleSharedConfig()
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("google", &OAuthToken{
		AccessToken:  "at",
		RefreshToken: "rt",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"https://www.googleapis.com/auth/youtube.readonly"},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/youtube-music/login", nil)
	rec := httptest.NewRecorder()
	srv.HandleAuthLogin(rec, req)

	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("prompt") != "" {
		t.Fatalf("prompt = %q, want empty when refresh token already exists", q.Get("prompt"))
	}
	if q.Get("include_granted_scopes") != "true" {
		t.Fatalf("include_granted_scopes = %q", q.Get("include_granted_scopes"))
	}
}

func TestHandleAuthCallback_SharedConnectionStoresUnderConnectionID(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"shared-at","refresh_token":"shared-rt","token_type":"Bearer","expires_in":3600,"scope":"https://www.googleapis.com/auth/youtube.readonly https://www.googleapis.com/auth/photospicker.mediaitems.readonly https://www.googleapis.com/auth/contacts.readonly"}`)
	}))
	defer tokenSrv.Close()

	cfg := googleSharedConfig()
	gconn := cfg.Connections["google"]
	gconn.TokenURL = tokenSrv.URL
	cfg.Connections["google"] = gconn
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.oauthStates.Set("google-photos:ok", true, time.Minute)

	req := httptest.NewRequest("GET", "/v1/auth/google-photos/callback?code=auth-code&state=ok", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if srv.tokens.Get("google") == nil {
		t.Fatal("expected token under connection id google")
	}
	if srv.tokens.Get("google-photos") != nil {
		t.Fatal("should not store a second token under surface id")
	}
	if srv.tokens.Get("google").AccessToken != "shared-at" {
		t.Fatalf("token = %+v", srv.tokens.Get("google"))
	}
}

func TestHandleAuthCallback_PreservesRefreshTokenAndScopes(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No refresh_token and no scope in response — provider omitted them.
		fmt.Fprint(w, `{"access_token":"new-at","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenSrv.Close()

	cfg := googleSharedConfig()
	gconn := cfg.Connections["google"]
	gconn.TokenURL = tokenSrv.URL
	cfg.Connections["google"] = gconn
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("google", &OAuthToken{
		AccessToken:  "old",
		RefreshToken: "keep-rt",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"https://www.googleapis.com/auth/youtube.readonly"},
	})
	srv.oauthStates.Set("youtube-music:st", true, time.Minute)

	req := httptest.NewRequest("GET", "/v1/auth/youtube-music/callback?code=auth-code&state=st", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	tok := srv.tokens.Get("google")
	if tok.RefreshToken != "keep-rt" {
		t.Fatalf("refresh = %q, want keep-rt", tok.RefreshToken)
	}
	if len(tok.Scopes) != 1 || tok.Scopes[0] != "https://www.googleapis.com/auth/youtube.readonly" {
		t.Fatalf("scopes = %v", tok.Scopes)
	}
	if tok.AccessToken != "new-at" {
		t.Fatalf("access = %q", tok.AccessToken)
	}
}

func TestHandleAuthRevoke_SharedConnection(t *testing.T) {
	cfg := googleSharedConfig()
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("google", &OAuthToken{AccessToken: "at", Expiry: time.Now().Add(time.Hour)})

	req := httptest.NewRequest(http.MethodDelete, "/v1/auth/google-photos", nil)
	w := httptest.NewRecorder()
	srv.HandleAuthRevoke(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if srv.tokens.Get("google") != nil {
		t.Fatal("expected google connection token to be revoked")
	}
}
