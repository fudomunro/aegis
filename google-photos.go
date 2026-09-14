package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// GooglePhotosConnector proxies the Google Photos Library API and implements
// PhotoProvider. It lists media-item metadata only — photo bytes stay on
// Google Photos (non-Guardian canonical, issue #16).
type GooglePhotosConnector struct {
	server *Server
}

// NewGooglePhotosConnector creates a connector wired to the given server.
func NewGooglePhotosConnector(srv *Server) *GooglePhotosConnector {
	return &GooglePhotosConnector{server: srv}
}

const googlePhotosServiceID = "google-photos"

// googlePhotosUpstreamBase returns the configured Photos Library API base.
func (c *GooglePhotosConnector) googlePhotosUpstreamBase() string {
	const defaultBase = "https://photoslibrary.googleapis.com/v1"
	if svc := c.server.config.GetService(googlePhotosServiceID); svc != nil && svc.Endpoint != "" {
		return strings.TrimRight(svc.Endpoint, "/")
	}
	return defaultBase
}

// Capabilities returns what Google Photos data types and operations are supported.
func (c *GooglePhotosConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "photos",
				Description: "Photo library — list media items (metadata only; bytes stay on Google Photos)",
				Operations: []Operation{
					{
						Name:        "read_list",
						Description: "List the user's photos as neutral metadata (paginated). Permission key: photos.",
						Method:      "GET",
						Path:        "/mediaItems",
						Parameters: []Parameter{
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page (1-100)", Default: 100},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{
							Description: "Neutral photos: taken_at, width, height, mime_type, filename, caption, sources[] (service, id, url, canonical, blob_url)",
							Fields:      []string{"photos", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single media item's metadata by ID",
						Method:      "GET",
						Path:        "/mediaItems/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Google Photos media item ID", Required: true},
						},
						Response: ResponseSpec{
							Description: "Photo metadata with service-local id, product URL, dimensions",
							Fields:      []string{"id", "filename", "mimeType", "mediaMetadata", "productUrl"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Google Photos is referenced.
func (c *GooglePhotosConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	base := root + "/v1/services/" + googlePhotosServiceID
	return fmt.Sprintf(`When the user's request involves Google Photos or their photo library, you MUST use the Guardian proxy at %s instead of calling Google Photos directly.

For "my photos" across every connected photo service, prefer the unified library endpoint first:
GET %s/v1/library/photos — photo metadata in one neutral list (bytes stay on the source service; sources[] carry canonical blob locations)

Google Photos-specific endpoints (call the full proxy URL; Guardian authenticates you):
GET /mediaItems — list media items (neutral photo metadata)
GET /mediaItems/{id} — one media item

Photos are metadata-only. Guardian never downloads or stores image bytes. Use sources[].url / sources[].blob_url to point the user at the original on Google Photos. blob_url values from Google expire (often within about an hour) — do not cache them as permanent links.
Always include the full proxy base URL in your requests.`, base, root)
}

// HandleProxy processes a Google Photos Library API request through the connector.
func (c *GooglePhotosConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", googlePhotosServiceID, "method", r.Method, "path", remainingPath)

	token := c.server.getValidToken(googlePhotosServiceID, logger)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"no valid Google Photos token — authorize at /v1/auth/"+googlePhotosServiceID+"/login")
		return
	}

	// Agent-facing neutral library paths.
	if remainingPath == "/mediaItems" || remainingPath == "/mediaItems/" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleListMediaItems(w, r, token)
		return
	}
	if strings.HasPrefix(remainingPath, "/mediaItems/") {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimPrefix(remainingPath, "/mediaItems/")
		id = strings.Trim(id, "/")
		if id == "" || strings.Contains(id, "/") {
			writeJSONError(w, http.StatusBadRequest, "missing media item id")
			return
		}
		c.handleGetMediaItem(w, r, token, id)
		return
	}

	// Generic authenticated pass-through for other Photos Library paths.
	upstreamURL := c.googlePhotosUpstreamBase() + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		logger.Error("failed to build upstream request", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := guardianHTTPClient.Do(req)
	if err != nil {
		logger.Error("upstream request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "Google Photos request failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (c *GooglePhotosConnector) handleListMediaItems(w http.ResponseWriter, r *http.Request, token string) {
	photos, truncated, err := c.ListUserPhotos(r.Context())
	if err != nil {
		if err == ErrUnauthorized {
			writeJSONError(w, http.StatusUnauthorized,
				"no valid Google Photos token — authorize at /v1/auth/"+googlePhotosServiceID+"/login")
			return
		}
		slog.Warn("google-photos list failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch photos from Google Photos")
		return
	}
	if photos == nil {
		photos = []Photo{}
	}
	result := PhotoList{
		Photos: photos,
		Total:  len(photos),
		Synced: true,
		ByService: map[string]LibraryServiceStatus{
			googlePhotosServiceID: {
				Status:    LibraryStatusOK,
				Count:     len(photos),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build photos list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GooglePhotosConnector) handleGetMediaItem(w http.ResponseWriter, r *http.Request, token, id string) {
	logger := slog.With("service", googlePhotosServiceID, "method", r.Method, "path", "/mediaItems/"+id)
	upstreamURL := c.googlePhotosUpstreamBase() + "/mediaItems/" + id
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		logger.Error("failed to build media item request", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := guardianHTTPClient.Do(req)
	if err != nil {
		logger.Error("media item request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "Google Photos request failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to read Google Photos response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
		return
	}
	var item googlePhotosMediaItem
	if err := json.Unmarshal(data, &item); err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to parse Google Photos response")
		return
	}
	photo := mapGooglePhotosItem(item)
	out, err := json.Marshal(photo)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to encode photo")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// ListUserPhotos implements PhotoProvider. Fetches media items and maps them
// into neutral Photo values. Bytes are never downloaded — only metadata and
// the short-lived base/product URLs Google exposes.
func (c *GooglePhotosConnector) ListUserPhotos(ctx context.Context) ([]Photo, bool, error) {
	token := c.server.getValidToken(googlePhotosServiceID, slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	upstreamBase := c.googlePhotosUpstreamBase()
	var all []Photo
	pageToken := ""
	truncated := false
	for page := 0; page < libraryPageCap; page++ {
		u := upstreamBase + "/mediaItems?pageSize=100"
		if pageToken != "" {
			u += "&pageToken=" + pageToken
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := guardianHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("google photos mediaItems returned %d", resp.StatusCode)
		}
		var raw googlePhotosMediaItemList
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		for _, item := range raw.MediaItems {
			p := mapGooglePhotosItem(item)
			if len(p.Sources) > 0 && p.Sources[0].ID != "" {
				all = append(all, p)
			}
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			break
		}
		if page == libraryPageCap-1 && pageToken != "" {
			truncated = true
		}
	}
	return all, truncated, nil
}

// googlePhotosMediaItem is the subset of a Photos Library media item we use.
type googlePhotosMediaItem struct {
	ID            string                 `json:"id"`
	Description   string                 `json:"description"`
	ProductURL    string                 `json:"productUrl"`
	BaseURL       string                 `json:"baseUrl"`
	MimeType      string                 `json:"mimeType"`
	Filename      string                 `json:"filename"`
	MediaMetadata *googlePhotosMediaMeta `json:"mediaMetadata"`
}

type googlePhotosMediaMeta struct {
	CreationTime string `json:"creationTime"`
	Width        string `json:"width"`
	Height       string `json:"height"`
	Photo        *struct {
		CameraMake  string `json:"cameraMake"`
		CameraModel string `json:"cameraModel"`
	} `json:"photo"`
	Video *struct{} `json:"video"`
}

type googlePhotosMediaItemList struct {
	MediaItems    []googlePhotosMediaItem `json:"mediaItems"`
	NextPageToken string                  `json:"nextPageToken"`
}

func parsePhotosDimension(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func mapGooglePhotosItem(item googlePhotosMediaItem) Photo {
	p := newPhoto(
		googlePhotosServiceID,
		"Google Photos",
		item.ID,
		item.Filename,
		item.MimeType,
		"",
		0,
		0,
	)
	if p.Filename == "" {
		p.Filename = item.Description
	}
	p.Caption = item.Description
	if len(p.Sources) > 0 {
		p.Sources[0].URL = item.ProductURL
		p.Sources[0].BlobURL = item.BaseURL
		p.Sources[0].ThumbnailURL = item.BaseURL
		if item.BaseURL != "" {
			// Google's baseUrl is a short-lived signed URL (~1h). Record that
			// so agents do not treat it as a permanent link.
			p.Sources[0].Extra = map[string]any{
				"blob_url_expires": "approx 1h",
			}
		}
	}
	if md := item.MediaMetadata; md != nil {
		p.TakenAt = md.CreationTime
		// Normalize to RFC3339 UTC when parseable.
		if t, err := time.Parse(time.RFC3339Nano, md.CreationTime); err == nil {
			p.TakenAt = t.UTC().Format(time.RFC3339)
		}
		p.Width = parsePhotosDimension(md.Width)
		p.Height = parsePhotosDimension(md.Height)
		if md.Photo != nil {
			extra := map[string]any{}
			if md.Photo.CameraMake != "" {
				extra["camera_make"] = md.Photo.CameraMake
			}
			if md.Photo.CameraModel != "" {
				extra["camera_model"] = md.Photo.CameraModel
			}
			if len(extra) > 0 {
				p.Extra = extra
			}
		}
	}
	return p
}
