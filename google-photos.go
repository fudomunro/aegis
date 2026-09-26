package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GooglePhotosConnector uses the Google Photos Picker API and implements
// PhotoProvider. Full Library API reads require app verification (post-2025
// scope restrictions), so Aegis demos and day-to-day use go through
// user-selected picker sessions: the human picks photos once; agents then
// list that selection as neutral Photo metadata. Bytes stay on Google Photos
// (non-Aegis canonical, issue #16).
// https://github.com/fudomunro/aegis/issues/21
type GooglePhotosConnector struct {
	server *Server

	mu sync.Mutex
	// lastSessionID is the most recent picker session Aegis created or
	// observed as complete. ListUserPhotos serves that selection so
	// /v1/library/photos works after the user finishes picking.
	lastSessionID string
}

// NewGooglePhotosConnector creates a connector wired to the given server.
func NewGooglePhotosConnector(srv *Server) *GooglePhotosConnector {
	return &GooglePhotosConnector{server: srv}
}

const googlePhotosServiceID = "google-photos"

const googlePhotosServiceName = "Google Photos"

// googlePhotosUpstreamBase returns the configured Photos Picker API base.
func (c *GooglePhotosConnector) googlePhotosUpstreamBase() string {
	return c.server.upstreamBaseFor(googlePhotosServiceID, "https://photospicker.googleapis.com/v1")
}

func (c *GooglePhotosConnector) setLastSession(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSessionID = id
}

func (c *GooglePhotosConnector) getLastSession() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSessionID
}

// Capabilities returns what Google Photos data types and operations are supported.
func (c *GooglePhotosConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "photos",
				Description: "Photos via the Google Photos Picker API — user selects photos; Aegis lists only that selection (metadata only; bytes stay on Google Photos)",
				Operations: []Operation{
					{
						Name:        "create_picker_session",
						Description: "Create a picker session and get a picker_uri for the user to open (permission key: photos)",
						Method:      "POST",
						Path:        "/picker/sessions",
						Response: ResponseSpec{
							Description: "Session with id and picker_uri (user opens picker_uri to select photos)",
							Fields:      []string{"id", "picker_uri", "media_items_set"},
						},
					},
					{
						Name:        "get_picker_session",
						Description: "Poll a picker session until media_items_set is true",
						Method:      "GET",
						Path:        "/picker/sessions/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Picker session ID", Required: true},
						},
						Response: ResponseSpec{
							Description: "Session status including media_items_set and polling hints",
							Fields:      []string{"id", "media_items_set", "picker_uri"},
						},
					},
					{
						Name:        "list_picked_photos",
						Description: "List photos from a completed picker session as neutral Photo values",
						Method:      "GET",
						Path:        "/picker/mediaItems",
						Parameters: []Parameter{
							{Name: "sessionId", In: "query", Type: "string", Description: "Picker session ID (required by Google)", Required: true},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page (1-100)", Default: 100},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{
							Description: "Neutral photos: taken_at, dimensions, mime_type, filename, sources[] (canonical blob_url)",
							Fields:      []string{"photos", "total"},
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
	return fmt.Sprintf(`When the user's request involves Google Photos, you MUST use the Aegis proxy at %s instead of calling Google Photos directly.

Google Photos uses the Picker API: the user must explicitly select photos once. Full library sync is not available without Google app verification.

Picker flow (call the full proxy URL; Aegis authenticates you):
1. POST /picker/sessions — create a session; return picker_uri to the user and ask them to open it
2. GET /picker/sessions/{id} — poll until media_items_set is true (follow pollingConfig in the response when present)
3. GET /picker/mediaItems?sessionId={id} — list the selection as neutral Photo metadata
4. For photos across every connected photo service after a pick, also try:
GET %s/v1/library/photos — last completed Google Photos pick plus other photo services

Photos are metadata-only. Aegis never downloads or stores image bytes. Use sources[].blob_url for the short-lived download URL (often expires in about an hour) — do not treat it as a permanent link.
Always include the full proxy base URL in your requests.`, base, root)
}

// HandleProxy processes a Google Photos Picker API request through the connector.
func (c *GooglePhotosConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", googlePhotosServiceID, "method", r.Method, "path", remainingPath)

	token := c.server.getValidToken(googlePhotosServiceID, logger)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"no valid Google Photos token — authorize at /v1/auth/"+googlePhotosServiceID+"/login")
		return
	}

	switch {
	case remainingPath == "/picker/sessions" || remainingPath == "/picker/sessions/":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed — use POST to create a session")
			return
		}
		c.handleCreateSession(w, r, token)
		return
	case strings.HasPrefix(remainingPath, "/picker/sessions/"):
		id := strings.Trim(strings.TrimPrefix(remainingPath, "/picker/sessions/"), "/")
		if id == "" || strings.Contains(id, "/") {
			writeJSONError(w, http.StatusBadRequest, "missing session id")
			return
		}
		switch r.Method {
		case http.MethodGet:
			c.handleGetSession(w, r, token, id)
		case http.MethodDelete:
			c.handleDeleteSession(w, r, token, id)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	case remainingPath == "/picker/mediaItems" || remainingPath == "/picker/mediaItems/" ||
		remainingPath == "/mediaItems" || remainingPath == "/mediaItems/":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleListPickedMediaItems(w, r, token)
		return
	}

	// Generic authenticated pass-through for other Picker paths (/v1/…).
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
	resp, err := aegisHTTPClient.Do(req)
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

func (c *GooglePhotosConnector) handleCreateSession(w http.ResponseWriter, r *http.Request, token string) {
	upstreamURL := c.googlePhotosUpstreamBase() + "/sessions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, strings.NewReader("{}"))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		slog.Error("picker session create failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "Google Photos picker session create failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to read Google Photos response")
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		writeGooglePhotosUpstreamError(w, data, resp.StatusCode)
		return
	}
	var session googlePickerSession
	if err := json.Unmarshal(data, &session); err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to parse picker session")
		return
	}
	if session.ID != "" {
		c.setLastSession(session.ID)
	}
	out, err := json.Marshal(mapPickerSession(session))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to encode session")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(out)
}

func (c *GooglePhotosConnector) handleGetSession(w http.ResponseWriter, r *http.Request, token, id string) {
	upstreamURL := c.googlePhotosUpstreamBase() + "/sessions/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "Google Photos picker session get failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to read Google Photos response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeGooglePhotosUpstreamError(w, data, resp.StatusCode)
		return
	}
	var session googlePickerSession
	if err := json.Unmarshal(data, &session); err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to parse picker session")
		return
	}
	if session.MediaItemsSet {
		c.setLastSession(id)
	}
	out, err := json.Marshal(mapPickerSession(session))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to encode session")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func (c *GooglePhotosConnector) handleDeleteSession(w http.ResponseWriter, r *http.Request, token, id string) {
	upstreamURL := c.googlePhotosUpstreamBase() + "/sessions/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodDelete, upstreamURL, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "Google Photos picker session delete failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to read Google Photos response")
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		writeGooglePhotosUpstreamError(w, data, resp.StatusCode)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "session_id": id})
}

func (c *GooglePhotosConnector) handleListPickedMediaItems(w http.ResponseWriter, r *http.Request, token string) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		sessionID = c.getLastSession()
	}
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest,
			"missing sessionId — create a picker session at POST /picker/sessions, have the user open picker_uri, then pass sessionId")
		return
	}
	c.setLastSession(sessionID)

	photos, truncated, err := c.listSessionPhotos(r.Context(), token, sessionID)
	if err != nil {
		slog.Warn("google-photos picker list failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch picked photos from Google Photos")
		return
	}
	if photos == nil {
		photos = []Photo{}
	}
	result := PhotoList{
		Photos:    photos,
		Total:     len(photos),
		Synced:    true,
		Truncated: truncated,
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

// writeGooglePhotosUpstreamError surfaces Google's message (e.g. insufficient
// scopes, session not ready) when present.
func writeGooglePhotosUpstreamError(w http.ResponseWriter, body []byte, status int) {
	var payload struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	msg := "Google Photos request failed"
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Message != "" {
		msg = payload.Error.Message
		if status == http.StatusForbidden || strings.Contains(strings.ToLower(msg), "scope") {
			msg += " — re-authorize at /v1/auth/" + googlePhotosServiceID + "/login with the Photos Picker scope " +
				"(enable the Photos Picker API; Library API readonly no longer lists the full library for unverified apps)"
		}
	}
	writeJSONError(w, status, msg)
}

// ListUserPhotos implements PhotoProvider. It returns the last completed
// picker session's selection (empty when the user has not picked yet), so the
// unified library works without requiring full Library API verification.
func (c *GooglePhotosConnector) ListUserPhotos(ctx context.Context) ([]Photo, bool, error) {
	token := c.server.getValidToken(googlePhotosServiceID, slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}
	sessionID := c.getLastSession()
	if sessionID == "" {
		return []Photo{}, false, nil
	}
	photos, truncated, err := c.listSessionPhotos(ctx, token, sessionID)
	if err != nil {
		// Session may have expired; degrade to empty rather than failing the library.
		slog.Warn("google-photos last picker session list failed", "session", sessionID, "error", err)
		return []Photo{}, false, nil
	}
	return photos, truncated, nil
}

func (c *GooglePhotosConnector) listSessionPhotos(ctx context.Context, token, sessionID string) ([]Photo, bool, error) {
	upstreamBase := c.googlePhotosUpstreamBase()
	var all []Photo
	pageToken := ""
	truncated := false
	for page := 0; page < libraryPageCap; page++ {
		u := upstreamBase + "/mediaItems?pageSize=100&sessionId=" + url.QueryEscape(sessionID)
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("picker mediaItems returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		var raw googlePickerMediaItemList
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		for _, item := range raw.MediaItems {
			p := mapPickedMediaItem(item, sessionID)
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

// googlePickerSession is the subset of a Picker PickingSession we use.
type googlePickerSession struct {
	ID            string `json:"id"`
	PickerURI     string `json:"pickerUri"`
	MediaItemsSet bool   `json:"mediaItemsSet"`
	PollingConfig *struct {
		PollInterval string `json:"pollInterval"`
		Timeout      string `json:"timeout"`
	} `json:"pollingConfig"`
}

// mapPickerSession returns an agent-friendly session payload.
func mapPickerSession(s googlePickerSession) map[string]any {
	out := map[string]any{
		"id":              s.ID,
		"picker_uri":      s.PickerURI,
		"media_items_set": s.MediaItemsSet,
		"picker_uri_note": "Open picker_uri in a browser so the user can select photos; append /autoclose to auto-close the window after Done",
	}
	if s.PollingConfig != nil {
		out["polling_config"] = map[string]any{
			"poll_interval": s.PollingConfig.PollInterval,
			"timeout":       s.PollingConfig.Timeout,
		}
	}
	return out
}

// googlePickerMediaItem is the subset of a Picker PickedMediaItem we use.
type googlePickerMediaItem struct {
	ID         string `json:"id"`
	CreateTime string `json:"createTime"`
	Type       string `json:"type"`
	MediaFile  *struct {
		BaseURL           string `json:"baseUrl"`
		MimeType          string `json:"mimeType"`
		Filename          string `json:"filename"`
		MediaFileMetadata *struct {
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			CameraMake  string `json:"cameraMake"`
			CameraModel string `json:"cameraModel"`
		} `json:"mediaFileMetadata"`
	} `json:"mediaFile"`
}

type googlePickerMediaItemList struct {
	MediaItems    []googlePickerMediaItem `json:"mediaItems"`
	NextPageToken string                  `json:"nextPageToken"`
}

func mapPickedMediaItem(item googlePickerMediaItem, sessionID string) Photo {
	filename := ""
	mimeType := ""
	baseURL := ""
	width, height := 0, 0
	extra := map[string]any{}
	if item.MediaFile != nil {
		filename = item.MediaFile.Filename
		mimeType = item.MediaFile.MimeType
		baseURL = item.MediaFile.BaseURL
		if md := item.MediaFile.MediaFileMetadata; md != nil {
			width = md.Width
			height = md.Height
			if md.CameraMake != "" {
				extra["camera_make"] = md.CameraMake
			}
			if md.CameraModel != "" {
				extra["camera_model"] = md.CameraModel
			}
		}
	}
	takenAt := item.CreateTime
	if t, err := time.Parse(time.RFC3339Nano, item.CreateTime); err == nil {
		takenAt = t.UTC().Format(time.RFC3339)
	}
	p := newPhoto(
		googlePhotosServiceID,
		googlePhotosServiceName,
		item.ID,
		filename,
		mimeType,
		takenAt,
		width,
		height,
	)
	if item.Type != "" {
		extra["media_type"] = strings.ToLower(item.Type)
	}
	if sessionID != "" {
		extra["picker_session_id"] = sessionID
	}
	if len(extra) > 0 {
		p.Extra = extra
	}
	if len(p.Sources) > 0 {
		p.Sources[0].BlobURL = baseURL
		p.Sources[0].ThumbnailURL = baseURL
		if baseURL != "" {
			if p.Sources[0].Extra == nil {
				p.Sources[0].Extra = map[string]any{}
			}
			p.Sources[0].Extra["blob_url_expires"] = "approx 1h"
			p.Sources[0].Extra["api"] = "photos-picker"
		}
	}
	return p
}
