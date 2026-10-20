package connectors

import (
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
			"size":      12,
			"data":      "aGVsbG8gd29ybGQ=",
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
	raw.Payload = &struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
		Body struct {
			Data         string `json:"data"`
			AttachmentID string `json:"attachmentId"`
		} `json:"body"`
		Parts []struct {
			MimeType string `json:"mimeType"`
			Body     struct {
				Data string `json:"data"`
			} `json:"body"`
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"parts"`
	}{}
	raw.Payload.Body.AttachmentID = "att1"
	got := GmailAttachmentSummaries(raw)
	if len(got) != 1 || got[0]["id"] != "att1" {
		t.Fatalf("got %v", got)
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
