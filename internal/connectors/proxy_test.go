package connectors

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// proxyUpstream stands up a canned provider response and records the request
// it saw, so tests can assert the shared pipeline's outbound rules.
type proxyUpstream struct {
	*httptest.Server
	status  int
	body    string
	gotAuth string
	gotCT   string
}

func newProxyUpstream(t *testing.T, status int, body string) *proxyUpstream {
	t.Helper()
	u := &proxyUpstream{status: status, body: body}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.gotAuth = r.Header.Get("Authorization")
		u.gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(u.status)
		io.WriteString(w, u.body)
	}))
	t.Cleanup(u.Close)
	return u
}

func proxyTestHost(t *testing.T, serviceID, endpoint string) *testHost {
	t.Helper()
	h := newTestHostWithService(serviceID, endpoint, "https://aegis.example.com")
	h.setToken(serviceID, "tok-1")
	return h
}

func TestRunProxy_PassthroughCachesRawBytes(t *testing.T) {
	up := newProxyUpstream(t, http.StatusOK, `{"_type":"locations","data":[]}`)
	host := proxyTestHost(t, OwnTracksServiceID, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v0/locations?user=me", nil)
	w := httptest.NewRecorder()
	RunProxy(host, w, req, "/api/v0/locations", ProxyOpts{
		serviceID:   OwnTracksServiceID,
		displayName: OwnTracksServiceName,
		defaultTTL:  time.Minute,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != up.body {
		t.Fatalf("body = %q, want raw upstream bytes %q", got, up.body)
	}
	if w.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("cache header = %q", w.Header().Get("X-Aegis-Cache"))
	}
	if up.gotAuth != "Bearer tok-1" {
		t.Fatalf("upstream Authorization = %q", up.gotAuth)
	}

	// Second GET must be served from cache, and the cache must hold the raw
	// bytes rather than a re-marshaled copy.
	w2 := httptest.NewRecorder()
	RunProxy(host, w2, req, "/api/v0/locations", ProxyOpts{
		serviceID:   OwnTracksServiceID,
		displayName: OwnTracksServiceName,
		defaultTTL:  time.Minute,
	})
	if w2.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q body=%s", w2.Header().Get("X-Aegis-Cache"), w2.Body.String())
	}
	if w2.Body.String() != up.body {
		t.Fatalf("cached body = %q", w2.Body.String())
	}
}

func TestRunProxy_CustomTokenResolverAndCopy(t *testing.T) {
	up := newProxyUpstream(t, http.StatusOK, `{}`)
	host := newTestHostWithService(OwnTracksServiceID, up.URL, "https://aegis.example.com")
	// No OAuth token on the host: the static-header resolver must supply it.
	host.services[OwnTracksServiceID].Headers = map[string]string{"X-API-Key": "secret"}

	req := httptest.NewRequest(http.MethodGet, "/api/v0/locations", nil)
	w := httptest.NewRecorder()
	RunProxy(host, w, req, "/api/v0/locations", ProxyOpts{
		serviceID:      OwnTracksServiceID,
		displayName:    OwnTracksServiceName,
		missingToken:   "set headers on the owntracks service",
		rejectToken:    "rejected — set an API key",
		rejectForbidden: true,
		token:          NewOwnTracksConnector(host).OwnTracksToken,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if up.gotAuth != "Bearer secret" {
		t.Fatalf("upstream Authorization = %q", up.gotAuth)
	}

	// No credential at all → the connector's own missing-token copy.
	empty := newTestHostWithService(OwnTracksServiceID, up.URL, "https://aegis.example.com")
	w2 := httptest.NewRecorder()
	RunProxy(empty, w2, httptest.NewRequest(http.MethodGet, "/x", nil), "/x", ProxyOpts{
		serviceID:    OwnTracksServiceID,
		displayName:  OwnTracksServiceName,
		missingToken: "set headers on the owntracks service",
		token:        NewOwnTracksConnector(empty).OwnTracksToken,
	})
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), "set headers on the owntracks service") {
		t.Fatalf("body = %s", w2.Body.String())
	}
}

func TestRunProxy_RejectForbiddenMapsToUnauthorized(t *testing.T) {
	up := newProxyUpstream(t, http.StatusForbidden, `{"error":"bad key"}`)
	host := proxyTestHost(t, OwnTracksServiceID, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v0/locations", nil)
	w := httptest.NewRecorder()
	RunProxy(host, w, req, "/api/v0/locations", ProxyOpts{
		serviceID:       OwnTracksServiceID,
		displayName:     OwnTracksServiceName,
		rejectToken:     "rejected — set an API key",
		rejectForbidden: true,
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rejected — set an API key") {
		t.Fatalf("body = %s", w.Body.String())
	}

	// Without the opt, an OAuth provider's 403 (insufficient scope) stays
	// visible instead of being reported as a revoked token.
	host.setService(SpotifyServiceID, up.URL)
	host.setToken(SpotifyServiceID, "tok-1")
	w2 := httptest.NewRecorder()
	RunProxy(host, w2, req, "/api/v0/locations", ProxyOpts{
		serviceID:   SpotifyServiceID,
		displayName: "Spotify",
	})
	if w2.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", w2.Code, w2.Body.String())
	}
}

func TestRunProxy_DefaultAuthCopyAndRateLimit(t *testing.T) {
	up := newProxyUpstream(t, http.StatusUnauthorized, `{}`)
	host := proxyTestHost(t, YNABServiceID, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/budgets", nil)
	w := httptest.NewRecorder()
	RunProxy(host, w, req, "/budgets", ProxyOpts{
		serviceID:   YNABServiceID,
		displayName: YNABServiceName,
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/v1/auth/ynab/login") {
		t.Fatalf("default reject copy missing reconnect hint: %s", w.Body.String())
	}

	up.status = http.StatusTooManyRequests
	w2 := httptest.NewRecorder()
	RunProxy(host, w2, req, "/budgets", ProxyOpts{
		serviceID:   YNABServiceID,
		displayName: YNABServiceName,
	})
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", w2.Code)
	}
	if w2.Header().Get("Retry-After") != "30" {
		t.Fatalf("Retry-After = %q", w2.Header().Get("Retry-After"))
	}
}

func TestRunProxy_TransformNilResultPassesBytesThrough(t *testing.T) {
	up := newProxyUpstream(t, http.StatusOK, "<html>not json</html>")
	host := proxyTestHost(t, YNABServiceID, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/budgets/b1/transactions", nil)
	w := httptest.NewRecorder()
	RunProxy(host, w, req, "/budgets/b1/transactions", ProxyOpts{
		serviceID:   YNABServiceID,
		displayName: YNABServiceName,
		transform: func(method, path string, data []byte, logger *slog.Logger) any {
			return nil
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != "<html>not json</html>" {
		t.Fatalf("body = %q, want the unmarshable upstream bytes untouched", w.Body.String())
	}
}

func TestStreamProxy_ForwardsAndCopiesUnchanged(t *testing.T) {
	up := newProxyUpstream(t, http.StatusCreated, `{"id":"x"}`)
	logger := slog.Default()

	req := httptest.NewRequest(http.MethodPost, "/v1/picker/sessions?alt=json", strings.NewReader(`{"sessionInfo":{}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	StreamProxy(w, req, up.URL, "/v1/picker/sessions", "tok-2", "Google Photos", logger)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != `{"id":"x"}` {
		t.Fatalf("body = %q", w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
	if up.gotAuth != "Bearer tok-2" {
		t.Fatalf("upstream Authorization = %q", up.gotAuth)
	}
	if up.gotCT != "application/json" {
		t.Fatalf("upstream Content-Type = %q", up.gotCT)
	}

	// Upstream statuses pass through unmapped — the connector has already
	// had its say about credentials before calling in.
	up.status = http.StatusNotFound
	w2 := httptest.NewRecorder()
	StreamProxy(w2, req, up.URL, "/v1/picker/sessions", "tok-2", "Google Photos", logger)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w2.Code)
	}
}

func TestStreamProxy_UpstreamFailureIsBadGateway(t *testing.T) {
	up := newProxyUpstream(t, http.StatusOK, `{}`)
	base := up.URL
	up.Close()

	req := httptest.NewRequest(http.MethodGet, "/messages/m1", nil)
	w := httptest.NewRecorder()
	StreamProxy(w, req, base, "/messages/m1", "tok-3", "Gmail", slog.Default())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Gmail request failed") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestYNABTransformProxyResponse(t *testing.T) {
	c := &YNABConnector{}
	listing := `{"data":{"transactions":[
		{"id":"t1","amount":-1200,"payee_name":"Cafe","deleted":false},
		{"id":"t2","amount":-500,"payee_name":"Gone","deleted":true}]}}`

	got := c.TransformProxyResponse(http.MethodGet, "/budgets/b1/transactions", []byte(listing), slog.Default())
	list, ok := got.(types.TransactionList)
	if !ok {
		t.Fatalf("type = %T, want types.TransactionList", got)
	}
	if list.Total != 1 {
		t.Fatalf("total = %d, want 1 (deleted rows skipped)", list.Total)
	}

	// Only GET listings transform; everything else keeps the upstream bytes.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/budgets/b1/transactions", listing},
		{http.MethodGet, "/budgets/b1", listing},
		{http.MethodGet, "/budgets/b1/transactions", "<html>boom</html>"},
	} {
		if got := c.TransformProxyResponse(tc.method, tc.path, []byte(tc.body), slog.Default()); got != nil {
			t.Fatalf("TransformProxyResponse(%s %s) = %T, want nil passthrough", tc.method, tc.path, got)
		}
	}
}

func TestYNABHandleProxy_TransactionsUseNeutralShape(t *testing.T) {
	up := newProxyUpstream(t, http.StatusOK,
		`{"data":{"transactions":[{"id":"t1","amount":-1200,"payee_name":"Cafe","deleted":false}]}}`)
	host := proxyTestHost(t, YNABServiceID, up.URL)
	c := NewYNABConnector(host)

	req := httptest.NewRequest(http.MethodGet, "/budgets/b1/transactions", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/budgets/b1/transactions")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list types.TransactionList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if list.Total != 1 {
		t.Fatalf("total = %d body=%s", list.Total, w.Body.String())
	}
	if w.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("cache header = %q", w.Header().Get("X-Aegis-Cache"))
	}

	w2 := httptest.NewRecorder()
	c.HandleProxy(w2, req, "/budgets/b1/transactions")
	if w2.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q body=%s", w2.Header().Get("X-Aegis-Cache"), w2.Body.String())
	}
}

func TestOwnTracksHandleProxy_RejectsBadKeyAndCaches(t *testing.T) {
	up := newProxyUpstream(t, http.StatusForbidden, `{"error":"nope"}`)
	host := proxyTestHost(t, OwnTracksServiceID, up.URL)
	c := NewOwnTracksConnector(host)

	req := httptest.NewRequest(http.MethodGet, "/api/v0/locations", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/api/v0/locations")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "set an API key") {
		t.Fatalf("body = %s", w.Body.String())
	}

	up.status = http.StatusOK
	up.body = `{"_type":"locations","data":[]}`
	w2 := httptest.NewRecorder()
	c.HandleProxy(w2, req, "/api/v0/locations")
	if w2.Code != http.StatusOK || w2.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("status = %d cache=%q body=%s", w2.Code, w2.Header().Get("X-Aegis-Cache"), w2.Body.String())
	}
	w3 := httptest.NewRecorder()
	c.HandleProxy(w3, req, "/api/v0/locations")
	if w3.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q", w3.Header().Get("X-Aegis-Cache"))
	}
}
