package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// gmailPayloadJSON builds a minimal format=full Gmail message body.
func gmailPayloadJSON(id string) string {
	subject := "Hello " + id
	body := base64.RawURLEncoding.EncodeToString([]byte("body of " + id))
	return fmt.Sprintf(`{
		"id": %q,
		"threadId": "thread-1",
		"labelIds": ["INBOX", "UNREAD"],
		"snippet": "body of %s",
		"internalDate": "1717232400000",
		"payload": {
			"headers": [
				{"name":"From","value":"Ada <ada@example.com>"},
				{"name":"To","value":"me@example.com"},
				{"name":"Subject","value":%q},
				{"name":"Message-ID","value":"<%s@example.com>"}
			],
			"body": {"data": %q}
		}
	}`, id, id, subject, id, body)
}

func disableGmailArchivePace(t *testing.T) {
	t.Helper()
	prev := gmailArchiveGetInterval
	gmailArchiveGetInterval = 0
	t.Cleanup(func() { gmailArchiveGetInterval = prev })
}

func TestGmailSyncMailArchive(t *testing.T) {
	disableGmailArchivePace(t)
	var listCalls, getCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		listCalls++
		q := r.URL.Query().Get("q")
		if q != "in:anywhere" {
			t.Errorf("query = %q", q)
		}
		if r.URL.Query().Get("pageToken") == "page-2" {
			json.NewEncoder(w).Encode(map[string]any{
				"messages": []map[string]string{{"id": "m3", "threadId": "thread-1"}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{
				{"id": "m1", "threadId": "thread-1"},
				{"id": "m2", "threadId": "thread-1"},
			},
			"nextPageToken": "page-2",
		})
	})
	mux.HandleFunc("/users/me/messages/", func(w http.ResponseWriter, r *http.Request) {
		getCalls++
		id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
		w.Write([]byte(gmailPayloadJSON(id)))
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)

	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv.tokens.Save(connectors.GoogleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGoogleGmailConnector(srv)
	res, err := (&gmailArchiveSyncer{GoogleGmailConnector: conn, host: srv}).SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 2})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Stored != 2 || res.Done || !res.Truncated {
		t.Fatalf("first result = %+v", res)
	}
	if getCalls < 2 {
		t.Fatalf("getCalls = %d", getCalls)
	}

	// Continue from cursor — second batch finishes the archive.
	res2, err := (&gmailArchiveSyncer{GoogleGmailConnector: conn, host: srv}).SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 10})
	if err != nil {
		t.Fatalf("sync2: %v", err)
	}
	if res2.Stored != 1 || !res2.Done {
		t.Fatalf("second result = %+v", res2)
	}

	st, err := a.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.MessageCount != 3 {
		t.Fatalf("count = %d", st.MessageCount)
	}
	hits, err := a.Search(MailSearchQuery{Q: "body", WithBody: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if hits.Total != 3 {
		t.Fatalf("fts total = %d", hits.Total)
	}

	// Reset cursor re-ingests from the first page.
	res3, err := (&gmailArchiveSyncer{GoogleGmailConnector: conn, host: srv}).SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 2, ResetCursor: true})
	if err != nil {
		t.Fatalf("sync3: %v", err)
	}
	if res3.Stored != 2 {
		t.Fatalf("reset result = %+v", res3)
	}
}

func TestGmailSyncUnauthorized(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	// No token in the store.
	conn := connectors.NewGoogleGmailConnector(srv)
	res, err := (&gmailArchiveSyncer{GoogleGmailConnector: conn, host: srv}).SyncMailArchive(context.Background(), a, MailSyncOptions{})
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected error on result")
	}
}

func TestGmailSyncListErrorIsFailOpen(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(up.Close)

	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv.tokens.Save(connectors.GoogleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGoogleGmailConnector(srv)
	res, err := (&gmailArchiveSyncer{GoogleGmailConnector: conn, host: srv}).SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 5})
	if err == nil {
		t.Fatal("expected list error")
	}
	if res.Error == "" || res.Stored != 0 {
		t.Fatalf("result = %+v", res)
	}
	cur, _ := a.LoadSyncCursor(connectors.GoogleGmailServiceID)
	if cur.Error == "" {
		t.Fatal("expected cursor error to be recorded")
	}
}

func TestGmailSyncSkipsBadMessage(t *testing.T) {
	disableGmailArchivePace(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{
				{"id": "good"},
				{"id": "bad"},
			},
		})
	})
	mux.HandleFunc("/users/me/messages/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
		if id == "bad" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(gmailPayloadJSON(id)))
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)

	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv.tokens.Save(connectors.GoogleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGoogleGmailConnector(srv)
	res, err := (&gmailArchiveSyncer{GoogleGmailConnector: conn, host: srv}).SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 10})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Stored != 1 || res.Failed != 1 || !res.Done {
		t.Fatalf("result = %+v", res)
	}
}

func TestMailHTTPSyncRunsIngest(t *testing.T) {
	disableGmailArchivePace(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/users/me/messages/") {
			w.Write([]byte(gmailPayloadJSON("m1")))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{{"id": "m1"}},
		})
	}))
	t.Cleanup(up.Close)

	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv.tokens.Save(connectors.GoogleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	w := doMailReq(t, srv, http.MethodPost, "/v1/mail/sync", `{"max_messages":5,"reset_cursor":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	var res MailSyncResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("json: %v", err)
	}
	if res.Stored != 1 || !res.Done {
		t.Fatalf("res = %+v", res)
	}

	// Invalid JSON body is a client error.
	w = doMailReq(t, srv, http.MethodPost, "/v1/mail/sync", `{`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad json code = %d", w.Code)
	}
}

// Quota 403s must pause the batch and leave the page token on the unsaved
// page so hydrates are retried after the window resets (not skipped forever).
func TestGmailSyncQuotaPausesWithoutSkippingPage(t *testing.T) {
	disableGmailArchivePace(t)
	quotaMode := true
	var getCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{
				{"id": "m1"},
				{"id": "m2"},
				{"id": "m3"},
			},
			"nextPageToken": "page-2",
		})
	})
	mux.HandleFunc("/users/me/messages/", func(w http.ResponseWriter, r *http.Request) {
		getCalls++
		id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
		if quotaMode && getCalls >= 2 {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"message":"Quota exceeded for quota metric 'Total Query Cost'"}}`))
			return
		}
		w.Write([]byte(gmailPayloadJSON(id)))
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)

	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv.tokens.Save(connectors.GoogleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGoogleGmailConnector(srv)
	res, err := (&gmailArchiveSyncer{GoogleGmailConnector: conn, host: srv}).SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 10})
	if err == nil {
		t.Fatal("expected quota error")
	}
	if !isGmailQuotaError(err) {
		t.Fatalf("err = %v", err)
	}
	if res.Stored != 1 || res.Failed != 0 || !res.Truncated {
		t.Fatalf("result = %+v", res)
	}
	cur, err := a.LoadSyncCursor(connectors.GoogleGmailServiceID)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cur.PageToken != "" || cur.Done {
		t.Fatalf("cursor must stay on first page for retry: %+v", cur)
	}

	// Next call resumes the same page and stores the rest once quota clears.
	quotaMode = false
	getCalls = 0
	res2, err := (&gmailArchiveSyncer{GoogleGmailConnector: conn, host: srv}).SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 10})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res2.Stored < 2 {
		t.Fatalf("resume result = %+v", res2)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.ByService[connectors.GoogleGmailServiceID] != 3 {
		t.Fatalf("by_service = %+v", st.ByService)
	}
}
