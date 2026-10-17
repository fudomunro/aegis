package core

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// adminHTML is the admin dashboard shell (CSS + SPA JS). Data loads via API.
//
//go:embed web/admin.html
var adminHTML string

// AdminService is a redacted view of a configured service for the admin UI.
// Secrets (client_secret, tokens) are never included.
type AdminService struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Action      string   `json:"action"`
	Endpoint    string   `json:"endpoint,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	CacheTTL    string   `json:"cache_ttl"`
	Description string   `json:"description,omitempty"`
	// Permissions is the agent access map by data type (nil = unrestricted).
	Permissions map[string]string `json:"permissions,omitempty"`
	// AvailableDataTypes lists connector-known types the UI can edit.
	AvailableDataTypes []string `json:"available_data_types,omitempty"`
	// WritableDataTypes lists connector types that currently expose at least
	// one mutating operation. The admin form only offers Editable for these;
	// saved write levels are clamped so permissions cannot exceed the connector.
	WritableDataTypes []string `json:"writable_data_types,omitempty"`
	HasOAuth          bool     `json:"has_oauth"`
	// HasAPIKey is true when static API-key headers are present (presence only).
	HasAPIKey bool `json:"has_api_key,omitempty"`
	// ConnectionID is the shared provider connection this surface uses (issue #26).
	ConnectionID   string       `json:"connection_id,omitempty"`
	HasClientID    bool         `json:"has_client_id"`
	HasSecret      bool         `json:"has_client_secret"`
	OAuthScopes    []string     `json:"oauth_scopes,omitempty"`
	Token          *TokenStatus `json:"token,omitempty"`
	HasPlaylist    bool         `json:"has_playlist_provider"`
	HasLikedTrack  bool         `json:"has_liked_track_provider"`
	HasAlbum       bool         `json:"has_album_provider"`
	HasArtist      bool         `json:"has_artist_provider"`
	HasContact     bool         `json:"has_contact_provider"`
	HasPhoto       bool         `json:"has_photo_provider"`
	HasEvent       bool         `json:"has_event_provider"`
	HasMessage     bool         `json:"has_message_provider"`
	HasTransaction bool         `json:"has_transaction_provider"`
	// SupportsPhotoPicker is true when the connector exposes a picker session
	// create operation (e.g. Google Photos Picker API).
	SupportsPhotoPicker bool `json:"supports_photo_picker,omitempty"`
	// CanonicalFor lists library kinds where this service is the configured
	// authoritative non-Aegis location (issue #16).
	CanonicalFor []string `json:"canonical_for,omitempty"`
	ProxyPath    string   `json:"proxy_path"`
	LoginPath    string   `json:"login_path,omitempty"`
}

// AdminRuntime describes non-secret server identity and auth posture.
type AdminRuntime struct {
	AuthMode         string   `json:"auth_mode"`
	BaseURL          string   `json:"base_url"`
	ApertureURL      string   `json:"aperture_url,omitempty"`
	APIKeyConfigured bool     `json:"api_key_configured"`
	AllowedLogins    []string `json:"allowed_logins,omitempty"`
	// DataDir is the resolved local state directory (issue #74).
	DataDir string `json:"data_dir,omitempty"`
}

// AdminCacheStats summarizes the in-memory response cache.
type AdminCacheStats struct {
	Live  int `json:"live"`
	Total int `json:"total"`
	Bytes int `json:"live_bytes"`
}

// AdminSummary is the full admin dashboard payload.
type AdminSummary struct {
	Runtime  AdminRuntime    `json:"runtime"`
	Services []AdminService  `json:"services"`
	Cache    AdminCacheStats `json:"cache"`
}

// HandleAdminLibrary returns one unified library dataset for the admin UI.
// GET /v1/admin/library/{kind}
//
// Same aggregate pipeline and JSON shape as the agent /v1/library/{kind}
// endpoints, but access is not recorded on the Home access-count stats
// (issue #62). Admin browsing must not look like agent traffic.
func (s *Server) HandleAdminLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	kindID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/library/"), "/")
	if kindID == "" || strings.Contains(kindID, "/") {
		writeJSONError(w, http.StatusNotFound, "unknown library kind")
		return
	}
	var handler http.HandlerFunc
	for _, kind := range s.allLibraryKinds() {
		if kind.ID != kindID {
			continue
		}
		handler = s.libraryRouteHandlers()[kind.Path]
		break
	}
	if handler == nil {
		writeJSONError(w, http.StatusNotFound, "unknown library kind")
		return
	}
	handler(w, withSkipAccessMetrics(r))
}

// adminShellPaths are friendly SPA routes (issue #150) plus the legacy
// /v1/admin alias. Trailing-slash variants are included so bookmarks and
// hand-typed URLs keep working.
var adminShellPaths = map[string]bool{
	"/":          true,
	"/home":      true,
	"/home/":     true,
	"/library":   true,
	"/library/":  true,
	"/services":  true,
	"/services/": true,
	"/advanced":  true,
	"/advanced/": true,
	"/v1/admin":  true,
	"/v1/admin/": true,
}

// HandleAdmin serves the HTML admin dashboard (shell only; data loads via API).
// GET /  /home  /library  /services  /advanced  (and legacy /v1/admin)
//
// Friendly paths (issue #150) are the durable URLs the SPA pushes with
// history.pushState; refresh must land back on the same section. The shell
// contains no secrets or live data — only markup and client JS. It is still
// wrapped in AuthMiddleware so the API surface is not advertised to
// unauthenticated peers (issue #98). OAuth callback pages remain public.
func (s *Server) HandleAdmin(w http.ResponseWriter, r *http.Request) {
	if !adminShellPaths[r.URL.Path] {
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	fmt.Fprint(w, adminHTML)
}

// HandleAdminSummary returns redacted service, token, and cache overview data.
// GET /v1/admin/summary
func (s *Server) HandleAdminSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tokens := s.tokens.Status()
	snapshots := s.config.ServiceSnapshots()
	services := make([]AdminService, 0, len(snapshots))
	for _, svc := range snapshots {
		// Quicken and Signal are file-based imports (Advanced tab), not
		// configured OAuth/static services — keep them off the services list
		// (issues #141, #168).
		if svc.ID == quickenServiceID || svc.ID == signalServiceID {
			continue
		}
		item := AdminService{
			ID:           svc.ID,
			Name:         svc.Name,
			Action:       svc.Action,
			Endpoint:     svc.Endpoint,
			Aliases:      svc.Aliases,
			CacheTTL:     svc.CacheTTL.String(),
			Description:  svc.Description,
			Permissions:  svc.Permissions.Clone(),
			CanonicalFor: svc.CanonicalFor,
			HasOAuth:     svc.Connection != "",
			HasAPIKey:    svc.HasStaticAPIKey(),
			ConnectionID: s.config.connectionIDForService(svc.ID),
			ProxyPath:    "/v1/services/" + svc.ID,
			HasPlaylist:  false,
		}
		if connectionID, conn := s.config.ConnectionForService(svc.ID); connectionID != "" {
			item.ConnectionID = connectionID
			item.HasOAuth = true
			if conn != nil {
				item.HasClientID = conn.ClientID != ""
				item.HasSecret = conn.ClientSecret != ""
				item.OAuthScopes = s.config.requiredScopes(svc.ID)
			}
			item.LoginPath = "/v1/auth/" + svc.ID + "/login"
		}
		if connector, ok := s.connectors[svc.ID]; ok {
			if _, isProvider := connector.(types.PlaylistProvider); isProvider {
				item.HasPlaylist = true
			}
			if _, isProvider := connector.(types.LikedTrackProvider); isProvider {
				item.HasLikedTrack = true
			}
			if _, isProvider := connector.(types.AlbumProvider); isProvider {
				item.HasAlbum = true
			}
			if _, isProvider := connector.(types.ArtistProvider); isProvider {
				item.HasArtist = true
			}
			if _, isProvider := connector.(types.ContactProvider); isProvider {
				item.HasContact = true
			}
			if _, isProvider := connector.(types.PhotoProvider); isProvider {
				item.HasPhoto = true
			}
			if _, isProvider := connector.(types.CalendarEventProvider); isProvider {
				item.HasEvent = true
			}
			if _, isProvider := connector.(types.MessageProvider); isProvider {
				item.HasMessage = true
			}
			if _, isProvider := connector.(types.TransactionProvider); isProvider {
				item.HasTransaction = true
			}
			caps := connector.Capabilities()
			types := make([]string, 0, len(caps.DataTypes))
			for _, dt := range caps.DataTypes {
				types = append(types, dt.Name)
				for _, op := range dt.Operations {
					if strings.Contains(op.Path, "/picker/sessions") && strings.EqualFold(op.Method, http.MethodPost) {
						item.SupportsPhotoPicker = true
					}
				}
			}
			item.AvailableDataTypes = types
			item.WritableDataTypes = s.writableDataTypesFor(item.ID, caps)
		}
		// Token store keys are provider connection IDs only.
		if st, ok := tokens[item.ConnectionID]; ok {
			st := st
			item.Token = &st
		}
		services = append(services, item)
	}

	live, total, bytes := s.cache.Stats()
	allowed := s.allowedLogins
	if allowed == nil {
		allowed = []string{}
	}

	resp := AdminSummary{
		Runtime: AdminRuntime{
			AuthMode:         s.authMode,
			BaseURL:          s.baseURL,
			ApertureURL:      s.apertureURL,
			APIKeyConfigured: s.apiKey != "",
			AllowedLogins:    allowed,
			DataDir:          s.dataDir,
		},
		Services: services,
		Cache: AdminCacheStats{
			Live:  live,
			Total: total,
			Bytes: bytes,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleAdminCache lists in-memory cache entries.
// GET /v1/admin/cache
func (s *Server) HandleAdminCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	items := s.cache.Snapshot()
	if items == nil {
		items = []CacheItem{}
	}
	live, total, bytes := s.cache.Stats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"entries": items,
		"live":    live,
		"total":   total,
		"bytes":   bytes,
	})
}

// HandleAdminCacheClear wipes the response cache.
// POST /v1/admin/cache/clear
func (s *Server) HandleAdminCacheClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.cache.Clear()
	withPeerLog(slog.With("handler", "admin_cache_clear"), r).Info("admin cache cleared")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
}

// adminPermissionsRequest is the body for PUT service permissions.
type adminPermissionsRequest struct {
	// Permissions is the new map. Empty/null clears the block (unrestricted).
	Permissions types.Permissions `json:"permissions"`
}

// adminTypePoliciesRequest is the body for PUT type policies.
type adminTypePoliciesRequest struct {
	// Types maps data-type / permission keys to TypePolicy (issue #148).
	Types map[string]types.TypePolicy `json:"types"`
}

// HandleAdminTypePolicies reads or writes the type-owned agent policy map.
//
//	GET /v1/admin/type-policies
//	PUT /v1/admin/type-policies  {"types":{"tracks":{"default":"read","providers":{"spotify":"none"}}}}
//
// Type policy is primary: default plus optional per-provider overrides that
// can only tighten. Service permission maps remain migration overrides.
func (s *Server) HandleAdminTypePolicies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		policies := s.config.TypePolicies()
		if policies == nil {
			policies = types.TypePolicies{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"version": permissionsFileVersion,
			"types":   policies,
		})
	case http.MethodPut:
		body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		var req adminTypePoliciesRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
		policies := req.Types
		if policies == nil {
			policies = types.TypePolicies{}
		}
		if err := s.config.SetTypePolicies(policies); err != nil {
			withPeerLog(slog.With("handler", "admin_type_policies"), r).
				Warn("failed to save type policies", "error", err)
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.cache.Clear()
		withPeerLog(slog.With("handler", "admin_type_policies"), r).
			Info("type policies updated", "types", len(policies))
		saved := s.config.TypePolicies()
		if saved == nil {
			saved = types.TypePolicies{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"version": permissionsFileVersion,
			"types":   saved,
			"status":  "saved",
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleAdminServicePermissions reads or writes a service's agent permission map.
//
//	GET /v1/admin/services/{id}/permissions
//	PUT /v1/admin/services/{id}/permissions  {"permissions": {"default":"read","player":"none"}}
func (s *Server) HandleAdminServicePermissions(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/v1/admin/services/")
	trimmed = strings.Trim(trimmed, "/")
	id := strings.TrimSuffix(trimmed, "/permissions")
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}

	svc := s.config.GetService(id)
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured", id))
		return
	}

	switch r.Method {
	case http.MethodGet:
		perms := svc.Permissions.Clone()
		if perms == nil {
			perms = map[string]string{}
		}
		effective := s.effectivePermissions(svc)
		var effectiveOut map[string]string
		if effective != nil {
			effectiveOut = effective.Clone()
		}
		if effectiveOut == nil {
			effectiveOut = map[string]string{}
		}
		var types []string
		var writable []string
		if connector, ok := s.connectors[id]; ok {
			caps := connector.Capabilities()
			for _, dt := range caps.DataTypes {
				types = append(types, dt.Name)
			}
			writable = s.writableDataTypesFor(id, caps)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"service_id":            id,
			"permissions":           perms,
			"effective_permissions": effectiveOut,
			"available_data_types":  types,
			"writable_data_types":   writable,
		})
	case http.MethodPut:
		body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		var req adminPermissionsRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
		// Permissions may only further constrain Aegis — never grant write
		// where neither proxy ops nor mirror adapters can write that type.
		saved := req.Permissions
		if connector, ok := s.connectors[id]; ok && len(saved) > 0 {
			caps := connector.Capabilities()
			saved = clampPermissionsWithWritable(saved, caps, s.writableDataSet(id, caps))
		}
		if err := s.config.SetServicePermissions(id, saved); err != nil {
			withPeerLog(slog.With("handler", "admin_permissions"), r).
				Warn("failed to save permissions", "service", id, "error", err)
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Cache may hold responses that are now denied.
		s.cache.Clear()
		withPeerLog(slog.With("handler", "admin_permissions"), r).
			Info("service permissions updated", "service", id, "keys", len(req.Permissions))

		updated := s.config.GetService(id)
		perms := map[string]string{}
		if updated != nil {
			perms = updated.Permissions.Clone()
			if perms == nil {
				perms = map[string]string{}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"service_id":  id,
			"permissions": perms,
			"status":      "saved",
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleAdminHookHealth reports Aperture pre-request hook wiring health and
// optionally validates a pasted Aperture policy/hooks JSON document.
//
// GET  /v1/admin/hook-health
// POST /v1/admin/hook-health/check   body: Aperture policy or hooks JSON
func (s *Server) HandleAdminHookHealth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rep := s.hookHealth.Snapshot(s.baseURL)
		sortHookIssues(rep.Issues)
		writeJSON(w, rep)
	case http.MethodPost:
		body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			writeJSONError(w, http.StatusBadRequest, "paste Aperture policy/hooks JSON in the request body")
			return
		}
		rep := s.CheckApertureConfig(body)
		sortHookIssues(rep.Issues)
		writeJSON(w, rep)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
