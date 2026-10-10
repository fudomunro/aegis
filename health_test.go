package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleHealthz(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.HandleHealthz(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"ok"`) {
		t.Fatalf("body = %q, want status ok", body)
	}

	// HEAD is allowed and returns no body.
	req = httptest.NewRequest(http.MethodHead, "/healthz", nil)
	w = httptest.NewRecorder()
	srv.HandleHealthz(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", w.Body.String())
	}

	// POST is rejected.
	req = httptest.NewRequest(http.MethodPost, "/healthz", nil)
	w = httptest.NewRecorder()
	srv.HandleHealthz(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
}

func TestHandleReadyz(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	srv.HandleReadyz(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"ready"`) {
		t.Fatalf("body = %q, want ready", body)
	}

	// Missing tokens → not ready.
	broken := &Server{config: &Config{}}
	w = httptest.NewRecorder()
	broken.HandleReadyz(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("broken status = %d, want 503", w.Code)
	}

	req = httptest.NewRequest(http.MethodHead, "/readyz", nil)
	w = httptest.NewRecorder()
	srv.HandleReadyz(w, req)
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body=%q", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPut, "/readyz", nil)
	w = httptest.NewRecorder()
	srv.HandleReadyz(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", w.Code)
	}
}

func TestHealthzRouteUnauthenticated(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.HandleHealthz)
	mux.HandleFunc("GET /readyz", srv.HandleReadyz)
	mux.HandleFunc("GET /metrics", srv.AuthMiddleware(srv.HandleMetrics))

	// Health probes need no credentials.
	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 without auth", path, w.Code)
		}
	}

	// Metrics requires auth (issue #98: do not advertise agent identities).
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /metrics status = %d, want 401", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated /metrics status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "aegis_hook_outcomes_total") {
		t.Fatalf("metrics body missing hook series:\n%s", body)
	}
}

func TestAuditRouteFamily(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/v1/pre-request", "hook"},
		{"/v1/admin/summary", "admin"},
		{"/v1/library/tracks", "library"},
		{"/v1/services/spotify/me", "service"},
		{"/v1/mail/search", "mail"},
		{"/v1/auth/spotify/login", "auth"},
		{"/v1/apps/token", "apps"},
		{"/healthz", "observability"},
		{"/other", "other"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if got := auditRouteFamily(req); got != tc.want {
			t.Fatalf("auditRouteFamily(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
	if got := auditRouteFamily(nil); got != "other" {
		t.Fatalf("nil request = %q, want other", got)
	}
}

func TestDeny_RecordsAuthAuditAndProm(t *testing.T) {
	dir := t.TempDir()
	audit, err := OpenAuditLog(dir+"/audit.db", 30)
	if err != nil {
		t.Fatal(err)
	}
	defer audit.Close()
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.audit = audit
	srv.prom = NewPromMetrics()

	req := httptest.NewRequest(http.MethodGet, "/v1/library/tracks", nil)
	w := httptest.NewRecorder()
	srv.deny(w, req, nil, "missing or invalid API key")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}

	rows, err := audit.List(AuditFilter{Decision: DecisionDeny})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if rows[0].Surface != MetricSurfaceAuth || rows[0].Target != "library" {
		t.Fatalf("audit row = %+v, want auth/library", rows[0])
	}
	if rows[0].Reason != "auth" || rows[0].Method != http.MethodGet {
		t.Fatalf("audit row = %+v", rows[0])
	}
	text := srv.prom.promText()
	if !strings.Contains(text, `aegis_permission_denials_total{surface="auth",target="library"} 1`) {
		t.Fatalf("metrics missing auth denial:\n%s", text)
	}
	if !strings.Contains(text, `aegis_audit_events_total{decision="deny"} 1`) {
		t.Fatalf("metrics missing audit deny counter:\n%s", text)
	}
}

func TestRecordHook_IncrementsProm(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.recordHook(HookObservation{Action: "allow"})
	srv.recordHook(HookObservation{Action: "parse_fail"})
	text := srv.prom.promText()
	if !strings.Contains(text, `aegis_hook_outcomes_total{action="allow"} 1`) {
		t.Fatalf("missing allow hook counter:\n%s", text)
	}
	if !strings.Contains(text, `aegis_hook_outcomes_total{action="parse_fail"} 1`) {
		t.Fatalf("missing parse_fail hook counter:\n%s", text)
	}
	// Nil-safe.
	var nilSrv *Server
	nilSrv.recordHook(HookObservation{Action: "allow"})
}

func TestHandleMetrics_MethodAndNilProm(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.HandleMetrics(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}

	srv.prom = nil
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w = httptest.NewRecorder()
	srv.HandleMetrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("nil prom status = %d, want 200", w.Code)
	}
}

func TestObserveUpstream_Histogram(t *testing.T) {
	p := NewPromMetrics()
	p.observeUpstream("spotify", 3*time.Millisecond, 200)
	p.observeUpstream("spotify", 300*time.Millisecond, 200)
	p.observeUpstream("spotify", 0, 0) // error path
	p.observeUpstream("", 10*time.Millisecond, 200)
	var nilP *PromMetrics
	nilP.observeUpstream("x", time.Millisecond, 200)

	text := p.promText()
	if !strings.Contains(text, `aegis_upstream_duration_seconds_count{service="spotify"} 3`) {
		t.Fatalf("missing spotify histogram count:\n%s", text)
	}
	if !strings.Contains(text, `aegis_upstream_duration_seconds_bucket{service="spotify",le="+Inf"} 3`) {
		t.Fatalf("missing +Inf bucket:\n%s", text)
	}
	if !strings.Contains(text, `aegis_upstream_duration_seconds_count{service="unknown"} 1`) {
		t.Fatalf("missing unknown service histogram:\n%s", text)
	}
}

func TestPromText_CacheAndAccessSeries(t *testing.T) {
	p := NewPromMetrics()
	p.observeAccess(AccessEvent{Surface: "library", Target: "tracks", Cache: MetricCacheHit})
	p.observeAccess(AccessEvent{Surface: "library", Target: "tracks", Cache: MetricCacheMiss})
	p.observeAccess(AccessEvent{Surface: MetricSurfaceService, Target: "spotify", Decision: DecisionDeny, Reason: "permission"})
	p.observeAccess(AccessEvent{}) // defaults

	text := p.promText()
	for _, want := range []string{
		`aegis_access_total{surface="library",target="tracks",decision="allow"} 2`,
		`aegis_cache_events_total{result="hit"} 1`,
		`aegis_cache_events_total{result="miss"} 1`,
		`aegis_permission_denials_total{surface="service",target="spotify"} 1`,
		`aegis_access_total{surface="unknown",target="unknown",decision="allow"} 1`,
		`aegis_process_start_time_seconds`,
		`aegis_process_uptime_seconds`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
	if NewPromMetrics().promText() == "" {
		t.Fatal("empty registry should still emit process series")
	}
	var nilP *PromMetrics
	if nilP.promText() != "" {
		t.Fatal("nil registry should emit empty text")
	}
}

func TestStatusCodeOrZero(t *testing.T) {
	if statusCodeOrZero(nil, nil) != 0 {
		t.Fatal("nil resp should be 0")
	}
	if statusCodeOrZero(nil, errTest) != 0 {
		t.Fatal("error should be 0")
	}
	resp := &http.Response{StatusCode: 502}
	if statusCodeOrZero(resp, nil) != 502 {
		t.Fatal("want 502")
	}
	if statusOrZero(200, errTest) != 0 || statusOrZero(200, nil) != 200 {
		t.Fatal("statusOrZero wrong")
	}
}

var errTest = errTestType{}

type errTestType struct{}

func (errTestType) Error() string { return "test error" }
