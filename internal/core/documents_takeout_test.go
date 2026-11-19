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

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// takeoutMbox is a one-message Takeout mbox export: a PDF attachment and a
// text attachment (issue #215 fixture).
const takeoutMbox = "From 1717232400@example.com Sat Jun  1 09:00:00 2024\n" +
	"From: sender@example.com\r\n" +
	"To: me@example.com\r\n" +
	"Subject: Certificate\r\n" +
	"Date: Sat, 01 Jun 2024 09:00:00 +0000\r\n" +
	"Message-ID: <cert-1@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"OUTER\"\r\n" +
	"\r\n" +
	"--OUTER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Please find the certificate attached.\r\n" +
	"--OUTER\r\n" +
	"Content-Type: application/pdf; name=\"cert.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"cert.pdf\"\r\n" +
	"\r\n" +
	"JVBERi0xLjQ=\r\n" +
	"--OUTER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"notes.txt\"\r\n" +
	"\r\n" +
	"aGVsbG8gd29ybGQ=\r\n" +
	"--OUTER--\r\n"

// importTakeoutMbox feeds the fixture through the Takeout ingest into the
// server's mail archive — the real import path.
func importTakeoutMbox(t *testing.T, srv *Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "takeout.mbox")
	if err := os.WriteFile(path, []byte(takeoutMbox), 0o600); err != nil {
		t.Fatal(err)
	}
	ingest := connectors.NewTakeoutIngest()
	res, err := ingest.IngestMail(context.Background(), mail.Sink{A: srv.mail},
		shared.MailIngestOptions{Path: path, MaxMessages: 10})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if res.Stored != 1 {
		t.Fatalf("ingest result = %+v, want 1 stored", res)
	}
}

// listTakeoutDocs returns GET /me/documents for the server.
func listTakeoutDocs(t *testing.T, srv *Server, query string) kinds.DocumentList {
	t.Helper()
	req := httptest.NewRequest("GET", "/me/documents"+query, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().LibraryHandler("documents"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	var list kinds.DocumentList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return list
}

func TestTakeoutAttachments_ImportListAndByteFetch(t *testing.T) {
	cfg := &Config{}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.mail = openTestMailArchive(t)
	importTakeoutMbox(t, srv)

	list := listTakeoutDocs(t, srv, "")
	var pdf *kinds.Document
	for i := range list.Documents {
		if list.Documents[i].Title == "cert.pdf" {
			pdf = &list.Documents[i]
		}
	}
	if pdf == nil {
		t.Fatalf("cert.pdf missing from %+v", list.Documents)
	}
	if pdf.DocumentType != kinds.EmailAttachmentDocumentType {
		t.Fatalf("document_type = %q", pdf.DocumentType)
	}
	if pdf.Correspondent == "" || pdf.CreatedAt == "" {
		t.Fatalf("doc = %+v, want correspondent and date", pdf)
	}
	if len(pdf.Sources) != 1 {
		t.Fatalf("sources = %+v", pdf.Sources)
	}
	src := pdf.Sources[0]
	if src.Service != connectors.TakeoutServiceID || src.Canonical {
		t.Fatalf("source = %+v, want non-canonical takeout", src)
	}
	if !strings.Contains(src.BlobURL, "/me/documents/"+connectors.TakeoutServiceID+"/") {
		t.Fatalf("blob_url = %q", src.BlobURL)
	}

	// Byte fetch for the Takeout-only attachment (the whole game).
	req := httptest.NewRequest("GET", src.BlobURL, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.SetPathValue("service", src.Service)
	req.SetPathValue("id", src.ID)
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/me/documents/{service}/{id}/bytes"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bytes status = %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "%PDF-1.4" {
		t.Fatalf("bytes = %q", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "cert.pdf") {
		t.Fatalf("content-disposition = %q", cd)
	}

	// Text route resolves the text attachment through the archive.
	var notes *kinds.Document
	for i := range list.Documents {
		if list.Documents[i].Title == "notes.txt" {
			notes = &list.Documents[i]
		}
	}
	if notes == nil {
		t.Fatalf("notes.txt missing from %+v", list.Documents)
	}
	req2 := httptest.NewRequest("GET", "/me/documents/x/y/text", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	req2.SetPathValue("service", notes.Sources[0].Service)
	req2.SetPathValue("id", notes.Sources[0].ID)
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/me/documents/{service}/{id}/text"))(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("text status = %d body=%s", w2.Code, w2.Body.String())
	}
	var text kinds.DocumentText
	if err := json.Unmarshal(w2.Body.Bytes(), &text); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if text.Text != "hello world" {
		t.Fatalf("text = %q", text.Text)
	}
}

func TestTakeoutAttachments_MergesWithLiveProviderCopy(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-gmail", Name: "Gmail", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.mail = openTestMailArchive(t)
	importTakeoutMbox(t, srv)

	// The live provider knows the same attachment of the same message.
	live := kinds.EmailAttachmentDocument(
		kinds.Message{
			EntityID: "cert-1@example.com",
			Subject:  "Certificate",
			Snippet:  "Please find the certificate attached.",
			SentAt:   "2024-06-01T09:00:00Z",
		},
		kinds.AttachmentRef{ID: "0.1", Filename: "cert.pdf", MimeType: "application/pdf", Size: 8},
		kinds.DocumentSource{
			Service:     "google-gmail",
			ServiceName: "Gmail",
			ID:          "m1~0.1",
			Canonical:   true,
			BlobURL:     "https://aegis.example.com/v1/services/google-gmail/messages/m1/attachments/0.1",
		},
	)
	srv.connectors["google-gmail"] = &fakeDocumentProvider{
		documents: []kinds.Document{live},
	}

	list := listTakeoutDocs(t, srv, "")
	var pdf *kinds.Document
	var count int
	for i := range list.Documents {
		if list.Documents[i].Title == "cert.pdf" {
			count++
			pdf = &list.Documents[i]
		}
	}
	if count != 1 {
		t.Fatalf("cert.pdf listed %d times, want 1: %+v", count, list.Documents)
	}
	if len(pdf.Sources) != 2 {
		t.Fatalf("sources = %+v, want live + takeout copies", pdf.Sources)
	}
	services := []string{pdf.Sources[0].Service, pdf.Sources[1].Service}
	if services[0] == services[1] {
		t.Fatalf("copies must name both sources: %+v", pdf.Sources)
	}
	if pdf.Match == "" {
		t.Fatal("merged document must record its match kind")
	}
}

func TestTakeoutAttachments_BytesRouteGates(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"documents/metadata": {Default: shared.AccessRead}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.search = newTestSearchIndex(t)
	srv.mail = openTestMailArchive(t)
	importTakeoutMbox(t, srv)

	req := httptest.NewRequest("GET", "/me/documents/google-takeout/x~2/bytes", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.SetPathValue("service", "google-takeout")
	req.SetPathValue("id", "x~2")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.library().RouteHandler("/me/documents/{service}/{id}/bytes"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("metadata-only key must not read bytes: status = %d body=%s", w.Code, w.Body.String())
	}

	cfg.typePolicies = policy.DataGrants{"documents": {Default: shared.AccessRead}}
	srv2 := testServerWithBase(cfg, "https://aegis.example.com")
	srv2.search = newTestSearchIndex(t)
	srv2.mail = openTestMailArchive(t)
	importTakeoutMbox(t, srv2)
	list := listTakeoutDocs(t, srv2, "")
	if len(list.Documents) == 0 {
		t.Fatal("documents grant must list attachment documents")
	}

	// Unknown part and method handling on the byte route.
	req2 := httptest.NewRequest("GET", "/me/documents/google-takeout/nope~9/bytes", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	req2.SetPathValue("service", "google-takeout")
	req2.SetPathValue("id", "nope~9")
	w2 := httptest.NewRecorder()
	srv2.AuthMiddleware(srv2.library().RouteHandler("/me/documents/{service}/{id}/bytes"))(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("missing part status = %d", w2.Code)
	}

	req3 := httptest.NewRequest("POST", "/me/documents/x/y/bytes", nil)
	req3.Header.Set("Authorization", "Bearer test-key")
	req3.SetPathValue("service", "google-takeout")
	req3.SetPathValue("id", "x~2")
	w3 := httptest.NewRecorder()
	srv2.AuthMiddleware(srv2.library().RouteHandler("/me/documents/{service}/{id}/bytes"))(w3, req3)
	if w3.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", w3.Code)
	}
}
