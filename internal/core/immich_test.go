package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"

	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func testImmichServer(t *testing.T, handler http.Handler) (*Server, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	cfg := &Config{
		Services: []Service{
			{
				ID:           connectors.ImmichServiceID,
				Name:         connectors.ImmichServiceName,
				Action:       "inject",
				Endpoint:     ts.URL,
				Headers:      map[string]string{"X-API-Key": "test-immich-key"},
				CanonicalFor: []string{"photos"},
				GrantLimits:  policy.Permissions{"default": policy.AccessRead, "photos": policy.AccessRead},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors[connectors.ImmichServiceID] = connectors.NewImmichConnector(srv)
	return srv, ts
}

func TestImmichConnector_LibraryAggregate(t *testing.T) {
	srv, _ := testImmichServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"assets": map[string]any{
				"items": []any{map[string]any{
					"id":               "home-1",
					"type":             "IMAGE",
					"originalFileName": "yard.jpg",
					"fileCreatedAt":    "2022-05-05T10:00:00Z",
					"exifInfo":         map[string]any{"exifImageWidth": 100, "exifImageHeight": 80},
				}},
				"nextPage": nil,
			},
		})
	}))

	req := httptest.NewRequest("GET", "/v1/library/photos", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("photos"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list types.PhotoList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 1 || list.Photos[0].Filename != "yard.jpg" {
		t.Fatalf("list = %+v", list)
	}
	if !list.Photos[0].Sources[0].Canonical {
		t.Fatal("immich source should be canonical")
	}
	if list.ByService[connectors.ImmichServiceID].Status != types.LibraryStatusOK {
		t.Fatalf("by_service = %+v", list.ByService)
	}
}
