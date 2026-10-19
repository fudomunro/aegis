package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestGoogleDriveListUserDocuments_MapsGoogleDocs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.Query().Get("q"), googleDocsMime) {
			t.Errorf("list query should filter Google Docs, got %q", r.URL.Query().Get("q"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{{
				"id":           "file-1",
				"name":         "Apartment lease 2024",
				"mimeType":     googleDocsMime,
				"createdTime":  "2024-01-15T12:00:00Z",
				"modifiedTime": "2024-01-16T09:00:00Z",
				"description":  "Lease for 12 Example Street",
				"webViewLink":  "https://docs.google.com/document/d/file-1/edit",
				"owners": []map[string]any{{
					"displayName": "Ada Lovelace",
					"emailAddress": "ada@example.com",
				}},
			}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := newTestHostWithService(GoogleDriveServiceID, srv.URL, "https://aegis.example.com")
	h.setToken(GoogleDriveServiceID, "tok-1")
	c := NewGoogleDriveConnector(h)
	docs, truncated, err := c.ListUserDocuments(context.Background())
	if err != nil {
		t.Fatalf("ListUserDocuments: %v", err)
	}
	if truncated {
		t.Fatal("single page should not be truncated")
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	d := docs[0]
	if d.Title != "Apartment lease 2024" || d.EntityID != "file-1" {
		t.Fatalf("mapped doc = %+v", d)
	}
	if d.DocumentType != "google-doc" {
		t.Fatalf("document_type = %q", d.DocumentType)
	}
	if d.Correspondent != "Ada Lovelace" {
		t.Fatalf("correspondent = %q", d.Correspondent)
	}
	if len(d.People) != 1 || d.People[0].Name != "Ada Lovelace" {
		t.Fatalf("people = %+v", d.People)
	}
	if d.Excerpt != "Lease for 12 Example Street" {
		t.Fatalf("excerpt = %q", d.Excerpt)
	}
	if len(d.Sources) != 1 || !d.Sources[0].Canonical {
		t.Fatalf("sources = %+v", d.Sources)
	}
	if !strings.Contains(d.Sources[0].BlobURL, "/files/file-1/export") {
		t.Fatalf("blob_url = %q", d.Sources[0].BlobURL)
	}
	if !strings.Contains(d.Sources[0].BlobURL, "html") {
		t.Fatalf("blob_url should export HTML for later markdown: %q", d.Sources[0].BlobURL)
	}
}

func TestGoogleDriveFetchDocumentText_PlainTextExport(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/files/file-1/export", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mimeType"); got != "text/plain" {
			t.Errorf("FetchDocumentText should export text/plain, got %q", got)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Lease body as plain text"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := newTestHostWithService(GoogleDriveServiceID, srv.URL, "https://aegis.example.com")
	h.setToken(GoogleDriveServiceID, "tok-1")
	c := NewGoogleDriveConnector(h)
	text, truncated, err := c.FetchDocumentText(context.Background(), "file-1")
	if err != nil {
		t.Fatalf("FetchDocumentText: %v", err)
	}
	if truncated {
		t.Fatal("should not mark truncated")
	}
	if text != "Lease body as plain text" {
		t.Fatalf("text = %q", text)
	}
}

func TestGoogleDriveExportFileHTML(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/files/file-1/export", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mimeType"); got != "text/html" {
			t.Errorf("HTML export mime = %q", got)
		}
		w.Write([]byte("<h1>Lease</h1><p>Body</p>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := newTestHostWithService(GoogleDriveServiceID, srv.URL, "https://aegis.example.com")
	h.setToken(GoogleDriveServiceID, "tok-1")
	c := NewGoogleDriveConnector(h)
	html, err := c.ExportFileHTML(context.Background(), "file-1")
	if err != nil {
		t.Fatalf("ExportFileHTML: %v", err)
	}
	if !strings.Contains(html, "<h1>Lease</h1>") {
		t.Fatalf("html = %q", html)
	}
}

func TestGoogleDriveUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	h := newTestHostWithService(GoogleDriveServiceID, srv.URL, "https://aegis.example.com")
	h.setToken(GoogleDriveServiceID, "tok-1")
	c := NewGoogleDriveConnector(h)
	_, _, err := c.ListUserDocuments(context.Background())
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestGoogleDriveNoToken(t *testing.T) {
	h := newTestHostWithService(GoogleDriveServiceID, "http://127.0.0.1:1", "https://aegis.example.com")
	c := NewGoogleDriveConnector(h)
	_, _, err := c.ListUserDocuments(context.Background())
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestGoogleDriveCapabilitiesAndInstructions(t *testing.T) {
	c := NewGoogleDriveConnector(newTestHost("https://aegis.example.com"))
	caps := c.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "documents" {
		t.Fatalf("caps = %+v", caps)
	}
	instr := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(instr, "/v1/library/documents") {
		t.Fatalf("instructions missing library path: %s", instr)
	}
	if !strings.Contains(instr, "plain text") && !strings.Contains(instr, "HTML") {
		t.Fatal("instructions should mention HTML/plain-text export")
	}
}
