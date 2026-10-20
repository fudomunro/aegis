package connectors

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
	ImmichServiceID   = "immich"
	ImmichServiceName = "Immich"
	// immichPageSize is the per-request asset page size for search/metadata.
	immichPageSize = 250
)

// ImmichUpstreamBase returns the configured Immich API base (…/api).
func (c *ImmichConnector) ImmichUpstreamBase() string {
	return c.host.UpstreamBaseFor(ImmichServiceID, "http://localhost:2283/api")
}

// ImmichWebBase derives the Immich web UI origin from the API endpoint
// (https://host:2283/api → https://host:2283) for deep links.
func (c *ImmichConnector) ImmichWebBase() string {
	base := strings.TrimSuffix(c.ImmichUpstreamBase(), "/api")
	return strings.TrimRight(base, "/")
}

// ApplyImmichAuth copies configured static headers (API key) onto the request.
// An OAuth token, if present, is also sent as a Bearer fallback.
func (c *ImmichConnector) ApplyImmichAuth(req *http.Request, logger *slog.Logger) {
	if svc := c.host.ServiceConfig(ImmichServiceID); svc != nil {
		for k, v := range svc.Headers {
			req.Header.Set(k, v)
		}
	}
	if tok := c.host.GetValidToken(ImmichServiceID, logger); tok != "" {
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
	return fmt.Sprintf(`When the user's request involves Immich (self-hosted photos), use Aegis's unified data API. Do not call Immich directly or ask the user for an API key.

GET %s/v1/library/photos — photo metadata from every connected photo service, including Immich (bytes stay on Immich)

Photos are metadata-only. Use sources[].blob_url to fetch bytes (Aegis attaches the Immich API key). Immich holds the authoritative copy (canonical home).

Byte transport only (not a general Immich API):
GET %s/v1/services/immich/assets/{id}/original — original bytes
GET %s/v1/services/immich/assets/{id}/thumbnail — thumbnail bytes

%s`, root, root, root, types.PhotoSourceDiscipline())
}

// HandleProxy forwards Immich API paths, attaching the configured API key.
// Binary download paths are passed through without JSON rewriting.
func (c *ImmichConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", ImmichServiceID, "method", r.Method, "path", remainingPath)

	if remainingPath == "" {
		remainingPath = "/"
	}

	upstream := c.ImmichUpstreamBase() + remainingPath
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
		WriteJSONError(w, http.StatusInternalServerError, "failed to build Immich request")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	c.ApplyImmichAuth(req, logger)

	resp, err := HTTPClient.Do(req)
	if err != nil {
		logger.Error("immich request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "Immich request failed")
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "failed to read Immich response")
		return
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		WriteJSONError(w, resp.StatusCode,
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
		WriteJSONError(w, resp.StatusCode, fmt.Sprintf("Immich returned %d: %s", resp.StatusCode, msg))
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
	upstream := c.ImmichUpstreamBase() + "/search/metadata"

	var all []types.Photo
	truncated := false
	nextPage := 1
	for page := 0; page < LibraryPageCap; page++ {
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
		c.ApplyImmichAuth(req, logger)

		resp, err := HTTPClient.Do(req)
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
			if p := c.NeutralPhoto(asset); p.TakenAt != "" || p.Filename != "" || len(p.Sources) > 0 {
				all = append(all, p)
			}
		}

		if raw.Assets.NextPage.raw == nil {
			return all, false, nil
		}
		next, ok := raw.Assets.NextPage.AsInt()
		if !ok || next <= 0 {
			return all, false, nil
		}
		if page == LibraryPageCap-1 {
			truncated = true
		}
		nextPage = next
	}
	return all, truncated, nil
}

// NeutralPhoto maps one Immich asset to Aegis's Photo shape.
func (c *ImmichConnector) NeutralPhoto(a immichAsset) types.Photo {
	taken := ""
	if a.Exif.DateTimeOriginal != "" {
		taken = a.Exif.DateTimeOriginal
	} else if a.FileCreatedAt != "" {
		taken = a.FileCreatedAt
	}
	taken = types.NormalizePhotoTakenAt(taken)

	mime := ImmichMimeForType(a.Type, a.OriginalFileName)
	filename := a.OriginalFileName
	if filename == "" {
		filename = a.ID
	}

	proxyRoot := strings.TrimRight(c.host.BaseURL(), "/") + "/v1/services/" + ImmichServiceID
	src := types.PhotoSource{
		Service:      ImmichServiceID,
		ServiceName:  ImmichServiceName,
		ID:           a.ID,
		URL:          c.ImmichWebBase() + "/photos/" + a.ID,
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

// FetchPhotoBytes implements types.PhotoBytesProvider for the photo-share /
// ownership-claim PoC (issue #20). It downloads one asset original from
// Immich and returns the bytes for this call only — Aegis still does not
// store photo bytes on disk or in cache.
func (c *ImmichConnector) FetchPhotoBytes(ctx context.Context, id string) ([]byte, string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "", fmt.Errorf("immich asset id is required")
	}
	upstream := c.ImmichUpstreamBase() + "/assets/" + url.PathEscape(id) + "/original"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		return nil, "", err
	}
	c.ApplyImmichAuth(req, slog.Default())

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	// Cap at Bluesky's image embed limit plus slack so a huge original cannot
	// exhaust memory during a share. Callers surface a clearer size error.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, "", types.ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return nil, "", fmt.Errorf("immich asset original returned %d: %s", resp.StatusCode, msg)
	}
	mime := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if mime == "" || mime == "application/octet-stream" {
		mime = ImmichMimeForType("", "")
	}
	return data, mime, nil
}

func ImmichMimeForType(assetType, filename string) string {
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

func (p immichPage) AsInt() (int, bool) {
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
