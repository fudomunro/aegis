package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

// newTestImmichHost wires a mock Immich upstream behind the test Host with the
// static API-key header the connector attaches to every request.
func newTestImmichHost(t *testing.T, handler http.Handler) (*testHost, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	host := newTestHostWithService(ImmichServiceID, ts.URL, "https://aegis.example.com")
	host.services[ImmichServiceID].Headers = map[string]string{"X-API-Key": "test-immich-key"}
	return host, ts
}

func TestImmichConnector_SystemInstructions(t *testing.T) {
	conn := NewImmichConnector(newTestHostWithService(ImmichServiceID, "http://immich.local:2283/api", "https://aegis.example.com"))
	instr := conn.SystemInstructions("https://aegis.example.com")
	for _, want := range []string{
		"/v1/services/immich/assets/{id}/original",
		"/v1/library/photos",
		"blob_url",
		"canonical home",
	} {
		if !strings.Contains(instr, want) {
			t.Fatalf("instructions missing %q: %s", want, instr)
		}
	}
	if strings.Contains(instr, "/search/metadata") {
		t.Fatalf("must not advertise Immich asset search (issue #119): %s", instr)
	}
}

func TestImmichConnector_ListUserPhotos(t *testing.T) {
	var gotAuth string
	var gotPage float64
	host, _ := newTestImmichHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/metadata" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		gotAuth = r.Header.Get("X-API-Key")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotPage, _ = body["page"].(float64)

		json.NewEncoder(w).Encode(map[string]any{
			"assets": map[string]any{
				"total": 2,
				"count": 2,
				"items": []any{
					map[string]any{
						"id":               "asset-1",
						"type":             "IMAGE",
						"originalFileName": "beach.jpg",
						"fileCreatedAt":    "2023-07-04T18:30:00.000Z",
						"checksum":         "ABCDEF",
						"exifInfo": map[string]any{
							"dateTimeOriginal": "2023-07-04T18:30:00+00:00",
							"exifImageWidth":   4032,
							"exifImageHeight":  3024,
							"description":      "Sunset",
						},
					},
					map[string]any{
						"id":               "asset-2",
						"type":             "VIDEO",
						"originalFileName": "clip.mp4",
						"fileCreatedAt":    "2024-01-01T12:00:00.000Z",
					},
				},
				"nextPage": nil,
			},
		})
	}))

	conn := NewImmichConnector(host)
	photos, truncated, err := conn.ListUserPhotos(context.Background())
	if err != nil {
		t.Fatalf("ListUserPhotos: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if gotAuth != "test-immich-key" {
		t.Fatalf("X-API-Key = %q", gotAuth)
	}
	if gotPage != 1 {
		t.Fatalf("page = %v", gotPage)
	}
	if len(photos) != 2 {
		t.Fatalf("photos = %d", len(photos))
	}

	p := photos[0]
	if p.Filename != "beach.jpg" || p.Caption != "Sunset" {
		t.Fatalf("photo meta = %+v", p)
	}
	if p.Width != 4032 || p.Height != 3024 {
		t.Fatalf("dimensions = %dx%d", p.Width, p.Height)
	}
	if p.TakenAt == "" || !strings.HasPrefix(p.TakenAt, "2023-07-04") {
		t.Fatalf("taken_at = %q", p.TakenAt)
	}
	if p.MimeType != "image/jpeg" {
		t.Fatalf("mime = %q", p.MimeType)
	}
	src := p.Sources[0]
	if !src.Canonical || src.Service != ImmichServiceID {
		t.Fatalf("source = %+v", src)
	}
	if !strings.Contains(src.BlobURL, "/v1/services/immich/assets/asset-1/original") {
		t.Fatalf("blob_url = %q", src.BlobURL)
	}
	if !strings.Contains(src.ThumbnailURL, "/thumbnail") {
		t.Fatalf("thumb = %q", src.ThumbnailURL)
	}
	if src.Extra["content_hash"] != "abcdef" {
		t.Fatalf("content_hash = %v", src.Extra["content_hash"])
	}

	if photos[1].MimeType != "video/mp4" {
		t.Fatalf("video mime = %q", photos[1].MimeType)
	}
	if photos[1].Sources[0].Canonical != true {
		t.Fatal("video should be canonical")
	}
}

func TestImmichConnector_ListUserPhotos_Unauthorized(t *testing.T) {
	host, _ := newTestImmichHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Invalid API key"}`))
	}))
	_, _, err := NewImmichConnector(host).ListUserPhotos(context.Background())
	if err != kinds.ErrUnauthorized {
		t.Fatalf("err = %v, want kinds.ErrUnauthorized", err)
	}
}

func TestImmichConnector_ListUserPhotos_Paginates(t *testing.T) {
	page := 0
	host, _ := newTestImmichHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		next := any(nil)
		if page == 1 {
			next = "2"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"assets": map[string]any{
				"items": []any{map[string]any{
					"id":               "a" + string(rune('0'+page)),
					"type":             "IMAGE",
					"originalFileName": "p" + string(rune('0'+page)) + ".jpg",
					"fileCreatedAt":    "2024-01-01T00:00:00Z",
				}},
				"nextPage": next,
			},
		})
	}))
	photos, truncated, err := NewImmichConnector(host).ListUserPhotos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(photos) != 2 {
		t.Fatalf("photos = %d, want 2 pages", len(photos))
	}
}

func TestImmichConnector_HandleProxy_BinaryPassthrough(t *testing.T) {
	host, _ := newTestImmichHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/assets/asset-1/original" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-immich-key" {
			t.Errorf("missing api key")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
	}))

	req := httptest.NewRequest("GET", "/v1/services/immich/assets/asset-1/original", nil)
	// Drive the connector HandleProxy directly with remaining path.
	w := httptest.NewRecorder()
	NewImmichConnector(host).HandleProxy(w, req, "/assets/asset-1/original")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("content-type = %s", w.Header().Get("Content-Type"))
	}
	if w.Body.Len() != 4 {
		t.Fatalf("body len = %d", w.Body.Len())
	}
}
