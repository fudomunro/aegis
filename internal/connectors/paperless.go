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
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// PaperlessConnector lists self-hosted Paperless-ngx documents as neutral
// Document metadata (issue #143). Document bytes stay on Paperless — the
// foreign/local canonical store. Auth is an API token in service headers
// (Authorization: Token …); no OAuth flow is required. Aegis does not OCR
// or store files; excerpt is a short preview of Paperless content and full
// text is fetched only for the gated text endpoint.
type PaperlessConnector struct {
	host Host
}

// NewPaperlessConnector creates a connector wired to the given Host.
func NewPaperlessConnector(host Host) *PaperlessConnector {
	return &PaperlessConnector{host: host}
}

const (
	PaperlessServiceID   = "paperless"
	PaperlessServiceName = "Paperless-ngx"
	// paperlessPageSize is the per-request document page size.
	paperlessPageSize = 100
	// paperlessExcerptRunes is the max excerpt length in runes.
	paperlessExcerptRunes = 280
)

// PaperlessUpstreamBase returns the configured Paperless API base (…/api).
func (c *PaperlessConnector) PaperlessUpstreamBase() string {
	return c.host.UpstreamBaseFor(PaperlessServiceID, "http://localhost:8000/api")
}

// PaperlessWebBase derives the Paperless web UI origin from the API endpoint
// (https://host:8000/api → https://host:8000) for deep links.
func (c *PaperlessConnector) PaperlessWebBase() string {
	base := strings.TrimSuffix(c.PaperlessUpstreamBase(), "/api")
	base = strings.TrimSuffix(base, "/api/")
	return strings.TrimRight(base, "/")
}

// ApplyPaperlessAuth copies configured static headers onto the request.
// An Authorization header is used as-is (Token/Bearer); otherwise the value
// is sent as Authorization: Token …, matching Paperless-ngx API tokens.
func (c *PaperlessConnector) ApplyPaperlessAuth(req *http.Request, logger *slog.Logger) {
	if svc := c.host.ServiceConfig(PaperlessServiceID); svc != nil {
		for k, v := range svc.Headers {
			req.Header.Set(k, v)
		}
		if req.Header.Get("Authorization") != "" {
			return
		}
	}
	if tok := c.paperlessToken(logger); tok != "" {
		lower := strings.ToLower(tok)
		if strings.HasPrefix(lower, "token ") || strings.HasPrefix(lower, "bearer ") {
			req.Header.Set("Authorization", tok)
		} else {
			req.Header.Set("Authorization", "Token "+tok)
		}
	}
}

func (c *PaperlessConnector) paperlessToken(logger *slog.Logger) string {
	if svc := c.host.ServiceConfig(PaperlessServiceID); svc != nil {
		for k, v := range svc.Headers {
			if strings.EqualFold(k, "Authorization") && strings.TrimSpace(v) != "" {
				v = strings.TrimSpace(v)
				lower := strings.ToLower(v)
				if strings.HasPrefix(lower, "token ") {
					return strings.TrimSpace(v[len("token "):])
				}
				if strings.HasPrefix(lower, "bearer ") {
					return strings.TrimSpace(v[len("bearer "):])
				}
				return v
			}
			if strings.EqualFold(k, "X-Api-Key") && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return c.host.GetValidToken(PaperlessServiceID, logger)
}

// Capabilities returns Paperless document operations reachable through the proxy.
func (c *PaperlessConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "documents",
				Description: "Self-hosted Paperless-ngx documents — metadata + short excerpt; files stay on Paperless (foreign/local canonical)",
				Operations: []types.Operation{
					{
						Name:        "list_documents",
						Description: "List documents (permission key: documents)",
						Method:      "GET",
						Path:        "/documents",
						Parameters: []types.Parameter{
							{Name: "page", In: "query", Type: "integer", Description: "1-based page", Default: 1},
							{Name: "page_size", In: "query", Type: "integer", Description: "Page size (default 100)", Default: 100},
						},
						Response: types.ResponseSpec{
							Description: "Paperless document list; use GET /v1/library/documents for neutral Document values",
							Fields:      []string{"count", "next", "results"},
						},
					},
					{
						Name:        "get_document",
						Description: "Fetch one document including OCR content (permission key: documents)",
						Method:      "GET",
						Path:        "/documents/{id}",
						Parameters: []types.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Paperless document id", Required: true},
						},
					},
					{
						Name:        "download_document",
						Description: "Download original file bytes (permission key: documents)",
						Method:      "GET",
						Path:        "/documents/{id}/download",
						Parameters: []types.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Paperless document id", Required: true},
						},
					},
					{
						Name:        "list_tags",
						Description: "List Paperless tags",
						Method:      "GET",
						Path:        "/tags",
					},
				},
			},
		},
	}
}

// SystemInstructions returns the agent hint when Paperless is referenced.
func (c *PaperlessConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Paperless-ngx (receipts, warranties, leases, letters), use Aegis's unified data API. Do not call Paperless directly or ask the user for an API token.

GET %s/v1/library/documents — document metadata from every connected source (title, dates, tags, correspondent; excerpt only when documents=read). Filter with q/tag/after/before.

GET %s/v1/library/documents/{service}/{id}/text — full extracted text for one document. Requires documents=read; metadata access is list-only.

Document bytes stay on Paperless. Use sources[].blob_url / sources[].url to reach the file. Aegis does not store or OCR documents.`, root, root)
}

// HandleProxy forwards Paperless API paths, attaching the configured token.
// Binary download paths are passed through without JSON rewriting.
func (c *PaperlessConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", PaperlessServiceID, "method", r.Method, "path", remainingPath)

	if remainingPath == "" {
		remainingPath = "/"
	}

	upstream := c.PaperlessUpstreamBase() + remainingPath
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}

	isBinary := strings.HasSuffix(remainingPath, "/download") ||
		strings.HasSuffix(remainingPath, "/preview") ||
		strings.HasSuffix(remainingPath, "/thumb")

	var body io.Reader
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, body)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to build Paperless request")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	c.ApplyPaperlessAuth(req, logger)

	resp, err := HTTPClient.Do(req)
	if err != nil {
		logger.Error("paperless request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "Paperless request failed")
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "failed to read Paperless response")
		return
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		WriteJSONError(w, resp.StatusCode,
			"Paperless rejected the request — set Authorization: Token <api-token> on the paperless service")
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
		WriteJSONError(w, resp.StatusCode, fmt.Sprintf("Paperless returned %d: %s", resp.StatusCode, msg))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
}

// paperlessPage is the DRF-style paginated list envelope.
type paperlessPage struct {
	Count    int             `json:"count"`
	Next     string          `json:"next"`
	Previous string          `json:"previous"`
	Results  json.RawMessage `json:"results"`
}

// paperlessDocument is the Paperless-ngx document serializer subset we map.
type paperlessDocument struct {
	ID                   int             `json:"id"`
	Title                string          `json:"title"`
	Content              string          `json:"content"`
	Correspondent        json.RawMessage `json:"correspondent"`
	DocumentType         json.RawMessage `json:"document_type"`
	Tags                 json.RawMessage `json:"tags"`
	Created              string          `json:"created"`
	Modified             string          `json:"modified"`
	Added                string          `json:"added"`
	OriginalFileName     string          `json:"original_file_name"`
	ArchiveSerialNumber  json.RawMessage `json:"archive_serial_number"`
	MimeType             string          `json:"mime_type"`
	Checksum             string          `json:"checksum"`
	PageCount            json.RawMessage `json:"page_count"`
}

// paperlessNamed is a tiny id/name row (tags, correspondents, types).
type paperlessNamed struct {
	ID   json.RawMessage `json:"id"`
	Name string          `json:"name"`
}

func (c *PaperlessConnector) apiGet(ctx context.Context, logger *slog.Logger, path string, query url.Values, out any) error {
	u := c.PaperlessUpstreamBase() + path
	if encoded := query.Encode(); encoded != "" {
		u += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	c.ApplyPaperlessAuth(req, logger)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return types.ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("paperless returned %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

// lookupNames fetches an id→name map for tags/correspondents/document types.
func (c *PaperlessConnector) lookupNames(ctx context.Context, logger *slog.Logger, path string) map[string]string {
	out := map[string]string{}
	var page paperlessPage
	q := url.Values{}
	q.Set("page_size", "500")
	if err := c.apiGet(ctx, logger, path, q, &page); err != nil {
		return out
	}
	var rows []paperlessNamed
	if err := json.Unmarshal(page.Results, &rows); err != nil {
		return out
	}
	for _, row := range rows {
		out[paperlessIDString(row.ID)] = row.Name
	}
	return out
}

func paperlessIDString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return strconv.FormatInt(int64(n), 10)
	}
	return strings.Trim(string(raw), `"`)
}

func paperlessIDList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var many []json.RawMessage
	if err := json.Unmarshal(raw, &many); err == nil {
		var out []string
		for _, item := range many {
			if id := paperlessIDString(item); id != "" {
				out = append(out, id)
			}
		}
		return out
	}
	if id := paperlessIDString(raw); id != "" {
		return []string{id}
	}
	return nil
}

func paperlessExcerpt(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	runes := []rune(content)
	if len(runes) <= paperlessExcerptRunes {
		return content
	}
	return strings.TrimSpace(string(runes[:paperlessExcerptRunes])) + "…"
}

func normalizePaperlessTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	// Date-only values from Paperless.
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return raw
}

// NeutralDocument maps one Paperless row to a neutral Document.
func (c *PaperlessConnector) NeutralDocument(doc paperlessDocument, tagNames, corrNames, typeNames map[string]string) types.Document {
	id := strconv.Itoa(doc.ID)
	web := c.PaperlessWebBase()
	src := types.DocumentSource{
		Service:     PaperlessServiceID,
		ServiceName: PaperlessServiceName,
		ID:          id,
		URL:         web + "/documents/" + id + "/details",
		Canonical:   true,
		BlobURL:     c.PaperlessUpstreamBase() + "/documents/" + id + "/download",
		Extra:       map[string]any{},
	}
	if doc.Checksum != "" {
		src.Extra["checksum"] = doc.Checksum
	}
	if asn := paperlessIDString(doc.ArchiveSerialNumber); asn != "" {
		src.Extra["archive_serial"] = asn
	}

	var tags []string
	for _, tid := range paperlessIDList(doc.Tags) {
		name := tagNames[tid]
		if name == "" {
			name = tid
		}
		tags = append(tags, name)
	}

	corr := ""
	for _, cid := range paperlessIDList(doc.Correspondent) {
		if name := corrNames[cid]; name != "" {
			corr = name
			break
		}
	}
	docType := ""
	for _, tid := range paperlessIDList(doc.DocumentType) {
		if name := typeNames[tid]; name != "" {
			docType = name
			break
		}
	}

	entityID := doc.Checksum
	if entityID == "" {
		entityID = paperlessIDString(doc.ArchiveSerialNumber)
	}

	mime := strings.TrimSpace(doc.MimeType)
	if mime == "" {
		mime = mimeFromFilename(doc.OriginalFileName)
	}

	d := types.Document{
		Title:         strings.TrimSpace(doc.Title),
		MimeType:      mime,
		CreatedAt:     normalizePaperlessTime(doc.Created),
		ModifiedAt:    normalizePaperlessTime(doc.Modified),
		Correspondent: corr,
		DocumentType:  docType,
		Tags:          tags,
		Excerpt:       paperlessExcerpt(doc.Content),
		EntityID:      entityID,
		Sources:       []types.DocumentSource{src},
	}
	if corr != "" {
		d.People = []types.Contact{{Name: corr}}
	}
	if d.Title == "" {
		d.Title = doc.OriginalFileName
	}
	return d
}

func mimeFromFilename(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".tif"), strings.HasSuffix(lower, ".tiff"):
		return "image/tiff"
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain"
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return "text/html"
	default:
		return ""
	}
}

// ListUserDocuments implements DocumentProvider. It pages GET /api/documents
// and maps rows to neutral Document values with a short excerpt only.
func (c *PaperlessConnector) ListUserDocuments(ctx context.Context) ([]types.Document, bool, error) {
	logger := slog.Default()
	tagNames := c.lookupNames(ctx, logger, "/tags")
	corrNames := c.lookupNames(ctx, logger, "/correspondents")
	typeNames := c.lookupNames(ctx, logger, "/document_types")

	var out []types.Document
	truncated := false
	nextPath := "/documents"
	query := url.Values{}
	query.Set("page_size", strconv.Itoa(paperlessPageSize))
	query.Set("ordering", "-created")

	for page := 0; page < LibraryPageCap; page++ {
		var envelope paperlessPage
		path := nextPath
		q := query
		if page > 0 {
			// Follow DRF "next" URLs relative to the API base.
			if nextPath == "" {
				break
			}
			// next may be absolute; keep only path+query after /api.
			if u, err := url.Parse(nextPath); err == nil && u.Path != "" {
				p := u.Path
				if i := strings.Index(p, "/api/"); i >= 0 {
					p = p[i+len("/api"):]
				}
				path = p
				q = u.Query()
			}
		}
		if err := c.apiGet(ctx, logger, path, q, &envelope); err != nil {
			if len(out) > 0 && err == types.ErrUnauthorized {
				return out, truncated, err
			}
			return out, truncated, err
		}
		var rows []paperlessDocument
		if err := json.Unmarshal(envelope.Results, &rows); err != nil {
			return out, truncated, err
		}
		for _, row := range rows {
			out = append(out, c.NeutralDocument(row, tagNames, corrNames, typeNames))
		}
		nextPath = envelope.Next
		if nextPath == "" {
			break
		}
		if page == LibraryPageCap-1 && nextPath != "" {
			truncated = true
		}
	}
	return out, truncated, nil
}

// FetchDocumentText implements DocumentProvider. It returns Paperless OCR
// content for one document (issue #143). Callers must gate this behind
// documents=read — never metadata.
func (c *PaperlessConnector) FetchDocumentText(ctx context.Context, id string) (string, bool, error) {
	logger := slog.Default()
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false, fmt.Errorf("empty document id")
	}
	var doc paperlessDocument
	if err := c.apiGet(ctx, logger, "/documents/"+url.PathEscape(id), nil, &doc); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(doc.Content), false, nil
}
