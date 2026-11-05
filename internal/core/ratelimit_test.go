package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimitConfig_Effective(t *testing.T) {
	def := RateLimitConfig{}.effective()
	if def.perMinute != defaultAuthPerMinute || def.failPerIP != defaultAuthFailuresPerIP ||
		def.failPerToken != defaultAuthFailuresPerToken || def.window != defaultRateWindowSeconds*time.Second {
		t.Fatalf("defaults = %+v", def)
	}
	custom := RateLimitConfig{AuthPerMinute: 5, AuthFailuresPerIP: -1, WindowSeconds: 60}.effective()
	if custom.perMinute != 5 || custom.failPerIP != 0 || custom.window != time.Minute {
		t.Fatalf("custom = %+v", custom)
	}
}

func TestRateLimiter_HitAndFailureCaps(t *testing.T) {
	l := newRateLimiter()
	fixed := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return fixed }

	if !l.hit("ip:a", 2, time.Minute) {
		t.Fatal("first hit must pass")
	}
	if !l.hit("ip:a", 2, time.Minute) {
		t.Fatal("second hit must pass")
	}
	if l.hit("ip:a", 2, time.Minute) {
		t.Fatal("third hit must be damped")
	}

	if l.recordFailure("tok:x", 3, time.Minute) {
		t.Fatal("cap not reached after one failure")
	}
	if l.recordFailure("tok:x", 3, time.Minute) {
		t.Fatal("cap not reached after two failures")
	}
	if !l.recordFailure("tok:x", 3, time.Minute) {
		t.Fatal("third failure must reach the cap")
	}
	if !l.locked("tok:x", 3, time.Minute) {
		t.Fatal("key must be locked after the cap")
	}
	if l.locked("tok:y", 3, time.Minute) {
		t.Fatal("other keys must not be locked")
	}

	// Window expiry clears the lock.
	l.now = func() time.Time { return fixed.Add(2 * time.Minute) }
	if l.locked("tok:x", 3, time.Minute) {
		t.Fatal("lock must expire with the window")
	}
	if !l.hit("ip:a", 2, time.Minute) {
		t.Fatal("hits must reset with the window")
	}
}

func TestRateLimiter_EvictionBoundsMemory(t *testing.T) {
	l := newRateLimiter()
	for i := 0; i < maxRateTrackedKeys+100; i++ {
		l.hit(fmt.Sprintf("ip:%d", i), 10, time.Minute)
	}
	l.mu.Lock()
	n := len(l.counts)
	l.mu.Unlock()
	if n > maxRateTrackedKeys {
		t.Fatalf("tracked keys = %d, want <= %d", n, maxRateTrackedKeys)
	}
}

func TestRateLimitAuth_DampsRouteVolume(t *testing.T) {
	srv := testServerWithBase(&Config{RateLimits: RateLimitConfig{AuthPerMinute: 2}}, "https://aegis.example.com")
	srv.rl = newRateLimiter()
	h := srv.rateLimitAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodPost, "/v1/apps/token", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/v1/apps/token", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("over-cap request = %d, want 429", w.Code)
	}
}

func TestAuthBruteForce_LocksOutIP(t *testing.T) {
	srv := testServerWithBase(&Config{
		RateLimits: RateLimitConfig{AuthFailuresPerIP: 3, AuthFailuresPerToken: -1, WindowSeconds: 60},
	}, "https://aegis.example.com")
	srv.rl = newRateLimiter()
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	attempt := func(key string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/library/tracks", nil)
		req.Header.Set("X-Api-Key", key)
		w := httptest.NewRecorder()
		h(w, req)
		return w.Code
	}

	// Distinct wrong keys (separate token counters) from one IP.
	for i := 0; i < 3; i++ {
		if got := attempt(fmt.Sprintf("wrong-key-%d", i)); got != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i, got)
		}
	}
	// The IP is now locked out — even the valid key is damped.
	if got := attempt("test-key"); got != http.StatusTooManyRequests {
		t.Fatalf("locked-out IP with valid key = %d, want 429", got)
	}
}

func TestAuthBruteForce_LocksOutTokenAcrossIPs(t *testing.T) {
	srv := testServerWithBase(&Config{
		RateLimits: RateLimitConfig{AuthFailuresPerIP: -1, AuthFailuresPerToken: 3, WindowSeconds: 60},
	}, "https://aegis.example.com")
	srv.rl = newRateLimiter()
	h := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/library/tracks", nil)
		req.RemoteAddr = fmt.Sprintf("203.0.113.%d:41000", i+1)
		req.Header.Set("X-Api-Key", "probed-key")
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i, w.Code)
		}
	}
	// Same presented key from a fresh IP: the per-token cap still holds.
	req := httptest.NewRequest(http.MethodGet, "/v1/library/tracks", nil)
	req.RemoteAddr = "198.51.100.7:41000"
	req.Header.Set("X-Api-Key", "probed-key")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("locked-out token from fresh IP = %d, want 429", w.Code)
	}
}
