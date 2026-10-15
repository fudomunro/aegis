package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// blueskyLibraryCap bounds one list call so a huge feed cannot stall the
// library aggregate. Callers can re-run with a cursor later.
const blueskyLibraryCap = 100

// blueskyAuthorFeedFilter prefers original posts with media when listing
// photos; notifications cover messages.
const (
	blueskyFeedFilterPostsNoReplies = "posts_no_replies"
	blueskyFeedFilterWithMedia      = "posts_with_media"
)

type blueskyFeedView struct {
	Cursor string `json:"cursor"`
	Feed   []struct {
		Post blueskyPostView `json:"post"`
	} `json:"feed"`
}

type blueskyPostView struct {
	URI    string `json:"uri"`
	CID    string `json:"cid"`
	Record struct {
		Text      string `json:"text"`
		CreatedAt string `json:"createdAt"`
	} `json:"record"`
	// Embed may be the raw record embed (image.ref.$link) or the hydrated
	// view form (app.bsky.embed.images#view with thumb/fullsize CDN URLs).
	Embed *struct {
		Type   string `json:"$type"`
		Images []struct {
			Alt         string `json:"alt"`
			Thumb       string `json:"thumb"`
			Fullsize    string `json:"fullsize"`
			AspectRatio *struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"aspectRatio"`
			Image struct {
				MimeType string          `json:"mimeType"`
				Size     int             `json:"size"`
				Ref      json.RawMessage `json:"ref"`
			} `json:"image"`
		} `json:"images"`
	} `json:"embed"`
}

// blobCIDFromImageRef accepts the AT Proto blob ref shapes:
// {"$link":"bafy…"} (IPLD JSON), a bare CID string, or empty when only the
// hydrated view URLs are present.
func blobCIDFromImageRef(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			return strings.TrimSpace(str)
		}
	}
	var obj struct {
		Link string `json:"$link"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && strings.TrimSpace(obj.Link) != "" {
		return strings.TrimSpace(obj.Link)
	}
	return ""
}

// blobCIDFromURL pulls the blob CID out of a Bluesky CDN URL:
// https://cdn.bsky.app/img/feed_fullsize/plain/did:plc:…/bafkrei…[@ext]
func blobCIDFromURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	path := u
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if i := strings.Index(last, "@"); i >= 0 {
		last = last[:i]
	}
	return last
}

func didFromPostURI(postURI string) string {
	// at:// URIs are not host:port URLs — Go's url.Parse splits "did:plc:…"
	// at the first colon. Take the authority as the segment before the first "/".
	s := strings.TrimSpace(postURI)
	s = strings.TrimPrefix(s, "at://")
	if i := strings.Index(s, "/"); i >= 0 {
		return s[:i]
	}
	return s
}

func didFromBlobURLs(fullsize, thumb, postURI string) string {
	for _, u := range []string{fullsize, thumb} {
		// …/plain/did:plc:xxx/bafkrei…
		parts := strings.Split(strings.Trim(u, "/"), "/")
		for i, p := range parts {
			if p == "plain" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return didFromPostURI(postURI)
}

type blueskyNotificationList struct {
	Cursor        string                `json:"cursor"`
	Notifications []blueskyNotification `json:"notifications"`
}

type blueskyNotification struct {
	URI        string `json:"uri"`
	CID        string `json:"cid"`
	Reason     string `json:"reason"`
	ReasonSubj string `json:"reasonSubject"`
	IsRead     bool   `json:"isRead"`
	IndexedAt  string `json:"indexedAt"`
	Author     struct {
		DID         string `json:"did"`
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Record json.RawMessage `json:"record"`
}

// ListUserPhotos implements types.PhotoProvider. It lists the user's own
// Bluesky image posts (metadata only — bytes stay on the PDS / CDN).
func (c *BlueskyConnector) ListUserPhotos(ctx context.Context) ([]types.Photo, bool, error) {
	logger := slog.With("service", BlueskyServiceID)
	sess, err := c.createSession(ctx)
	if err != nil {
		return nil, false, err
	}

	var all []types.Photo
	truncated := false
	cursor := ""
	for page := 0; page < LibraryPageCap; page++ {
		q := url.Values{}
		q.Set("actor", sess.Handle)
		q.Set("limit", strconv.Itoa(blueskyLibraryCap))
		q.Set("filter", blueskyFeedFilterWithMedia)
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		data, err := c.authedGet(ctx, sess.AccessJwt, "/xrpc/app.bsky.feed.getAuthorFeed", q)
		if err != nil {
			return nil, false, err
		}
		var feed blueskyFeedView
		if err := json.Unmarshal(data, &feed); err != nil {
			return nil, false, fmt.Errorf("decode bluesky author feed: %w", err)
		}
		for _, item := range feed.Feed {
			all = append(all, c.photosFromPost(item.Post)...)
		}
		if feed.Cursor == "" {
			return all, truncated, nil
		}
		if page == LibraryPageCap-1 {
			truncated = true
		}
		cursor = feed.Cursor
	}
	logger.Debug("listed bluesky photos", "count", len(all), "truncated", truncated)
	return all, truncated, nil
}

// photosFromPost maps one post's image embeds to neutral Photo values.
func (c *BlueskyConnector) photosFromPost(post blueskyPostView) []types.Photo {
	if post.Embed == nil || len(post.Embed.Images) == 0 {
		return nil
	}
	if !strings.HasPrefix(post.Embed.Type, "app.bsky.embed.images") {
		return nil
	}
	rkey := rkeyFromATURI(post.URI)
	did := didFromPostURI(post.URI)
	proxyRoot := strings.TrimRight(c.host.BaseURL(), "/") + "/v1/services/" + BlueskyServiceID
	web := blueskyPostURL("", post.URI)

	out := make([]types.Photo, 0, len(post.Embed.Images))
	for i, img := range post.Embed.Images {
		blobCID := blobCIDFromImageRef(img.Image.Ref)
		if blobCID == "" {
			blobCID = blobCIDFromURL(img.Fullsize)
		}
		if blobCID == "" {
			blobCID = blobCIDFromURL(img.Thumb)
		}
		imgDID := didFromBlobURLs(img.Fullsize, img.Thumb, post.URI)
		if imgDID == "" {
			imgDID = did
		}
		mime := strings.TrimSpace(img.Image.MimeType)
		if mime == "" {
			mime = "image/jpeg"
		}
		id := rkey
		if i > 0 {
			id = rkey + "#" + strconv.Itoa(i)
		}
		src := types.PhotoSource{
			Service:     BlueskyServiceID,
			ServiceName: BlueskyServiceName,
			ID:          id,
			URL:         web,
			// Bytes stay on the Bluesky PDS. This is not the photo's
			// archival home when it was shared from Immich/Google.
			Canonical: false,
			Extra: map[string]any{
				"post_uri": post.URI,
				"post_cid": post.CID,
				"blob_cid": blobCID,
				"alt":      img.Alt,
			},
		}
		if blobCID != "" && imgDID != "" {
			src.BlobURL = proxyRoot + "/xrpc/com.atproto.sync.getBlob?did=" +
				url.QueryEscape(imgDID) + "&cid=" + url.QueryEscape(blobCID)
			src.ThumbnailURL = img.Thumb
		} else if img.Fullsize != "" {
			// Fall back to the public CDN URL when the blob CID is unknown.
			src.BlobURL = img.Fullsize
			src.ThumbnailURL = img.Thumb
		}
		if img.Image.Size > 0 {
			src.Extra["byte_size"] = img.Image.Size
		}
		width, height := 0, 0
		if img.AspectRatio != nil {
			width, height = img.AspectRatio.Width, img.AspectRatio.Height
		}
		// content_hash is left empty — the library merge can still group
		// by taken_at + dimensions when they are known.
		p := types.Photo{
			TakenAt:  types.NormalizePhotoTakenAt(post.Record.CreatedAt),
			Width:    width,
			Height:   height,
			MimeType: mime,
			Filename: "bsky-" + rkey + "-" + strconv.Itoa(i) + mimeExt(mime),
			Caption:  strings.TrimSpace(post.Record.Text),
			Sources:  []types.PhotoSource{src},
		}
		if img.Alt != "" && p.Caption == "" {
			p.Caption = img.Alt
		}
		out = append(out, p)
	}
	return out
}

func mimeExt(mime string) string {
	switch strings.ToLower(mime) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}

// ListMessages implements types.MessageProvider. It maps Bluesky
// notifications (likes, replies, mentions, follows) into neutral messages so
// /v1/library/messages can include in-network activity.
func (c *BlueskyConnector) ListMessages(ctx context.Context) ([]types.Message, bool, error) {
	sess, err := c.createSession(ctx)
	if err != nil {
		return nil, false, err
	}

	var all []types.Message
	truncated := false
	cursor := ""
	for page := 0; page < LibraryPageCap; page++ {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(blueskyLibraryCap))
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		data, err := c.authedGet(ctx, sess.AccessJwt, "/xrpc/app.bsky.notification.listNotifications", q)
		if err != nil {
			return nil, false, err
		}
		var list blueskyNotificationList
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, false, fmt.Errorf("decode bluesky notifications: %w", err)
		}
		for _, n := range list.Notifications {
			all = append(all, c.neutralMessage(n, sess.Handle))
		}
		if list.Cursor == "" {
			return all, truncated, nil
		}
		if page == LibraryPageCap-1 {
			truncated = true
		}
		cursor = list.Cursor
	}
	return all, truncated, nil
}

func (c *BlueskyConnector) neutralMessage(n blueskyNotification, selfHandle string) types.Message {
	subject := blueskyNotificationSubject(n.Reason)
	snippet := strings.TrimSpace(string(n.Record))
	// Record is a generic JSON blob; pull text out for a readable snippet.
	if strings.HasPrefix(snippet, "{") {
		var rec struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(n.Record, &rec); err == nil && strings.TrimSpace(rec.Text) != "" {
			snippet = strings.TrimSpace(rec.Text)
		} else {
			snippet = subject
		}
	}
	if snippet == "" {
		snippet = subject
	}

	fromName := n.Author.DisplayName
	if fromName == "" {
		fromName = n.Author.Handle
	}
	from := types.NewContact(BlueskyServiceID, BlueskyServiceName, n.Author.DID, fromName, nil, nil)
	from.Handles = []string{n.Author.Handle}

	sentAt := types.NormalizePhotoTakenAt(n.IndexedAt)
	src := types.MessageSource{
		Service:     BlueskyServiceID,
		ServiceName: BlueskyServiceName,
		ID:          n.URI,
		ThreadID:    n.ReasonSubj,
		URL:         blueskyPostURL(n.Author.Handle, n.URI),
		Canonical:   true,
		Extra: map[string]any{
			"reason":         n.Reason,
			"notification":   true,
			"reason_subject": n.ReasonSubj,
		},
	}

	msg := types.Message{
		Channel:    types.MessageChannelSocial,
		Subject:    subject,
		Snippet:    snippet,
		From:       &from,
		SentAt:     sentAt,
		ReceivedAt: sentAt,
		Direction:  "inbound",
		Read:       n.IsRead,
		ThreadID:   n.ReasonSubj,
		EntityID:   n.URI,
		Labels:     []string{n.Reason, "bluesky"},
		Sources:    []types.MessageSource{src},
		Extra: map[string]any{
			"self_handle": selfHandle,
			"reason":      n.Reason,
		},
	}
	return msg
}

func blueskyNotificationSubject(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "like":
		return "Liked your post"
	case "like-via-repost":
		return "Liked your repost"
	case "repost":
		return "Reposted your post"
	case "repost-via-repost":
		return "Reposted your repost"
	case "follow":
		return "Started following you"
	case "mention":
		return "Mentioned you"
	case "reply":
		return "Replied to your post"
	case "quote":
		return "Quoted your post"
	case "starterpack-joined":
		return "Joined via your starter pack"
	default:
		if reason == "" {
			return "Bluesky notification"
		}
		return "Bluesky " + reason
	}
}

// authedGet calls a PDS xrpc endpoint with the session JWT.
func (c *BlueskyConnector) authedGet(ctx context.Context, accessJWT, path string, query url.Values) ([]byte, error) {
	u := TrimRightSlash(c.BlueskyUpstreamBase()) + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessJWT)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, types.ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bluesky %s returned %d: %s", path, resp.StatusCode, truncateBytes(data, 200))
	}
	return data, nil
}
