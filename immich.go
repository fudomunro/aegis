package main

import (
	"bytes"
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

// ImmichConnector exposes a self-hosted Immich library as neutral Photo
// metadata. Photo bytes stay on Immich — the foreign/local canonical store
// (issue #16 phase 4 / issue #21). Auth is an API key in service headers
// (typically X-API-Key); no OAuth flow is required.
type ImmichConnector struct {
	host Host
}

// NewImmichConnector creates a connector wired to the given Host (issue #15 seam).
func NewImmichConnector(host Host) *ImmichConnector {
	return &ImmichConnector{host: host}
}

const (
	immichServiceID   = "immich"
	immichServiceName = "Immich"
	// immichPageSize is the per-request asset page size for search/metadata.
	immichPageSize = 250
)

// immichUpstreamBase returns the configured Immich API base (…/api).
func (c *ImmichConnector) immichUpstreamBase() string {
	return c.host.UpstreamBaseFor(immichServiceID, "http://localhost:2283/api")
}

// immichWebBase derives the Immich web UI origin from the API endpoint
// (https://host:2283/api → https://host:2283) for deep links.
func (c *ImmichConnector) immichWebBase() string {
	base := strings.TrimSuffix(c.immichUpstreamBase(), "/api")
	return strings.TrimRight(base, "/")
}

// applyImmichAuth copies configured static headers (API key) onto the request.
// An OAuth token, if present, is also sent as a Bearer fallback.
func (c *ImmichConnector) applyImmichAuth(req *http.Request, logger *slog.Logger) {
	if svc := c.host.ServiceConfig(immichServiceID); svc != nil {
		for k, v := range svc.Headers {
			req.Header.Set(k, v)
		}
	}
	if tok := c.host.GetValidToken(immichServiceID, logger); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// Capabilities returns Immich photo operations reachable through the proxy.
func (c *ImmichConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "photos",
				Description: "Self-hosted Immich library — metadata only; bytes stay on Immich (foreign/local canonical)",
				Operations: []types.Operation{
					{
						Name:        "list_assets",
						Description: "List photo/video assets via Immich search/metadata (permission key: photos)",
						Method:      "POST",
						Path:        "/search/metadata",
						Parameters: []types.Parameter{
							{Name: "page", In: "body", Type: "integer", Description: "1-based page", Default: 1},
							{Name: "size", In: "body", Type: "integer", Description: "Page size (default 250)", Default: 250},
						},
						Response: types.ResponseSpec{
							Description: "Immich search response; use GET /v1/library/photos for neutral Photo values",
							Fields:      []string{"assets.total", "assets.items", "assets.nextPage"},
						},
					},
					{
						Name:        "get_asset_original",
						Description: "Download original bytes for one asset (permission key: photos)",
						Method:      "GET",
						Path:        "/assets/{id}/original",
						Parameters: []types.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Immich asset UUID", Required: true},
						},
					},
					{
						Name:        "get_asset_thumbnail",
						Description: "Download a thumbnail for one asset (permission key: photos)",
						Method:      "GET",
						Path:        "/assets/{id}/thumbnail",
						Parameters: []types.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Immich asset UUID", Required: true},
						},
					},
					{
						Name:        "list_albums",
						Description: "List Immich albums",
						Method:      "GET",
						Path:        "/albums",
						Response: types.ResponseSpec{
							Description: "Immich album list",
							Fields:      []string{"id", "albumName", "assetCount"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the agent hint when Immich is referenced.
func (c *ImmichConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	proxy := root + "/v1/services/" + immichServiceID
	return fmt.Sprintf(`When the user's request involves Immich (self-hosted photos), you MUST use the Aegis proxy at %s instead of calling Immich directly. Aegis adds the Immich API key (X-API-Key header from config) — do not ask the user for it.

Preferred unified library first:
GET %s/v1/library/photos — all photo metadata from every connected photo service, including Immich (bytes stay on Immich).

Immich-specific endpoints (call the full proxy URL; Aegis authenticates you):
POST /search/metadata — {"page":1,"size":250,"withExif":true} list assets
GET /assets/{id}/original — download original bytes for one asset
GET /assets/{id}/thumbnail — thumbnail bytes
GET /albums — list albums

Photos are metadata-only in the library. Use sources[].blob_url (Aegis proxy URL) to fetch bytes; Immich holds the authoritative copy (foreign canonical).
Always include the full proxy base URL in your requests.

%s`, proxy, root, photoSourceDiscipline())
}

// HandleProxy forwards Immich API paths, attaching the configured API key.
// Binary download paths are passed through without JSON rewriting.
func (c *ImmichConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", immichServiceID, "method", r.Method, "path", remainingPath)

	if remainingPath == "" {
		remainingPath = "/"
	}

	upstream := c.immichUpstreamBase() + remainingPath
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}

	// Binary passthrough for original/thumbnail downloads.
	isBinary := strings.HasSuffix(remainingPath, "/original") ||
		strings.HasSuffix(remainingPath, "/thumbnail") ||
		strings.HasSuffix(remainingPath, "/download")

	var body io.Reader
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build Immich request")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	c.applyImmichAuth(req, logger)

	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		logger.Error("immich request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "Immich request failed")
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to read Immich response")
		return
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		writeJSONError(w, resp.StatusCode,
			"Immich rejected the request — set an API key in service headers (X-API-Key)")
		return
	}

	if isBinary {
		if cd := resp.Header.Get("Content-Disposition"); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		}
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
		return
	}

	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 240 {
			msg = msg[:240] + "…"
		}
		writeJSONError(w, resp.StatusCode, fmt.Sprintf("Immich returned %d: %s", resp.StatusCode, msg))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
}

// ListUserPhotos implements PhotoProvider. It pages Immich POST /search/metadata
// and maps assets to neutral Photo values with canonical sources pointing at
// Immich (via Aegis proxy download URLs).
func (c *ImmichConnector) ListUserPhotos(ctx context.Context) ([]types.Photo, bool, error) {
	logger := slog.Default()
	upstream := c.immichUpstreamBase() + "/search/metadata"

	var all []types.Photo
	truncated := false
	nextPage := 1
	for page := 0; page < libraryPageCap; page++ {
		payload := map[string]any{
			"page":     nextPage,
			"size":     immichPageSize,
			"withExif": true,
			"order":    "desc",
		}
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, false, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(buf))
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Content-Type", "application/json")
		c.applyImmichAuth(req, logger)

		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, false, types.ErrUnauthorized
		}
		if resp.StatusCode != http.StatusOK {
			msg := strings.TrimSpace(string(data))
			if len(msg) > 200 {
				msg = msg[:200] + "…"
			}
			return nil, false, fmt.Errorf("immich search/metadata returned %d: %s", resp.StatusCode, msg)
		}

		var raw immichSearchResponse
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, fmt.Errorf("decode immich search response: %w", err)
		}

		for _, asset := range raw.Assets.Items {
			if p := c.neutralPhoto(asset); p.TakenAt != "" || p.Filename != "" || len(p.Sources) > 0 {
				all = append(all, p)
			}
		}

		if raw.Assets.NextPage.raw == nil {
			return all, false, nil
		}
		next, ok := raw.Assets.NextPage.asInt()
		if !ok || next <= 0 {
			return all, false, nil
		}
		if page == libraryPageCap-1 {
			truncated = true
		}
		nextPage = next
	}
	return all, truncated, nil
}

// neutralPhoto maps one Immich asset to Aegis's Photo shape.
func (c *ImmichConnector) neutralPhoto(a immichAsset) types.Photo {
	taken := ""
	if a.Exif.DateTimeOriginal != "" {
		taken = a.Exif.DateTimeOriginal
	} else if a.FileCreatedAt != "" {
		taken = a.FileCreatedAt
	}
	taken = normalizePhotoTakenAt(taken)

	mime := immichMimeForType(a.Type, a.OriginalFileName)
	filename := a.OriginalFileName
	if filename == "" {
		filename = a.ID
	}

	proxyRoot := strings.TrimRight(c.host.BaseURL(), "/") + "/v1/services/" + immichServiceID
	src := types.PhotoSource{
		Service:      immichServiceID,
		ServiceName:  immichServiceName,
		ID:           a.ID,
		URL:          c.immichWebBase() + "/photos/" + a.ID,
		Canonical:    true,
		BlobURL:      proxyRoot + "/assets/" + url.PathEscape(a.ID) + "/original",
		ThumbnailURL: proxyRoot + "/assets/" + url.PathEscape(a.ID) + "/thumbnail",
		Extra: map[string]any{
			"asset_type": a.Type,
		},
	}
	if a.Checksum != "" {
		src.Extra["content_hash"] = strings.ToLower(a.Checksum)
		src.Extra["checksum"] = strings.ToLower(a.Checksum)
	}

	p := types.Photo{
		TakenAt:  taken,
		Width:    a.Exif.ExifImageWidth,
		Height:   a.Exif.ExifImageHeight,
		MimeType: mime,
		Filename: filename,
		Caption:  strings.TrimSpace(a.Exif.Description),
		Sources:  []types.PhotoSource{src},
	}
	return p
}

func immichMimeForType(assetType, filename string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".heic"), strings.HasSuffix(lower, ".heif"):
		return "image/heic"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(lower, ".mov"):
		return "video/quicktime"
	}
	if strings.EqualFold(assetType, "VIDEO") {
		return "video/mp4"
	}
	return "image/jpeg"
}

// immichSearchResponse is the subset of Immich's search payload we need.
type immichSearchResponse struct {
	Assets struct {
		Total    int           `json:"total"`
		Count    int           `json:"count"`
		Items    []immichAsset `json:"items"`
		NextPage immichPage    `json:"nextPage"`
	} `json:"assets"`
}

// immichPage accepts string, number, or null for Immich's nextPage field.
type immichPage struct {
	raw any
}

func (p *immichPage) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		p.raw = nil
		return nil
	}
	if err := json.Unmarshal(b, &p.raw); err != nil {
		return err
	}
	return nil
}

func (p immichPage) String() string {
	if p.raw == nil {
		return ""
	}
	switch v := p.raw.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func (p immichPage) asInt() (int, bool) {
	s := strings.TrimSpace(p.String())
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// immichAsset is the Immich AssetResponseDto subset we map.
type immichAsset struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	OriginalFileName string `json:"originalFileName"`
	FileCreatedAt    string `json:"fileCreatedAt"`
	Checksum         string `json:"checksum"`
	Exif             struct {
		DateTimeOriginal string `json:"dateTimeOriginal"`
		ExifImageWidth   int    `json:"exifImageWidth"`
		ExifImageHeight  int    `json:"exifImageHeight"`
		Description      string `json:"description"`
	} `json:"exifInfo"`
}
