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

func TestPaperlessListUserDocuments_MapsNeutralShape(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token abc123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": 7, "name": "housing"}},
		})
	})
	mux.HandleFunc("/api/correspondents", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": 3, "name": "Harbour Property"}},
		})
	})
	mux.HandleFunc("/api/document_types", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": 4, "name": "lease"}},
		})
	})
	mux.HandleFunc("/api/documents", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"count": 1,
			"next":  "",
			"results": []map[string]any{{
				"id":                    11,
				"title":                 "Apartment lease 2024",
				"content":               strings.Repeat("lease text ", 80),
				"correspondent":         3,
				"document_type":         4,
				"tags":                  []int{7},
				"created":               "2024-01-15T12:00:00Z",
				"modified":              "2024-01-16T09:00:00Z",
				"original_file_name":    "lease.pdf",
				"mime_type":             "application/pdf",
				"checksum":              "sha256:abc",
				"archive_serial_number": 99,
			}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := newTestHostWithService(PaperlessServiceID, srv.URL+"/api", "https://aegis.example.com")
	h.services[PaperlessServiceID].Headers = map[string]string{"Authorization": "Token abc123"}
	c := NewPaperlessConnector(h)
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
	if d.Title != "Apartment lease 2024" || d.MimeType != "application/pdf" {
		t.Fatalf("mapped doc = %+v", d)
	}
	if d.Correspondent != "Harbour Property" || d.DocumentType != "lease" {
		t.Fatalf("lookups = %+v", d)
	}
	if len(d.Tags) != 1 || d.Tags[0] != "housing" {
		t.Fatalf("tags = %+v", d.Tags)
	}
	if d.EntityID != "sha256:abc" {
		t.Fatalf("entity = %q", d.EntityID)
	}
	if d.Excerpt == "" {
		t.Fatal("expected excerpt from content")
	}
	if len([]rune(d.Excerpt)) > 300 {
		t.Fatalf("excerpt too long (%d runes)", len([]rune(d.Excerpt)))
	}
	if len(d.Sources) != 1 || !d.Sources[0].Canonical {
		t.Fatalf("sources = %+v", d.Sources)
	}
	if !strings.HasSuffix(d.Sources[0].BlobURL, "/documents/11/download") {
		t.Fatalf("blob_url = %q", d.Sources[0].BlobURL)
	}
	if len(d.People) != 1 || d.People[0].Name != "Harbour Property" {
		t.Fatalf("people = %+v", d.People)
	}
}

func TestPaperlessFetchDocumentText(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/documents/11", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 11, "title": "Lease", "content": "full ocr body",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := newTestHostWithService(PaperlessServiceID, srv.URL+"/api", "https://aegis.example.com")
	h.services[PaperlessServiceID].Headers = map[string]string{"Authorization": "Token abc123"}
	c := NewPaperlessConnector(h)
	text, truncated, err := c.FetchDocumentText(context.Background(), "11")
	if err != nil {
		t.Fatalf("FetchDocumentText: %v", err)
	}
	if truncated {
		t.Fatal("should not mark truncated")
	}
	if text != "full ocr body" {
		t.Fatalf("text = %q", text)
	}
}

func TestPaperlessUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	h := newTestHostWithService(PaperlessServiceID, srv.URL+"/api", "https://aegis.example.com")
	c := NewPaperlessConnector(h)
	_, _, err := c.ListUserDocuments(context.Background())
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestPaperlessCapabilitiesAndInstructions(t *testing.T) {
	c := NewPaperlessConnector(newTestHost("https://aegis.example.com"))
	caps := c.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "documents" {
		t.Fatalf("caps = %+v", caps)
	}
	if len(caps.DataTypes[0].Operations) == 0 {
		t.Fatal("expected operations")
	}
	instr := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(instr, "/v1/library/documents") {
		t.Fatalf("instructions missing library path: %s", instr)
	}
	if !strings.Contains(c.PaperlessWebBase(), "localhost:8000") {
		t.Fatalf("web base = %q", c.PaperlessWebBase())
	}
}

func TestPaperlessHandleProxy_BinaryPassthrough(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/documents/5/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := newTestHostWithService(PaperlessServiceID, srv.URL+"/api", "https://aegis.example.com")
	h.services[PaperlessServiceID].Headers = map[string]string{"Authorization": "Token abc123"}
	c := NewPaperlessConnector(h)

	req := httptest.NewRequest("GET", "/v1/services/paperless/documents/5/download", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/documents/5/download")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != "%PDF-1.4" {
		t.Fatalf("body = %q", w.Body.String())
	}
}
