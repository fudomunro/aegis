package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleSetup_RendersServiceCards(t *testing.T) {
	srv := testServerWithBase(&Config{
		Connections: map[string]Connection{
			"spotify": {
				Name:     "Spotify",
				ClientID: "cid",
				Scopes:   []string{"user-library-read"},
			},
		},
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Description: "Proxies Spotify", Connection: "spotify", OAuthScopes: []string{"user-library-read"}},
			{ID: "tidal", Name: "Tidal", Description: "blocked"},
		},
	}, "https://guardian.example.com")
	srv.tokens.Tokens["spotify"] = &OAuthToken{
		AccessToken:  "at",
		RefreshToken: "rt",
		Expiry:       time.Now().Add(time.Hour),
	}

	req := httptest.NewRequest("GET", "/v1/setup", nil)
	w := httptest.NewRecorder()
	srv.HandleSetup(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html" {
		t.Fatalf("content-type = %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"Guardian Setup", "Spotify", "Tidal", "Connected", "No OAuth", "user-library-read"} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup page missing %q", want)
		}
	}
}

func TestRenderServiceCard_StatusVariants(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")

	cases := []struct {
		name   string
		svc    Service
		status TokenStatus
		want   []string
	}{
		{
			name: "not connected",
			svc:  Service{ID: "spotify", Name: "Spotify", Connection: "spotify"},
			want: []string{"Not Connected", "Connect"},
		},
		{
			name:   "connected",
			svc:    Service{ID: "spotify", Name: "Spotify", Connection: "spotify"},
			status: TokenStatus{HasToken: true, ExpiresIn: "1h0m0s"},
			want:   []string{"Connected", "Reconnect"},
		},
		{
			name:   "expired with refresh",
			svc:    Service{ID: "spotify", Name: "Spotify", Connection: "spotify"},
			status: TokenStatus{HasToken: true, Expired: true, HasRefresh: true},
			want:   []string{"Expired (will auto-refresh)", "Reconnect"},
		},
		{
			name:   "expired without refresh",
			svc:    Service{ID: "spotify", Name: "Spotify", Connection: "spotify"},
			status: TokenStatus{HasToken: true, Expired: true},
			want:   []string{"badge-red", "Reconnect"},
		},
		{
			name: "no oauth",
			svc:  Service{ID: "tidal", Name: "Tidal"},
			want: []string{"No OAuth", "connection"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := srv.renderServiceCard(tc.svc, tc.status)
			for _, want := range tc.want {
				if !strings.Contains(html, want) {
					t.Fatalf("card missing %q in:\n%s", want, html)
				}
			}
		})
	}
}
