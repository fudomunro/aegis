package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

// sharedMessageID is present in both sample Takeout mbox and gmailPayloadJSON("shared").
const sharedMessageID = "shared@example.com"

func takeoutMboxWithShared(t *testing.T) string {
	t.Helper()
	mbox := sampleMbox + `From MAILER-DAEMON Mon Jan  4 13:00:00 2024
From: Grace <grace@example.com>
To: me@example.com
Subject: Shared dual-source
Message-ID: <` + sharedMessageID + `>
Date: Mon, 04 Jan 2024 13:00:00 +0000
X-GM-LABELS: INBOX
Content-Type: text/plain; charset=utf-8

Present in both Gmail sync and Takeout.
`
	path := filepath.Join(t.TempDir(), "All mail Including Spam and Trash.mbox")
	if err := os.WriteFile(path, []byte(mbox), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func syncGmailShared(t *testing.T, srv *Server, a *mail.Archive) {
	t.Helper()
	disableGmailArchivePace(t)
	var mux = http.NewServeMux()
	mux.HandleFunc("/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{{"id": "shared", "threadId": "thread-shared"}},
		})
	})
	mux.HandleFunc("/users/me/messages/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
		_, _ = w.Write([]byte(gmailPayloadJSON(id)))
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv.tokens.Save(connectors.GoogleGmailServiceID, &tokens.OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleGmailConnector(srv)
	if _, err := conn.IngestMail(context.Background(), mail.Sink{A: a}, shared.MailIngestOptions{MaxMessages: 10, ResetCursor: true}); err != nil {
		t.Fatalf("gmail sync: %v", err)
	}
}

func importTakeout(t *testing.T, a *mail.Archive, path string, reset bool) shared.MailIngestResult {
	t.Helper()
	res, err := connectors.NewTakeoutIngest().IngestMail(context.Background(), mail.Sink{A: a},
		shared.MailIngestOptions{Query: path, MaxMessages: 50, ResetCursor: reset})
	if err != nil {
		t.Fatalf("takeout import: %v", err)
	}
	return res
}

func searchMergedShared(t *testing.T, a *mail.Archive) *kinds.Message {
	t.Helper()
	hits, err := a.Search(mail.SearchQuery{Q: "Shared dual-source", WithBody: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for i := range hits.Messages {
		if kinds.NormalizeMessageID(hits.Messages[i].EntityID) == sharedMessageID {
			return &hits.Messages[i]
		}
	}
	t.Fatalf("shared message missing from search: %+v", hits.Messages)
	return nil
}

// Takeout first, then Gmail archive sync: both sources attach on Message-ID
// and search returns one multi-source message (issue #139).
func TestMailSources_TakeoutThenGmail_MergesOnMessageID(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)

	path := takeoutMboxWithShared(t)
	tres := importTakeout(t, a, path, false)
	if tres.Stored < 4 {
		t.Fatalf("takeout stored = %+v", tres)
	}
	syncGmailShared(t, srv, a)

	st, err := a.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.ByService[connectors.TakeoutServiceID] < 4 || st.ByService[connectors.GoogleGmailServiceID] != 1 {
		t.Fatalf("by_service = %+v", st.ByService)
	}

	msg := searchMergedShared(t, a)
	if len(msg.Sources) != 2 || msg.Match != "message_id" {
		t.Fatalf("shared sources/match = %+v", msg)
	}
	var services []string
	for _, src := range msg.Sources {
		services = append(services, src.Service)
	}
	if !containsAll(services, connectors.GoogleGmailServiceID, connectors.TakeoutServiceID) {
		t.Fatalf("sources services = %v", services)
	}
	// Takeout is a sync location, not the mailbox home.
	for _, src := range msg.Sources {
		if src.Service == connectors.TakeoutServiceID && src.Canonical {
			t.Fatalf("takeout marked canonical: %+v", src)
		}
		if src.Service == connectors.GoogleGmailServiceID && !src.Canonical {
			t.Fatalf("gmail not canonical: %+v", src)
		}
	}
}

// Gmail first, then Takeout: Takeout must attach as a second source rather
// than skip because Gmail already has the Message-ID (issue #139).
func TestMailSources_GmailThenTakeout_AttachesTakeoutSource(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)

	syncGmailShared(t, srv, a)
	path := takeoutMboxWithShared(t)
	tres := importTakeout(t, a, path, false)
	// sample mbox (3) + shared dual-source (1) — shared attaches, not skipped.
	if tres.Stored != 4 || tres.Skipped != 0 {
		t.Fatalf("takeout stored (expected shared attached) = %+v", tres)
	}

	msg := searchMergedShared(t, a)
	if len(msg.Sources) != 2 || msg.Match != "message_id" {
		t.Fatalf("shared sources/match = %+v", msg)
	}

	// Re-import still skips this source's own copies (read-only Takeout).
	tres2 := importTakeout(t, a, path, true)
	if tres2.Stored != 0 || tres2.Skipped != 4 {
		t.Fatalf("takeout re-import = %+v", tres2)
	}
}

// Archive-backed library listing includes Takeout-only history and merges
// dual-source identities into one collection entry (issue #139).
func TestMailSources_LibraryIncludesArchiveCollection(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)

	syncGmailShared(t, srv, a)
	path := takeoutMboxWithShared(t)
	importTakeout(t, a, path, false)

	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("messages"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["mail-archive"].Status != kinds.LibraryStatusOK {
		t.Fatalf("by_service = %+v", resp.ByService)
	}

	var shared, takeoutOnly *kinds.Message
	for i := range resp.Messages {
		m := &resp.Messages[i]
		if kinds.NormalizeMessageID(m.EntityID) == sharedMessageID {
			shared = m
		}
		if strings.Contains(m.Subject, "Takeout hello") {
			takeoutOnly = m
		}
	}
	if shared == nil || len(shared.Sources) < 2 || shared.Match != "message_id" {
		t.Fatalf("shared library entry = %+v", shared)
	}
	if takeoutOnly == nil {
		t.Fatalf("takeout-only missing from library: %+v", resp.Messages)
	}
}

// HasProviders must count the local archive as a message source when no live
// MessageProvider is registered (issue #139).
func TestMailSources_HasProvidersArchiveOnly(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.connectors = map[string]connectors.ServiceConnector{}
	if !library.KindHasProviders("messages", srv.library()) {
		t.Fatal("expected archive-only HasProviders")
	}
	if err := a.Upsert(sampleMailRecord("a1", "Archive only", "hello")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	resp, err := a.Search(mail.SearchQuery{Limit: 2, WithBody: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Messages) != 1 || resp.Total != 1 {
		t.Fatalf("msgs=%d total=%d", len(resp.Messages), resp.Total)
	}
}

func containsAll(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, v := range list {
			if v == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
