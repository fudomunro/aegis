package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// GoogleDriveConnector lists Google Docs from Drive as neutral Document
// metadata (issue #143). Content is pulled via Drive files.export as HTML or
// plain text — markdown conversion is deferred. Document bytes stay on Google
// Drive (non-Aegis canonical). Auth is the shared Google connection OAuth
// token (drive.readonly covers list + export).
type GoogleDriveConnector struct {
	host Host
}

// NewGoogleDriveConnector creates a connector wired to the given Host.
func NewGoogleDriveConnector(host Host) *GoogleDriveConnector {
	return &GoogleDriveConnector{host: host}
}

const (
	GoogleDriveServiceID   = "google-drive"
	GoogleDriveServiceName = "Google Drive"
	// googleDocsMime is the native Google Docs file type on Drive.
	googleDocsMime = "application/vnd.google-apps.document"
	// googleDriveListPageSize is the Drive files.list page size.
	googleDriveListPageSize = 100
	// googleDriveDefaultExportMime is the agent-facing text export for
	// FetchDocumentText. HTML is also available via the content proxy.
	googleDriveDefaultExportMime = "text/plain"
	// googleDriveHTMLExportMime preserves structure for a later markdown pass.
	googleDriveHTMLExportMime = "text/html"
	// googleDriveListFields is the Drive partial-response field mask for
	// neutral Document mapping (no file bytes).
	googleDriveListFields = "nextPageToken,files(id,name,mimeType,createdTime,modifiedTime,description,owners(displayName,emailAddress),webViewLink,shortcutDetails(targetId))"
)

// GoogleDriveUpstreamBase returns the configured Drive API base.
func (c *GoogleDriveConnector) GoogleDriveUpstreamBase() string {
	return c.host.UpstreamBaseFor(GoogleDriveServiceID, "https://www.googleapis.com/drive/v3")
}

// Capabilities returns Drive document operations reachable through the proxy.
func (c *GoogleDriveConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "documents",
				Description: "Google Docs via Drive — metadata + HTML/plain-text export; files stay on Drive (non-Aegis canonical)",
				Operations: []types.Operation{
					{
						Name:        "list_documents",
						Description: "List Google Docs (permission key: documents)",
						Method:      "GET",
						Path:        "/files",
						Parameters: []types.Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Drive query (default: Google Docs, not trashed)"},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Page size (default 100)", Default: googleDriveListPageSize},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token"},
						},
						Response: types.ResponseSpec{
							Description: "Drive file list; use GET /v1/library/documents for neutral Document values",
							Fields:      []string{"files", "nextPageToken"},
						},
					},
					{
						Name:        "export_document",
						Description: "Export Google Docs content as HTML or plain text (permission key: documents)",
						Method:      "GET",
						Path:        "/files/{id}/export",
						Parameters: []types.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Drive file ID", Required: true},
							{Name: "mimeType", In: "query", Type: "string", Description: "text/html or text/plain", Default: googleDriveDefaultExportMime},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the agent hint when Google Docs/Drive is referenced.
func (c *GoogleDriveConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Google Docs or paperwork stored in Drive, use Aegis's unified data API. Do not call the Drive API directly or ask the user for OAuth.

GET %s/v1/library/documents — document metadata from every connected source (Google Docs contribute title, dates, owner). Filter with q/tag/after/before.

GET %s/v1/library/documents/{service}/{id}/text — full document text for one doc (plain-text export). Requires documents=read.

Content export is HTML or plain text via Drive files.export — not markdown yet. Files stay on Google Drive; use sources[].url / sources[].blob_url to open or export the original.`, root, root)
}

// HandleProxy forwards Drive API paths, attaching the Google OAuth token.
// Export paths return the export body (HTML or plain text) as-is.
func (c *GoogleDriveConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", GoogleDriveServiceID, "method", r.Method, "path", remainingPath)
	token := c.host.GetValidToken(GoogleDriveServiceID, logger)
	if token == "" {
		WriteJSONError(w, http.StatusUnauthorized,
			"no valid Google Drive token — authorize at /v1/auth/"+GoogleDriveServiceID+"/login")
		return
	}
	if remainingPath == "" {
		remainingPath = "/"
	}
	upstream := c.GoogleDriveUpstreamBase() + remainingPath
	if encoded := r.URL.Query().Encode(); encoded != "" {
		upstream += "?" + encoded
	}

	isExport := strings.Contains(remainingPath, "/export")
	var body io.Reader
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, body)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to build Google Drive request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		logger.Error("google drive request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "Google Drive request failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "failed to read Google Drive response")
		return
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		WriteJSONError(w, resp.StatusCode,
			"Google Drive rejected the request — re-authorize at /v1/auth/"+GoogleDriveServiceID+"/login with drive.readonly")
		return
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 240 {
			msg = msg[:240] + "…"
		}
		WriteJSONError(w, resp.StatusCode, fmt.Sprintf("Google Drive returned %d: %s", resp.StatusCode, msg))
		return
	}
	if isExport {
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			if strings.Contains(r.URL.Query().Get("mimeType"), "html") {
				ct = "text/html; charset=utf-8"
			} else {
				ct = "text/plain; charset=utf-8"
			}
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
}

// googleDriveFile is the Drive files.list row subset we map.
type googleDriveFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MimeType    string `json:"mimeType"`
	CreatedTime string `json:"createdTime"`
	ModifiedTime string `json:"modifiedTime"`
	Description string `json:"description"`
	WebViewLink string `json:"webViewLink"`
	Owners      []struct {
		DisplayName string `json:"displayName"`
		Email       string `json:"emailAddress"`
	} `json:"owners"`
	ShortcutDetails *struct {
		TargetID string `json:"targetId"`
	} `json:"shortcutDetails"`
}

type googleDriveListResponse struct {
	Files         []googleDriveFile `json:"files"`
	NextPageToken string            `json:"nextPageToken"`
}

func (c *GoogleDriveConnector) apiGet(ctx context.Context, logger *slog.Logger, path string, query url.Values, out any) error {
	u := c.GoogleDriveUpstreamBase() + path
	if encoded := query.Encode(); encoded != "" {
		u += "?" + encoded
	}
	token := c.host.GetValidToken(GoogleDriveServiceID, logger)
	if token == "" {
		return types.ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
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
		return fmt.Errorf("google drive returned %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

// NeutralDocument maps one Drive file to a neutral Document.
func (c *GoogleDriveConnector) NeutralDocument(f googleDriveFile) types.Document {
	src := types.DocumentSource{
		Service:     GoogleDriveServiceID,
		ServiceName: GoogleDriveServiceName,
		ID:          f.ID,
		URL:         f.WebViewLink,
		Canonical:   true,
		BlobURL:     c.GoogleDriveUpstreamBase() + "/files/" + url.PathEscape(f.ID) + "/export?mimeType=" + url.QueryEscape(googleDriveHTMLExportMime),
	}
	d := types.Document{
		Title:      strings.TrimSpace(f.Name),
		MimeType:   f.MimeType,
		CreatedAt:  f.CreatedTime,
		ModifiedAt: f.ModifiedTime,
		// Drive has no Paperless-style document_type; native Docs are the type.
		DocumentType: "google-doc",
		Excerpt:      strings.TrimSpace(f.Description),
		EntityID:     f.ID,
		Sources:      []types.DocumentSource{src},
	}
	if f.MimeType == "" {
		d.MimeType = googleDocsMime
	}
	for _, o := range f.Owners {
		name := strings.TrimSpace(o.DisplayName)
		if name == "" {
			name = strings.TrimSpace(o.Email)
		}
		if name == "" {
			continue
		}
		if d.Correspondent == "" {
			d.Correspondent = name
		}
		d.People = append(d.People, types.Contact{
			Name:   name,
			Emails: nonEmptyString(o.Email),
		})
	}
	return d
}

func nonEmptyString(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return []string{s}
}

// ListUserDocuments implements DocumentProvider. It pages Drive files.list
// for Google Docs and maps them to neutral Document values. Listing is
// metadata-only — content is fetched only on FetchDocumentText / export.
func (c *GoogleDriveConnector) ListUserDocuments(ctx context.Context) ([]types.Document, bool, error) {
	logger := slog.Default()
	var out []types.Document
	truncated := false
	pageToken := ""

	for page := 0; page < LibraryPageCap; page++ {
		q := url.Values{}
		q.Set("q", "mimeType='"+googleDocsMime+"' and trashed=false")
		q.Set("pageSize", fmt.Sprint(googleDriveListPageSize))
		q.Set("fields", googleDriveListFields)
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var list googleDriveListResponse
		if err := c.apiGet(ctx, logger, "/files", q, &list); err != nil {
			return out, truncated, err
		}
		for _, f := range list.Files {
			out = append(out, c.NeutralDocument(f))
		}
		pageToken = list.NextPageToken
		if pageToken == "" {
			break
		}
		if page == LibraryPageCap-1 {
			truncated = true
		}
	}
	return out, truncated, nil
}

// FetchDocumentText implements DocumentProvider. It exports Google Docs as
// plain text (HTML is available via the content proxy). Callers must gate
// this behind documents=read — never metadata. Markdown conversion is
// deferred (issue #143 follow-up).
func (c *GoogleDriveConnector) FetchDocumentText(ctx context.Context, id string) (string, bool, error) {
	logger := slog.Default()
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false, fmt.Errorf("empty document id")
	}
	text, err := c.exportFile(ctx, logger, id, googleDriveDefaultExportMime)
	if err != nil {
		return "", false, err
	}
	return text, false, nil
}

// ExportFileHTML exports one file as HTML (structure-preserving intermediate
// for a later markdown pass). Used by tests and available via the proxy.
func (c *GoogleDriveConnector) ExportFileHTML(ctx context.Context, id string) (string, error) {
	return c.exportFile(ctx, slog.Default(), id, googleDriveHTMLExportMime)
}

func (c *GoogleDriveConnector) exportFile(ctx context.Context, logger *slog.Logger, id, mime string) (string, error) {
	token := c.host.GetValidToken(GoogleDriveServiceID, logger)
	if token == "" {
		return "", types.ErrUnauthorized
	}
	u := c.GoogleDriveUpstreamBase() + "/files/" + url.PathEscape(id) + "/export?mimeType=" + url.QueryEscape(mime)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", types.ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("google drive export returned %d", resp.StatusCode)
	}
	return string(data), nil
}
