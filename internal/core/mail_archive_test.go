package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/links"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

func mailTestServer(t *testing.T, archive *mail.Archive) *Server {
	t.Helper()
	cfg := &Config{Services: []Service{{
		ID:          connectors.GoogleGmailServiceID,
		Name:        connectors.GoogleGmailServiceName,
		Action:      "inject",
		GrantLimits: types.Permissions{"messages": "read"},
	}}}
	srv := &Server{
		apiKey:      "test-key",
		adminAPIKey: "test-key",
		authMode:    AuthModeAPIKey,
		config:      cfg,
		cache:       NewCache(),
		mbCache:     NewCache(),
		tokens:      mustTokens(t),
		oauthStates: shared.NewStateStore(),
		connectors:  make(map[string]connectors.ServiceConnector),
		metrics:     NewAccessMetrics(defaultMetricsCapacity),
		links:       mustLinks(t),
		mail:        archive,
		hookHealth:  NewHookHealth(defaultHookObservationCapacity),
	}
	srv.connectors[connectors.GoogleGmailServiceID] = connectors.NewGoogleGmailConnector(srv)
	srv.wireMailIngests()
	return srv
}

func mustTokens(t *testing.T) *tokens.TokenStore {
	t.Helper()
	ts, err := newTestTokenStore("", "pass")
	if err != nil {
		t.Fatalf("token store: %v", err)
	}
	return ts
}

func mustLinks(t *testing.T) *links.Store {
	t.Helper()
	st, err := links.NewStore("")
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	return st
}

func doMailReq(t *testing.T, srv *Server, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.handleMailRoutes)(w, req)
	return w
}

func TestMailHTTPRequiresAuth(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	req := httptest.NewRequest(http.MethodGet, "/v1/mail/status", nil)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.handleMailRoutes)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", w.Code)
	}
}

func TestMailHTTPSearchGetSyncStatus(t *testing.T) {
	a := openTestMailArchive(t)
	if err := a.Upsert(sampleMailRecord("m1", "Quarterly report", "The quarterly numbers are attached.")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	gz, _ := mail.GzipPayload([]byte(`{"id":"m1"}`))
	rec := sampleMailRecord("m1", "Quarterly report", "The quarterly numbers are attached.")
	rec.PayloadGzip = gz
	_ = a.Upsert(rec)

	srv := mailTestServer(t, a)

	w := doMailReq(t, srv, http.MethodGet, "/v1/mail/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", w.Code, w.Body.String())
	}
	var st mail.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if st.MessageCount != 1 {
		t.Fatalf("status = %+v", st)
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/search?q=quarterly&include_body=1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("search code = %d body=%s", w.Code, w.Body.String())
	}
	var hits mail.SearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &hits); err != nil {
		t.Fatalf("search json: %v", err)
	}
	if hits.Total != 1 || hits.Messages[0].Body == "" {
		t.Fatalf("hits = %+v", hits)
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/messages/"+connectors.GoogleGmailServiceID+"/m1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get code = %d body=%s", w.Code, w.Body.String())
	}
	var msg types.Message
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("get json: %v", err)
	}
	if msg.Subject != "Quarterly report" {
		t.Fatalf("msg = %+v", msg)
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/messages/"+connectors.GoogleGmailServiceID+"/m1/raw", "")
	if w.Code != http.StatusOK {
		t.Fatalf("raw code = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "m1") {
		t.Fatalf("raw = %s", w.Body.String())
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/messages/"+connectors.GoogleGmailServiceID+"/missing", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing code = %d", w.Code)
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/nope", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown route code = %d", w.Code)
	}

	w = doMailReq(t, srv, http.MethodPost, "/v1/mail/search", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("search post code = %d", w.Code)
	}
}

func TestMailHTTPDisabledAndDenied(t *testing.T) {
	srv := mailTestServer(t, nil)
	w := doMailReq(t, srv, http.MethodGet, "/v1/mail/status", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled code = %d body=%s", w.Code, w.Body.String())
	}

	a := openTestMailArchive(t)
	srv = mailTestServer(t, a)
	srv.config.Services[0].GrantLimits = types.Permissions{"messages": "none"}
	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/status", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("denied code = %d", w.Code)
	}
}

func TestMailHTTPSyncRequiresConnector(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	w := doMailReq(t, srv, http.MethodPost, "/v1/mail/sync", `{"service":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}

	srv.connectors["dummy"] = &stubNoSyncConnector{}
	srv.config.Services = append(srv.config.Services, Service{
		ID:          "dummy",
		Name:        "Dummy",
		Action:      "inject",
		GrantLimits: types.Permissions{"messages": "read"},
	})
	w = doMailReq(t, srv, http.MethodPost, "/v1/mail/sync", `{"service":"dummy","reset_cursor":true}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
}

type stubNoSyncConnector struct{}

func (s *stubNoSyncConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}

func (s *stubNoSyncConnector) SystemInstructions(string) string { return "" }

func (s *stubNoSyncConnector) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func TestMailArchiveHints(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	req := httptest.NewRequest(http.MethodGet, "/v1/mail/search", nil)
	if hints := srv.mailArchiveHints(req, "search my email archive for invoices"); len(hints) == 0 {
		t.Fatal("expected archive hints")
	}
	if hints := srv.mailArchiveHints(req, "what is the oldest available message?"); len(hints) == 0 {
		t.Fatal("expected archive hints for oldest-message questions")
	}
	if hints := srv.mailArchiveHints(req, "play some jazz"); len(hints) != 0 {
		t.Fatalf("unexpected hints: %v", hints)
	}
	// Finding a specific message should get search-first guidance.
	for _, msg := range []string{
		"find my email about the invoice",
		"where is the mail from last week?",
		"look up the email about baptism",
	} {
		if hints := srv.mailArchiveHints(req, msg); len(hints) == 0 {
			t.Fatalf("expected archive hints for %q", msg)
		}
	}
	srv.mail = nil
	if hints := srv.mailArchiveHints(req, "search my email archive"); len(hints) != 0 {
		t.Fatalf("disabled archive should not hint: %v", hints)
	}
}
