package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func openTestMailArchive(t *testing.T) *MailArchive {
	t.Helper()
	a, err := OpenMailArchive(filepath.Join(t.TempDir(), "mail-archive.db"))
	if err != nil {
		t.Fatalf("open mail archive: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func sampleMailRecord(id, subject, body string) MailArchiveRecord {
	return MailArchiveRecord{
		Service:     googleGmailServiceID,
		ServiceName: googleGmailServiceName,
		ID:          id,
		ThreadID:    "t-" + id,
		MessageID:   "<" + id + "@example.com>",
		Mailbox:     "INBOX",
		Labels:      []string{"INBOX", "UNREAD"},
		Subject:     subject,
		Snippet:     body[:minInt(20, len(body))],
		FromName:    "Ada Lovelace",
		FromEmail:   "ada@example.com",
		To:          []Contact{newContact(googleGmailServiceID, googleGmailServiceName, "", "Me", []string{"me@example.com"}, nil)},
		SentAt:      "2024-06-01T09:00:00Z",
		ReceivedAt:  "2024-06-01T09:00:05Z",
		Direction:   "inbound",
		Read:        false,
		BodyText:    body,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestMailArchiveDisabled(t *testing.T) {
	a, err := OpenMailArchive("")
	if err != nil || a != nil {
		t.Fatalf("empty path should disable archive, got %v %v", a, err)
	}
	if err := a.Upsert(MailArchiveRecord{Service: "x", ID: "1"}); err == nil {
		t.Fatal("expected disabled upsert error")
	}
	if _, err := a.Get("x", "1"); err == nil {
		t.Fatal("expected disabled get error")
	}
	if _, err := a.Search(MailSearchQuery{}); err == nil {
		t.Fatal("expected disabled search error")
	}
}

func TestMailArchiveUpsertGetSearch(t *testing.T) {
	a := openTestMailArchive(t)
	payload := []byte(`{"id":"m1","subject":"Invoice due"}`)
	gz, err := GzipPayload(payload)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	rec := sampleMailRecord("m1", "Invoice due", "Please pay the June invoice for widgets.")
	rec.PayloadGzip = gz
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Second upsert replaces metadata but keeps payload when omitted.
	rec.Subject = "Invoice due (updated)"
	rec.PayloadGzip = nil
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert replace: %v", err)
	}
	got, err := a.Get(googleGmailServiceID, "m1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Subject != "Invoice due (updated)" {
		t.Fatalf("subject = %q", got.Subject)
	}
	if len(got.PayloadGzip) == 0 {
		t.Fatal("expected payload to be retained")
	}
	raw, err := got.Payload()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if !strings.Contains(string(raw), "Invoice due") {
		t.Fatalf("payload = %s", raw)
	}

	if err := a.Upsert(sampleMailRecord("m2", "Lunch plans", "Want to grab tacos tomorrow?")); err != nil {
		t.Fatalf("upsert m2: %v", err)
	}

	hits, err := a.Search(MailSearchQuery{Q: "invoice", WithBody: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if hits.Total != 1 || len(hits.Messages) != 1 {
		t.Fatalf("fts hits = %+v", hits)
	}
	if !strings.Contains(hits.Messages[0].Body, "widgets") {
		t.Fatalf("body missing: %+v", hits.Messages[0])
	}

	byFrom, err := a.Search(MailSearchQuery{From: "ada@example.com"})
	if err != nil {
		t.Fatalf("search from: %v", err)
	}
	if byFrom.Total != 2 {
		t.Fatalf("from total = %d", byFrom.Total)
	}

	byTo, err := a.Search(MailSearchQuery{To: "me@example.com", After: "2024-06-01", Before: "2024-06-02"})
	if err != nil {
		t.Fatalf("search to/date: %v", err)
	}
	if byTo.Total != 2 {
		t.Fatalf("to/date total = %d", byTo.Total)
	}

	byLabel, err := a.Search(MailSearchQuery{Labels: []string{"INBOX"}})
	if err != nil {
		t.Fatalf("search label: %v", err)
	}
	if byLabel.Total != 2 {
		t.Fatalf("label total = %d", byLabel.Total)
	}

	// FTS operator characters must not error the query.
	if _, err := a.Search(MailSearchQuery{Q: `NEAR(a b) OR "x*"`}); err != nil {
		t.Fatalf("hostile fts query: %v", err)
	}

	st, err := a.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Enabled || st.MessageCount != 2 || st.ByService[googleGmailServiceID] != 2 {
		t.Fatalf("status = %+v", st)
	}
}

func TestMailArchiveSyncCursor(t *testing.T) {
	a := openTestMailArchive(t)
	if err := a.SaveSyncCursor(googleGmailServiceID, MailSyncCursor{
		PageToken:  "tok1",
		LastSyncAt: "2024-06-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	cur, err := a.LoadSyncCursor(googleGmailServiceID)
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if cur.PageToken != "tok1" || cur.Done || cur.LastSyncAt != "2024-06-01T00:00:00Z" {
		t.Fatalf("cursor = %+v", cur)
	}
	if err := a.SaveSyncCursor(googleGmailServiceID, MailSyncCursor{PageToken: "tok1", Done: true}); err != nil {
		t.Fatalf("save done: %v", err)
	}
	cur, _ = a.LoadSyncCursor(googleGmailServiceID)
	if !cur.Done {
		t.Fatal("expected done cursor")
	}
}

func TestRecordFromMessage(t *testing.T) {
	msg := newMessage(googleGmailServiceID, googleGmailServiceName, "id9", MessageChannelEmail, "Hello", "hi", "2024-06-01T09:00:00Z")
	msg.From = &Contact{Name: "Ada", Emails: []string{"Ada@Example.com"}}
	msg.EntityID = "<id9@example.com>"
	msg.Body = "body text"
	rec := RecordFromMessage("", "", msg, []byte{1})
	if rec.Service != googleGmailServiceID || rec.ID != "id9" || rec.FromEmail != "Ada@Example.com" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.MessageID != "<id9@example.com>" || rec.BodyText != "body text" {
		t.Fatalf("rec meta = %+v", rec)
	}
}

func mailTestServer(t *testing.T, archive *MailArchive) *Server {
	t.Helper()
	cfg := &Config{Services: []Service{{
		ID:          googleGmailServiceID,
		Name:        googleGmailServiceName,
		Action:      "inject",
		Permissions: Permissions{"messages": "read"},
	}}}
	srv := &Server{
		apiKey:      "test-key",
		authMode:    AuthModeAPIKey,
		config:      cfg,
		cache:       NewCache(),
		tokens:      mustTokens(t),
		oauthStates: NewStateStore(),
		connectors:  make(map[string]ServiceConnector),
		metrics:     NewAccessMetrics(defaultMetricsCapacity),
		links:       mustLinks(t),
		mail:        archive,
	}
	srv.connectors[googleGmailServiceID] = NewGoogleGmailConnector(srv)
	return srv
}

func mustTokens(t *testing.T) *TokenStore {
	t.Helper()
	ts, err := NewTokenStore("", "pass")
	if err != nil {
		t.Fatalf("token store: %v", err)
	}
	return ts
}

func mustLinks(t *testing.T) *EntityLinkStore {
	t.Helper()
	st, err := NewEntityLinkStore("")
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
	gz, _ := GzipPayload([]byte(`{"id":"m1"}`))
	rec := sampleMailRecord("m1", "Quarterly report", "The quarterly numbers are attached.")
	rec.PayloadGzip = gz
	_ = a.Upsert(rec)

	srv := mailTestServer(t, a)

	w := doMailReq(t, srv, http.MethodGet, "/v1/mail/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", w.Code, w.Body.String())
	}
	var st MailArchiveStatus
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
	var hits MailSearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &hits); err != nil {
		t.Fatalf("search json: %v", err)
	}
	if hits.Total != 1 || hits.Messages[0].Body == "" {
		t.Fatalf("hits = %+v", hits)
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/messages/"+googleGmailServiceID+"/m1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get code = %d body=%s", w.Code, w.Body.String())
	}
	var msg Message
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("get json: %v", err)
	}
	if msg.Subject != "Quarterly report" {
		t.Fatalf("msg = %+v", msg)
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/messages/"+googleGmailServiceID+"/m1/raw", "")
	if w.Code != http.StatusOK {
		t.Fatalf("raw code = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "m1") {
		t.Fatalf("raw = %s", w.Body.String())
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/messages/"+googleGmailServiceID+"/missing", "")
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
	srv.config.Services[0].Permissions = Permissions{"messages": "none"}
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
		Permissions: Permissions{"messages": "read"},
	})
	w = doMailReq(t, srv, http.MethodPost, "/v1/mail/sync", `{"service":"dummy","reset_cursor":true}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
}

type stubNoSyncConnector struct{}

func (s *stubNoSyncConnector) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (s *stubNoSyncConnector) SystemInstructions(string) string  { return "" }
func (s *stubNoSyncConnector) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func TestMailArchiveHints(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	if hints := srv.mailArchiveHints("search my email archive for invoices"); len(hints) == 0 {
		t.Fatal("expected archive hints")
	}
	if hints := srv.mailArchiveHints("play some jazz"); len(hints) != 0 {
		t.Fatalf("unexpected hints: %v", hints)
	}
	srv.mail = nil
	if hints := srv.mailArchiveHints("search my email archive"); len(hints) != 0 {
		t.Fatalf("disabled archive should not hint: %v", hints)
	}
}
