package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
)

type fakePhotoProvider struct {
	photos []kinds.Photo
	err    error
}

func (f *fakePhotoProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakePhotoProvider) SystemInstructions(string) string { return "fake" }
func (f *fakePhotoProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakePhotoProvider) ListUserPhotos(context.Context) ([]kinds.Photo, bool, error) {
	return f.photos, false, f.err
}

func TestHandleLibraryPhotos_AggregatesServices(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-photos", Name: "Google Photos", Action: "inject"},
		{ID: "icloud", Name: "iCloud", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-photos": &fakePhotoProvider{photos: []kinds.Photo{
			kinds.NewPhoto("google-photos", "Google Photos", "1", "a.jpg", "image/jpeg", "2024-01-01T00:00:00Z", 10, 10),
			kinds.NewPhoto("google-photos", "Google Photos", "2", "b.jpg", "image/jpeg", "2024-01-02T00:00:00Z", 10, 10),
		}},
		"icloud": &fakePhotoProvider{photos: []kinds.Photo{
			kinds.NewPhoto("icloud", "iCloud", "9", "c.jpg", "image/jpeg", "2024-01-03T00:00:00Z", 10, 10),
		}},
	}

	req := httptest.NewRequest("GET", "/v1/library/photos", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("photos"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.PhotoList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 3 {
		t.Fatalf("total = %d, want 3: %+v", resp.Total, resp.Photos)
	}
	if resp.ByService["google-photos"].Count != 2 || resp.ByService["icloud"].Count != 1 {
		t.Fatalf("by_service = %+v", resp.ByService)
	}
	// Newest first.
	if resp.Photos[0].TakenAt < resp.Photos[1].TakenAt {
		t.Fatalf("photos should be newest-first: %+v", resp.Photos)
	}
}

func TestHandleLibraryPhotos_NoProviders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/library/photos", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("photos"))(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleLibraryPhotos_DeniedByPermission(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "google-photos",
		Name:        "Google Photos",
		Action:      "inject",
		GrantLimits: policy.Permissions{"photos": policy.AccessNone},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-photos": &fakePhotoProvider{photos: []kinds.Photo{
			kinds.NewPhoto("google-photos", "Google Photos", "1", "a.jpg", "image/jpeg", "2024-01-01T00:00:00Z", 1, 1),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/photos", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("photos"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryPhotos_UnauthorizedService(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-photos", Name: "Google Photos", Action: "inject"},
		{ID: "icloud", Name: "iCloud", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-photos": &fakePhotoProvider{photos: []kinds.Photo{
			kinds.NewPhoto("google-photos", "Google Photos", "1", "a.jpg", "image/jpeg", "2024-01-01T00:00:00Z", 1, 1),
		}},
		"icloud": &fakePhotoProvider{err: kinds.ErrUnauthorized},
	}
	req := httptest.NewRequest("GET", "/v1/library/photos", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("photos"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.PhotoList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ByService["icloud"].Status != kinds.LibraryStatusUnauthorized {
		t.Fatalf("icloud status = %+v", resp.ByService["icloud"])
	}
}

func TestHandleLibraryPhotos_ErrorService(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-photos", Name: "Google Photos", Action: "inject"},
		{ID: "icloud", Name: "iCloud", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-photos": &fakePhotoProvider{photos: []kinds.Photo{
			kinds.NewPhoto("google-photos", "Google Photos", "1", "a.jpg", "image/jpeg", "2024-01-01T00:00:00Z", 1, 1),
		}},
		"icloud": &fakePhotoProvider{err: errors.New("upstream boom")},
	}
	req := httptest.NewRequest("GET", "/v1/library/photos", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("photos"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.PhotoList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ByService["icloud"].Status != kinds.LibraryStatusError {
		t.Fatalf("icloud status = %+v", resp.ByService["icloud"])
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1 (partial ok)", resp.Total)
	}
}

func TestHandleLibraryPhotos_MethodNotAllowed(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "google-photos", Name: "Google Photos", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"google-photos": &fakePhotoProvider{}}
	req := httptest.NewRequest("POST", "/v1/library/photos", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("photos"))(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestPhotoKeywordsMatch(t *testing.T) {
	kind := LibraryKindByID("photos")
	if kind == nil {
		t.Fatal("photos kind not registered")
	}
	for _, msg := range []string{
		"what photos do I have?",
		"list my pictures",
		"show my photo library",
		"photos from last summer",
		"google photos",
		"camera roll",
	} {
		if !kind.Mentions(msg) {
			t.Errorf("expected Mentions(%q)", msg)
		}
	}
	if kind.Mentions("what playlists do I have?") {
		t.Error("photos should not match playlist questions")
	}
}

func TestPreRequest_InjectsPhotoLibraryHint(t *testing.T) {
	// Service name must not appear in the user message — library hints are
	// only injected when no service is matched (same as contacts).
	cfg := &Config{Services: []Service{
		{ID: "google-photos", Name: "Google Photos", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example")
	srv.connectors["google-photos"] = &fakePhotoProvider{}

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{RequestID: "req-photos", LoginName: "alice"},
		UserMessage: "list my pictures from last summer",
		RequestBody: json.RawMessage(`{
			"messages": [{"role": "user", "content": "list my pictures from last summer"}]
		}`),
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Action != ActionModify {
		t.Fatalf("action = %q, want modify: %s", resp.Action, w.Body.String())
	}
	if !strings.Contains(string(resp.RequestBody), "/v1/library/photos") {
		t.Fatalf("request_body missing photos library endpoint: %s", resp.RequestBody)
	}
}

func TestHasPhotoProviders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	if kindHasProviders("photos", srv) {
		t.Fatal("expected no photo providers")
	}
	srv.connectors["google-photos"] = &fakePhotoProvider{}
	if !kindHasProviders("photos", srv) {
		t.Fatal("expected photo provider after registration")
	}
}

func TestGooglePhotosConnector_PickerFlow_ListUserPhotos(t *testing.T) {
	var listCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"id":            "sess-1",
				"pickerUri":     "https://photos.google.com/picker/sess-1",
				"mediaItemsSet": false,
				"pollingConfig": map[string]any{"pollInterval": "5s", "timeout": "600s"},
			})
			return
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/sess-1":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":            "sess-1",
				"pickerUri":     "https://photos.google.com/picker/sess-1",
				"mediaItemsSet": true,
			})
			return
		case r.Method == http.MethodGet && r.URL.Path == "/mediaItems":
			if r.URL.Query().Get("sessionId") != "sess-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			listCalls++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"mediaItems": []map[string]any{
					{
						"id":         "gp-item-1",
						"createTime": "2023-07-04T18:30:00Z",
						"type":       "PHOTO",
						"mediaFile": map[string]any{
							"baseUrl":  "https://lh3.googleusercontent.com/short-lived",
							"mimeType": "image/jpeg",
							"filename": "beach.jpg",
							"mediaFileMetadata": map[string]any{
								"width":       4032,
								"height":      3024,
								"cameraMake":  "Apple",
								"cameraModel": "iPhone",
							},
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID:       "google-photos",
		Name:     "Google Photos",
		Action:   "inject",
		Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("google-photos", &tokens.OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGooglePhotosConnector(srv)

	// No session yet — library is empty, not an error.
	photos, _, err := conn.ListUserPhotos(context.Background())
	if err != nil {
		t.Fatalf("ListUserPhotos without session: %v", err)
	}
	if len(photos) != 0 {
		t.Fatalf("want empty before pick, got %d", len(photos))
	}

	// Create session.
	createReq := httptest.NewRequest("POST", "/picker/sessions", nil)
	createW := httptest.NewRecorder()
	conn.HandleProxy(createW, createReq, "/picker/sessions")
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createW.Code, createW.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created["picker_uri"] == "" || created["id"] != "sess-1" {
		t.Fatalf("created session = %+v", created)
	}

	// Poll until ready.
	getReq := httptest.NewRequest("GET", "/picker/sessions/sess-1", nil)
	getW := httptest.NewRecorder()
	conn.HandleProxy(getW, getReq, "/picker/sessions/sess-1")
	if getW.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getW.Code, getW.Body.String())
	}
	if !strings.Contains(getW.Body.String(), `"media_items_set":true`) &&
		!strings.Contains(getW.Body.String(), `"media_items_set": true`) {
		// map is marshaled without space after colon by encoding/json
		var sess map[string]any
		if err := json.Unmarshal(getW.Body.Bytes(), &sess); err != nil || sess["media_items_set"] != true {
			t.Fatalf("session not ready: %s", getW.Body.String())
		}
	}

	// List via proxy.
	listReq := httptest.NewRequest("GET", "/picker/mediaItems?sessionId=sess-1", nil)
	listW := httptest.NewRecorder()
	conn.HandleProxy(listW, listReq, "/picker/mediaItems")
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listW.Code, listW.Body.String())
	}
	var listResp kinds.PhotoList
	if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listResp.Total != 1 {
		t.Fatalf("proxy list total = %d: %s", listResp.Total, listW.Body.String())
	}

	// Library aggregate now sees the last pick.
	photos, truncated, err := conn.ListUserPhotos(context.Background())
	if err != nil {
		t.Fatalf("ListUserPhotos: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(photos) != 1 {
		t.Fatalf("got %d photos, want 1", len(photos))
	}
	p := photos[0]
	if p.Filename != "beach.jpg" || p.Width != 4032 || p.Height != 3024 {
		t.Fatalf("mapped photo = %+v", p)
	}
	if p.TakenAt != "2023-07-04T18:30:00Z" {
		t.Fatalf("taken_at = %q", p.TakenAt)
	}
	if len(p.Sources) != 1 || !p.Sources[0].Canonical {
		t.Fatalf("sources = %+v", p.Sources)
	}
	if p.Sources[0].BlobURL == "" || p.Sources[0].ID != "gp-item-1" {
		t.Fatalf("source = %+v", p.Sources[0])
	}
	if p.Extra["picker_session_id"] != "sess-1" {
		t.Fatalf("extra = %+v", p.Extra)
	}
	if listCalls == 0 {
		t.Fatal("expected mediaItems list call")
	}
}

func TestGooglePhotosConnector_ListUserPhotos_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "google-photos", Name: "Google Photos", Action: "inject",
	}}}, "https://aegis.example.com")
	conn := connectors.NewGooglePhotosConnector(srv)
	_, _, err := conn.ListUserPhotos(context.Background())
	if !errors.Is(err, kinds.ErrUnauthorized) {
		t.Fatalf("err = %v, want kinds.ErrUnauthorized", err)
	}
}

func TestGooglePhotosConnector_SystemInstructions_MentionPicker(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewGooglePhotosConnector(srv)
	instr := conn.SystemInstructions("https://aegis.example.com")
	for _, want := range []string{
		"/v1/library/photos",
		"/picker/sessions",
		"Picker API",
		"Aegis authenticates you",
	} {
		if !strings.Contains(instr, want) {
			t.Fatalf("missing %q in instructions:\n%s", want, instr)
		}
	}
}
