package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newGmailHost(t *testing.T, endpoint string) *testHost {
	t.Helper()
	h := newTestHost("https://aegis.example.com")
	h.setService(GoogleGmailServiceID, endpoint)
	h.setToken(GoogleGmailServiceID, "test-token")
	return h
}

func TestHandleGetAttachment_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/me/messages/msg123/attachments/att456" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"size":         12,
			"data":         "aGVsbG8gd29ybGQ=",
			"attachmentId": "att456",
		})
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/services/google-gmail/messages/msg123/attachments/att456", nil)
	c.HandleGetAttachment(w, req, "test-token", "msg123", "att456")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "aGVsbG8") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestHandleGetAttachment_NotFoundAndQuota(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/attachments/gone"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"Not Found"}}`))
		case strings.HasSuffix(r.URL.Path, "/attachments/busy"):
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"Quota exceeded for quota metric 'Total Query Cost'"}}`))
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))

	w := httptest.NewRecorder()
	c.HandleGetAttachment(w, httptest.NewRequest(http.MethodGet, "/x", nil), "test-token", "msg1", "gone")
	if w.Code != http.StatusNotFound {
		t.Fatalf("notfound status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	c.HandleGetAttachment(w, httptest.NewRequest(http.MethodGet, "/x", nil), "test-token", "msg1", "busy")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	c.HandleGetAttachment(w, httptest.NewRequest(http.MethodGet, "/x", nil), "test-token", "at:bad", "att")
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign id status = %d", w.Code)
	}
}

func TestHandleGetMessage_Quota429(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    403,
				"message": "Quota exceeded for quota metric 'Total Query Cost' and limit 'Units per minute per user'",
			},
		})
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	w := httptest.NewRecorder()
	c.HandleGetMessage(w, httptest.NewRequest(http.MethodGet, "/x", nil), "test-token", "msg1")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleGetMessage_ForeignID404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not call upstream for foreign id")
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	w := httptest.NewRecorder()
	c.HandleGetMessage(w, httptest.NewRequest(http.MethodGet, "/x", nil), "test-token", "at:/did:plc:x/app.bsky.feed.post/y")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestGmailAttachmentSummaries(t *testing.T) {
	raw := GoogleGmailMessage{}
	if got := GmailAttachmentSummaries(raw); got != nil {
		t.Fatalf("empty payload: %v", got)
	}
	raw.Payload = &gmailMessagePart{}
	raw.Payload.Body.AttachmentID = "att1"
	got := GmailAttachmentSummaries(raw)
	if len(got) != 1 || got[0].ID != "att1" {
		t.Fatalf("top-level body attachment: %v", got)
	}
}

func TestGmailAttachmentSummaries_NestedParts(t *testing.T) {
	raw := GoogleGmailMessage{
		Payload: &gmailMessagePart{
			MimeType: "multipart/mixed",
			Parts: []gmailMessagePart{
				{
					MimeType: "multipart/alternative",
					Parts: []gmailMessagePart{
						{MimeType: "text/plain", Body: gmailMessageBody{Data: "aGk="}},
					},
				},
				{
					MimeType: "application/pdf",
					Filename: "death-cert.pdf",
					Body:     gmailMessageBody{AttachmentID: "0.1", Size: 4096},
				},
			},
		},
	}
	got := GmailAttachmentSummaries(raw)
	if len(got) != 1 {
		t.Fatalf("want one nested attachment, got %v", got)
	}
	if got[0].ID != "0.1" || got[0].Filename != "death-cert.pdf" ||
		got[0].MimeType != "application/pdf" || got[0].Size != 4096 {
		t.Fatalf("got %+v", got[0])
	}
}

func TestGmailAttachmentSummaries_FilenameFromHeaders(t *testing.T) {
	raw := GoogleGmailMessage{
		Payload: &gmailMessagePart{
			Parts: []gmailMessagePart{
				{
					MimeType: "image/jpeg",
					Headers: []gmailMessageHeader{
						{Name: "Content-Disposition", Value: `attachment; filename="census.jpg"`},
					},
					Body: gmailMessageBody{AttachmentID: "att9"},
				},
			},
		},
	}
	got := GmailAttachmentSummaries(raw)
	if len(got) != 1 || got[0].Filename != "census.jpg" {
		t.Fatalf("got %v", got)
	}
}

func TestHandleProxy_AttachmentListRoute(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/me/messages/msg1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if !strings.Contains(r.URL.RawQuery, "format=full") {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "msg1",
			"payload": {
				"mimeType": "multipart/mixed",
				"parts": [
					{"mimeType": "application/pdf", "filename": "cert.pdf", "body": {"attachmentId": "0.1", "size": 10}}
				]
			}
		}`))
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/services/google-gmail/messages/msg1/attachments", nil)
	c.HandleProxy(w, req, "/messages/msg1/attachments")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"id":"0.1"`) || !strings.Contains(body, "cert.pdf") || !strings.Contains(body, `"total":1`) {
		t.Fatalf("body = %s", body)
	}
}

func TestHandleListAttachments_ForeignID404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not call upstream for foreign id")
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	c.HandleProxy(w, req, "/messages/caal8h=c=+n9h=pku72@mail.gmail.com/attachments")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestNeutralAttachmentDocument_SameSourceAsMessage(t *testing.T) {
	c := NewGoogleGmailConnector(newGmailHost(t, "https://gmail.example.com"))
	raw := GoogleGmailMessage{
		ID:           "msg1",
		Snippet:      "Please find the certificate attached",
		InternalDate: "1700000000000",
		Payload: &gmailMessagePart{
			Headers: []gmailMessageHeader{
				{Name: "From", Value: "County Clerk <clerk@example.com>"},
				{Name: "Subject", Value: "Birth certificate"},
			},
		},
	}
	att := GmailAttachmentSummary{ID: "0.1", Filename: "birth.pdf", MimeType: "application/pdf", Size: 20}
	doc := c.NeutralAttachmentDocument(raw, att)
	if doc.Title != "birth.pdf" || doc.MimeType != "application/pdf" {
		t.Fatalf("doc = %+v", doc)
	}
	if len(doc.Sources) != 1 {
		t.Fatalf("sources = %+v", doc.Sources)
	}
	src := doc.Sources[0]
	if src.Service != GoogleGmailServiceID || src.ServiceName != GoogleGmailServiceName {
		t.Fatalf("source service = %s / %s", src.Service, src.ServiceName)
	}
	if src.ID != "msg1~0.1" {
		t.Fatalf("source id = %q", src.ID)
	}
	if !strings.Contains(src.BlobURL, "/v1/services/google-gmail/messages/msg1/attachments/0.1") {
		t.Fatalf("blob_url = %q", src.BlobURL)
	}
	if !src.Canonical {
		t.Fatal("gmail should be canonical for its own attachment bytes")
	}
	if doc.Correspondent != "County Clerk" && doc.Correspondent != "clerk@example.com" {
		t.Fatalf("correspondent = %q", doc.Correspondent)
	}
	if doc.CreatedAt == "" {
		t.Fatal("missing created_at")
	}
}

func TestListUserDocuments_FromMessageAttachments(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/me/messages":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"messages":[{"id":"m1","threadId":"t1"}]}`))
		case r.URL.Path == "/users/me/messages/m1":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"id": "m1",
				"internalDate": "1700000000000",
				"payload": {
					"headers": [{"name": "Subject", "value": "Records"}, {"name": "From", "value": "a@example.com"}],
					"parts": [
						{"mimeType": "application/pdf", "filename": "a.pdf", "body": {"attachmentId": "attA", "size": 5}},
						{"mimeType": "text/plain", "body": {"data": "aGk="}}
					]
				}
			}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	docs, truncated, err := c.ListUserDocuments(context.Background())
	if err != nil {
		t.Fatalf("ListUserDocuments: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].Sources[0].Service != GoogleGmailServiceID {
		t.Fatalf("service = %s", docs[0].Sources[0].Service)
	}
	if docs[0].Sources[0].ID != "m1~attA" || docs[0].Title != "a.pdf" {
		t.Fatalf("doc = %+v", docs[0])
	}
}

func TestHandleGetAttachment_LongID(t *testing.T) {
	longID := strings.Repeat("A", 426)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/attachments/"+longID) {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"size":12,"data":"aGVsbG8=","attachmentId":"` + longID + `"}`))
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	c.HandleGetAttachment(w, req, "test-token", "msg123", longID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleProxy_AttachmentRoute_LongID(t *testing.T) {
	longID := strings.Repeat("B", 426)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"size":1,"data":"aGk="}`))
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/services/google-gmail/messages/msg1/attachments/"+longID, nil)
	c.HandleProxy(w, req, "/messages/msg1/attachments/"+longID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestListUserDocuments_KeepsPartialOnLaterError(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case r.URL.Path == "/users/me/messages" && calls == 1:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"messages":[{"id":"m1","threadId":"t1"},{"id":"m2","threadId":"t2"}],"nextPageToken":"p2"}`))
		case r.URL.Path == "/users/me/messages/m1":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"id": "m1",
				"payload": {
					"parts": [{"mimeType": "application/pdf", "filename": "keep.pdf", "body": {"attachmentId": "att1"}}]
				}
			}`))
		case r.URL.Path == "/users/me/messages/m2":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id": "m2", "payload": {"parts": []}}`))
		case r.URL.Path == "/users/me/messages":
			// Second list page fails (quota / transient).
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"message":"Quota exceeded for quota metric 'Total Query Cost'"}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	docs, truncated, err := c.ListUserDocuments(context.Background())
	if err != nil {
		t.Fatalf("partial results must fail-open, got err=%v", err)
	}
	if !truncated {
		t.Fatal("expected truncated=true after upstream error")
	}
	if len(docs) != 1 || docs[0].Title != "keep.pdf" {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestListUserDocuments_FirstPageErrorStillFails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"Quota exceeded for quota metric 'Total Query Cost'"}}`))
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	_, _, err := c.ListUserDocuments(context.Background())
	if err == nil {
		t.Fatal("cold-start list failure should still surface an error")
	}
}

func TestFetchDocumentText_LongAttachmentID(t *testing.T) {
	longID := strings.Repeat("C", 426)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "m1",
			"payload": {
				"parts": [
					{"mimeType": "text/plain", "body": {"attachmentId": "` + longID + `", "data": "aGVsbG8="}}
				]
			}
		}`))
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	text, _, err := c.FetchDocumentText(context.Background(), "m1~"+longID)
	if err != nil || text != "hello" {
		t.Fatalf("text = %q err=%v", text, err)
	}
}

func TestFetchDocumentText_InlineAndBinary(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "m1",
			"payload": {
				"parts": [
					{"mimeType": "text/plain", "body": {"attachmentId": "txt1", "data": "aGVsbG8="}},
					{"mimeType": "application/pdf", "body": {"attachmentId": "pdf1", "size": 3}}
				]
			}
		}`))
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	text, _, err := c.FetchDocumentText(context.Background(), "m1~txt1")
	if err != nil || text != "hello" {
		t.Fatalf("text attachment: %q %v", text, err)
	}
	text, _, err = c.FetchDocumentText(context.Background(), "m1~pdf1")
	if err != nil || text != "" {
		t.Fatalf("binary attachment should be empty text, got %q %v", text, err)
	}
}

func TestHandleProxy_AttachmentRoute(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/me/messages/msg1/attachments/att1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"size":1,"data":"aGk="}`))
	}))
	defer ts.Close()
	c := NewGoogleGmailConnector(newGmailHost(t, ts.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/services/google-gmail/messages/msg1/attachments/att1", nil)
	c.HandleProxy(w, req, "/messages/msg1/attachments/att1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}
