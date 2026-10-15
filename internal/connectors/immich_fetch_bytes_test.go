package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestImmichFetchPhotoBytes(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/assets/a1/original" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("X-API-Key") != "ik" {
				t.Errorf("api key = %q", r.Header.Get("X-API-Key"))
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("JPEGDATA"))
		}))
		defer ts.Close()
		host := newTestHost("https://aegis.example.com")
		host.setService(ImmichServiceID, ts.URL)
		svc := host.ServiceConfig(ImmichServiceID)
		svc.Headers = map[string]string{"X-API-Key": "ik"}

		conn := NewImmichConnector(host)
		data, mime, err := conn.FetchPhotoBytes(context.Background(), "a1")
		if err != nil {
			t.Fatalf("FetchPhotoBytes: %v", err)
		}
		if string(data) != "JPEGDATA" || mime != "image/jpeg" {
			t.Fatalf("data=%q mime=%q", data, mime)
		}
	})

	t.Run("missing id", func(t *testing.T) {
		conn := NewImmichConnector(newTestHost("https://aegis.example.com"))
		if _, _, err := conn.FetchPhotoBytes(context.Background(), "  "); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer ts.Close()
		host := newTestHost("https://aegis.example.com")
		host.setService(ImmichServiceID, ts.URL)
		conn := NewImmichConnector(host)
		if _, _, err := conn.FetchPhotoBytes(context.Background(), "a1"); err != types.ErrUnauthorized {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("upstream error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("boom"))
		}))
		defer ts.Close()
		host := newTestHost("https://aegis.example.com")
		host.setService(ImmichServiceID, ts.URL)
		conn := NewImmichConnector(host)
		if _, _, err := conn.FetchPhotoBytes(context.Background(), "a1"); err == nil {
			t.Fatal("expected error")
		}
	})
}
