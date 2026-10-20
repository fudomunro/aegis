package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

type fakePhotoBytesProvider struct {
	data     []byte
	mimeType string
	err      error
	fetched  string
}

func (f *fakePhotoBytesProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakePhotoBytesProvider) SystemInstructions(string) string { return "fake" }
func (f *fakePhotoBytesProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakePhotoBytesProvider) FetchPhotoBytes(_ context.Context, id string) ([]byte, string, error) {
	f.fetched = id
	return f.data, f.mimeType, f.err
}

type fakePhotoShareTarget struct {
	result types.PhotoShareResult
	err    error
	req    types.PhotoShareRequest
}

func (f *fakePhotoShareTarget) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakePhotoShareTarget) SystemInstructions(string) string { return "fake" }
func (f *fakePhotoShareTarget) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakePhotoShareTarget) SharePhoto(_ context.Context, req types.PhotoShareRequest) (types.PhotoShareResult, error) {
	f.req = req
	return f.result, f.err
}

func testPhotoShareServer(t *testing.T, sourceWrite, targetWrite bool) (*Server, *fakePhotoBytesProvider, *fakePhotoShareTarget) {
	t.Helper()
	srcPerms := types.Permissions{"default": types.AccessRead, "photos": types.AccessRead}
	if !sourceWrite {
		srcPerms = types.Permissions{"default": types.AccessRead, "photos": types.AccessNone}
	}
	dstPerms := types.Permissions{"default": types.AccessRead, "photos": types.AccessFull}
	if !targetWrite {
		dstPerms = types.Permissions{"default": types.AccessRead, "photos": types.AccessRead}
	}
	cfg := &Config{
		Services: []Service{
			{ID: "immich", Name: "Immich", Action: "inject", GrantLimits: srcPerms},
			{ID: "bluesky", Name: "Bluesky", Action: "inject", GrantLimits: dstPerms},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	src := &fakePhotoBytesProvider{
		data:     []byte("JPEGDATA"),
		mimeType: "image/jpeg",
	}
	dst := &fakePhotoShareTarget{
		result: types.PhotoShareResult{
			Service: "bluesky",
			PostURI: "at://did:plc:alice/app.bsky.feed.post/3kxyz",
			PostURL: "https://bsky.app/profile/alice.bsky.social/post/3kxyz",
			PostID:  "3kxyz",
		},
	}
	srv.connectors = map[string]connectors.ServiceConnector{
		"immich":  src,
		"bluesky": dst,
	}
	return srv, src, dst
}

func postPhotoShare(t *testing.T, srv *Server, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/library/photos/share", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePhotoShare)(w, req)
	return w
}

func TestHandlePhotoShare_HappyPathClaim(t *testing.T) {
	srv, src, dst := testPhotoShareServer(t, true, true)
	if ledger, err := OpenCopyLedger(filepath.Join(t.TempDir(), "copy_ledger.db")); err == nil {
		t.Cleanup(func() { ledger.Close() })
		srv.copies = ledger
	}
	w := postPhotoShare(t, srv, map[string]any{
		"source_service": "immich",
		"source_id":      "asset-1",
		"caption":        "hello",
		"alt":            "alt",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if src.fetched != "asset-1" {
		t.Fatalf("fetched = %q", src.fetched)
	}
	if dst.req.ContentSHA256 == "" || len(dst.req.Data) != 8 {
		t.Fatalf("share request = %+v data=%q", dst.req, dst.req.Data)
	}
	var resp types.PhotoShareResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Claim == nil {
		t.Fatalf("missing claim: %s", w.Body.String())
	}
	if resp.Claim.Type != "sha256+public-post" {
		t.Fatalf("claim type = %q", resp.Claim.Type)
	}
	if resp.Claim.SHA256 != dst.req.ContentSHA256 {
		t.Fatalf("claim hash = %q want %q", resp.Claim.SHA256, dst.req.ContentSHA256)
	}
	if resp.Claim.ByteSize != 8 {
		t.Fatalf("byte_size = %d", resp.Claim.ByteSize)
	}
	if resp.PostURL != "https://bsky.app/profile/alice.bsky.social/post/3kxyz" {
		t.Fatalf("post_url = %q", resp.PostURL)
	}
	if resp.Claim.TargetService != "bluesky" || resp.Claim.SourceService != "immich" {
		t.Fatalf("claim identity = %+v", resp.Claim)
	}
	if !strings.Contains(resp.Claim.Note, "PoC ownership claim") {
		t.Fatalf("claim note = %q", resp.Claim.Note)
	}

	// Copy ledger records the share as a durable provenance breadcrumb.
	rec, ok := srv.copies.Get("photos", "bluesky", "3kxyz")
	if !ok {
		t.Fatal("expected copy ledger row for bluesky post")
	}
	if rec.Provenance != CopyProvenanceShare {
		t.Fatalf("provenance = %q", rec.Provenance)
	}
	if rec.Version.ContentHash != resp.Claim.SHA256 {
		t.Fatalf("ledger hash = %q want %q", rec.Version.ContentHash, resp.Claim.SHA256)
	}
	if rec.EntityKey != "photos:immich:asset-1" {
		t.Fatalf("entity_key = %q", rec.EntityKey)
	}
}

func TestHandlePhotoShare_DryRunDoesNotPublish(t *testing.T) {
	srv, _, dst := testPhotoShareServer(t, true, true)
	w := postPhotoShare(t, srv, map[string]any{
		"source_service": "immich",
		"source_id":      "asset-1",
		"dry_run":        true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if dst.req.Data != nil {
		t.Fatal("dry-run should not publish")
	}
	var resp types.PhotoShareResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.DryRun || resp.Claim != nil {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestHandlePhotoShare_RejectsBadInput(t *testing.T) {
	srv, _, _ := testPhotoShareServer(t, true, true)
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing source_service", map[string]any{"source_id": "x"}, http.StatusBadRequest},
		{"missing source_id", map[string]any{"source_service": "immich"}, http.StatusBadRequest},
		{"same service", map[string]any{"source_service": "bluesky", "source_id": "x", "target_service": "bluesky"}, http.StatusBadRequest},
		// Unconfigured services fail the same permission gate as mirror (403).
		{"unknown source", map[string]any{"source_service": "nope", "source_id": "x"}, http.StatusForbidden},
		{"unknown target", map[string]any{"source_service": "immich", "source_id": "x", "target_service": "nope"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postPhotoShare(t, srv, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
		})
	}

	req := httptest.NewRequest("GET", "/v1/library/photos/share", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePhotoShare)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", w.Code)
	}
}

func TestHandlePhotoShare_PermissionDenials(t *testing.T) {
	t.Run("read denied on source", func(t *testing.T) {
		srv, _, _ := testPhotoShareServer(t, false, true)
		w := postPhotoShare(t, srv, map[string]any{"source_service": "immich", "source_id": "a"})
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("write denied on target", func(t *testing.T) {
		srv, _, _ := testPhotoShareServer(t, true, false)
		w := postPhotoShare(t, srv, map[string]any{"source_service": "immich", "source_id": "a"})
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "write access") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
}

func TestHandlePhotoShare_UpstreamErrors(t *testing.T) {
	t.Run("unauthorized source", func(t *testing.T) {
		srv, src, _ := testPhotoShareServer(t, true, true)
		src.err = types.ErrUnauthorized
		w := postPhotoShare(t, srv, map[string]any{"source_service": "immich", "source_id": "a"})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("empty bytes", func(t *testing.T) {
		srv, src, _ := testPhotoShareServer(t, true, true)
		src.data = nil
		w := postPhotoShare(t, srv, map[string]any{"source_service": "immich", "source_id": "a"})
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("share target error", func(t *testing.T) {
		srv, _, dst := testPhotoShareServer(t, true, true)
		dst.err = context.DeadlineExceeded
		w := postPhotoShare(t, srv, map[string]any{"source_service": "immich", "source_id": "a"})
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestHandlePhotoShare_UnsupportedSourceWithoutBytes(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "google-photos", Name: "Google Photos", Action: "inject", GrantLimits: types.Permissions{"photos": types.AccessRead}},
			{ID: "bluesky", Name: "Bluesky", Action: "inject", GrantLimits: types.Permissions{"photos": types.AccessFull}},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-photos": &fakePhotoProvider{},
		"bluesky":       &fakePhotoShareTarget{},
	}
	w := postPhotoShare(t, srv, map[string]any{"source_service": "google-photos", "source_id": "a"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cannot fetch photo bytes") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestBlueskySharePhoto_IntegrationMock(t *testing.T) {
	// End-to-end through the real Bluesky connector against a mock PDS.
	pds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.server.createSession":
			json.NewEncoder(w).Encode(map[string]any{
				"accessJwt": "jwt", "did": "did:plc:alice", "handle": "alice.bsky.social",
			})
		case "/xrpc/com.atproto.repo.uploadBlob":
			json.NewEncoder(w).Encode(map[string]any{
				"blob": map[string]any{
					"$type": "blob", "ref": map[string]any{"$link": "bafy"},
					"mimeType": "image/jpeg", "size": 8,
				},
			})
		case "/xrpc/com.atproto.repo.createRecord":
			json.NewEncoder(w).Encode(map[string]any{
				"uri": "at://did:plc:alice/app.bsky.feed.post/abc",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer pds.Close()

	immich := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/assets/asset-1/original" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("JPEGDATA"))
	}))
	defer immich.Close()

	cfg := &Config{
		Services: []Service{
			{
				ID: "immich", Name: "Immich", Action: "inject",
				Endpoint:    immich.URL,
				Headers:     map[string]string{"X-API-Key": "ik"},
				GrantLimits: types.Permissions{"photos": types.AccessRead},
			},
			{
				ID: "bluesky", Name: "Bluesky", Action: "inject",
				Endpoint: pds.URL,
				Headers: map[string]string{
					"X-Bluesky-Handle":       "alice.bsky.social",
					"X-Bluesky-App-Password": "app-pass",
				},
				GrantLimits: types.Permissions{"photos": types.AccessFull},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"immich":  connectors.NewImmichConnector(srv),
		"bluesky": connectors.NewBlueskyConnector(srv),
	}

	w := postPhotoShare(t, srv, map[string]any{
		"source_service": "immich",
		"source_id":      "asset-1",
		"caption":        "from immich",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.PhotoShareResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.PostURL != "https://bsky.app/profile/alice.bsky.social/post/abc" {
		t.Fatalf("post_url = %q", resp.PostURL)
	}
	if resp.Claim == nil || resp.Claim.ByteSize != 8 {
		t.Fatalf("claim = %+v", resp.Claim)
	}
	// SHA-256 of "JPEGDATA"
	if len(resp.Claim.SHA256) != 64 {
		t.Fatalf("sha = %q", resp.Claim.SHA256)
	}
	if resp.Claim.SharedAt == "" {
		t.Fatal("shared_at missing")
	}
	// Ensure timestamps parse as RFC3339-ish.
	if _, err := time.Parse(time.RFC3339, resp.Claim.SharedAt); err != nil {
		// Bluesky uses millisecond format; accept both.
		if _, err2 := time.Parse("2006-01-02T15:04:05.000Z", resp.Claim.SharedAt); err2 != nil {
			t.Fatalf("shared_at = %q", resp.Claim.SharedAt)
		}
	}
}
