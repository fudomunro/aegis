package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestGmailSyncMailArchive(t *testing.T) {
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
	srv.config.GetService(googleGmailServiceID).Endpoint = up.URL
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	conn := NewGoogleGmailConnector(srv)
	res, err := conn.SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 2})
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
	res2, err := conn.SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 10})
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
	res3, err := conn.SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 2, ResetCursor: true})
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
	conn := NewGoogleGmailConnector(srv)
	res, err := conn.SyncMailArchive(context.Background(), a, MailSyncOptions{})
	if err != ErrUnauthorized {
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
	srv.config.GetService(googleGmailServiceID).Endpoint = up.URL
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	conn := NewGoogleGmailConnector(srv)
	res, err := conn.SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 5})
	if err == nil {
		t.Fatal("expected list error")
	}
	if res.Error == "" || res.Stored != 0 {
		t.Fatalf("result = %+v", res)
	}
	cur, _ := a.LoadSyncCursor(googleGmailServiceID)
	if cur.Error == "" {
		t.Fatal("expected cursor error to be recorded")
	}
}

func TestGmailSyncSkipsBadMessage(t *testing.T) {
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
	srv.config.GetService(googleGmailServiceID).Endpoint = up.URL
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	conn := NewGoogleGmailConnector(srv)
	res, err := conn.SyncMailArchive(context.Background(), a, MailSyncOptions{MaxMessages: 10})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Stored != 1 || res.Failed != 1 || !res.Done {
		t.Fatalf("result = %+v", res)
	}
}

func TestMailHTTPSyncRunsIngest(t *testing.T) {
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
	srv.config.GetService(googleGmailServiceID).Endpoint = up.URL
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

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
