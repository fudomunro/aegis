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
	"time"
)

// Immich setup admin API (issue #39). Guides operators through connecting a
// self-hosted Immich as the local/foreign canonical photo store without
// leaving the Aegis admin UI or hand-editing aegis.json.

// ImmichSetupCheck is one row of the live acceptance checklist.
type ImmichSetupCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ImmichSetupStatus is GET /v1/admin/immich/setup.
type ImmichSetupStatus struct {
	Configured         bool     `json:"configured"`
	Endpoint           string   `json:"endpoint,omitempty"`
	HasAPIKey          bool     `json:"has_api_key"`
	CanonicalFor       []string `json:"canonical_for,omitempty"`
	CanonicalConflict  string   `json:"canonical_conflict,omitempty"`
	Snippet            string   `json:"snippet,omitempty"`
	Checks             []ImmichSetupCheck `json:"checks,omitempty"`
	LibraryStatus      string   `json:"library_status,omitempty"`
	LibraryCount       int      `json:"library_count,omitempty"`
	LibraryError       string   `json:"library_error,omitempty"`
}

// ImmichTestRequest is POST /v1/admin/immich/test. Omitted fields use the
// configured service. The API key is never stored or echoed back.
type ImmichTestRequest struct {
	Endpoint string `json:"endpoint,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

// ImmichTestResponse is the result of probing Immich.
type ImmichTestResponse struct {
	OK      bool   `json:"ok"`
	Status  int    `json:"status,omitempty"`
	Message string `json:"message"`
}

// ImmichConfigRequest is POST /v1/admin/immich/config — save connection from
// the admin UI into service_connections.json (not aegis.json).
type ImmichConfigRequest struct {
	Endpoint                  string `json:"endpoint"`
	APIKey                    string `json:"api_key,omitempty"`
	ClaimCanonicalForPhotos   bool   `json:"claim_canonical_for_photos"`
}

// HandleAdminImmichSetup serves GET /v1/admin/immich/setup and dispatches
// POST sub-actions under /v1/admin/immich/*.
func (s *Server) HandleAdminImmichSetup(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/immich"), "/")
	switch {
	case path == "" || path == "setup":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeImmichSetupStatus(w, r, true)
	case path == "test":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleImmichTest(w, r)
	case path == "config":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleImmichConfig(w, r)
	case path == "check":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeImmichSetupStatus(w, r, true)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) writeImmichSetupStatus(w http.ResponseWriter, r *http.Request, withChecks bool) {
	status := s.immichSetupStatus(r.Context(), withChecks)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) immichSetupStatus(ctx context.Context, withChecks bool) ImmichSetupStatus {
	svc := s.config.GetService(immichServiceID)
	status := ImmichSetupStatus{}
	if svc != nil {
		status.Configured = true
		status.Endpoint = svc.Endpoint
		status.HasAPIKey = svc.HasStaticAPIKey()
		status.CanonicalFor = append([]string(nil), svc.CanonicalFor...)
	}
	// Surface who currently owns photos when Immich does not.
	if svc == nil || !svc.IsCanonicalFor("photos") {
		for _, other := range s.config.ServiceSnapshots() {
			if other.ID == immichServiceID {
				continue
			}
			if other.IsCanonicalFor("photos") {
				status.CanonicalConflict = other.ID
				break
			}
		}
	}
	status.Snippet = immichConfigSnippet(status.Endpoint)

	if !withChecks {
		return status
	}
	status.Checks = s.runImmichChecks(ctx, &status)
	return status
}

func immichConfigSnippet(endpoint string) string {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = "http://photos.home.ts.net:2283/api"
	}
	// Display-only bootstrap snippet for operators who prefer aegis.json.
	return fmt.Sprintf(`{
  "id": "immich",
  "name": "Immich",
  "action": "inject",
  "endpoint": %q,
  "headers": { "X-API-Key": "YOUR_IMMICH_API_KEY" },
  "cache_ttl": "5m",
  "canonical_for": ["photos"],
  "permissions": { "default": "read", "photos": "read" }
}`, endpoint)
}

// runImmichChecks executes the issue #39 acceptance checklist against the
// live configuration. Fail-open: a broken Immich marks checks failed but never
// errors the admin request.
func (s *Server) runImmichChecks(ctx context.Context, status *ImmichSetupStatus) []ImmichSetupCheck {
	checks := []ImmichSetupCheck{
		{ID: "service_configured", Label: "Immich service configured"},
		{ID: "api_key", Label: "API key present (X-API-Key)"},
		{ID: "canonical_owner", Label: "Immich is canonical_for photos"},
		{ID: "endpoint_reachable", Label: "Immich endpoint reachable"},
		{ID: "library_listing", Label: "Library lists Immich photos"},
		{ID: "canonical_stamp", Label: "Sources stamped canonical + proxy blob_url"},
		{ID: "bytes_stay_upstream", Label: "Photo bytes stay on Immich"},
	}
	byID := map[string]int{}
	for i, c := range checks {
		byID[c.ID] = i
	}
	mark := func(id, detail string, ok bool) {
		i, exists := byID[id]
		if !exists {
			return
		}
		checks[i].OK = ok
		checks[i].Detail = detail
	}

	svc := s.config.GetService(immichServiceID)
	if svc == nil {
		mark("service_configured", "Add Immich with the form above (or aegis.json)", false)
		mark("api_key", "Not configured", false)
		mark("canonical_owner", "Not configured", false)
		mark("endpoint_reachable", "Not configured", false)
		mark("library_listing", "Not configured", false)
		mark("canonical_stamp", "Not configured", false)
		mark("bytes_stay_upstream", "Product rule: Aegis never stores photo bytes", true)
		return checks
	}
	mark("service_configured", "id=immich", true)

	if status.HasAPIKey {
		mark("api_key", "Header set (value not shown)", true)
	} else {
		mark("api_key", "Set X-API-Key via this form or aegis.json headers", false)
	}

	if svc.IsCanonicalFor("photos") {
		mark("canonical_owner", "canonical_for includes photos", true)
	} else if status.CanonicalConflict != "" {
		mark("canonical_owner", "Currently claimed by "+status.CanonicalConflict+" — enable claim below to move it", false)
	} else {
		mark("canonical_owner", "No service claims canonical_for photos (optional)", true)
	}

	// Probe reachability + auth using the configured connector path.
	probe := s.probeImmich(ctx, svc.Endpoint, headerValue(svc.Headers, "X-API-Key"))
	if probe.OK {
		mark("endpoint_reachable", probe.Message, true)
	} else {
		mark("endpoint_reachable", probe.Message, false)
		mark("library_listing", "Skipped — endpoint/auth failed", false)
		mark("canonical_stamp", "Skipped — endpoint/auth failed", false)
		mark("bytes_stay_upstream", "Product rule: Aegis never stores photo bytes", true)
		return checks
	}

	photos, truncated, err := s.listImmichPhotos(ctx)
	if err != nil {
		status.LibraryStatus = "error"
		if err == ErrUnauthorized {
			status.LibraryStatus = "unauthorized"
		}
		status.LibraryError = err.Error()
		mark("library_listing", err.Error(), false)
		mark("canonical_stamp", "Skipped — listing failed", false)
		mark("bytes_stay_upstream", "Product rule: Aegis never stores photo bytes", true)
		return checks
	}
	status.LibraryStatus = "ok"
	status.LibraryCount = len(photos)
	if truncated {
		status.LibraryStatus = "ok"
		status.LibraryError = "truncated at page cap"
	}
	mark("library_listing", fmt.Sprintf("%d photos", len(photos)), len(photos) > 0)

	stampOK, stampDetail := immichStampSummary(photos, s.baseURL)
	mark("canonical_stamp", stampDetail, stampOK)

	// Product invariant, not a live probe.
	mark("bytes_stay_upstream", "Metadata + proxy only; downloads stream from Immich", true)
	return checks
}

func headerValue(headers map[string]string, key string) string {
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// probeImmich checks endpoint reachability and API key auth. Tries
// GET /users/me (auth) then GET /server/ping (reachability).
func (s *Server) probeImmich(ctx context.Context, endpoint, apiKey string) ImmichTestResponse {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if base == "" {
		return ImmichTestResponse{OK: false, Message: "endpoint is empty — use http://host:2283/api"}
	}
	if !strings.HasSuffix(base, "/api") {
		// Tolerate a bare host:port and append /api.
		if strings.Contains(base, "://") && !strings.Contains(strings.TrimPrefix(base, "://"), "/") {
			base += "/api"
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// Prefer an authenticated call so we surface bad keys clearly.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/users/me", nil)
	if err == nil {
		if apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
		resp, err := aegisHTTPClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return ImmichTestResponse{OK: true, Status: resp.StatusCode, Message: "Immich accepted the API key"}
			}
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				return ImmichTestResponse{OK: false, Status: resp.StatusCode, Message: "Immich rejected the API key (Account → API Keys)"}
			}
			// Fall through to ping for non-auth failures (404 on older APIs).
			_ = body
		}
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/server/ping", nil)
	if err != nil {
		return ImmichTestResponse{OK: false, Message: err.Error()}
	}
	if apiKey != "" {
		req2.Header.Set("X-API-Key", apiKey)
	}
	resp2, err := aegisHTTPClient.Do(req2)
	if err != nil {
		return ImmichTestResponse{OK: false, Message: "Immich unreachable: " + err.Error()}
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		if apiKey == "" {
			return ImmichTestResponse{OK: true, Status: resp2.StatusCode, Message: "Immich reachable (no API key set yet)"}
		}
		return ImmichTestResponse{OK: true, Status: resp2.StatusCode, Message: "Immich reachable"}
	}
	return ImmichTestResponse{OK: false, Status: resp2.StatusCode, Message: fmt.Sprintf("Immich returned HTTP %d", resp2.StatusCode)}
}

func (s *Server) listImmichPhotos(ctx context.Context) ([]Photo, bool, error) {
	conn, ok := s.connectors[immichServiceID].(*ImmichConnector)
	if !ok || conn == nil {
		// Connector not registered in this process — use a temporary one.
		conn = NewImmichConnector(s)
	}
	return conn.ListUserPhotos(ctx)
}

func immichStampSummary(photos []Photo, baseURL string) (bool, string) {
	if len(photos) == 0 {
		return false, "no photos to inspect"
	}
	proxyPrefix := strings.TrimRight(baseURL, "/") + "/v1/services/" + immichServiceID + "/"
	for _, p := range photos {
		for _, src := range p.Sources {
			if src.Service != immichServiceID {
				continue
			}
			if !src.Canonical {
				return false, "Immich source missing canonical:true"
			}
			if src.BlobURL == "" || !strings.HasPrefix(src.BlobURL, proxyPrefix) {
				return false, "blob_url is not an Aegis proxy URL"
			}
			return true, "canonical + proxy blob_url present"
		}
	}
	return false, "no Immich sources in listing"
}

func (s *Server) handleImmichTest(w http.ResponseWriter, r *http.Request) {
	var req ImmichTestRequest
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	apiKey := strings.TrimSpace(req.APIKey)
	if endpoint == "" || apiKey == "" {
		if svc := s.config.GetService(immichServiceID); svc != nil {
			if endpoint == "" {
				endpoint = svc.Endpoint
			}
			if apiKey == "" {
				apiKey = headerValue(svc.Headers, "X-API-Key")
			}
		}
	}
	result := s.probeImmich(r.Context(), endpoint, apiKey)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleImmichConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req ImmichConfigRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		writeJSONError(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	// Normalize bare host:port to …/api so the connector base is correct.
	if !strings.Contains(strings.TrimPrefix(endpoint, "http://"), "/") ||
		strings.HasSuffix(strings.TrimRight(endpoint, "/"), ":2283") {
		if u, err := url.Parse(endpoint); err == nil && u.Host != "" && (u.Path == "" || u.Path == "/") {
			endpoint = strings.TrimRight(endpoint, "/") + "/api"
		}
	}
	if !strings.HasSuffix(strings.TrimRight(endpoint, "/"), "/api") &&
		!strings.Contains(endpoint, "/api/") {
		// Allow endpoints that already include a path beyond /api.
		if strings.Count(strings.TrimPrefix(endpoint, "http://"), "/") == 0 {
			endpoint = strings.TrimRight(endpoint, "/") + "/api"
		}
	}

	entry := serviceConnectionEntry{
		Name:        "Immich",
		Action:      "inject",
		Endpoint:    strings.TrimRight(endpoint, "/"),
		CacheTTL:    "5m",
		Description: "Self-hosted Immich photo library. Photo bytes stay on Immich (foreign/local canonical).",
		Aliases:     []string{"immich photos", "self-hosted photos"},
		Headers:     map[string]string{},
	}
	if key := strings.TrimSpace(req.APIKey); key != "" {
		entry.Headers["X-API-Key"] = key
	}
	if req.ClaimCanonicalForPhotos {
		entry.CanonicalFor = []string{"photos"}
	} else {
		entry.ClearCanonicalFor = true
	}

	if err := s.config.SetServiceConnection(immichServiceID, entry); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Refresh cache so library listings pick up the new home immediately.
	s.cache.Clear()
	withPeerLog(slogImmich(), r).Info("immich connection saved from admin UI",
		"endpoint", entry.Endpoint, "claim_photos", req.ClaimCanonicalForPhotos)

	s.writeImmichSetupStatus(w, r, true)
}

func slogImmich() *slog.Logger {
	return slog.With("handler", "admin_immich_setup")
}
