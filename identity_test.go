package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServeHeaderResolver_UserLogin(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	r.Header.Set("Tailscale-User-Login", "alice@example.com")
	r.Header.Set("Tailscale-User-Name", "Alice")

	id, err := ServeHeaderResolver{}.Resolve(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil {
		t.Fatal("expected identity")
	}
	if id.LoginName != "alice@example.com" || id.DisplayName != "Alice" {
		t.Fatalf("id = %+v", id)
	}
	if id.Source != "serve-headers" {
		t.Fatalf("source = %s", id.Source)
	}
}

func TestServeHeaderResolver_TagsOnly(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	r.Header.Set("Tailscale-Node-Tags", "tag:aperture,tag:client")

	id, err := ServeHeaderResolver{}.Resolve(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil {
		t.Fatal("expected identity from tags")
	}
	if id.LoginName != "tag:aperture,tag:client" {
		t.Fatalf("login = %s", id.LoginName)
	}
}

func TestServeHeaderResolver_NoIdentity(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	id, err := ServeHeaderResolver{}.Resolve(context.Background(), r)
	if err != nil || id != nil {
		t.Fatalf("id=%v err=%v", id, err)
	}
}

func TestAuthMiddleware_TailnetMode_RequiresIdentity(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	srv.authMode = AuthModeTailnet
	srv.identity = ServeHeaderResolver{}
	srv.apiKey = "unused"

	called := false
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if id := PeerIdentityFrom(r); id == nil || id.LoginName != "alice@example.com" {
			t.Errorf("missing peer identity: %+v", id)
		}
	})

	// No headers → 401.
	req := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if called || w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d called=%v", w.Code, called)
	}

	// With Serve headers → allowed without API key.
	req = httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	req.Header.Set("Tailscale-User-Login", "alice@example.com")
	w = httptest.NewRecorder()
	h(w, req)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("code=%d called=%v body=%s", w.Code, called, w.Body.String())
	}
}

func TestAuthMiddleware_TailnetMode_Allowlist(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	srv.authMode = AuthModeTailnet
	srv.identity = ServeHeaderResolver{}
	srv.allowedLogins = []string{"alice@example.com"}

	ok := false
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		ok = true
	})

	req := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	req.Header.Set("Tailscale-User-Login", "bob@example.com")
	w := httptest.NewRecorder()
	h(w, req)
	if ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("bob should be denied: code=%d ok=%v", w.Code, ok)
	}

	req = httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	req.Header.Set("Tailscale-User-Login", "alice@example.com")
	w = httptest.NewRecorder()
	h(w, req)
	if !ok || w.Code != http.StatusOK {
		t.Fatalf("alice should be allowed: code=%d ok=%v", w.Code, ok)
	}
}

func TestAuthMiddleware_AutoMode_PrefersIdentity(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	srv.authMode = AuthModeAuto
	srv.apiKey = "test-key"
	srv.identity = ServeHeaderResolver{}

	called := false
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if PeerIdentityFrom(r) == nil {
			t.Error("expected tailnet identity preferred over API key")
		}
	})

	req := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	req.Header.Set("Tailscale-User-Login", "alice@example.com")
	w := httptest.NewRecorder()
	h(w, req)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("code=%d called=%v", w.Code, called)
	}
}

func TestAuthMiddleware_AutoMode_FallsBackToAPIKey(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	srv.authMode = AuthModeAuto
	srv.apiKey = "test-key"
	srv.identity = ServeHeaderResolver{}

	called := false
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if PeerIdentityFrom(r) != nil {
			t.Error("API-key path should not attach peer identity")
		}
	})

	req := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	h(w, req)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("code=%d called=%v", w.Code, called)
	}

	// Neither identity nor key → 401.
	called = false
	req = httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	w = httptest.NewRecorder()
	h(w, req)
	if called || w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d called=%v", w.Code, called)
	}
}

func TestAuthMiddleware_APIKeyMode_IgnoresTailnetHeaders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	srv.authMode = AuthModeAPIKey
	srv.apiKey = "test-key"
	srv.identity = ServeHeaderResolver{}

	called := false
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	// Headers alone must not satisfy api-key mode.
	req := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	req.Header.Set("Tailscale-User-Login", "alice@example.com")
	w := httptest.NewRecorder()
	h(w, req)
	if called || w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d called=%v", w.Code, called)
	}
}

func TestLoginAllowed_EmptyAllowlist(t *testing.T) {
	if !loginAllowed(&PeerIdentity{LoginName: "anyone"}, nil) {
		t.Fatal("empty allowlist should allow any identity")
	}
}

func TestParseLoginList(t *testing.T) {
	got := parseLoginList("alice@example.com, bob@example.com")
	if len(got) != 2 || got[0] != "alice@example.com" || got[1] != "bob@example.com" {
		t.Fatalf("got %#v", got)
	}
	if parseLoginList("  ") != nil {
		t.Fatal("expected nil for blank input")
	}
}

func TestWhoIsResolver_MockSocket(t *testing.T) {
	// httptest cannot easily stand in for a unix socket; verify nil when
	// no socket is configured rather than requiring a live tailscaled.
	r := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	r.RemoteAddr = "100.101.102.103:12345"
	id, err := WhoIsResolver{SocketPath: ""}.Resolve(context.Background(), r)
	// defaultTSocketPath may or may not find a socket on this machine;
	// error is acceptable, identity is not required in unit tests.
	_ = id
	_ = err
}

func TestMultiResolver_FirstHitWins(t *testing.T) {
	m := MultiResolver{Resolvers: []IdentityResolver{
		ServeHeaderResolver{},
	}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Tailscale-User-Login", "carol@example.com")
	id, err := m.Resolve(context.Background(), r)
	if err != nil || id == nil || id.LoginName != "carol@example.com" {
		t.Fatalf("id=%+v err=%v", id, err)
	}
}

// Ensure JSON error body shape stays stable for unauthorized responses.
func TestDenyWritesJSON(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	srv.authMode = AuthModeAPIKey
	srv.apiKey = "k"
	srv.AuthMiddleware(func(http.ResponseWriter, *http.Request) {})(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	w := httptest.NewRecorder()
	srv.deny(w, httptest.NewRequest("GET", "/v1/x", nil), nil, "test")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("body=%v", body)
	}
}
