package connectors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func newBlueskyHost(t *testing.T, endpoint string) *testHost {
	t.Helper()
	h := newTestHost("https://aegis.example.com")
	h.setService(BlueskyServiceID, endpoint)
	svc := h.ServiceConfig(BlueskyServiceID)
	svc.Headers = map[string]string{
		blueskyHandleHeader: "alice.bsky.social",
		blueskyPassHeader:   "app-pass",
	}
	return h
}

func TestBlueskyCredentialsFromHeaders(t *testing.T) {
	cred, ok := BlueskyCredentialsFromHeaders(map[string]string{
		"X-Bluesky-Handle":       " bob.bsky.social ",
		"X-Bluesky-App-Password": "secret",
	})
	if !ok || cred.Handle != "bob.bsky.social" || cred.AppPassword != "secret" {
		t.Fatalf("cred = %+v ok=%v", cred, ok)
	}
	if _, ok := BlueskyCredentialsFromHeaders(map[string]string{"X-Bluesky-Handle": "x"}); ok {
		t.Fatal("missing password should not be ok")
	}
}

func TestBlueskySharePhoto_HappyPath(t *testing.T) {
	var sawSession, sawUpload, sawCreate atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/xrpc/com.atproto.server.createSession":
			sawSession.Add(1)
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			json.Unmarshal(body, &payload)
			if payload["identifier"] != "alice.bsky.social" || payload["password"] != "app-pass" {
				t.Errorf("session payload = %v", payload)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"accessJwt": "jwt-1",
				"did":       "did:plc:alice",
				"handle":    "alice.bsky.social",
			})
		case r.URL.Path == "/xrpc/com.atproto.repo.uploadBlob":
			sawUpload.Add(1)
			if r.Header.Get("Authorization") != "Bearer jwt-1" {
				t.Errorf("upload auth = %q", r.Header.Get("Authorization"))
			}
			data, _ := io.ReadAll(r.Body)
			if string(data) != "JPEGDATA" {
				t.Errorf("upload body = %q", data)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"blob": map[string]any{
					"$type":    "blob",
					"ref":      map[string]any{"$link": "bafyreiabc"},
					"mimeType": "image/jpeg",
					"size":     len(data),
				},
			})
		case r.URL.Path == "/xrpc/com.atproto.repo.createRecord":
			sawCreate.Add(1)
			if r.Header.Get("Authorization") != "Bearer jwt-1" {
				t.Errorf("create auth = %q", r.Header.Get("Authorization"))
			}
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["repo"] != "did:plc:alice" || payload["collection"] != "app.bsky.feed.post" {
				t.Errorf("create payload keys = %v", payload)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"uri": "at://did:plc:alice/app.bsky.feed.post/3kxyz",
				"cid": "bafyreicid",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	host := newBlueskyHost(t, ts.URL)
	conn := NewBlueskyConnector(host)
	res, err := conn.SharePhoto(context.Background(), types.PhotoShareRequest{
		SourceService: "immich",
		SourceID:      "asset-1",
		Data:          []byte("JPEGDATA"),
		MimeType:      "image/jpeg",
		Caption:       "hello",
		Alt:           "alt text",
		ContentSHA256: "deadbeef",
	})
	if err != nil {
		t.Fatalf("SharePhoto: %v", err)
	}
	if sawSession.Load() != 1 || sawUpload.Load() != 1 || sawCreate.Load() != 1 {
		t.Fatalf("calls session=%d upload=%d create=%d", sawSession.Load(), sawUpload.Load(), sawCreate.Load())
	}
	if res.PostURI != "at://did:plc:alice/app.bsky.feed.post/3kxyz" {
		t.Fatalf("PostURI = %q", res.PostURI)
	}
	if res.PostURL != "https://bsky.app/profile/alice.bsky.social/post/3kxyz" {
		t.Fatalf("PostURL = %q", res.PostURL)
	}
	if res.PostID != "3kxyz" {
		t.Fatalf("PostID = %q", res.PostID)
	}
}

func TestBlueskySharePhoto_SizeAndMime(t *testing.T) {
	conn := NewBlueskyConnector(newBlueskyHost(t, "https://example.invalid"))
	big := make([]byte, blueskyImageMaxBytes+1)
	if _, err := conn.SharePhoto(context.Background(), types.PhotoShareRequest{Data: big, MimeType: "image/jpeg"}); err == nil {
		t.Fatal("expected size error")
	}
	if _, err := conn.SharePhoto(context.Background(), types.PhotoShareRequest{Data: []byte("x"), MimeType: "video/mp4"}); err == nil {
		t.Fatal("expected mime error")
	}
	if _, err := conn.SharePhoto(context.Background(), types.PhotoShareRequest{MimeType: "image/jpeg"}); err == nil {
		t.Fatal("expected empty-bytes error")
	}
}

func TestBlueskySharePhoto_Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()
	conn := NewBlueskyConnector(newBlueskyHost(t, ts.URL))
	_, err := conn.SharePhoto(context.Background(), types.PhotoShareRequest{
		Data:     []byte("JPEGDATA"),
		MimeType: "image/jpeg",
	})
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
}

func TestBlueskySharePhoto_MissingCredentials(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	host.setService(BlueskyServiceID, "https://example.invalid")
	conn := NewBlueskyConnector(host)
	_, err := conn.SharePhoto(context.Background(), types.PhotoShareRequest{
		Data:     []byte("JPEGDATA"),
		MimeType: "image/jpeg",
	})
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
}

func TestBlueskyHandleProxy_NotImplemented(t *testing.T) {
	conn := NewBlueskyConnector(newBlueskyHost(t, "https://example.invalid"))
	w := httptest.NewRecorder()
	conn.HandleProxy(w, httptest.NewRequest("POST", "/v1/services/bluesky/xrpc/foo", nil), "/xrpc/foo")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/v1/library/photos/share") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestBlueskyCapabilitiesAndInstructions(t *testing.T) {
	conn := NewBlueskyConnector(newBlueskyHost(t, "https://example.invalid"))
	caps := conn.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "photos" {
		t.Fatalf("caps = %+v", caps)
	}
	if len(caps.DataTypes[0].Operations) == 0 || caps.DataTypes[0].Operations[0].Method != "POST" {
		t.Fatalf("ops = %+v", caps.DataTypes[0].Operations)
	}
	instr := conn.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(instr, "/v1/library/photos/share") || !strings.Contains(instr, "issue #20") {
		t.Fatalf("instructions = %s", instr)
	}
}

func TestRkeyFromATURI(t *testing.T) {
	if got := rkeyFromATURI("at://did:plc:x/app.bsky.feed.post/abc"); got != "abc" {
		t.Fatalf("rkey = %q", got)
	}
	if got := rkeyFromATURI("at://did:plc:x/app.bsky.feed.post/abc/"); got != "abc" {
		t.Fatalf("rkey trailing = %q", got)
	}
}

func TestSharePhotoSHA256(t *testing.T) {
	got := SharePhotoSHA256([]byte("JPEGDATA"))
	if len(got) != 64 {
		t.Fatalf("sha length = %d (%s)", len(got), got)
	}
	if got != SharePhotoSHA256([]byte("JPEGDATA")) {
		t.Fatal("sha not stable")
	}
	if got == SharePhotoSHA256([]byte("OTHER")) {
		t.Fatal("different inputs must hash differently")
	}
}
