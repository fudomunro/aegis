package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// BlueskyConnector publishes photos to the user's Bluesky account via the
// AT Protocol (app.bsky.feed.post + images embed). This is a proof-of-concept
// share path for photo ownership/provenance claims (issue #20): Aegis fetches
// photo bytes ephemerally from a canonical source, posts them, and returns a
// content-hash + public post locator. Aegis still does not store photo bytes.
//
// Auth is a Bluesky handle + app password in service headers (like Immich's
// API key). Agents never see those credentials.
type BlueskyConnector struct {
	host Host
}

// NewBlueskyConnector creates a connector wired to the given Host (issue #15 seam).
func NewBlueskyConnector(host Host) *BlueskyConnector {
	return &BlueskyConnector{host: host}
}

const (
	// BlueskyServiceID is the connector / config service ID.
	BlueskyServiceID = "bluesky"
	// BlueskyServiceName is the human-readable label.
	BlueskyServiceName = "Bluesky"
	// blueskyDefaultEndpoint is the public Bluesky PDS entrypoint.
	// Override via the configured service endpoint for other PDS hosts.
	blueskyDefaultEndpoint = "https://bsky.social"
	// blueskyImageMaxBytes is AT Protocol's blob limit for image embeds.
	blueskyImageMaxBytes = 1_000_000
	// header names for the app-password credential pair.
	blueskyHandleHeader = "X-Bluesky-Handle"
	blueskyPassHeader   = "X-Bluesky-App-Password"
)

// BlueskyUpstreamBase returns the configured PDS base URL.
func (c *BlueskyConnector) BlueskyUpstreamBase() string {
	return c.host.UpstreamBaseFor(BlueskyServiceID, blueskyDefaultEndpoint)
}

// Capabilities returns Bluesky share and library operations reachable through Aegis.
func (c *BlueskyConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "photos",
				Description: "Share photos to Bluesky as public image posts, and list the user's Bluesky image posts (ownership-claim PoC, issue #20). Photo bytes stay on the PDS/CDN; Aegis does not store media.",
				Operations: []types.Operation{
					{
						Name:        "share_photo",
						Description: "Publish one photo to Bluesky and return a content-hash claim (permission key: photos write). Prefer POST /v1/library/photos/share over raw xrpc.",
						Method:      "POST",
						Path:        "/v1/library/photos/share",
						Parameters: []types.Parameter{
							{Name: "source_service", In: "body", Type: "string", Description: "Photo source connector ID (e.g. immich)", Required: true},
							{Name: "source_id", In: "body", Type: "string", Description: "Service-local photo/asset ID", Required: true},
							{Name: "caption", In: "body", Type: "string", Description: "Optional post text"},
							{Name: "alt", In: "body", Type: "string", Description: "Optional image alt text"},
							{Name: "dry_run", In: "body", Type: "boolean", Description: "Plan without publishing", Default: false},
						},
						Response: types.ResponseSpec{
							Description: "Post URI/URL plus a PoC ownership claim (sha256 + public post)",
							Fields:      []string{"target_service", "post_uri", "post_url", "claim"},
						},
					},
					{
						Name:        "get_blob",
						Description: "Download one image blob by CID (permission key: photos). Used by sources[].blob_url.",
						Method:      "GET",
						Path:        "/xrpc/com.atproto.sync.getBlob",
						Parameters: []types.Parameter{
							{Name: "did", In: "query", Type: "string", Description: "Repo DID that holds the blob", Required: true},
							{Name: "cid", In: "query", Type: "string", Description: "Blob CID", Required: true},
						},
						Response: types.ResponseSpec{
							Description: "Raw image bytes",
							Fields:      []string{"(binary)"},
						},
					},
				},
			},
			{
				Name:        "messages",
				Description: "Bluesky notifications (likes, replies, mentions, follows) as neutral messages for /v1/library/messages.",
				Operations: []types.Operation{
					{
						Name:        "list_notifications",
						Description: "List recent notifications as neutral Message values (permission key: messages)",
						Method:      "GET",
						Path:        "/xrpc/app.bsky.notification.listNotifications",
						Parameters: []types.Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Page size (default 50)", Default: 50},
							{Name: "cursor", In: "query", Type: "string", Description: "Pagination cursor"},
						},
						Response: types.ResponseSpec{
							Description: "Neutral messages: subject, snippet, from, sent_at, sources[]",
							Fields:      []string{"messages", "total"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Bluesky is referenced.
func (c *BlueskyConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Bluesky (sharing photos, their feed images, their posts, or their contacts), you MUST use Aegis at %s instead of calling Bluesky/AT Protocol directly. Aegis owns the Bluesky handle and app password — never ask the user for them.

Share photos (proof of concept for photo ownership claims):
1. Resolve a photo via GET %s/v1/library/photos (or ask for source_service + source_id).
2. POST %s/v1/library/photos/share with {"source_service":"immich","source_id":"...","caption":"..." }.
3. Return the post_url to the user and keep claim.sha256 / claim.post_uri as the ownership evidence.

Library sync (metadata only; bytes stay on Bluesky):
- GET %s/v1/library/photos — includes Bluesky image posts from the user's feed.
- GET %s/v1/library/messages — only the user's own posts and replies (not reposts or likes/follows).
- GET %s/v1/library/contacts — the user's Bluesky following list (handles).
- Image bytes: use sources[].blob_url (Aegis proxy getBlob); do not scrape cdn.bsky.app directly.

Aegis does not store photo bytes. The share downloads the original from the canonical photo service, hashes it (claim.sha256), posts a copy to Bluesky, and discards the local buffer. claim is a PoC (issue #20) — content hash + public post — not a cryptographic proof.

Always include the full Aegis base URL in your requests.
%s`, root, root, root, root, root, root, types.PhotoSourceDiscipline())
}

// HandleProxy serves read-only Bluesky xrpc paths that agents need for
// library detail (blob download, author feed, follows). Mutating xrpc remains
// reserved for the orchestrated share endpoint.
func (c *BlueskyConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", BlueskyServiceID, "method", r.Method, "path", remainingPath)
	path := "/" + strings.TrimPrefix(remainingPath, "/")

	switch {
	case r.Method == http.MethodGet && (path == "/xrpc/com.atproto.sync.getBlob" || strings.HasPrefix(path, "/xrpc/com.atproto.sync.getBlob")):
		c.handleGetBlob(w, r, logger)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/xrpc/app.bsky.feed.getAuthorFeed"):
		c.handleAuthorFeedProxy(w, r, logger)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/xrpc/"):
		// Generic read-only xrpc GET passthrough with session auth.
		c.handleXRPCGet(w, r, path, logger)
		return
	}

	logger.Info("bluesky proxy path not implemented; prefer library/share endpoints")
	WriteJSONError(w, http.StatusNotFound,
		"bluesky proxy path not implemented in this PoC — use POST /v1/library/photos/share or GET /v1/library/{photos,messages}")
}

func (c *BlueskyConnector) handleGetBlob(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	q := r.URL.Query()
	did := strings.TrimSpace(q.Get("did"))
	cid := strings.TrimSpace(q.Get("cid"))
	if did == "" || cid == "" {
		WriteJSONError(w, http.StatusBadRequest, "did and cid query parameters are required")
		return
	}
	sess, err := c.createSession(r.Context())
	if err != nil {
		if err == types.ErrUnauthorized {
			WriteJSONError(w, http.StatusUnauthorized, "no valid Bluesky credentials — set X-Bluesky-Handle and X-Bluesky-App-Password")
			return
		}
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	u := TrimRightSlash(c.BlueskyUpstreamBase()) + "/xrpc/com.atproto.sync.getBlob?did=" +
		url.QueryEscape(did) + "&cid=" + url.QueryEscape(cid)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+sess.AccessJwt)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		WriteJSONError(w, http.StatusUnauthorized, "bluesky rejected the blob request")
		return
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("bluesky getBlob returned %d: %s", resp.StatusCode, truncateBytes(body, 200)))
		return
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
	logger.Debug("served bluesky blob", "cid", cid, "did", did)
}

func (c *BlueskyConnector) handleAuthorFeedProxy(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	c.handleXRPCGet(w, r, "/xrpc/app.bsky.feed.getAuthorFeed", logger)
}

func (c *BlueskyConnector) handleXRPCGet(w http.ResponseWriter, r *http.Request, path string, logger *slog.Logger) {
	sess, err := c.createSession(r.Context())
	if err != nil {
		if err == types.ErrUnauthorized {
			WriteJSONError(w, http.StatusUnauthorized, "no valid Bluesky credentials — set X-Bluesky-Handle and X-Bluesky-App-Password")
			return
		}
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	u := TrimRightSlash(c.BlueskyUpstreamBase()) + path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+sess.AccessJwt)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		WriteJSONError(w, http.StatusUnauthorized, "bluesky rejected the request")
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
	logger.Debug("bluesky xrpc get", "path", path, "status", resp.StatusCode, "bytes", len(body))
}

// BlueskyCredentials holds the app-password pair from service headers.
type BlueskyCredentials struct {
	Handle      string
	AppPassword string
}

// BlueskyCredentialsFromHeaders extracts handle + app password from configured
// service headers. Both are required; Aegis never echoes them back.
func BlueskyCredentialsFromHeaders(headers map[string]string) (BlueskyCredentials, bool) {
	var cred BlueskyCredentials
	for k, v := range headers {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case strings.ToLower(blueskyHandleHeader), "handle":
			cred.Handle = strings.TrimSpace(v)
		case strings.ToLower(blueskyPassHeader), "app_password", "bluesky_app_password":
			cred.AppPassword = strings.TrimSpace(v)
		}
	}
	return cred, cred.Handle != "" && cred.AppPassword != ""
}

func (c *BlueskyConnector) credentials() (BlueskyCredentials, bool) {
	svc := c.host.ServiceConfig(BlueskyServiceID)
	if svc == nil {
		return BlueskyCredentials{}, false
	}
	return BlueskyCredentialsFromHeaders(svc.Headers)
}

type blueskySession struct {
	AccessJwt string `json:"accessJwt"`
	DID       string `json:"did"`
	Handle    string `json:"handle"`
}

type blueskyBlobRef struct {
	Link string `json:"$link"`
}

type blueskyBlob struct {
	Type     string         `json:"$type"`
	Ref      blueskyBlobRef `json:"ref"`
	MimeType string         `json:"mimeType"`
	Size     int            `json:"size"`
}

type blueskyUploadBlobResponse struct {
	Blob blueskyBlob `json:"blob"`
}

type blueskyCreateRecordResponse struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// createSession exchanges the app password for a short-lived access JWT.
func (c *BlueskyConnector) createSession(ctx context.Context) (blueskySession, error) {
	cred, ok := c.credentials()
	if !ok {
		return blueskySession{}, types.ErrUnauthorized
	}
	body, err := json.Marshal(map[string]string{
		"identifier": cred.Handle,
		"password":   cred.AppPassword,
	})
	if err != nil {
		return blueskySession{}, err
	}
	endpoint := TrimRightSlash(c.BlueskyUpstreamBase()) + "/xrpc/com.atproto.server.createSession"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return blueskySession{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return blueskySession{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return blueskySession{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return blueskySession{}, types.ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return blueskySession{}, fmt.Errorf("bluesky createSession returned %d: %s", resp.StatusCode, truncateBytes(data, 200))
	}
	var sess blueskySession
	if err := json.Unmarshal(data, &sess); err != nil {
		return blueskySession{}, fmt.Errorf("decode bluesky session: %w", err)
	}
	if sess.AccessJwt == "" || sess.DID == "" {
		return blueskySession{}, fmt.Errorf("bluesky session missing accessJwt/did")
	}
	return sess, nil
}

// SharePhoto implements types.PhotoShareTarget. It uploads the image blob and
// creates an app.bsky.feed.post with an images embed. Bytes are used only for
// this call.
func (c *BlueskyConnector) SharePhoto(ctx context.Context, req types.PhotoShareRequest) (types.PhotoShareResult, error) {
	if len(req.Data) == 0 {
		return types.PhotoShareResult{}, fmt.Errorf("no photo bytes to share")
	}
	if len(req.Data) > blueskyImageMaxBytes {
		return types.PhotoShareResult{}, fmt.Errorf(
			"photo is %d bytes; Bluesky image embeds cap at %d — share a smaller derivative",
			len(req.Data), blueskyImageMaxBytes)
	}
	mime := strings.TrimSpace(req.MimeType)
	if mime == "" {
		mime = "image/jpeg"
	}
	if !strings.HasPrefix(mime, "image/") {
		return types.PhotoShareResult{}, fmt.Errorf("bluesky share requires an image/* mime type, got %q", mime)
	}

	logger := slog.With("service", BlueskyServiceID)
	sess, err := c.createSession(ctx)
	if err != nil {
		return types.PhotoShareResult{}, err
	}

	blob, err := c.uploadBlob(ctx, sess.AccessJwt, req.Data, mime)
	if err != nil {
		return types.PhotoShareResult{}, err
	}

	alt := strings.TrimSpace(req.Alt)
	if alt == "" {
		alt = strings.TrimSpace(req.Caption)
	}
	if alt == "" {
		alt = "Shared photo"
	}
	text := strings.TrimSpace(req.Caption)
	createdAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	record := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      text,
		"createdAt": createdAt,
		"embed": map[string]any{
			"$type": "app.bsky.embed.images",
			"images": []any{
				map[string]any{
					"alt":   alt,
					"image": blob,
				},
			},
		},
	}
	// Keep a machine-readable hash in the post language field's absence —
	// the API response claim is authoritative; no noisy hashtag required.
	createBody, err := json.Marshal(map[string]any{
		"repo":       sess.DID,
		"collection": "app.bsky.feed.post",
		"record":     record,
	})
	if err != nil {
		return types.PhotoShareResult{}, err
	}

	endpoint := TrimRightSlash(c.BlueskyUpstreamBase()) + "/xrpc/com.atproto.repo.createRecord"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(createBody))
	if err != nil {
		return types.PhotoShareResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+sess.AccessJwt)

	resp, err := HTTPClient.Do(httpReq)
	if err != nil {
		return types.PhotoShareResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return types.PhotoShareResult{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return types.PhotoShareResult{}, types.ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return types.PhotoShareResult{}, fmt.Errorf("bluesky createRecord returned %d: %s", resp.StatusCode, truncateBytes(data, 200))
	}
	var created blueskyCreateRecordResponse
	if err := json.Unmarshal(data, &created); err != nil {
		return types.PhotoShareResult{}, fmt.Errorf("decode bluesky createRecord: %w", err)
	}
	if created.URI == "" {
		return types.PhotoShareResult{}, fmt.Errorf("bluesky createRecord returned no uri")
	}

	result := types.PhotoShareResult{
		Service:  BlueskyServiceID,
		PostURI:  created.URI,
		PostID:   rkeyFromATURI(created.URI),
		PostURL:  blueskyPostURL(sess.Handle, created.URI),
		SharedAt: createdAt,
	}
	logger.Info("shared photo to bluesky",
		"post_uri", result.PostURI,
		"bytes", len(req.Data),
		"sha256", req.ContentSHA256)
	return result, nil
}

func (c *BlueskyConnector) uploadBlob(ctx context.Context, accessJWT string, data []byte, mime string) (blueskyBlob, error) {
	endpoint := TrimRightSlash(c.BlueskyUpstreamBase()) + "/xrpc/com.atproto.repo.uploadBlob"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return blueskyBlob{}, err
	}
	req.Header.Set("Content-Type", mime)
	req.Header.Set("Authorization", "Bearer "+accessJWT)

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return blueskyBlob{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return blueskyBlob{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return blueskyBlob{}, types.ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return blueskyBlob{}, fmt.Errorf("bluesky uploadBlob returned %d: %s", resp.StatusCode, truncateBytes(body, 200))
	}
	var out blueskyUploadBlobResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return blueskyBlob{}, fmt.Errorf("decode bluesky uploadBlob: %w", err)
	}
	if out.Blob.Ref.Link == "" {
		return blueskyBlob{}, fmt.Errorf("bluesky uploadBlob returned empty blob ref")
	}
	if out.Blob.Type == "" {
		out.Blob.Type = "blob"
	}
	if out.Blob.MimeType == "" {
		out.Blob.MimeType = mime
	}
	if out.Blob.Size == 0 {
		out.Blob.Size = len(data)
	}
	return out.Blob, nil
}

// rkeyFromATURI pulls the record key from at://did/collection/rkey.
func rkeyFromATURI(uri string) string {
	parts := strings.Split(strings.TrimSuffix(uri, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// blueskyPostURL builds the public web URL for a post.
func blueskyPostURL(handle, atURI string) string {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		handle = didFromPostURI(atURI)
	}
	rkey := rkeyFromATURI(atURI)
	if handle == "" || rkey == "" {
		return ""
	}
	return "https://bsky.app/profile/" + url.PathEscape(handle) + "/post/" + url.PathEscape(rkey)
}

func truncateBytes(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// SharePhotoSHA256 is the claim helper used by core: lowercase hex of the bytes.
func SharePhotoSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
