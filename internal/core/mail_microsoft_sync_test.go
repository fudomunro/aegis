package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
)

// Microsoft Graph mail ingest through the core-owned archive sink (issue
// #399): the connector walk must survive the MailSink seam — store, cursor
// round-trip, source inventory — exactly like the Gmail ingest.

func mailMicrosoftTestServer(t *testing.T, archive *mail.Archive) *Server {
	t.Helper()
	srv := mailTestServer(t, archive)
	srv.config.Services = append(srv.config.Services, Service{
		ID:     connectors.MicrosoftMailServiceID,
		Name:   connectors.MicrosoftMailServiceName,
		Action: "inject",
	})
	srv.connectors[connectors.MicrosoftMailServiceID] = connectors.NewMicrosoftMailConnector(srv)
	srv.wireMailIngests()
	return srv
}

func microsoftGraphMock(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/me/mailFolders":
			_, _ = w.Write([]byte(`{"value":[{"id":"f-inbox","displayName":"Inbox"}]}`))
		case r.URL.Path == "/me/messages" && r.URL.Query().Get("$skiptoken") == "p2":
			_, _ = w.Write([]byte(`{"value":[
				{"id":"m2","internetMessageId":"<m2@example.com>","subject":"Second",
				 "bodyPreview":"two","body":{"contentType":"text","content":"body two"},
				 "sentDateTime":"2024-03-01T12:00:00Z","isRead":true,"parentFolderId":"f-inbox"}
			]}`))
		case r.URL.Path == "/me/messages":
			next := "http://" + r.Host + "/me/messages?$skiptoken=p2"
			_, _ = w.Write([]byte(`{"value":[
				{"id":"m1","internetMessageId":"<m1@example.com>","subject":"First",
				 "bodyPreview":"one","body":{"contentType":"text","content":"body one"},
				 "sentDateTime":"2024-03-01T11:00:00Z","isRead":false,"parentFolderId":"f-inbox"}
			],"@odata.nextLink":"` + next + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestMicrosoftMailSyncMailArchive(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailMicrosoftTestServer(t, a)
	up := microsoftGraphMock(t)
	srv.config.updateService(connectors.MicrosoftMailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	if err := srv.tokens.Save(connectors.MicrosoftMailServiceID,
		&tokens.OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("token save: %v", err)
	}

	conn := connectors.NewMicrosoftMailConnector(srv)
	sink := mail.Sink{A: a}
	res, err := conn.IngestMail(context.Background(), sink, shared.MailIngestOptions{ResetCursor: true})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Stored != 2 || !res.Done {
		t.Fatalf("result = %+v", res)
	}

	st, err := a.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.ByService[connectors.MicrosoftMailServiceID] != 2 {
		t.Fatalf("by_service = %+v", st.ByService)
	}
	if !st.SyncDone[connectors.MicrosoftMailServiceID] {
		t.Fatalf("sync_done = %+v, want the microsoft-mail walk recorded done", st.SyncDone)
	}

	rec, err := a.Get(connectors.MicrosoftMailServiceID, "m1")
	if err != nil {
		t.Fatalf("archive get: %v", err)
	}
	msg := rec.Message()
	if msg.Subject != "First" || msg.Body != "body one" {
		t.Fatalf("record message = %+v", msg)
	}
	if len(msg.Sources) == 0 || !msg.Sources[0].Canonical {
		t.Fatalf("sources = %+v, want canonical (live mailbox home)", msg.Sources)
	}
}

func TestWireMailIngests_RegistersMicrosoftMail(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailMicrosoftTestServer(t, a)
	if srv.mailIngestFor(connectors.MicrosoftMailServiceID) == nil {
		t.Fatal("microsoft-mail ingest must be wired")
	}
	if srv.mailIngestFor(connectors.GoogleGmailServiceID) == nil {
		t.Fatal("gmail ingest must stay wired")
	}
	if srv.mailIngestFor(connectors.TakeoutServiceID) == nil {
		t.Fatal("takeout ingest must stay wired")
	}
	var ids []string
	for _, s := range connectors.MailSources() {
		ids = append(ids, s.ServiceID)
	}
	if !strings.Contains(strings.Join(ids, ","), connectors.MicrosoftMailServiceID) {
		t.Fatalf("mail sources = %v, want microsoft-mail declared", ids)
	}
}
