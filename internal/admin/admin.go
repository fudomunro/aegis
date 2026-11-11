package admin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/hookhealth"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
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
	HasOAuth    bool     `json:"has_oauth"`
	// HasAPIKey is true when static API-key headers are present (presence only).
	HasAPIKey bool `json:"has_api_key,omitempty"`
	// ConnectionID is the shared provider connection this surface uses (issue #26).
	ConnectionID   string              `json:"connection_id,omitempty"`
	HasClientID    bool                `json:"has_client_id"`
	HasSecret      bool                `json:"has_client_secret"`
	OAuthScopes    []string            `json:"oauth_scopes,omitempty"`
	Token          *tokens.TokenStatus `json:"token,omitempty"`
	HasPlaylist    bool                `json:"has_playlist_provider"`
	HasLikedTrack  bool                `json:"has_liked_track_provider"`
	HasAlbum       bool                `json:"has_album_provider"`
	HasArtist      bool                `json:"has_artist_provider"`
	HasContact     bool                `json:"has_contact_provider"`
	HasPhoto       bool                `json:"has_photo_provider"`
	HasEvent       bool                `json:"has_event_provider"`
	HasMessage     bool                `json:"has_message_provider"`
	HasTransaction bool                `json:"has_transaction_provider"`
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
func (h *Handlers) HandleAdminLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	kindID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/library/"), "/")
	if kindID == "" || strings.Contains(kindID, "/") {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown library kind")
		return
	}
	k, ok := h.KindCatalog.RegistryKind(kindID)
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown library kind")
		return
	}
	handler := listHandlerFor(k)
	if handler == nil {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown library kind")
		return
	}
	handler(w, h.Host.WithSkipAccessMetrics(r))
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
func (h *Handlers) HandleAdmin(w http.ResponseWriter, r *http.Request) {
	if !adminShellPaths[r.URL.Path] {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
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
func (h *Handlers) HandleAdminSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tokens := h.Tokens.Status()
	snapshots := h.Host.ServiceRows()
	services := make([]AdminService, 0, len(snapshots))
	for _, svc := range snapshots {
		// Local import stores are file-based (Advanced tab), not configured
		// OAuth/static services — keep them off the services list (issue #254).
		if connectors.LocalImportStoreIDs()[svc.ID] {
			continue
		}
		item := AdminService{
			ID:           svc.ID,
			Name:         svc.Name,
			Action:       svc.Action,
			Endpoint:     svc.Endpoint,
			Aliases:      svc.Aliases,
			CacheTTL:     svc.CacheTTL,
			Description:  svc.Description,
			CanonicalFor: svc.CanonicalFor,
			HasOAuth:     svc.Connection != "",
			HasAPIKey:    svc.HasStaticAPIKey,
			ConnectionID: h.Host.ConnectionIDForService(svc.ID),
			ProxyPath:    "/v1/services/" + svc.ID,
			HasPlaylist:  false,
		}
		if connectionID, hasClientID, hasSecret := h.Host.ConnectionSecretState(svc.ID); connectionID != "" {
			item.ConnectionID = connectionID
			item.HasOAuth = true
			item.HasClientID = hasClientID
			item.HasSecret = hasSecret
			item.OAuthScopes = h.Host.RequiredScopes(svc.ID)
			item.LoginPath = "/v1/auth/" + svc.ID + "/login"
		}
		if connector, ok := h.Connectors[svc.ID]; ok {
			if _, isProvider := connector.(kinds.PlaylistProvider); isProvider {
				item.HasPlaylist = true
			}
			if _, isProvider := connector.(kinds.LikedTrackProvider); isProvider {
				item.HasLikedTrack = true
			}
			if _, isProvider := connector.(kinds.AlbumProvider); isProvider {
				item.HasAlbum = true
			}
			if _, isProvider := connector.(kinds.ArtistProvider); isProvider {
				item.HasArtist = true
			}
			if _, isProvider := connector.(kinds.ContactProvider); isProvider {
				item.HasContact = true
			}
			if _, isProvider := connector.(kinds.PhotoProvider); isProvider {
				item.HasPhoto = true
			}
			if _, isProvider := connector.(kinds.CalendarEventProvider); isProvider {
				item.HasEvent = true
			}
			if _, isProvider := connector.(kinds.MessageProvider); isProvider {
				item.HasMessage = true
			}
			if _, isProvider := connector.(kinds.TransactionProvider); isProvider {
				item.HasTransaction = true
			}
			caps := connector.Capabilities()
			for _, dt := range caps.Kinds {
				for _, op := range dt.Operations {
					if strings.Contains(op.Path, "/picker/sessions") && strings.EqualFold(op.Method, http.MethodPost) {
						item.SupportsPhotoPicker = true
					}
				}
			}
		}
		// Token store keys are provider connection IDs only.
		if st, ok := tokens[item.ConnectionID]; ok {
			st := st
			item.Token = &st
		}
		services = append(services, item)
	}

	live, total, bytes := h.cacheStats()
	allowed := h.Host.RuntimeFacts().AllowedLogins
	if allowed == nil {
		allowed = []string{}
	}

	resp := AdminSummary{
		Runtime: AdminRuntime{
			AuthMode:         h.Host.RuntimeFacts().AuthMode,
			BaseURL:          h.BaseURL,
			ApertureURL:      h.Host.RuntimeFacts().ApertureURL,
			APIKeyConfigured: h.Host.RuntimeFacts().APIKeyConfigured,
			AllowedLogins:    allowed,
			DataDir:          h.DataDir,
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
func (h *Handlers) HandleAdminCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	items := h.cacheSnapshot()
	if items == nil {
		items = []shared.CacheItem{}
	}
	live, total, bytes := h.cacheStats()

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
func (h *Handlers) HandleAdminCacheClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.Host.InvalidateAllCaches()
	h.Host.Logger(r, "admin_cache_clear").Info("admin cache cleared")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
}

// adminDataGrantsRequest is the body for PUT data grants.
type adminDataGrantsRequest struct {
	// DataGrants maps kind keys to DataGrant (issue #174).
	DataGrants map[string]policy.DataGrant `json:"data_grants"`
}

// HandleAdminDataGrants reads or writes the agent data-grant map.
//
//	GET /v1/admin/data-grants
//	PUT /v1/admin/data-grants   {"data_grants":{"tracks":{"default":"read","providers":{"spotify":"none"}}}}
func (h *Handlers) HandleAdminDataGrants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		policies := h.Host.DataGrants()
		if policies == nil {
			policies = policy.DataGrants{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"version":     h.Host.DataGrantsFormatVersion(),
			"data_grants": policies,
		})
	case http.MethodPut:
		body, ok := shared.ReadBodyLimited(w, r, shared.MaxAdminBodyBytes)
		if !ok {
			return
		}
		var req adminDataGrantsRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
		policies := req.DataGrants
		if policies == nil {
			policies = policy.DataGrants{}
		}
		if err := h.Host.SetDataGrants(policies); err != nil {
			h.Host.Logger(r, "admin_data_grants").
				Warn("failed to save data grants", "error", err)
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.Host.InvalidateCaches()
		h.Host.Logger(r, "admin_data_grants").
			Info("data grants updated", "types", len(policies))
		saved := h.Host.DataGrants()
		if saved == nil {
			saved = policy.DataGrants{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"version":     h.Host.DataGrantsFormatVersion(),
			"data_grants": saved,
			"status":      "saved",
		})
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleAdminHookHealth reports Aperture pre-request hook wiring health and
// optionally validates a pasted Aperture policy/hooks JSON document.
//
// GET  /v1/admin/hook-health
// POST /v1/admin/hook-health/check   body: Aperture policy or hooks JSON
func (h *Handlers) HandleAdminHookHealth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rep := h.HookHealth.Snapshot(h.BaseURL)
		hookhealth.SortIssues(rep.Issues)
		shared.WriteJSON(w, rep)
	case http.MethodPost:
		body, ok := shared.ReadBodyLimited(w, r, shared.MaxAdminBodyBytes)
		if !ok {
			return
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			shared.WriteJSONError(w, http.StatusBadRequest, "paste Aperture policy/hooks JSON in the request body")
			return
		}
		rep := h.HookHealth.CheckApertureConfig(body, h.BaseURL)
		hookhealth.SortIssues(rep.Issues)
		shared.WriteJSON(w, rep)
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
