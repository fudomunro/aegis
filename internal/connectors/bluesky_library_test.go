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

func TestBlueskyListUserPhotos(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/xrpc/com.atproto.server.createSession":
			json.NewEncoder(w).Encode(map[string]any{
				"accessJwt": "jwt", "did": "did:plc:alice", "handle": "alice.bsky.social",
			})
		case r.URL.Path == "/xrpc/app.bsky.feed.getAuthorFeed":
			json.NewEncoder(w).Encode(map[string]any{
				"feed": []any{
					// Raw record embed with image.ref.$link.
					map[string]any{"post": map[string]any{
						"uri":    "at://did:plc:alice/app.bsky.feed.post/img1",
						"cid":    "bafy-post",
						"author": map[string]any{"did": "did:plc:alice", "handle": "alice.bsky.social"},
						"record": map[string]any{
							"$type":     "app.bsky.feed.post",
							"text":      "beach day",
							"createdAt": "2024-07-04T18:30:00.000Z",
						},
						"embed": map[string]any{
							"$type": "app.bsky.embed.images",
							"images": []any{
								map[string]any{
									"alt": "waves",
									"image": map[string]any{
										"mimeType": "image/jpeg",
										"size":     1234,
										"ref":      map[string]any{"$link": "bafkrei-img1"},
									},
								},
								map[string]any{
									"alt": "sun",
									"image": map[string]any{
										"mimeType": "image/png",
										"size":     99,
										"ref":      map[string]any{"$link": "bafkrei-img2"},
									},
								},
							},
						},
					}},
					// Hydrated view embed (CDN URLs carry the blob CID).
					map[string]any{"post": map[string]any{
						"uri":    "at://did:plc:alice/app.bsky.feed.post/img3",
						"cid":    "bafy-post3",
						"author": map[string]any{"did": "did:plc:alice", "handle": "alice.bsky.social"},
						"record": map[string]any{
							"$type":     "app.bsky.feed.post",
							"text":      "view form",
							"createdAt": "2024-07-05T10:00:00.000Z",
						},
						"embed": map[string]any{
							"$type": "app.bsky.embed.images#view",
							"images": []any{
								map[string]any{
									"alt":      "cat",
									"fullsize": "https://cdn.bsky.app/img/feed_fullsize/plain/did:plc:alice/bafkrei-img3",
									"thumb":    "https://cdn.bsky.app/img/feed_thumbnail/plain/did:plc:alice/bafkrei-img3",
									"aspectRatio": map[string]any{
										"width":  100,
										"height": 80,
									},
								},
							},
						},
					}},
					// Text-only post must be skipped.
					map[string]any{"post": map[string]any{
						"uri":    "at://did:plc:alice/app.bsky.feed.post/text1",
						"author": map[string]any{"did": "did:plc:alice", "handle": "alice.bsky.social"},
						"record": map[string]any{"$type": "app.bsky.feed.post", "text": "hello", "createdAt": "2024-01-01T00:00:00Z"},
					}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	conn := NewBlueskyConnector(newBlueskyHost(t, ts.URL))
	photos, truncated, err := conn.ListUserPhotos(context.Background())
	if err != nil {
		t.Fatalf("ListUserPhotos: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(photos) != 3 {
		t.Fatalf("photos = %d: %+v", len(photos), photos)
	}
	p := photos[0]
	if p.Caption != "beach day" || p.TakenAt == "" {
		t.Fatalf("photo = %+v", p)
	}
	if p.Sources[0].Service != BlueskyServiceID || p.Sources[0].ID != "img1" {
		t.Fatalf("source = %+v", p.Sources[0])
	}
	if p.Sources[0].Canonical {
		t.Fatal("bluesky image posts should not claim archival canonical")
	}
	if !strings.Contains(p.Sources[0].BlobURL, "com.atproto.sync.getBlob") ||
		!strings.Contains(p.Sources[0].BlobURL, "bafkrei-img1") {
		t.Fatalf("blob url = %q", p.Sources[0].BlobURL)
	}
	if photos[1].Sources[0].ID != "img1#1" {
		t.Fatalf("second image id = %q", photos[1].Sources[0].ID)
	}
	if photos[1].MimeType != "image/png" || !strings.HasSuffix(photos[1].Filename, ".png") {
		t.Fatalf("png photo = %+v", photos[1])
	}
	// View-form embed: CID recovered from the CDN URL.
	view := photos[2]
	if view.Width != 100 || view.Height != 80 {
		t.Fatalf("aspect ratio = %dx%d", view.Width, view.Height)
	}
	if view.Sources[0].Extra["blob_cid"] != "bafkrei-img3" {
		t.Fatalf("view blob_cid = %v", view.Sources[0].Extra["blob_cid"])
	}
	if !strings.Contains(view.Sources[0].BlobURL, "bafkrei-img3") ||
		!strings.Contains(view.Sources[0].BlobURL, "did=") {
		t.Fatalf("view blob url = %q", view.Sources[0].BlobURL)
	}
}

func TestBlueskyListMessages_OnlyOwnPostsAndReplies(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/xrpc/com.atproto.server.createSession":
			json.NewEncoder(w).Encode(map[string]any{
				"accessJwt": "jwt", "did": "did:plc:alice", "handle": "alice.bsky.social",
			})
		case r.URL.Path == "/xrpc/app.bsky.feed.getAuthorFeed":
			json.NewEncoder(w).Encode(map[string]any{
				"feed": []any{
					// Own original post.
					map[string]any{"post": map[string]any{
						"uri":    "at://did:plc:alice/app.bsky.feed.post/p1",
						"cid":    "bafy-p1",
						"author": map[string]any{"did": "did:plc:alice", "handle": "alice.bsky.social"},
						"record": map[string]any{
							"$type":     "app.bsky.feed.post",
							"text":      "hello world\nsecond line",
							"createdAt": "2024-08-01T12:00:00.000Z",
						},
					}},
					// Own reply.
					map[string]any{"post": map[string]any{
						"uri":    "at://did:plc:alice/app.bsky.feed.post/p2",
						"cid":    "bafy-p2",
						"author": map[string]any{"did": "did:plc:alice", "handle": "alice.bsky.social"},
						"record": map[string]any{
							"$type":     "app.bsky.feed.post",
							"text":      "nice point",
							"createdAt": "2024-08-02T12:00:00.000Z",
							"reply": map[string]any{
								"parent": map[string]any{"uri": "at://did:plc:bob/app.bsky.feed.post/x", "cid": "bafy-x"},
								"root":   map[string]any{"uri": "at://did:plc:bob/app.bsky.feed.post/x", "cid": "bafy-x"},
							},
						},
					}},
					// Repost of someone else — must be skipped (reasonRepost).
					map[string]any{
						"reason": map[string]any{"$type": "app.bsky.feed.defs#reasonRepost"},
						"post": map[string]any{
							"uri":    "at://did:plc:bob/app.bsky.feed.post/other",
							"author": map[string]any{"did": "did:plc:bob", "handle": "bob.bsky.social"},
							"record": map[string]any{"$type": "app.bsky.feed.post", "text": "other voice"},
						},
					},
					// Someone else's post without reason — must be skipped.
					map[string]any{"post": map[string]any{
						"uri":    "at://did:plc:carol/app.bsky.feed.post/c1",
						"author": map[string]any{"did": "did:plc:carol", "handle": "carol.bsky.social"},
						"record": map[string]any{"$type": "app.bsky.feed.post", "text": "not mine"},
					}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	conn := NewBlueskyConnector(newBlueskyHost(t, ts.URL))
	msgs, truncated, err := conn.ListMessages(context.Background())
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs = %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Channel != types.MessageChannelSocial || msgs[0].Direction != "outbound" {
		t.Fatalf("msg0 = %+v", msgs[0])
	}
	if msgs[0].Subject != "hello world" {
		t.Fatalf("subject = %q", msgs[0].Subject)
	}
	if msgs[0].Body != "hello world\nsecond line" {
		t.Fatalf("body = %q", msgs[0].Body)
	}
	if !containsLabel(msgs[0].Labels, "post") || containsLabel(msgs[0].Labels, "reply") {
		t.Fatalf("labels = %v", msgs[0].Labels)
	}
	if msgs[1].Subject != "nice point" || !containsLabel(msgs[1].Labels, "reply") {
		t.Fatalf("reply = %+v", msgs[1])
	}
	if msgs[1].ThreadID != "at://did:plc:bob/app.bsky.feed.post/x" {
		t.Fatalf("thread = %q", msgs[1].ThreadID)
	}
	for _, m := range msgs {
		if m.From == nil || m.From.Handles[0] != "alice.bsky.social" {
			t.Fatalf("from = %+v", m.From)
		}
	}
}

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func TestBlueskyListUserContacts(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/xrpc/com.atproto.server.createSession":
			json.NewEncoder(w).Encode(map[string]any{
				"accessJwt": "jwt", "did": "did:plc:alice", "handle": "alice.bsky.social",
			})
		case r.URL.Path == "/xrpc/app.bsky.graph.getFollows":
			json.NewEncoder(w).Encode(map[string]any{
				"follows": []any{
					map[string]any{
						"did": "did:plc:bob", "handle": "bob.bsky.social", "displayName": "Bob",
						"description": "writes code",
					},
					map[string]any{
						"did": "did:plc:carol", "handle": "carol.bsky.social",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	conn := NewBlueskyConnector(newBlueskyHost(t, ts.URL))
	contacts, truncated, err := conn.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if truncated || len(contacts) != 2 {
		t.Fatalf("contacts = %d truncated=%v", len(contacts), truncated)
	}
	if contacts[0].Name != "Bob" || contacts[0].Handles[0] != "bob.bsky.social" {
		t.Fatalf("bob = %+v", contacts[0])
	}
	if contacts[0].Sources[0].Service != BlueskyServiceID || contacts[0].Sources[0].ID != "did:plc:bob" {
		t.Fatalf("bob source = %+v", contacts[0].Sources[0])
	}
	if contacts[0].Extra["description"] != "writes code" {
		t.Fatalf("bob extra = %+v", contacts[0].Extra)
	}
	// Display name falls back to handle.
	if contacts[1].Name != "carol.bsky.social" {
		t.Fatalf("carol = %+v", contacts[1])
	}
}

func TestBlueskyHandleProxy_GetBlob(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.server.createSession":
			json.NewEncoder(w).Encode(map[string]any{
				"accessJwt": "jwt", "did": "did:plc:alice", "handle": "alice.bsky.social",
			})
		case "/xrpc/com.atproto.sync.getBlob":
			if r.URL.Query().Get("cid") != "bafkrei-x" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("JPEGDATA"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	conn := NewBlueskyConnector(newBlueskyHost(t, ts.URL))
	req := httptest.NewRequest("GET", "/xrpc/com.atproto.sync.getBlob?did=did:plc:alice&cid=bafkrei-x", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/xrpc/com.atproto.sync.getBlob")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != "JPEGDATA" {
		t.Fatalf("body = %q", w.Body.String())
	}

	// Missing params
	req2 := httptest.NewRequest("GET", "/xrpc/com.atproto.sync.getBlob", nil)
	w2 := httptest.NewRecorder()
	conn.HandleProxy(w2, req2, "/xrpc/com.atproto.sync.getBlob")
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("missing params status = %d", w2.Code)
	}
}

func TestSubjectFromPostText(t *testing.T) {
	if got := subjectFromPostText("  hello world\nmore", "fb"); got != "hello world" {
		t.Fatalf("got %q", got)
	}
	if got := subjectFromPostText("", "fb"); got != "fb" {
		t.Fatalf("empty got %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := subjectFromPostText(long, "fb"); len(got) != 80 {
		t.Fatalf("len = %d", len(got))
	}
}
