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
	Cursor string            `json:"cursor"`
	Feed   []blueskyFeedItem `json:"feed"`
}

// blueskyFeedItem is one author-feed row. Reason is set for reposts
// (app.bsky.feed.defs#reasonRepost) wrapping someone else's post.
type blueskyFeedItem struct {
	Post            blueskyPostView `json:"post"`
	Reason          json.RawMessage `json:"reason"`
	ReasonIndexedAt string          `json:"reasonIndexedAt"`
}

type blueskyPostView struct {
	URI    string `json:"uri"`
	CID    string `json:"cid"`
	Author struct {
		DID    string `json:"did"`
		Handle string `json:"handle"`
	} `json:"author"`
	Record struct {
		Type      string `json:"$type"`
		Text      string `json:"text"`
		CreatedAt string `json:"createdAt"`
		Reply     *struct {
			Parent struct {
				URI string `json:"uri"`
				CID string `json:"cid"`
			} `json:"parent"`
			Root struct {
				URI string `json:"uri"`
				CID string `json:"cid"`
			} `json:"root"`
		} `json:"reply"`
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

// blueskyOwnPostsMaxPages bounds message listing so a long history cannot
// turn into an unbounded walk of the author feed.
const blueskyOwnPostsMaxPages = 25

// ListMessages implements types.MessageProvider. It lists only the user's
// own Bluesky writing — original posts and replies they authored — and
// intentionally skips reposts, likes, follows, and other non-authored
// activity.
func (c *BlueskyConnector) ListMessages(ctx context.Context) ([]types.Message, bool, error) {
	logger := slog.With("service", BlueskyServiceID)
	sess, err := c.createSession(ctx)
	if err != nil {
		return nil, false, err
	}

	var all []types.Message
	truncated := false
	cursor := ""
	for page := 0; page < blueskyOwnPostsMaxPages; page++ {
		q := url.Values{}
		q.Set("actor", sess.Handle)
		q.Set("limit", strconv.Itoa(blueskyLibraryCap))
		// Prefer posts + replies; still filter reposts client-side.
		q.Set("filter", "posts_with_replies")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		data, err := c.authedGet(ctx, sess.AccessJwt, "/xrpc/app.bsky.feed.getAuthorFeed", q)
		if err != nil {
			// Partial results are better than failing the whole provider
			// when the PDS/appview briefly 502s mid-pagination.
			if len(all) > 0 {
				logger.Warn("bluesky author feed page failed; keeping partial", "error", err, "count", len(all))
				return all, true, nil
			}
			return nil, false, err
		}
		var feed blueskyFeedView
		if err := json.Unmarshal(data, &feed); err != nil {
			if len(all) > 0 {
				return all, true, nil
			}
			return nil, false, fmt.Errorf("decode bluesky author feed: %w", err)
		}
		for _, item := range feed.Feed {
			if msg, ok := c.neutralOwnPost(item, sess); ok {
				all = append(all, msg)
			}
		}
		if feed.Cursor == "" {
			break
		}
		if page == blueskyOwnPostsMaxPages-1 {
			truncated = true
		}
		cursor = feed.Cursor
	}
	logger.Debug("listed bluesky own posts/replies", "count", len(all), "truncated", truncated)
	return all, truncated, nil
}

// neutralOwnPost maps one author-feed item to a Message only when the user
// wrote it (original post or reply). Reposts and other people's content are
// skipped.
func (c *BlueskyConnector) neutralOwnPost(item blueskyFeedItem, sess blueskySession) (types.Message, bool) {
	// reasonRepost (and similar) wraps someone else's post — not our writing.
	if item.Reason != nil {
		return types.Message{}, false
	}
	post := item.Post
	authorHandle := post.Author.Handle
	authorDID := post.Author.DID
	if authorHandle != sess.Handle && authorDID != sess.DID {
		return types.Message{}, false
	}

	recordType := post.Record.Type
	if recordType != "" && recordType != "app.bsky.feed.post" {
		return types.Message{}, false
	}

	text := strings.TrimSpace(post.Record.Text)
	isReply := post.Record.Reply != nil
	kind := "post"
	subject := subjectFromPostText(text, "Bluesky post")
	if isReply {
		kind = "reply"
		subject = subjectFromPostText(text, "Bluesky reply")
	}

	sentAt := types.NormalizePhotoTakenAt(post.Record.CreatedAt)
	if sentAt == "" {
		sentAt = types.NormalizePhotoTakenAt(item.ReasonIndexedAt)
	}
	rkey := rkeyFromATURI(post.URI)
	web := blueskyPostURL(sess.Handle, post.URI)

	self := types.NewContact(BlueskyServiceID, BlueskyServiceName, sess.DID, sess.Handle, nil, nil)
	self.Handles = []string{sess.Handle}

	src := types.MessageSource{
		Service:     BlueskyServiceID,
		ServiceName: BlueskyServiceName,
		ID:          post.URI,
		URL:         web,
		Mailbox:     kind,
		Canonical:   true,
		Extra: map[string]any{
			"rkey":     rkey,
			"post_cid": post.CID,
			"kind":     kind,
			"post_uri": post.URI,
		},
	}
	if isReply && post.Record.Reply != nil {
		src.ThreadID = post.Record.Reply.Parent.URI
		src.Extra["reply_parent_uri"] = post.Record.Reply.Parent.URI
		src.Extra["reply_root_uri"] = post.Record.Reply.Root.URI
	}

	msg := types.Message{
		Channel:    types.MessageChannelSocial,
		Subject:    subject,
		Snippet:    text,
		Body:       text,
		From:       &self,
		SentAt:     sentAt,
		ReceivedAt: sentAt,
		// Our own writing — outbound from the library's point of view.
		Direction: "outbound",
		Read:      true,
		ThreadID:  src.ThreadID,
		EntityID:  post.URI,
		Labels:    []string{kind, "bluesky", "authored"},
		Sources:   []types.MessageSource{src},
		Extra: map[string]any{
			"kind": kind,
		},
	}
	return msg, true
}

func subjectFromPostText(text, fallback string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return fallback
	}
	// First line, capped so list views stay readable.
	if i := strings.IndexAny(t, "\r\n"); i > 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	if len(t) > 80 {
		t = t[:77] + "…"
	}
	return t
}

// ListUserContacts implements types.ContactProvider. It maps the user's
// Bluesky following list into neutral contacts (handle + display name + DID).
func (c *BlueskyConnector) ListUserContacts(ctx context.Context) ([]types.Contact, bool, error) {
	logger := slog.With("service", BlueskyServiceID)
	sess, err := c.createSession(ctx)
	if err != nil {
		return nil, false, err
	}

	var all []types.Contact
	truncated := false
	cursor := ""
	for page := 0; page < LibraryPageCap; page++ {
		q := url.Values{}
		q.Set("actor", sess.Handle)
		q.Set("limit", strconv.Itoa(blueskyLibraryCap))
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		data, err := c.authedGet(ctx, sess.AccessJwt, "/xrpc/app.bsky.graph.getFollows", q)
		if err != nil {
			return nil, false, err
		}
		var list blueskyFollowList
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, false, fmt.Errorf("decode bluesky follows: %w", err)
		}
		for _, f := range list.Follows {
			if ct := c.neutralContact(f); ct.Name != "" || len(ct.Handles) > 0 {
				all = append(all, ct)
			}
		}
		if list.Cursor == "" {
			break
		}
		if page == LibraryPageCap-1 {
			truncated = true
		}
		cursor = list.Cursor
	}
	logger.Debug("listed bluesky contacts", "count", len(all), "truncated", truncated)
	return all, truncated, nil
}

type blueskyFollowList struct {
	Cursor  string           `json:"cursor"`
	Follows []blueskyProfile `json:"follows"`
}

type blueskyProfile struct {
	DID         string `json:"did"`
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Avatar      string `json:"avatar"`
}

func (c *BlueskyConnector) neutralContact(f blueskyProfile) types.Contact {
	name := strings.TrimSpace(f.DisplayName)
	if name == "" {
		name = strings.TrimSpace(f.Handle)
	}
	ct := types.NewContact(BlueskyServiceID, BlueskyServiceName, f.DID, name, nil, nil)
	if h := strings.TrimSpace(f.Handle); h != "" {
		ct.Handles = []string{h}
	}
	src := types.ContactSource{
		Service:     BlueskyServiceID,
		ServiceName: BlueskyServiceName,
		ID:          f.DID,
		Name:        name,
		Canonical:   true,
		Extra: map[string]any{
			"handle":      f.Handle,
			"did":         f.DID,
			"profile_url": "https://bsky.app/profile/" + url.PathEscape(f.Handle),
		},
	}
	if f.Avatar != "" {
		src.Extra["avatar"] = f.Avatar
	}
	ct.Sources = []types.ContactSource{src}
	if d := strings.TrimSpace(f.Description); d != "" {
		ct.Extra = map[string]any{"description": d}
	}
	return ct
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
