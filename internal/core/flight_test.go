package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshToken_Singleflight(t *testing.T) {
	var calls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond) // hold so waiters pile up
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"shared","token_type":"Bearer","expires_in":3600,"scope":"user-library-read"}`)
	}))
	defer tokenSrv.Close()

	cfg := &Config{
		Connections: map[string]Connection{
			"spotify": {
				Name:         "Spotify",
				ClientID:     "cid",
				ClientSecret: "sec",
				AuthURL:      "https://accounts.spotify.com/authorize",
				TokenURL:     tokenSrv.URL,
				Scopes:       []string{"user-library-read"},
			},
		},
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Connection: "spotify", OAuthScopes: []string{"user-library-read"}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken:  "old",
		RefreshToken: "rt",
		Expiry:       time.Now().Add(-time.Minute),
		Scopes:       []string{"user-library-read"},
	})

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := srv.RefreshToken("spotify"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("RefreshToken: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream token calls = %d, want 1 (singleflight)", got)
	}
	tok := srv.tokens.Get("spotify")
	if tok == nil || tok.AccessToken != "shared" {
		t.Fatalf("stored token = %+v", tok)
	}
}

func TestTokenFlight_DoSharesResult(t *testing.T) {
	var f tokenFlight
	var calls atomic.Int32
	fn := func() (*OAuthToken, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return &OAuthToken{AccessToken: "ok"}, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := f.Do("k", fn)
			if err != nil || tok.AccessToken != "ok" {
				t.Errorf("Do = (%v, %v)", tok, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fn calls = %d, want 1", calls.Load())
	}
}
