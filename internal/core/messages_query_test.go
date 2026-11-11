package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
)

func decodeMessageList(t *testing.T, body string) kinds.MessageList {
	t.Helper()
	var list kinds.MessageList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode message list: %v\n%s", err, body)
	}
	return list
}

// TestMessagesQuery_FullHistoryPaging pins the issue #311 contract: history
// queries walk the full archive with cursor paging, not the 200-row overview
// window, and sort=asc answers oldest-first questions.
func TestMessagesQuery_FullHistoryPaging(t *testing.T) {
	a := openTestMailArchive(t)
	// 205 rows: the overview window (200 newest) hides the oldest five.
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 205; i++ {
		rec := sampleMailRecord(fmt.Sprintf("p-%03d", i), fmt.Sprintf("Page subject %03d", i), "paging body")
		rec.MessageID = fmt.Sprintf("<p-%03d@example.com>", i)
		rec.SentAt = base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		rec.BodyText = "paging body"
		if err := a.Upsert(rec); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	srv := mailTestServer(t, a)

	// The overview stays the capped window and admits it.
	w := doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages", "")
	if w.Code != http.StatusOK {
		t.Fatalf("overview code = %d body=%s", w.Code, w.Body.String())
	}
	overview := decodeMessageList(t, w.Body.String())
	if overview.Total != 200 || !overview.Truncated {
		t.Fatalf("overview total=%d truncated=%v", overview.Total, overview.Truncated)
	}

	// sort=asc walks the full history from the oldest message.
	seen := map[string]bool{}
	pages := 0
	cursor := ""
	for {
		path := "/v1/library/messages?sort=asc&limit=50"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w := doMessagesReq(t, srv, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("page code = %d body=%s", w.Code, w.Body.String())
		}
		list := decodeMessageList(t, w.Body.String())
		if list.Total != 205 {
			t.Fatalf("query total = %d", list.Total)
		}
		for _, m := range list.Messages {
			seen[m.Sources[0].ID] = true
		}
		pages++
		cursor = list.NextCursor
		if cursor == "" {
			break
		}
		if pages > 10 {
			t.Fatal("cursor walk did not terminate")
		}
	}
	if len(seen) != 205 {
		t.Fatalf("paged %d distinct rows, want 205", len(seen))
	}
	// The oldest rows sit outside the overview window; history queries
	// must still reach them.
	if !seen["p-000"] || !seen["p-004"] {
		t.Fatalf("oldest rows missing from history walk: p-000=%v p-004=%v", seen["p-000"], seen["p-004"])
	}

	// Newest-first queries reach the same full set.
	w = doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages?sort=desc&limit=500", "")
	if w.Code != http.StatusOK {
		t.Fatalf("desc code = %d", w.Code)
	}
	list := decodeMessageList(t, w.Body.String())
	if list.Total != 205 || len(list.Messages) != 205 {
		t.Fatalf("desc total=%d len=%d", list.Total, len(list.Messages))
	}
	if list.Messages[0].Subject != "Page subject 204" {
		t.Fatalf("desc first = %+v", list.Messages[0])
	}
}

// TestMessagesQuery_MergedIdentitySpace pins the merge-path decision: when
// live connectors and the archive both serve a query, results come from one
// merged identity space — a message matched through one copy keeps every
// source (issue #311).
func TestMessagesQuery_MergedIdentitySpace(t *testing.T) {
	a := openTestMailArchive(t)
	rec := sampleMailRecord("arch-1", "Shared hello", "the vault combination")
	rec.MessageID = "<shared@example.com>"
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	srv := mailTestServer(t, a)
	live := newMessage("google-gmail", "Gmail", "live-1", kinds.MessageChannelEmail, "Shared hello", "hi there", "2024-06-01T09:00:00Z")
	live.EntityID = "<shared@example.com>"
	live.From = &kinds.Contact{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}}
	srv.connectors[connectors.GoogleGmailServiceID] = &fakeMessageProvider{messages: []kinds.Message{live}}

	// q matches only the archived body; the live copy must survive as a
	// source of the same identity entry.
	w := doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages?q=vault", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	list := decodeMessageList(t, w.Body.String())
	if list.Total != 1 {
		t.Fatalf("merged query total = %d msgs=%+v", list.Total, list.Messages)
	}
	msg := list.Messages[0]
	if len(msg.Sources) < 2 {
		t.Fatalf("merged identity lost sources: %+v", msg)
	}
	ids := map[string]bool{}
	for _, src := range msg.Sources {
		ids[src.ID] = true
	}
	if !ids["arch-1"] || !ids["live-1"] {
		t.Fatalf("sources = %v", ids)
	}

	// Body is metadata-only on lists unless asked for (issue #311).
	if msg.Body != "" {
		t.Fatalf("list leaked body: %+v", msg)
	}
	w = doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages?q=vault&include_body=1", "")
	list = decodeMessageList(t, w.Body.String())
	if list.Messages[0].Body == "" {
		t.Fatalf("include_body missing body: %+v", list.Messages[0])
	}
}

// TestMessagesQuery_FiltersLiveRows: structured filters apply to live
// provider rows too, and the surface degrades to live-only when the archive
// is disabled.
func TestMessagesQuery_FiltersLiveRows(t *testing.T) {
	srv := mailTestServer(t, nil)
	from := kinds.Contact{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}}
	a := newMessage("google-gmail", "Gmail", "g1", kinds.MessageChannelEmail, "Quarterly report", "notes", "2024-06-01T09:00:00Z")
	a.From = &from
	a.To = []kinds.Contact{{Name: "Me", Emails: []string{"me@example.com"}}}
	a.Labels = []string{"INBOX"}
	b := newMessage("google-gmail", "Gmail", "g2", kinds.MessageChannelEmail, "Lunch plans", "tacos", "2024-06-02T09:00:00Z")
	b.From = &kinds.Contact{Name: "Mallory", Emails: []string{"mallory@example.com"}}
	srv.connectors[connectors.GoogleGmailServiceID] = &fakeMessageProvider{messages: []kinds.Message{a, b}}

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{"g1", "g2"}},
		{"from=ada", []string{"g1"}},
		{"to=me@example.com", []string{"g1"}},
		{"q=quarterly", []string{"g1"}},
		{"q=tacos", []string{"g2"}},
		{"label=INBOX", []string{"g1"}},
		{"after=2024-06-02", []string{"g2"}},
		{"before=2024-06-02T00:00:00Z", []string{"g1"}},
		{"from=ada&after=2024-06-02", nil},
	} {
		path := "/v1/library/messages"
		if tc.query != "" {
			path += "?" + tc.query
		}
		w := doMessagesReq(t, srv, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s code = %d body=%s", tc.query, w.Code, w.Body.String())
		}
		list := decodeMessageList(t, w.Body.String())
		if len(list.Messages) != len(tc.want) {
			t.Fatalf("%s hits = %+v, want %v", tc.query, list.Messages, tc.want)
		}
		got := map[string]bool{}
		for _, m := range list.Messages {
			got[m.Sources[0].ID] = true
		}
		for _, id := range tc.want {
			if !got[id] {
				t.Fatalf("%s missing %s in %+v", tc.query, id, list.Messages)
			}
		}
	}
}

// TestMessagesFetch_LiveFallback: every row the collection endpoint shows is
// fetchable by its source id; body comes from the archive when Aegis has it.
func TestMessagesFetch_LiveFallback(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	live := newMessage("google-gmail", "Gmail", "live-9", kinds.MessageChannelEmail, "Fresh live", "snippet", "2024-06-01T09:00:00Z")
	srv.connectors[connectors.GoogleGmailServiceID] = &fakeMessageProvider{messages: []kinds.Message{live}}

	w := doMessagesFetchReq(t, srv, connectors.GoogleGmailServiceID, "live-9")
	if w.Code != http.StatusOK {
		t.Fatalf("live fetch code = %d body=%s", w.Code, w.Body.String())
	}
	var msg kinds.Message
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Subject != "Fresh live" {
		t.Fatalf("msg = %+v", msg)
	}

	w = doMessagesFetchReq(t, srv, connectors.GoogleGmailServiceID, "nope")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing code = %d body=%s", w.Code, w.Body.String())
	}

	// Method gate on the fetch subroute.
	req := httptest.NewRequest("POST", "/v1/library/messages/google-gmail/live-9", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/v1/library/messages/{service}/{id}"))(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post fetch code = %d", rec.Code)
	}
}

// TestMessagesQuery_CacheSeparatesFilters: filtered queries must not share
// cached aggregates.
func TestMessagesQuery_CacheSeparatesFilters(t *testing.T) {
	a := openTestMailArchive(t)
	if err := a.Upsert(sampleMailRecord("m1", "Alpha topic", "body")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rec := sampleMailRecord("m2", "Beta topic", "body")
	rec.MessageID = "<m2@example.com>"
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	srv := mailTestServer(t, a)

	w := doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages?q=alpha", "")
	first := decodeMessageList(t, w.Body.String())
	w = doMessagesReq(t, srv, http.MethodGet, "/v1/library/messages?q=beta", "")
	second := decodeMessageList(t, w.Body.String())
	if first.Total != 1 || second.Total != 1 {
		t.Fatalf("first=%d second=%d", first.Total, second.Total)
	}
	if first.Messages[0].Subject == second.Messages[0].Subject {
		t.Fatalf("cache leaked between filters: %+v", first.Messages[0])
	}
}

// TestMessagesFetch_RawSubpathRetired: provider-shaped raw payloads left the
// library surface (issue #311).
func TestMessagesFetch_RawSubpathRetired(t *testing.T) {
	if apiRouteAllowed("GET", "/v1/library/messages/google-gmail/m1/raw") {
		t.Fatal("raw subpath must not be an agent route")
	}
	if apiRouteAllowed("GET", "/v1/mail/search") || apiRouteAllowed("GET", "/v1/mail/messages/a/b") {
		t.Fatal("retired /v1/mail retrieval must not be on the API allowlist")
	}
}
