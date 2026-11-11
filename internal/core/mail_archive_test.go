package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/hookhealth"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library/links"
	"github.com/fudomunro/aegis/internal/shared"
)

func mailTestServer(t *testing.T, archive *mail.Archive) *Server {
	t.Helper()
	cfg := &Config{Services: []Service{{
		ID:     connectors.GoogleGmailServiceID,
		Name:   connectors.GoogleGmailServiceName,
		Action: "inject",
	}}}
	srv := &Server{
		apiKey:      "test-key",
		adminAPIKey: "test-key",
		authMode:    AuthModeAPIKey,
		config:      cfg,
		cache:       shared.NewCache(),
		mbCache:     shared.NewCache(),
		tokens:      mustTokens(t),
		oauthStates: shared.NewStateStore(),
		connectors:  make(map[string]connectors.ServiceConnector),
		metrics:     metrics.NewAccessMetrics(0),
		links:       mustLinks(t),
		mail:        archive,
		hookHealth:  hookhealth.NewHookHealth(hookhealth.DefaultObservationCapacity),
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

// doAdminMailReq issues an admin mail-route request (issue #311: ingest and
// status are admin; agent retrieval is the library surface).
func doAdminMailReq(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var handler http.HandlerFunc
	switch {
	case strings.HasPrefix(path, "/v1/admin/mail/status"):
		handler = srv.HandleAdminMailStatus
	case strings.HasPrefix(path, "/v1/admin/mail/import"):
		handler = srv.HandleAdminMailImport
	default:
		handler = srv.HandleAdminMailSync
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(handler)(w, req)
	return w
}

// doMessagesReq issues an agent messages-collection request.
func doMessagesReq(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("messages"))(w, req)
	return w
}

// doMessagesFetchReq issues the messages fetch subroute request.
func doMessagesFetchReq(t *testing.T, srv *Server, service, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/library/messages/"+service+"/"+id, nil)
	req.SetPathValue("service", service)
	req.SetPathValue("id", id)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/v1/library/messages/{service}/{id}"))(w, req)
	return w
}

func TestMailHTTPRequiresAuth(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("messages"))(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", w.Code)
	}
}

func TestMailHTTPQueryAndFetch(t *testing.T) {
	a := openTestMailArchive(t)
	gz, _ := mail.GzipPayload([]byte(`{"id":"m1"}`))
	rec := sampleMailRecord("m1", "Quarterly report", "The quarterly numbers are attached.")
	rec.PayloadGzip = gz
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	srv := mailTestServer(t, a)

	w := doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages?q=quarterly&include_body=1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("search code = %d body=%s", w.Code, w.Body.String())
	}
	var hits kinds.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &hits); err != nil {
		t.Fatalf("search json: %v", err)
	}
	if hits.Total != 1 || hits.Messages[0].Body == "" {
		t.Fatalf("hits = %+v", hits)
	}

	// Without include_body the list carries metadata only (issue #311).
	w = doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages?q=quarterly", "")
	if w.Code != http.StatusOK {
		t.Fatalf("search code = %d body=%s", w.Code, w.Body.String())
	}
	hits = kinds.MessageList{}
	if err := json.Unmarshal(w.Body.Bytes(), &hits); err != nil {
		t.Fatalf("search json: %v", err)
	}
	if hits.Total != 1 || hits.Messages[0].Body != "" {
		t.Fatalf("stripped hits = %+v", hits)
	}

	w = doMessagesFetchReq(t, srv, connectors.GoogleGmailServiceID, "m1")
	if w.Code != http.StatusOK {
		t.Fatalf("get code = %d body=%s", w.Code, w.Body.String())
	}
	var msg kinds.Message
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("get json: %v", err)
	}
	if msg.Subject != "Quarterly report" || msg.Body == "" {
		t.Fatalf("msg = %+v", msg)
	}

	w = doMessagesFetchReq(t, srv, connectors.GoogleGmailServiceID, "missing")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing code = %d", w.Code)
	}

	w = doMessagesReq(t, srv, http.MethodPost, "/v1/library/messages", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("search post code = %d", w.Code)
	}
}

func TestMailHTTPDisabledAndDenied(t *testing.T) {
	srv := mailTestServer(t, nil)
	w := doMessagesFetchReq(t, srv, connectors.GoogleGmailServiceID, "m1")
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled code = %d body=%s", w.Code, w.Body.String())
	}

	a := openTestMailArchive(t)
	if err := a.Upsert(sampleMailRecord("m1", "Quarterly report", "hello")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	srv = mailTestServer(t, a)
	srv.config.typePolicies = policy.DataGrants{"messages": {Default: shared.AccessNone}}
	w = doMessagesFetchReq(t, srv, connectors.GoogleGmailServiceID, "m1")
	if w.Code != http.StatusForbidden {
		t.Fatalf("denied code = %d body=%s", w.Code, w.Body.String())
	}
	w = doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("list denied code = %d body=%s", w.Code, w.Body.String())
	}
}

func TestMailHTTPSyncRequiresConnector(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	w := doAdminMailReq(t, srv, http.MethodPost, "/v1/admin/mail/sync", `{"service":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}

	srv.connectors["dummy"] = &stubNoSyncConnector{}
	srv.config.Services = append(srv.config.Services, Service{
		ID:     "dummy",
		Name:   "Dummy",
		Action: "inject",
	})
	w = doAdminMailReq(t, srv, http.MethodPost, "/v1/admin/mail/sync", `{"service":"dummy","reset_cursor":true}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminMailStatus(t *testing.T) {
	a := openTestMailArchive(t)
	if err := a.Upsert(sampleMailRecord("m1", "Quarterly report", "hello")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	srv := mailTestServer(t, a)
	w := doAdminMailReq(t, srv, http.MethodGet, "/v1/admin/mail/status", "")
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
}

type stubNoSyncConnector struct{}

func (s *stubNoSyncConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}

func (s *stubNoSyncConnector) SystemInstructions(string) string { return "" }

func (s *stubNoSyncConnector) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func TestMailArchiveHints(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	req := httptest.NewRequest(http.MethodGet, "/v1/library/messages", nil)
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
	// The hint names the one retrieval surface (issue #311).
	for _, hint := range srv.mailArchiveHints(req, "search my email archive") {
		if strings.Contains(hint, "/v1/mail/") {
			t.Fatalf("hint still names retired routes: %s", hint)
		}
		if !strings.Contains(hint, "/v1/library/messages") {
			t.Fatalf("hint missing library path: %s", hint)
		}
	}
	srv.mail = nil
	if hints := srv.mailArchiveHints(req, "search my email archive"); len(hints) != 0 {
		t.Fatalf("disabled archive should not hint: %v", hints)
	}
}
