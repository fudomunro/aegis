package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestGoogleGmailConnector_ListMessages_MockAPI(t *testing.T) {
	msgIDBody := base64.RawURLEncoding.EncodeToString([]byte("Hello from Ada"))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/users/me/messages":
			q := r.URL.Query()
			if q.Get("q") == "" || q.Get("maxResults") == "" {
				t.Errorf("missing list params: %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"messages": []map[string]any{
					{"id": "m1", "threadId": "t1"},
					{"id": "m2", "threadId": "t2"},
				},
			})
		case r.URL.Path == "/users/me/messages/m1":
			json.NewEncoder(w).Encode(map[string]any{
				"id":       "m1",
				"threadId": "t1",
				"labelIds": []string{"INBOX", "UNREAD"},
				"snippet":  "Can we meet tomorrow?",
				// 2024-06-01T09:00:00Z
				"internalDate": "1717232400000",
				"payload": map[string]any{
					"headers": []map[string]any{
						{"name": "From", "value": "Ada Lovelace <ada@example.com>"},
						{"name": "To", "value": "grace@example.com"},
						{"name": "Cc", "value": "team@example.com"},
						{"name": "Subject", "value": "Meeting"},
						{"name": "Message-ID", "value": "<meet@example.com>"},
						{"name": "Date", "value": "Sat, 01 Jun 2024 09:00:00 +0000"},
					},
				},
			})
		case r.URL.Path == "/users/me/messages/m2":
			json.NewEncoder(w).Encode(map[string]any{
				"id":       "m2",
				"threadId": "t2",
				"labelIds": []string{"SENT"},
				"snippet":  "Hello from Ada",
				"payload": map[string]any{
					"headers": []map[string]any{
						{"name": "From", "value": "me@example.com"},
						{"name": "To", "value": "ada@example.com"},
						{"name": "Subject", "value": "Re: Meeting"},
						{"name": "Message-ID", "value": "<reply@example.com>"},
					},
					"body": map[string]any{"data": msgIDBody},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID:       googleGmailServiceID,
		Name:     googleGmailServiceName,
		Action:   "inject",
		Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})

	conn := NewGoogleGmailConnector(srv)
	messages, truncated, err := conn.ListMessages(context.Background())
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	inbox := messages[0]
	if inbox.Subject != "Meeting" || inbox.Channel != types.MessageChannelEmail {
		t.Fatalf("inbox = %+v", inbox)
	}
	if inbox.EntityID != "meet@example.com" || inbox.ThreadID != "t1" {
		t.Fatalf("identity = %+v", inbox)
	}
	if inbox.Read || inbox.Direction != "inbound" {
		t.Fatalf("flags = %+v", inbox)
	}
	if inbox.From == nil || len(inbox.From.Emails) != 1 || inbox.From.Emails[0] != "ada@example.com" {
		t.Fatalf("from = %+v", inbox.From)
	}
	if len(inbox.To) != 1 || inbox.To[0].Emails[0] != "grace@example.com" {
		t.Fatalf("to = %+v", inbox.To)
	}
	if len(inbox.CC) != 1 || inbox.CC[0].Emails[0] != "team@example.com" {
		t.Fatalf("cc = %+v", inbox.CC)
	}
	if inbox.SentAt != "2024-06-01T09:00:00Z" {
		t.Fatalf("sent_at = %q", inbox.SentAt)
	}
	if len(inbox.Sources) != 1 || inbox.Sources[0].Service != googleGmailServiceID || inbox.Sources[0].ID != "m1" {
		t.Fatalf("sources = %+v", inbox.Sources)
	}
	if inbox.Sources[0].Mailbox != "INBOX" || !inbox.Sources[0].Canonical {
		t.Fatalf("source flags = %+v", inbox.Sources[0])
	}
	sent := messages[1]
	if !sent.Read || sent.Direction != "outbound" || sent.Subject != "Re: Meeting" {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestGoogleGmailConnector_ListMessages_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: googleGmailServiceID, Name: googleGmailServiceName, Action: "inject",
	}}}, "https://aegis.example.com")
	conn := NewGoogleGmailConnector(srv)
	_, _, err := conn.ListMessages(context.Background())
	if !errors.Is(err, types.ErrUnauthorized) {
		t.Fatalf("err = %v, want types.ErrUnauthorized", err)
	}
}

func TestGoogleGmailConnector_ListMessages_HydrateCapTruncates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/users/me/messages" {
			items := make([]map[string]any, 0, 10)
			for i := 0; i < 10; i++ {
				items = append(items, map[string]any{"id": fmt.Sprintf("id%d", i), "threadId": "t"})
			}
			json.NewEncoder(w).Encode(map[string]any{
				"messages":      items,
				"nextPageToken": "more",
			})
			return
		}
		// metadata endpoints
		id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
		json.NewEncoder(w).Encode(map[string]any{
			"id":           id,
			"labelIds":     []string{"INBOX"},
			"internalDate": "1717232400000",
			"payload": map[string]any{
				"headers": []map[string]any{
					{"name": "From", "value": "a@example.com"},
					{"name": "Subject", "value": "S " + id},
					{"name": "Message-ID", "value": "<" + id + "@x>"},
				},
			},
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: googleGmailServiceID, Name: googleGmailServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})

	conn := NewGoogleGmailConnector(srv)
	messages, truncated, err := conn.ListMessages(context.Background())
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if !truncated {
		t.Fatal("expected truncated after hydrate cap")
	}
	if len(messages) != googleGmailLibraryCap {
		t.Fatalf("got %d messages, want %d", len(messages), googleGmailLibraryCap)
	}
}

func TestGoogleGmailConnector_HandleProxy_ListMessages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users/me/messages":
			json.NewEncoder(w).Encode(map[string]any{
				"messages": []map[string]any{{"id": "m9", "threadId": "t9"}},
			})
		case "/users/me/messages/m9":
			json.NewEncoder(w).Encode(map[string]any{
				"id":       "m9",
				"threadId": "t9",
				"labelIds": []string{"INBOX"},
				"snippet":  "hello",
				"payload": map[string]any{
					"headers": []map[string]any{
						{"name": "From", "value": "ada@example.com"},
						{"name": "Subject", "value": "Ping"},
						{"name": "Message-ID", "value": "<ping@x>"},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: googleGmailServiceID, Name: googleGmailServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := NewGoogleGmailConnector(srv)

	req := httptest.NewRequest("GET", "/me/messages", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/messages")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || resp.Messages[0].Subject != "Ping" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestGoogleGmailConnector_HandleProxy_GetMessageFull(t *testing.T) {
	body := base64.RawURLEncoding.EncodeToString([]byte("Full body text"))
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":       "abc",
			"threadId": "th1",
			"labelIds": []string{"INBOX"},
			"payload": map[string]any{
				"headers": []map[string]any{
					{"name": "From", "value": "ada@example.com"},
					{"name": "Subject", "value": "Deep"},
					{"name": "Message-ID", "value": "<deep@x>"},
				},
				"body": map[string]any{"data": body},
			},
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: googleGmailServiceID, Name: googleGmailServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := NewGoogleGmailConnector(srv)

	req := httptest.NewRequest("GET", "/messages/abc?format=full", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/messages/abc")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(gotPath, "/users/me/messages/abc") || !strings.Contains(gotPath, "format=full") {
		t.Fatalf("upstream = %q", gotPath)
	}
	var msg types.Message
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Subject != "Deep" || msg.Body != "Full body text" {
		t.Fatalf("msg = %+v", msg)
	}
}

func TestGoogleGmailConnector_HandleProxy_ListAndGetThread(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/users/me/threads":
			json.NewEncoder(w).Encode(map[string]any{
				"threads": []map[string]any{{"id": "th1", "snippet": "hello"}},
			})
		case r.URL.Path == "/users/me/threads/th1":
			json.NewEncoder(w).Encode(map[string]any{
				"id":      "th1",
				"snippet": "hello",
				"messages": []map[string]any{
					{
						"id":       "m1",
						"threadId": "th1",
						"labelIds": []string{"INBOX"},
						"payload": map[string]any{
							"headers": []map[string]any{
								{"name": "From", "value": "ada@example.com"},
								{"name": "Subject", "value": "Thread topic"},
								{"name": "Message-ID", "value": "<th1m1@x>"},
							},
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: googleGmailServiceID, Name: googleGmailServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := NewGoogleGmailConnector(srv)

	req := httptest.NewRequest("GET", "/me/threads", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/threads")
	if w.Code != http.StatusOK {
		t.Fatalf("threads status = %d body=%s", w.Code, w.Body.String())
	}
	var threadsResp struct {
		Total   int              `json:"total"`
		Threads []map[string]any `json:"threads"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &threadsResp); err != nil {
		t.Fatalf("decode threads: %v", err)
	}
	if threadsResp.Total != 1 {
		t.Fatalf("threads = %+v", threadsResp)
	}

	req = httptest.NewRequest("GET", "/threads/th1", nil)
	w = httptest.NewRecorder()
	conn.HandleProxy(w, req, "/threads/th1")
	if w.Code != http.StatusOK {
		t.Fatalf("thread get status = %d body=%s", w.Code, w.Body.String())
	}
	var threadResp struct {
		ID       string          `json:"id"`
		Total    int             `json:"total"`
		Messages []types.Message `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &threadResp); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	if threadResp.ID != "th1" || threadResp.Total != 1 || threadResp.Messages[0].Subject != "Thread topic" {
		t.Fatalf("thread = %+v", threadResp)
	}
}

func TestGoogleGmailConnector_HandleProxy_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: googleGmailServiceID, Name: googleGmailServiceName, Action: "inject",
	}}}, "https://aegis.example.com")
	srv.tokens.Save(googleGmailServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := NewGoogleGmailConnector(srv)

	req := httptest.NewRequest("POST", "/me/messages", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/messages")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestGoogleGmailConnector_HandleProxy_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: googleGmailServiceID, Name: googleGmailServiceName, Action: "inject",
	}}}, "https://aegis.example.com")
	conn := NewGoogleGmailConnector(srv)
	req := httptest.NewRequest("GET", "/me/messages", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/messages")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestGoogleGmailConnector_SystemInstructions_MentionLibrary(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := NewGoogleGmailConnector(srv)
	instr := conn.SystemInstructions("https://aegis.example.com")
	for _, want := range []string{
		"/v1/library/messages",
		"/me/messages",
		"/messages/{id}",
		"Gmail",
		"Aegis authenticates you",
		"Message-ID",
	} {
		if !strings.Contains(instr, want) {
			t.Fatalf("missing %q in instructions:\n%s", want, instr)
		}
	}
}

func TestGoogleGmailConnector_Capabilities_ReadOnlyMessages(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := NewGoogleGmailConnector(srv)
	caps := conn.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "messages" {
		t.Fatalf("caps = %+v", caps)
	}
	if types.DataTypeHasAgentWriteOp(caps, "messages") {
		t.Fatal("messages must not advertise agent write ops in this phase")
	}
	for _, op := range caps.DataTypes[0].Operations {
		if types.IsMutatingMethod(op.Method) {
			t.Fatalf("unexpected mutating op: %+v", op)
		}
	}
}

func TestGmailBareIDFromProxyPath(t *testing.T) {
	cases := []struct {
		path string
		id   string
		ok   bool
	}{
		{"/messages/abc", "abc", true},
		{"/me/messages/abc", "abc", true},
		{"/threads/t1", "t1", true},
		{"/me/threads/t1", "t1", true},
		{"/me/messages", "", false},
		{"/messages/", "", false},
	}
	for _, tc := range cases {
		for _, kind := range []string{"messages", "threads"} {
			id, ok := gmailBareIDFromProxyPath(tc.path, kind)
			wantOK := tc.ok && strings.Contains(tc.path, kind)
			wantID := ""
			if wantOK {
				wantID = tc.id
			}
			if ok != wantOK || id != wantID {
				t.Errorf("gmailBareIDFromProxyPath(%q,%q) = (%q,%v), want (%q,%v)",
					tc.path, kind, id, ok, wantID, wantOK)
			}
		}
	}
}

func TestGmailHelpers_HeaderAndDirection(t *testing.T) {
	if got := gmailInternalDateRFC3339("1717232400000"); got != "2024-06-01T09:00:00Z" {
		t.Fatalf("internalDate = %q", got)
	}
	if got := gmailInternalDateRFC3339(""); got != "" {
		t.Fatalf("empty internalDate = %q", got)
	}
	if got := gmailHeaderDateRFC3339("Sat, 01 Jun 2024 09:00:00 +0000"); got != "2024-06-01T09:00:00Z" {
		t.Fatalf("header date = %q", got)
	}
	if got := gmailDirectionFromLabels([]string{"SENT"}); got != "outbound" {
		t.Fatalf("direction = %q", got)
	}
	if got := gmailDirectionFromLabels([]string{"INBOX", "UNREAD"}); got != "inbound" {
		t.Fatalf("direction = %q", got)
	}
	if got := gmailDirectionFromLabels(nil); got != "unknown" {
		t.Fatalf("direction = %q", got)
	}
	if gmailReadFromLabels([]string{"INBOX", "UNREAD"}) {
		t.Fatal("unread should not be read")
	}
	if !gmailReadFromLabels([]string{"INBOX"}) {
		t.Fatal("inbox without UNREAD should be read")
	}
	addrs := mapGmailAddressHeader("Ada Lovelace <ada@example.com>, grace@example.com")
	if len(addrs) != 2 || addrs[0].Emails[0] != "ada@example.com" || addrs[0].Name != "Ada Lovelace" {
		t.Fatalf("addrs = %+v", addrs)
	}
	// Fail-open crude parse.
	odd := mapGmailAddressHeader("not-an-address")
	if len(odd) != 1 || odd[0].Name != "not-an-address" {
		t.Fatalf("odd = %+v", odd)
	}
}

func TestGmailListHTTPError(t *testing.T) {
	disabled := gmailListHTTPError("messages list", 403, []byte(`{"error":{"status":"PERMISSION_DENIED","message":"Gmail API has not been used in project 123 before or it is disabled"}}`))
	if !strings.Contains(disabled.Error(), "Gmail API is not enabled") ||
		!strings.Contains(disabled.Error(), "gmail.googleapis.com") {
		t.Fatalf("disabled error = %v", disabled)
	}
	if !strings.Contains(disabled.Error(), "messages list") || !strings.Contains(disabled.Error(), "403") {
		t.Fatalf("disabled error should keep op/status: %v", disabled)
	}

	quota := gmailListHTTPError("threads list", 429, []byte(`{"error":{"message":"Quota exceeded"}}`))
	if !strings.Contains(quota.Error(), "Quota exceeded") || !strings.Contains(quota.Error(), "threads list") {
		t.Fatalf("quota error = %v", quota)
	}

	empty := gmailListHTTPError("message get", 500, nil)
	if empty.Error() != "gmail message get returned 500" {
		t.Fatalf("empty body error = %v", empty)
	}

	longBody := bytes.Repeat([]byte("x"), 500)
	long := gmailListHTTPError("messages list", 400, longBody)
	if !strings.Contains(long.Error(), strings.Repeat("x", 400)) || strings.Contains(long.Error(), strings.Repeat("x", 401)) {
		t.Fatalf("long body should clip at 400: len=%d", len(long.Error()))
	}
}
