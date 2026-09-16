package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

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
	AvailableDataTypes []string     `json:"available_data_types,omitempty"`
	HasOAuth           bool         `json:"has_oauth"`
	HasClientID        bool         `json:"has_client_id"`
	HasSecret          bool         `json:"has_client_secret"`
	OAuthScopes        []string     `json:"oauth_scopes,omitempty"`
	Token              *TokenStatus `json:"token,omitempty"`
	HasPlaylist        bool         `json:"has_playlist_provider"`
	HasLikedTrack      bool         `json:"has_liked_track_provider"`
	HasAlbum           bool         `json:"has_album_provider"`
	HasArtist          bool         `json:"has_artist_provider"`
	HasContact         bool         `json:"has_contact_provider"`
	HasPhoto           bool         `json:"has_photo_provider"`
	// SupportsPhotoPicker is true when the connector exposes a picker session
	// create operation (e.g. Google Photos Picker API).
	SupportsPhotoPicker bool   `json:"supports_photo_picker,omitempty"`
	ProxyPath           string `json:"proxy_path"`
	LoginPath           string `json:"login_path,omitempty"`
}

// AdminRuntime describes non-secret server identity and auth posture.
type AdminRuntime struct {
	AuthMode         string   `json:"auth_mode"`
	BaseURL          string   `json:"base_url"`
	APIKeyConfigured bool     `json:"api_key_configured"`
	AllowedLogins    []string `json:"allowed_logins,omitempty"`
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

// HandleAdmin serves the HTML admin dashboard (shell only; data loads via API).
// GET /v1/admin
func (s *Server) HandleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/admin" && r.URL.Path != "/v1/admin/" {
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
	services := make([]AdminService, 0, len(s.config.Services))
	for _, svc := range s.config.Services {
		item := AdminService{
			ID:          svc.ID,
			Name:        svc.Name,
			Action:      svc.Action,
			Endpoint:    svc.Endpoint,
			Aliases:     svc.Aliases,
			CacheTTL:    svc.CacheTTL.String(),
			Description: svc.Description,
			Permissions: svc.Permissions.Clone(),
			HasOAuth:    svc.OAuth != nil,
			ProxyPath:   "/v1/services/" + svc.ID,
			HasPlaylist: false,
		}
		if svc.OAuth != nil {
			item.HasClientID = svc.OAuth.ClientID != ""
			item.HasSecret = svc.OAuth.ClientSecret != ""
			item.OAuthScopes = svc.OAuth.Scopes
			item.LoginPath = "/v1/auth/" + svc.ID + "/login"
		}
		if connector, ok := s.connectors[svc.ID]; ok {
			if _, isProvider := connector.(PlaylistProvider); isProvider {
				item.HasPlaylist = true
			}
			if _, isProvider := connector.(LikedTrackProvider); isProvider {
				item.HasLikedTrack = true
			}
			if _, isProvider := connector.(AlbumProvider); isProvider {
				item.HasAlbum = true
			}
			if _, isProvider := connector.(ArtistProvider); isProvider {
				item.HasArtist = true
			}
			if _, isProvider := connector.(ContactProvider); isProvider {
				item.HasContact = true
			}
			if _, isProvider := connector.(PhotoProvider); isProvider {
				item.HasPhoto = true
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
		}
		if st, ok := tokens[svc.ID]; ok {
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
			APIKeyConfigured: s.apiKey != "",
			AllowedLogins:    allowed,
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
	Permissions Permissions `json:"permissions"`
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
		var types []string
		if connector, ok := s.connectors[id]; ok {
			for _, dt := range connector.Capabilities().DataTypes {
				types = append(types, dt.Name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"service_id":           id,
			"permissions":          perms,
			"available_data_types": types,
		})
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		var req adminPermissionsRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
		if err := s.config.SetServicePermissions(id, req.Permissions); err != nil {
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

const adminHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Guardian — Admin</title>
<style>
  :root {
    --bg: #0d1117; --surface: #161b22; --surface2: #21262d; --border: #30363d;
    --text: #e6edf3; --dim: #8b949e; --accent: #58a6ff;
    --green: #3fb950; --orange: #d29922; --red: #f85149;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: system-ui, sans-serif; background: var(--bg); color: var(--text); min-height: 100vh; }
  a { color: var(--accent); text-decoration: none; }
  a:hover { text-decoration: underline; }
  code { background: var(--surface2); padding: 0.1rem 0.4rem; border-radius: 3px; font-size: 0.85rem; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
  .wrap { max-width: 1000px; margin: 0 auto; padding: 1.5rem 1.25rem 3rem; }
  header { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 0.75rem; margin-bottom: 1.25rem; }
  h1 { font-size: 1.5rem; font-weight: 650; letter-spacing: -0.02em; }
  .sub { color: var(--dim); font-size: 0.9rem; }
  .status-strip {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    gap: 0.75rem; margin-bottom: 1.5rem;
  }
  .stat {
    background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
    padding: 0.85rem 1rem;
  }
  .stat .label { color: var(--dim); font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 0.25rem; }
  .stat .value { font-size: 1.15rem; font-weight: 600; font-variant-numeric: tabular-nums; }
  .stat .value.ok { color: var(--green); }
  .stat .value.warn { color: var(--orange); }
  nav { display: flex; gap: 0.35rem; border-bottom: 1px solid var(--border); margin-bottom: 1.25rem; overflow-x: auto; }
  .tab {
    background: none; border: none; color: var(--dim); padding: 0.65rem 0.9rem;
    font: inherit; font-size: 0.9rem; cursor: pointer; border-bottom: 2px solid transparent;
    white-space: nowrap;
  }
  .tab:hover { color: var(--text); }
  .tab:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; border-radius: 4px; }
  .tab[aria-selected="true"] { color: var(--text); border-bottom-color: var(--accent); font-weight: 600; }
  .kind-switch { display: inline-flex; gap: 0.25rem; background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 0.2rem; }
  .kind-btn {
    background: none; border: none; color: var(--dim); padding: 0.4rem 0.75rem;
    font: inherit; font-size: 0.82rem; font-weight: 600; cursor: pointer; border-radius: 6px;
    white-space: nowrap;
  }
  .kind-btn:hover { color: var(--text); }
  .kind-btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
  .kind-btn[aria-pressed="true"] { background: var(--surface2); color: var(--text); }
  .panel[hidden] { display: none; }
  .toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 0.75rem; margin-bottom: 1rem; }
  .toolbar .grow { flex: 1; min-width: 160px; }
  input[type="search"], input[type="password"], input[type="text"] {
    width: 100%; background: var(--surface); border: 1px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 0.55rem 0.75rem; font: inherit; font-size: 0.9rem;
  }
  input:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; border-color: var(--accent); }
  .btn {
    display: inline-flex; align-items: center; gap: 0.35rem; padding: 0.5rem 0.95rem;
    border-radius: 6px; font: inherit; font-size: 0.85rem; font-weight: 600; cursor: pointer;
    border: 1px solid var(--border); background: var(--surface); color: var(--text);
  }
  .btn:hover { border-color: var(--accent); }
  .btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .btn-primary { background: var(--accent); border-color: var(--accent); color: #0d1117; }
  .btn-primary:hover { opacity: 0.9; }
  .btn-danger { color: var(--red); border-color: rgba(248,81,73,0.4); }
  .btn-danger:hover { border-color: var(--red); }
  .card {
    background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
    padding: 1.1rem 1.2rem; margin-bottom: 0.85rem;
  }
  .card-header { display: flex; flex-wrap: wrap; align-items: center; gap: 0.65rem; margin-bottom: 0.4rem; }
  .card-header h3 { font-size: 1.05rem; margin: 0; }
  .meta { color: var(--dim); font-size: 0.88rem; line-height: 1.5; }
  .meta code { font-size: 0.8rem; }
  .actions { margin-top: 0.85rem; display: flex; flex-wrap: wrap; gap: 0.5rem; }
  .badge {
    display: inline-block; padding: 0.12rem 0.55rem; border-radius: 999px;
    font-size: 0.72rem; font-weight: 650; letter-spacing: 0.01em;
  }
  .badge-green { background: rgba(63,185,80,0.15); color: var(--green); }
  .badge-orange { background: rgba(210,153,34,0.15); color: var(--orange); }
  .badge-red { background: rgba(248,81,73,0.15); color: var(--red); }
  .badge-gray { background: var(--surface2); color: var(--dim); }
  .badge-blue { background: rgba(88,166,255,0.15); color: var(--accent); }
  table { width: 100%; border-collapse: collapse; font-size: 0.88rem; }
  th, td { text-align: left; padding: 0.6rem 0.65rem; border-bottom: 1px solid var(--border); vertical-align: top; }
  th { color: var(--dim); font-weight: 600; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; }
  tr:last-child td { border-bottom: none; }
  td.key { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.8rem; word-break: break-all; }
  .num { font-variant-numeric: tabular-nums; white-space: nowrap; }
  .empty, .error-box, .auth-box {
    background: var(--surface); border: 1px dashed var(--border); border-radius: 8px;
    padding: 1.5rem; color: var(--dim); text-align: center; line-height: 1.55;
  }
  .error-box { border-color: rgba(248,81,73,0.45); color: #ffb1ad; text-align: left; }
  .auth-box { text-align: left; max-width: 420px; margin: 2rem auto; }
  .auth-box h2 { font-size: 1.05rem; margin-bottom: 0.4rem; color: var(--text); }
  .auth-box p { margin-bottom: 0.85rem; font-size: 0.9rem; }
  .auth-box form { display: flex; flex-direction: column; gap: 0.6rem; }
  .scopes { display: flex; flex-wrap: wrap; gap: 0.35rem; margin-top: 0.45rem; }
  .muted-num { color: var(--dim); }
  .playlist-source {
    display: inline-flex; align-items: center; gap: 0.3rem; margin: 0.15rem 0.25rem 0.15rem 0;
    background: var(--surface2); border-radius: 4px; padding: 0.15rem 0.45rem; font-size: 0.78rem;
  }
  .perm-editor {
    margin-top: 0.85rem; padding-top: 0.85rem; border-top: 1px solid var(--border);
  }
  .perm-editor h4 { font-size: 0.9rem; margin-bottom: 0.5rem; }
  .perm-section { margin-bottom: 0.9rem; }
  .perm-section-title {
    font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.04em;
    color: var(--dim); margin-bottom: 0.25rem; font-weight: 650;
  }
  .perm-section-help { margin-bottom: 0.55rem; }
  .perm-row {
    display: grid; grid-template-columns: minmax(100px, 1.2fr) minmax(120px, 1fr) auto;
    gap: 0.5rem; align-items: center; margin-bottom: 0.45rem;
  }
  .perm-row label { font-size: 0.85rem; color: var(--dim); line-height: 1.35; }
  .perm-row-library label { color: var(--text); }
  .perm-note { font-size: 0.72rem; color: var(--dim); font-weight: 400; }
  .perm-row select, .perm-add input, .perm-add select {
    width: 100%; background: var(--surface2); border: 1px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 0.4rem 0.55rem; font: inherit; font-size: 0.85rem;
  }
  .perm-row-disabled { opacity: 0.72; }
  .perm-row-disabled label { color: var(--dim); }
  .perm-locked {
    display: inline-flex; align-items: center; justify-content: center;
    min-height: 2.1rem; padding: 0.35rem 0.55rem; border-radius: 6px;
    background: var(--surface); border: 1px dashed var(--border);
    color: var(--dim); font-size: 0.8rem; font-variant-numeric: tabular-nums;
  }
  .perm-row select:disabled { opacity: 0.55; cursor: not-allowed; }
  .perm-add { display: grid; grid-template-columns: 1fr 1fr auto; gap: 0.5rem; margin-top: 0.55rem; }
  .perm-actions { display: flex; flex-wrap: wrap; gap: 0.45rem; margin-top: 0.75rem; }
  .perm-status { font-size: 0.82rem; color: var(--dim); margin-top: 0.45rem; min-height: 1.1em; }
  .perm-status.ok { color: var(--green); }
  .perm-status.err { color: var(--red); }
  .map-wrap {
    background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
    padding: 0.75rem; overflow-x: auto; margin-bottom: 0.85rem;
  }
  .map-wrap svg { display: block; max-width: 100%; height: auto; }
  .map-legend {
    display: flex; flex-wrap: wrap; gap: 0.65rem 1rem; margin: 0.5rem 0 1rem;
    color: var(--dim); font-size: 0.8rem;
  }
  .map-legend span { display: inline-flex; align-items: center; gap: 0.35rem; }
  .map-legend i {
    display: inline-block; width: 14px; height: 3px; border-radius: 2px; background: var(--dim);
  }
  .map-legend i.hook { background: #bc8cff; }
  .map-legend i.access { background: var(--accent); }
  .map-legend i.data { background: var(--green); }
  .map-legend i.meta { background: var(--dim); }
  .map-legend i.denied { background: var(--red); }
  .map-legend i.library { background: var(--orange); }
  .map-narrative { margin-bottom: 0.85rem; }
  .map-libraries { display: grid; gap: 0.55rem; }
  @media (max-width: 560px) {
    .wrap { padding: 1rem 0.85rem 2rem; }
    th:nth-child(3), td:nth-child(3) { display: none; }
    .perm-row, .perm-add { grid-template-columns: 1fr; }
  }
</style>
</head>
<body>
<div class="wrap">
  <header>
    <div>
      <h1>Guardian Admin</h1>
      <p class="sub">Services, tokens, library, and cache under this instance</p>
    </div>
    <p class="sub"><a href="/v1/setup">Setup wizard</a></p>
  </header>

  <div id="auth-gate" class="auth-box" hidden>
    <h2>API key required</h2>
    <p>Enter the Guardian API key. It is stored only in this browser tab.</p>
    <form id="auth-form">
      <input type="password" id="api-key-input" placeholder="API key" autocomplete="current-password" required>
      <button class="btn btn-primary" type="submit">Connect</button>
    </form>
  </div>

  <div id="dashboard" hidden>
    <div class="status-strip" id="status-strip"></div>

    <nav role="tablist" aria-label="Admin sections">
      <button class="tab" role="tab" id="tab-map" aria-controls="panel-map" aria-selected="true">Map</button>
      <button class="tab" role="tab" id="tab-services" aria-controls="panel-services" aria-selected="false">Services</button>
      <button class="tab" role="tab" id="tab-library" aria-controls="panel-library" aria-selected="false">Library</button>
      <button class="tab" role="tab" id="tab-cache" aria-controls="panel-cache" aria-selected="false">Cache</button>
      <button class="tab" role="tab" id="tab-runtime" aria-controls="panel-runtime" aria-selected="false">Runtime</button>
    </nav>

    <section id="panel-map" class="panel" role="tabpanel" aria-labelledby="tab-map">
      <div class="toolbar">
        <span class="meta" id="map-meta">How agents, Guardian, and your services connect</span>
        <div class="grow"></div>
        <button class="btn" id="reload-flow" type="button">Refresh</button>
      </div>
      <div id="map-narrative" class="meta map-narrative"></div>
      <div class="map-wrap" id="map-svg-wrap"><div class="empty">Loading map…</div></div>
      <div class="map-legend" aria-label="Edge legend">
        <span><i class="hook"></i> Hook</span>
        <span><i class="access"></i> Agent access</span>
        <span><i class="data"></i> Live data</span>
        <span><i class="library"></i> Library fan-in</span>
        <span><i class="meta"></i> Meta / needs auth</span>
        <span><i class="denied"></i> Blocked</span>
      </div>
      <div id="map-libraries" class="map-libraries"></div>
    </section>

    <section id="panel-services" class="panel" role="tabpanel" aria-labelledby="tab-services" hidden>
      <div class="toolbar">
        <div class="grow">
          <input type="search" id="service-filter" placeholder="Filter services…" aria-label="Filter services">
        </div>
        <button class="btn" id="reload-summary" type="button">Refresh</button>
      </div>
      <div id="services-list"><div class="empty">Loading services…</div></div>
    </section>

    <section id="panel-library" class="panel" role="tabpanel" aria-labelledby="tab-library" hidden>
      <div class="toolbar">
        <div class="kind-switch" role="group" aria-label="Library data type">
          <button type="button" class="kind-btn" data-library-kind="playlists" aria-pressed="true">Playlists</button>
          <button type="button" class="kind-btn" data-library-kind="tracks" aria-pressed="false">Liked tracks</button>
          <button type="button" class="kind-btn" data-library-kind="albums" aria-pressed="false">Albums</button>
          <button type="button" class="kind-btn" data-library-kind="artists" aria-pressed="false">Artists</button>
          <button type="button" class="kind-btn" data-library-kind="contacts" aria-pressed="false">Contacts</button>
          <button type="button" class="kind-btn" data-library-kind="photos" aria-pressed="false">Photos</button>
        </div>
        <div class="grow">
          <input type="search" id="library-filter" placeholder="Filter playlists…" aria-label="Filter library">
        </div>
        <button class="btn" id="reload-library" type="button">Reload from services</button>
      </div>
      <div id="library-meta" class="meta" style="margin-bottom:0.75rem"></div>
      <div id="library-list"><div class="empty">Loading library…</div></div>
    </section>

    <section id="panel-cache" class="panel" role="tabpanel" aria-labelledby="tab-cache" hidden>
      <div class="toolbar">
        <span class="meta" id="cache-summary"></span>
        <div class="grow"></div>
        <button class="btn" id="reload-cache" type="button">Refresh</button>
        <button class="btn btn-danger" id="clear-cache" type="button">Clear cache</button>
      </div>
      <div class="card" style="padding:0; overflow:hidden">
        <div style="overflow-x:auto">
          <table>
            <thead>
              <tr>
                <th>Key</th>
                <th>Size</th>
                <th>Expires</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody id="cache-body"><tr><td colspan="4" class="muted-num">Loading…</td></tr></tbody>
          </table>
        </div>
      </div>
    </section>

    <section id="panel-runtime" class="panel" role="tabpanel" aria-labelledby="tab-runtime" hidden>
      <div id="runtime-body"><div class="empty">Loading…</div></div>
    </section>
  </div>

  <div id="global-error" class="error-box" hidden></div>
</div>

<script>
(function () {
  const $ = (id) => document.getElementById(id);
  const KEY_STORAGE = "guardian.admin.apiKey";
  let apiKey = sessionStorage.getItem(KEY_STORAGE) || "";
  let summary = null;

  function authHeaders(extra) {
    const headers = Object.assign({}, extra || {});
    if (apiKey) {
      headers["X-Api-Key"] = apiKey;
    }
    return headers;
  }

  async function api(path, opts) {
    const res = await fetch(path, Object.assign({}, opts || {}, {
      headers: authHeaders((opts && opts.headers) || {}),
      credentials: "same-origin",
    }));
    if (res.status === 401) {
      const err = new Error("unauthorized");
      err.status = 401;
      throw err;
    }
    if (!res.ok) {
      let message = res.statusText;
      try {
        const body = await res.json();
        if (body && body.error) message = body.error;
      } catch (_) {}
      const err = new Error(message);
      err.status = res.status;
      throw err;
    }
    const text = await res.text();
    if (!text) return null;
    return JSON.parse(text);
  }

  function escapeHtml(value) {
    return String(value == null ? "" : value)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function formatBytes(n) {
    if (n == null || isNaN(n)) return "—";
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    return (n / (1024 * 1024)).toFixed(1) + " MB";
  }

  const FLOW_STATUS_COLORS = {
    connected: "#3fb950",
    "needs-auth": "#d29922",
    expired: "#d29922",
    blocked: "#f85149",
    configured: "#58a6ff",
  };
  const FLOW_EDGE_COLORS = {
    hook: "#bc8cff",
    access: "#58a6ff",
    data: "#3fb950",
    library: "#d29922",
    meta: "#8b949e",
    denied: "#f85149",
  };

  // Nested layout: Aperture wraps Guardian; Guardian holds stores + data types.
  function layoutFlowMap(topo) {
    const nodes = topo.nodes || [];
    const dataTypes = topo.data_types || [];
    const services = nodes.filter((n) => n.kind === "service");
    const byId = {};
    nodes.forEach((n) => { byId[n.id] = n; });

    const chipCols = 3;
    const chipW = 118;
    const chipH = 22;
    const chipGap = 6;
    const typeRows = Math.max(1, Math.ceil(dataTypes.length / chipCols));
    const typesBlockH = 18 + typeRows * (chipH + chipGap);

    const storeH = 44;
    const storeGap = 10;
    const guardianTitleH = 36;
    const guardianPad = 12;
    const guardianH = guardianTitleH + storeH + storeGap + typesBlockH + guardianPad * 2;
    const apertureTitleH = 28;
    const aperturePad = 12;
    const apertureH = apertureTitleH + guardianH + aperturePad * 2;

    const agents = { x: 20, y: 0, w: 130, h: 56, id: "agents" };
    const aperture = { x: 180, y: 20, w: 400, h: apertureH, id: "aperture" };
    const guardian = {
      x: aperture.x + aperturePad,
      y: aperture.y + apertureTitleH + aperturePad,
      w: aperture.w - aperturePad * 2,
      h: guardianH,
      id: "guardian",
    };
    agents.y = Math.floor(aperture.y + aperture.h / 2 - agents.h / 2);

    const pos = {};
    pos.agents = Object.assign({ label: byId.agents ? byId.agents.label : "LLM Agents", kind: "agent" }, agents);
    pos.aperture = Object.assign({
      label: byId.aperture ? byId.aperture.label : "Aperture",
      kind: "gateway",
      container: true,
    }, aperture);
    pos.guardian = Object.assign({
      label: byId.guardian ? byId.guardian.label : "Guardian",
      kind: "guardian",
      container: true,
      parent: "aperture",
    }, guardian);

    const storeW = Math.floor((guardian.w - guardianPad * 2 - storeGap) / 2);
    const storeY = guardian.y + guardianTitleH + guardianPad;
    pos.tokens = {
      x: guardian.x + guardianPad,
      y: storeY,
      w: storeW,
      h: storeH,
      label: byId.tokens ? byId.tokens.label : "Token vault",
      kind: "store",
      parent: "guardian",
    };
    pos.cache = {
      x: guardian.x + guardianPad + storeW + storeGap,
      y: storeY,
      w: storeW,
      h: storeH,
      label: byId.cache ? byId.cache.label : "Response cache",
      kind: "store",
      parent: "guardian",
    };

    const typeOrigin = {
      x: guardian.x + guardianPad,
      y: storeY + storeH + storeGap,
      cols: chipCols,
      chipW: chipW,
      chipH: chipH,
      gap: chipGap,
    };

    const svcW = 200;
    const svcH = 52;
    const svcGap = 12;
    const svcTotal = services.length * (svcH + svcGap) - svcGap;
    const svcStartY = Math.max(20, Math.floor(aperture.y + aperture.h / 2 - svcTotal / 2));
    services.forEach((n, i) => {
      pos[n.id] = {
        x: 620,
        y: svcStartY + i * (svcH + svcGap),
        w: svcW,
        h: svcH,
        label: n.label,
        kind: "service",
        status: n.status,
      };
    });

    const bottom = Math.max(aperture.y + aperture.h, svcStartY + svcTotal, agents.y + agents.h) + 24;
    return {
      pos: pos,
      typeOrigin: typeOrigin,
      dataTypes: dataTypes,
      width: 850,
      height: bottom,
    };
  }

  function flowAnchor(pos, side) {
    if (side === "left") return { x: pos.x, y: pos.y + pos.h / 2 };
    if (side === "right") return { x: pos.x + pos.w, y: pos.y + pos.h / 2 };
    if (side === "top") return { x: pos.x + pos.w / 2, y: pos.y };
    return { x: pos.x + pos.w / 2, y: pos.y + pos.h };
  }

  function flowEdgePath(from, to, style) {
    // Prefer connecting to the outer container when the target is nested.
    const a = flowAnchor(from, "right");
    const b = flowAnchor(to, "left");
    if (style === "library") {
      const midY = Math.max(a.y, b.y) + 18;
      return "M " + a.x + " " + a.y + " C " + (a.x + 40) + " " + midY + ", " + (b.x - 40) + " " + midY + ", " + b.x + " " + b.y;
    }
    // Access into nested Guardian: land on Aperture's left edge first.
    if (style === "access" && to.parent) {
      const outer = { x: to.x - 12, y: to.y - 40, w: to.w + 24, h: to.h + 52 };
      const ob = flowAnchor(outer, "left");
      const midX = (a.x + ob.x) / 2;
      return "M " + a.x + " " + a.y + " C " + midX + " " + a.y + ", " + midX + " " + ob.y + ", " + ob.x + " " + ob.y;
    }
    const midX = (a.x + b.x) / 2;
    return "M " + a.x + " " + a.y + " C " + midX + " " + a.y + ", " + midX + " " + b.y + ", " + b.x + " " + b.y;
  }

  function renderFlowMap(topo) {
    const layout = layoutFlowMap(topo);
    const pos = layout.pos;
    const parts = [];
    parts.push('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + layout.width + " " + layout.height + '" width="' + layout.width + '" height="' + layout.height + '" role="img" aria-label="Guardian data-flow map">');
    parts.push("<defs>");
    Object.keys(FLOW_EDGE_COLORS).forEach((style) => {
      parts.push('<marker id="flow-arrow-' + style + '" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="' + FLOW_EDGE_COLORS[style] + '"/></marker>');
    });
    parts.push("</defs>");

    // Edges first (under nodes).
    (topo.edges || []).forEach((e) => {
      // Containment is drawn as nesting — skip parent/child arrows.
      const fromN = (topo.nodes || []).find((n) => n.id === e.from);
      const toN = (topo.nodes || []).find((n) => n.id === e.to);
      if (fromN && toN && toN.parent === e.from) return;
      if (fromN && toN && fromN.parent === e.to) return;

      const from = pos[e.from];
      const to = pos[e.to];
      if (!from || !to) return;
      // Hook into nested Guardian: target Aperture box for a cleaner landing.
      let target = to;
      if (e.style === "hook" && e.to === "guardian" && pos.aperture) {
        target = pos.aperture;
      }
      const color = FLOW_EDGE_COLORS[e.style] || "#8b949e";
      const dash = e.style === "hook" || e.style === "meta" || e.style === "denied" || e.style === "library"
        ? ' stroke-dasharray="5 4"'
        : "";
      const d = flowEdgePath(from, target, e.style);
      parts.push('<path d="' + d + '" fill="none" stroke="' + color + '" stroke-width="1.6"' + dash + ' marker-end="url(#flow-arrow-' + (FLOW_EDGE_COLORS[e.style] ? e.style : "access") + ')"/>');
      const mx = (from.x + from.w + target.x) / 2;
      const my = (from.y + from.h / 2 + target.y + target.h / 2) / 2;
      if (e.label) {
        parts.push('<text x="' + mx + '" y="' + (my - 6) + '" text-anchor="middle" fill="#8b949e" font-size="10" font-family="system-ui,sans-serif">' + escapeHtml(e.label) + "</text>");
      }
    });

    // Aperture container.
    const ap = pos.aperture;
    parts.push('<rect x="' + ap.x + '" y="' + ap.y + '" width="' + ap.w + '" height="' + ap.h + '" rx="12" fill="#12161c" stroke="#bc8cff" stroke-width="1.4" stroke-dasharray="6 4"/>');
    parts.push('<text x="' + (ap.x + 12) + '" y="' + (ap.y + 18) + '" fill="#bc8cff" font-size="12" font-weight="650" font-family="system-ui,sans-serif">' + escapeHtml(ap.label) + '</text>');
    parts.push('<text x="' + (ap.x + ap.w - 12) + '" y="' + (ap.y + 18) + '" text-anchor="end" fill="#8b949e" font-size="10" font-family="system-ui,sans-serif">pre-request gateway</text>');

    // Guardian container.
    const g = pos.guardian;
    parts.push('<rect x="' + g.x + '" y="' + g.y + '" width="' + g.w + '" height="' + g.h + '" rx="10" fill="#161b22" stroke="#58a6ff" stroke-width="1.5"/>');
    parts.push('<text x="' + (g.x + 12) + '" y="' + (g.y + 18) + '" fill="#58a6ff" font-size="13" font-weight="650" font-family="system-ui,sans-serif">' + escapeHtml(g.label) + '</text>');
    parts.push('<text x="' + (g.x + g.w - 12) + '" y="' + (g.y + 18) + '" text-anchor="end" fill="#8b949e" font-size="10" font-family="system-ui,sans-serif">hook · proxy · library</text>');

    // Stores inside Guardian.
    [pos.tokens, pos.cache].forEach((p) => {
      parts.push('<rect x="' + p.x + '" y="' + p.y + '" width="' + p.w + '" height="' + p.h + '" rx="8" fill="#21262d" stroke="#30363d" stroke-width="1.2"/>');
      parts.push('<text x="' + (p.x + p.w / 2) + '" y="' + (p.y + p.h / 2) + '" text-anchor="middle" dominant-baseline="central" fill="#e6edf3" font-size="12" font-weight="600" font-family="system-ui,sans-serif">' + escapeHtml(p.label) + "</text>");
    });

    // Data types inside Guardian.
    const origin = layout.typeOrigin;
    parts.push('<text x="' + origin.x + '" y="' + (origin.y + 12) + '" fill="#8b949e" font-size="10" font-weight="600" font-family="system-ui,sans-serif">DATA TYPES</text>');
    (layout.dataTypes || []).forEach((dt, i) => {
      const col = i % origin.cols;
      const row = Math.floor(i / origin.cols);
      const x = origin.x + col * (origin.chipW + origin.gap);
      const y = origin.y + 18 + row * (origin.chipH + origin.gap);
      const stroke = dt.readable ? "#3fb950" : "#d29922";
      const fill = dt.readable ? "rgba(63,185,80,0.12)" : "rgba(210,153,34,0.12)";
      parts.push('<rect x="' + x + '" y="' + y + '" width="' + origin.chipW + '" height="' + origin.chipH + '" rx="6" fill="' + fill + '" stroke="' + stroke + '" stroke-width="1"/>');
      const label = dt.label + (dt.kind === "library" ? "" : " · proxy");
      parts.push('<text x="' + (x + origin.chipW / 2) + '" y="' + (y + origin.chipH / 2) + '" text-anchor="middle" dominant-baseline="central" fill="#e6edf3" font-size="10" font-family="system-ui,sans-serif">' + escapeHtml(label) + "</text>");
    });

    // Agents + services (leaf boxes).
    const leafServices = (topo.nodes || []).filter((n) => n.kind === "service");
    [pos.agents].concat(leafServices.map((n) => pos[n.id])).forEach((p) => {
      if (!p) return;
      const stroke = p.kind === "service"
        ? (FLOW_STATUS_COLORS[p.status] || "#30363d")
        : "#8b949e";
      parts.push('<rect x="' + p.x + '" y="' + p.y + '" width="' + p.w + '" height="' + p.h + '" rx="8" fill="#161b22" stroke="' + stroke + '" stroke-width="1.5"/>');
      const n = (topo.nodes || []).find((x) => x.id === p.id) || {};
      const subY = p.y + p.h / 2 - (n.status ? 6 : 0);
      parts.push('<text x="' + (p.x + p.w / 2) + '" y="' + subY + '" text-anchor="middle" dominant-baseline="central" fill="#e6edf3" font-size="13" font-weight="600" font-family="system-ui,sans-serif">' + escapeHtml(p.label) + "</text>");
      if (n.status) {
        parts.push('<text x="' + (p.x + p.w / 2) + '" y="' + (p.y + p.h / 2 + 12) + '" text-anchor="middle" fill="#8b949e" font-size="10" font-family="system-ui,sans-serif">' + escapeHtml(n.status) + "</text>");
      }
    });

    parts.push("</svg>");
    $("map-svg-wrap").innerHTML = parts.join("");
  }

  function renderFlowLibraries(libraries) {
    const root = $("map-libraries");
    if (!libraries || !libraries.length) {
      root.innerHTML = '<div class="empty">No unified library datasets registered.</div>';
      return;
    }
    root.innerHTML = libraries.map((lib) => {
      const providers = (lib.providers || []).length
        ? lib.providers.map((id) => "<code>" + escapeHtml(id) + "</code>").join(" ")
        : "<em>none</em>";
      return (
        '<article class="card">' +
        '<div class="card-header"><h3>' + escapeHtml(lib.label) + '</h3> <code>' + escapeHtml(lib.path) + "</code></div>" +
        '<div class="meta">' + escapeHtml(lib.sync_rule) + "</div>" +
        '<div class="meta">Providers: ' + providers + "</div>" +
        "</article>"
      );
    }).join("");
  }

  async function loadFlow() {
    try {
      const topo = await api("/v1/admin/flow");
      $("map-narrative").textContent = topo.narrative || "";
      renderFlowMap(topo);
      renderFlowLibraries(topo.libraries);
    } catch (err) {
      if (err.status === 401) {
        showAuthGate(true);
        return;
      }
      $("map-svg-wrap").innerHTML = '<div class="error-box">' + escapeHtml(err.message || "Failed to load data-flow map") + "</div>";
    }
  }

  function tokenBadge(token, hasOAuth) {
    if (!hasOAuth) return '<span class="badge badge-gray">No OAuth</span>';
    if (!token || !token.has_token) return '<span class="badge badge-orange">Not connected</span>';
    if (token.expired) {
      if (token.has_refresh) return '<span class="badge badge-orange">Expired · auto-refresh</span>';
      return '<span class="badge badge-red">Expired</span>';
    }
    return '<span class="badge badge-green">Connected</span>';
  }

  function actionBadge(action) {
    if (action === "block") return '<span class="badge badge-red">Block</span>';
    if (action === "inject") return '<span class="badge badge-blue">Inject</span>';
    return '<span class="badge badge-gray">' + escapeHtml(action || "—") + "</span>";
  }

  const ACCESS_LEVELS = ["none", "read", "write", "read-write"];
  // Types shown in the Library tab. Permissions for these map 1:1 to library views.
  const LIBRARY_DATA_TYPES = ["playlists", "tracks", "albums", "artists", "contacts", "photos"];
  const LIBRARY_TYPE_META = {
    playlists: { label: "Playlists", endpoint: "/v1/library/playlists" },
    tracks: { label: "Liked tracks", endpoint: "/v1/library/tracks" },
    albums: { label: "Albums", endpoint: "/v1/library/albums" },
    artists: { label: "Artists", endpoint: "/v1/library/artists" },
    contacts: { label: "Contacts", endpoint: "/v1/library/contacts" },
    photos: { label: "Photos", endpoint: "/v1/library/photos" },
  };
  // Connector types that are not part of the Library tab (proxy-only / future).
  const FALLBACK_DATA_TYPES = ["playlists", "tracks", "albums", "artists", "contacts", "photos",
    "user", "search", "player", "channels", "library", "recommendations"];
  const openPermEditors = new Set();

  function isLibraryDataType(key) {
    return LIBRARY_DATA_TYPES.indexOf(key) !== -1;
  }

  function normalizeAccess(level) {
    const v = String(level || "").toLowerCase();
    if (v === "none" || v === "deny" || v === "off") return "none";
    if (v === "read" || v === "ro" || v === "readonly" || v === "read-only") return "read";
    if (v === "write" || v === "wo" || v === "writeonly" || v === "write-only") return "write";
    if (v === "read-write" || v === "full" || v === "rw" || v === "readwrite") return "read-write";
    return v || "read-write";
  }

  function accessSelect(name, selected, extraClass, disabled) {
    const cur = normalizeAccess(selected);
    const dis = disabled ? " disabled" : "";
    return '<select class="' + (extraClass || "") + '" data-perm-key="' + escapeHtml(name) + '"' + dis + ">" +
      ACCESS_LEVELS.map((lvl) =>
        '<option value="' + lvl + '"' + (lvl === cur ? " selected" : "") + ">" + lvl + "</option>"
      ).join("") +
      "</select>";
  }

  function collectPermEditor(serviceId) {
    const root = document.getElementById("perm-editor-" + CSS.escape(serviceId));
    if (!root) return null;
    const map = {};
    // Include disabled (non-library) selects so saving does not drop their
    // existing values — the user simply cannot edit them from this form.
    root.querySelectorAll("select[data-perm-key]").forEach((sel) => {
      const key = sel.getAttribute("data-perm-key");
      if (!key || key === "__new__") return;
      map[key] = sel.value;
    });
    return map;
  }

  function permRow(key, level, serviceId, opts) {
    const o = opts || {};
    const disabled = !!o.disabled;
    const labelExtra = o.labelExtra || "";
    const note = o.note ? '<span class="perm-note">' + escapeHtml(o.note) + "</span>" : "";
    // Always emit a select (even when disabled) so collectPermEditor still
    // sees the value and Save does not drop locked keys from guardian.json.
    const control = accessSelect(key, level, disabled ? "perm-locked-select" : "", disabled);
    const action = o.hideRemove
      ? "<span></span>"
      : '<button type="button" class="btn btn-danger perm-remove" data-service="' + escapeHtml(serviceId) +
        '" data-key="' + escapeHtml(key) + '" title="Remove this row"' + (disabled ? " disabled" : "") + ">Remove</button>";
    return '<div class="perm-row' + (disabled ? " perm-row-disabled" : "") + (o.library ? " perm-row-library" : "") + '">' +
      "<label>" + escapeHtml(key) + labelExtra + (note ? "<br>" + note : "") + "</label>" +
      control +
      action +
      "</div>";
  }

  function renderPermEditor(svc) {
    const id = svc.id;
    const perms = svc.permissions || {};
    const known = (svc.available_data_types && svc.available_data_types.length)
      ? svc.available_data_types
      : FALLBACK_DATA_TYPES.slice();
    const keys = Object.keys(perms).sort((a, b) => {
      if (a === "default") return -1;
      if (b === "default") return 1;
      return a.localeCompare(b);
    });
    // default + known + any explicit permission keys not already listed
    const show = ["default"];
    known.forEach((k) => { if (k !== "default" && show.indexOf(k) === -1) show.push(k); });
    keys.forEach((k) => {
      if (show.indexOf(k) === -1) show.push(k);
    });

    // Library types this connector actually supports (intersection).
    const connectorTypes = known.filter((k) => k !== "default");
    const libraryTypes = LIBRARY_DATA_TYPES.filter((t) => connectorTypes.indexOf(t) !== -1);
    const otherTypes = show.filter((k) =>
      k !== "default" && !isLibraryDataType(k));

    const defaultLevel = perms["default"] != null ? perms["default"] : "read-write";
    let html = "";

    html += '<div class="perm-section">';
    html += '<h5 class="perm-section-title">Library data</h5>';
    html += '<p class="meta perm-section-help">These types appear in the Library tab. Setting <code>none</code> hides that library view for this service.</p>';
    if (!libraryTypes.length) {
      html += '<p class="meta">This connector does not feed any Library views.</p>';
    }
    libraryTypes.forEach((key) => {
      const meta = LIBRARY_TYPE_META[key] || { label: key, endpoint: "" };
      // Unset types inherit the default fallback level (server DefaultAccessType
      // is read-write when no permissions block exists).
      const level = perms[key] != null ? perms[key] : defaultLevel;
      html += permRow(key, level, id, {
        library: true,
        hideRemove: false,
        labelExtra: ' <span class="badge badge-blue">' + escapeHtml(meta.label) + "</span>",
        note: meta.endpoint ? "Library: " + meta.endpoint : "",
      });
    });
    html += "</div>";

    html += '<div class="perm-section">';
    html += '<h5 class="perm-section-title">Other data types</h5>';
    html += '<p class="meta perm-section-help">Not shown in the Library tab. Inputs are locked so permissions stay aligned with the data you can see there. Existing values are preserved on save.</p>';
    html += permRow("default", defaultLevel, id, {
      hideRemove: true,
      labelExtra: " (fallback)",
      note: "Applies to types not listed and to locked rows without an explicit level.",
    });
    if (!otherTypes.length) {
      html += '<p class="meta">No extra connector types.</p>';
    }
    otherTypes.forEach((key) => {
      const level = perms[key] != null ? perms[key] : defaultLevel;
      // Known connector types that aren't in Library stay locked. Explicit
      // custom keys the user added remain editable.
      const isCustom = FALLBACK_DATA_TYPES.indexOf(key) === -1 && known.indexOf(key) === -1;
      html += permRow(key, level, id, {
        disabled: !isCustom,
        hideRemove: isCustom,
        note: isCustom ? "Custom type (not in Library)" : "Not in Library — proxy/API type only",
      });
    });
    html += "</div>";

    // Custom key add still allowed for advanced/explicit mappings.
    html += '<div class="perm-add">' +
      '<input type="text" class="perm-custom-key" placeholder="custom type (advanced)" aria-label="Custom data type">' +
      accessSelect("__new__", "read", "perm-custom-level") +
      '<button type="button" class="btn perm-add-btn" data-service="' + escapeHtml(id) + '">Add</button>' +
      "</div>";

    return '<div class="perm-editor" id="perm-editor-' + escapeHtml(id) + '" data-service="' + escapeHtml(id) + '">' +
      "<h4>Agent data permissions</h4>" +
      '<p class="meta" style="margin-bottom:0.55rem">Levels: none · read · write · read-write. Saving writes <code>guardian.json</code> and clears the response cache.</p>' +
      html +
      '<div class="perm-actions">' +
      '<button type="button" class="btn btn-primary perm-save" data-service="' + escapeHtml(id) + '">Save permissions</button>' +
      '<button type="button" class="btn perm-reset" data-service="' + escapeHtml(id) + '">Reset form</button>' +
      '<button type="button" class="btn btn-danger perm-clear" data-service="' + escapeHtml(id) + '">Clear (unrestricted)</button>' +
      "</div>" +
      '<div class="perm-status" id="perm-status-' + escapeHtml(id) + '"></div>' +
      "</div>";
  }

  async function savePermissions(serviceId, permissions, clear) {
    const status = document.getElementById("perm-status-" + CSS.escape(serviceId));
    if (status) {
      status.textContent = clear ? "Clearing…" : "Saving…";
      status.className = "perm-status";
    }
    try {
      const body = clear ? { permissions: {} } : { permissions: permissions || {} };
      await api("/v1/admin/services/" + encodeURIComponent(serviceId) + "/permissions", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (status) {
        status.textContent = clear ? "Cleared — agents unrestricted." : "Saved.";
        status.className = "perm-status ok";
      }
      await loadSummary();
      openPermEditors.add(serviceId);
      renderServices($("service-filter").value);
    } catch (err) {
      if (err.status === 401) {
        showAuthGate(true);
        return;
      }
      if (status) {
        status.textContent = err.message || "Failed to save";
        status.className = "perm-status err";
      } else {
        showGlobalError(err.message || "Failed to save permissions");
      }
    }
  }

  function showAuthGate(show) {
    $("auth-gate").hidden = !show;
    $("dashboard").hidden = show;
    if (show) {
      const input = $("api-key-input");
      if (input) input.focus();
    }
  }

  function showGlobalError(message) {
    const box = $("global-error");
    box.hidden = !message;
    box.textContent = message || "";
  }

  function renderStatusStrip(data) {
    const connected = data.services.filter((s) => s.token && s.token.has_token && !s.token.expired).length;
    const total = data.services.length;
    const cache = data.cache;
    const stats = [
      { label: "Auth mode", value: escapeHtml(data.runtime.auth_mode || "—") },
      { label: "Services", value: connected + " / " + total + " connected", cls: connected === total && total > 0 ? "ok" : (connected === 0 ? "warn" : "") },
      { label: "Cache live", value: cache.live + " entries", cls: cache.live > 0 ? "ok" : "" },
      { label: "Cache size", value: formatBytes(cache.bytes) },
    ];
    $("status-strip").innerHTML = stats.map((s) =>
      '<div class="stat"><div class="label">' + escapeHtml(s.label) + '</div>' +
      '<div class="value ' + (s.cls || "") + '">' + s.value + "</div></div>"
    ).join("");
  }

  function renderServices(filterText) {
    if (!summary) return;
    const q = (filterText || "").trim().toLowerCase();
    const rows = summary.services.filter((svc) => {
      if (!q) return true;
      const hay = [svc.id, svc.name, svc.action, svc.endpoint, (svc.aliases || []).join(" "), svc.description]
        .join(" ").toLowerCase();
      return hay.indexOf(q) !== -1;
    });

    if (!rows.length) {
      $("services-list").innerHTML = '<div class="empty">No services match this filter.</div>';
      return;
    }

    $("services-list").innerHTML = rows.map((svc) => {
      const token = svc.token;
      const expires = token && token.has_token && !token.expired
        ? '<span class="dim"> · expires in ' + escapeHtml(token.expires_in) + "</span>"
        : "";
      const scopes = (svc.oauth_scopes || []).map((s) => '<span class="playlist-source">' + escapeHtml(s) + "</span>").join("");
      const aliases = (svc.aliases || []).length
        ? '<div class="meta">Aliases: ' + svc.aliases.map((a) => "<code>" + escapeHtml(a) + "</code>").join(" ") + "</div>"
        : "";
      const oauthBits = svc.has_oauth
        ? '<div class="meta">OAuth: client ID ' + (svc.has_client_id ? "set" : "missing") +
          " · secret " + (svc.has_client_secret ? "set" : "missing") +
          (scopes ? '<div class="scopes">' + scopes + "</div>" : "") + "</div>"
        : '<div class="meta">No OAuth configuration</div>';

      const perms = svc.permissions || {};
      const permKeys = Object.keys(perms).sort();
      const permBits = permKeys.length
        ? '<div class="meta">Permissions: ' + permKeys.map((k) =>
            '<code>' + escapeHtml(k) + "=" + escapeHtml(perms[k]) + "</code>").join(" ") + "</div>"
        : '<div class="meta">Permissions: <em>unrestricted</em></div>';

      const actions = [];
      if (svc.login_path && svc.has_oauth) {
        actions.push('<a class="btn" href="' + escapeHtml(svc.login_path) + '">' +
          (token && token.has_token ? "Reconnect" : "Connect") + "</a>");
      }
      actions.push('<code>' + escapeHtml(svc.proxy_path) + "</code>");
      if (svc.has_playlist_provider || svc.has_liked_track_provider || svc.has_album_provider ||
          svc.has_artist_provider || svc.has_contact_provider || svc.has_photo_provider) {
        const kinds = [];
        if (svc.has_playlist_provider) kinds.push("playlists");
        if (svc.has_liked_track_provider) kinds.push("liked tracks");
        if (svc.has_album_provider) kinds.push("albums");
        if (svc.has_artist_provider) kinds.push("artists");
        if (svc.has_contact_provider) kinds.push("contacts");
        if (svc.has_photo_provider) kinds.push("photos");
        actions.push('<span class="badge badge-blue">Library: ' + escapeHtml(kinds.join(", ")) + "</span>");
      }
      if (svc.supports_photo_picker) {
        actions.push('<button type="button" class="btn photo-pick" data-service="' + escapeHtml(svc.id) + '">' +
          "Pick photos…</button>");
      }
      actions.push('<button type="button" class="btn perm-toggle" data-service="' + escapeHtml(svc.id) + '">' +
        (openPermEditors.has(svc.id) ? "Hide permissions" : "Edit permissions") + "</button>");

      const editor = openPermEditors.has(svc.id) ? renderPermEditor(svc) : "";
      const pickStatus = svc.supports_photo_picker
        ? '<div class="perm-status photo-pick-status" id="photo-pick-status-' + escapeHtml(svc.id) + '"></div>'
        : "";

      return '<article class="card" data-service-card="' + escapeHtml(svc.id) + '">' +
        '<div class="card-header"><h3>' + escapeHtml(svc.name) + "</h3>" +
        actionBadge(svc.action) + tokenBadge(token, svc.has_oauth) + expires + "</div>" +
        '<div class="meta">' + escapeHtml(svc.description || svc.endpoint || "No description") + "</div>" +
        aliases + oauthBits +
        '<div class="meta">Cache TTL: <code>' + escapeHtml(svc.cache_ttl) + "</code></div>" +
        permBits +
        '<div class="actions">' + actions.join(" ") + "</div>" +
        pickStatus +
        editor +
        "</article>";
    }).join("");
  }

  function sourceBadges(sources) {
    return (sources || []).map((src) => {
      const name = escapeHtml(src.service_name || src.service);
      if (src.url) {
        return '<a class="playlist-source" href="' + escapeHtml(src.url) + '" target="_blank" rel="noopener">' + name + "</a>";
      }
      return '<span class="playlist-source">' + name + "</span>";
    }).join("");
  }

  const LIBRARY_KINDS = {
    playlists: {
      path: "/v1/library/playlists",
      key: "playlists",
      label: "playlists",
      filterPlaceholder: "Filter playlists…",
      emptyAll: "No playlists found. Connect a service, then reload.",
      emptyFilter: "No playlists match this filter.",
    },
    tracks: {
      path: "/v1/library/tracks",
      key: "tracks",
      label: "liked tracks",
      filterPlaceholder: "Filter liked tracks…",
      emptyAll: "No liked tracks found. Connect a music service, then reload.",
      emptyFilter: "No liked tracks match this filter.",
    },
    albums: {
      path: "/v1/library/albums",
      key: "albums",
      label: "saved albums",
      filterPlaceholder: "Filter albums…",
      emptyAll: "No saved albums found. Connect a music service, then reload.",
      emptyFilter: "No albums match this filter.",
    },
    artists: {
      path: "/v1/library/artists",
      key: "artists",
      label: "followed artists",
      filterPlaceholder: "Filter artists…",
      emptyAll: "No followed artists found. Connect a music service, then reload.",
      emptyFilter: "No artists match this filter.",
    },
    contacts: {
      path: "/v1/library/contacts",
      key: "contacts",
      label: "contacts",
      filterPlaceholder: "Filter contacts…",
      emptyAll: "No contacts found. Connect a contact-capable service, then reload.",
      emptyFilter: "No contacts match this filter.",
    },
    photos: {
      path: "/v1/library/photos",
      key: "photos",
      label: "photos",
      filterPlaceholder: "Filter photos…",
      emptyAll: "No photos found. On Google Photos, use “Pick photos…” on the service card (user selects once; Guardian lists that selection). Then reload.",
      emptyFilter: "No photos match this filter.",
    },
  };

  let libraryKind = "playlists";
  const libraryDataByKind = {
    playlists: null, tracks: null, albums: null, artists: null, contacts: null, photos: null,
  };

  function currentLibraryKind() {
    return LIBRARY_KINDS[libraryKind] || LIBRARY_KINDS.playlists;
  }

  function setLibraryKind(kind) {
    if (!LIBRARY_KINDS[kind]) return;
    libraryKind = kind;
    document.querySelectorAll("[data-library-kind]").forEach((btn) => {
      const on = btn.getAttribute("data-library-kind") === kind;
      btn.setAttribute("aria-pressed", on ? "true" : "false");
    });
    $("library-filter").placeholder = currentLibraryKind().filterPlaceholder;
    $("library-filter").value = "";
    if (!libraryDataByKind[kind]) loadLibrary(kind);
    else renderLibrary(libraryDataByKind[kind]);
  }

  function libraryStatusBadges(byService) {
    return Object.keys(byService || {}).sort().map((id) => {
      const st = byService[id];
      const cls = st.status === "ok"
        ? (st.truncated ? "badge-orange" : "badge-green")
        : st.status === "unauthorized" ? "badge-orange" : "badge-red";
      const count = st.status === "ok" ? " · " + st.count : "";
      const trunc = st.truncated ? " · truncated" : "";
      const title = st.error || (st.truncated
        ? "page cap hit — more items remain upstream"
        : st.status);
      return '<span class="badge ' + cls + '" title="' + escapeHtml(title) + '">' +
        escapeHtml(id) + ": " + escapeHtml(st.status) + count + trunc +
        "</span>";
    }).join(" ");
  }

  function libraryCardHeader(title, badges) {
    return '<div class="card-header"><h3>' + escapeHtml(title || "(untitled)") + "</h3>" + (badges || "") + "</div>";
  }

  function renderPlaylistCards(items, q) {
    return items.filter((p) => {
      if (!q) return true;
      const hay = [p.name, p.description, p.owner, (p.sources || []).map((s) => s.service).join(" ")]
        .join(" ").toLowerCase();
      return hay.indexOf(q) !== -1;
    }).map((p) => {
      const multi = (p.sources || []).length > 1;
      return '<article class="card">' +
        libraryCardHeader(p.name,
          '<span class="badge badge-gray">' + (p.track_count || 0) + " tracks</span>" +
          (multi ? '<span class="badge badge-blue">' + p.sources.length + " sources</span>" : "")) +
        (p.description ? '<div class="meta">' + escapeHtml(p.description) + "</div>" : "") +
        (p.owner ? '<div class="meta">Owner: ' + escapeHtml(p.owner) + "</div>" : "") +
        '<div class="scopes">' + sourceBadges(p.sources) + "</div>" +
        "</article>";
    }).join("");
  }

  function artistNames(artists) {
    return (artists || []).map((a) => a && a.name).filter(Boolean).join(", ");
  }

  function renderTrackCards(items, q) {
    return items.filter((t) => {
      if (!q) return true;
      const hay = [t.name, artistNames(t.artists), t.album && t.album.name, t.duration,
        (t.sources || []).map((s) => s.service).join(" ")]
        .join(" ").toLowerCase();
      return hay.indexOf(q) !== -1;
    }).map((t) => {
      const multi = (t.sources || []).length > 1;
      const artists = artistNames(t.artists);
      const album = t.album && t.album.name ? t.album.name : "";
      return '<article class="card">' +
        libraryCardHeader(t.name,
          (multi ? '<span class="badge badge-blue">' + t.sources.length + " sources</span>" : "")) +
        (artists ? '<div class="meta">Artists: ' + escapeHtml(artists) + "</div>" : "") +
        (album ? '<div class="meta">Album: ' + escapeHtml(album) + "</div>" : "") +
        (t.duration ? '<div class="meta">Duration: ' + escapeHtml(t.duration) + "</div>" : "") +
        '<div class="scopes">' + sourceBadges(t.sources) + "</div>" +
        "</article>";
    }).join("");
  }

  function renderContactCards(items, q) {
    return items.filter((c) => {
      if (!q) return true;
      const hay = [c.name, c.organization,
        (c.emails || []).join(" "), (c.phones || []).join(" "), (c.handles || []).join(" "),
        (c.sources || []).map((s) => s.service).join(" ")]
        .join(" ").toLowerCase();
      return hay.indexOf(q) !== -1;
    }).map((c) => {
      const multi = (c.sources || []).length > 1;
      const emails = (c.emails || []).map((e) => '<code>' + escapeHtml(e) + "</code>").join(" ");
      const phones = (c.phones || []).map((p) => '<code>' + escapeHtml(p) + "</code>").join(" ");
      const handles = (c.handles || []).map((h) => '<code>' + escapeHtml(h) + "</code>").join(" ");
      return '<article class="card">' +
        libraryCardHeader(c.name || "(unnamed)",
          multi ? '<span class="badge badge-blue">' + c.sources.length + " sources</span>" : "") +
        (emails ? '<div class="meta">Emails: ' + emails + "</div>" : "") +
        (phones ? '<div class="meta">Phones: ' + phones + "</div>" : "") +
        (handles ? '<div class="meta">Handles: ' + handles + "</div>" : "") +
        (c.organization ? '<div class="meta">Org: ' + escapeHtml(c.organization) + "</div>" : "") +
        '<div class="scopes">' + sourceBadges(c.sources) + "</div>" +
        "</article>";
    }).join("");
  }

  function renderPhotoCards(items, q) {
    return items.filter((p) => {
      if (!q) return true;
      const hay = [p.filename, p.caption, p.mime_type, p.taken_at,
        (p.sources || []).map((s) => s.service + " " + (s.id || "")).join(" ")]
        .join(" ").toLowerCase();
      return hay.indexOf(q) !== -1;
    }).map((p) => {
      const multi = (p.sources || []).length > 1;
      const dims = (p.width && p.height) ? (p.width + "×" + p.height) : "";
      const title = p.filename || p.caption || "(untitled photo)";
      const canonical = (p.sources || []).filter((s) => s.canonical).length;
      return '<article class="card">' +
        libraryCardHeader(title,
          (multi ? '<span class="badge badge-blue">' + p.sources.length + " sources</span> " : "") +
          (canonical ? '<span class="badge badge-gray">canonical off-guardian</span>' : "")) +
        (p.taken_at ? '<div class="meta">Taken: ' + escapeHtml(p.taken_at) + "</div>" : "") +
        (dims ? '<div class="meta">Size: ' + escapeHtml(dims) +
          (p.mime_type ? " · " + escapeHtml(p.mime_type) : "") + "</div>" : "") +
        (p.caption && p.caption !== p.filename ? '<div class="meta">' + escapeHtml(p.caption) + "</div>" : "") +
        '<div class="scopes">' + sourceBadges(p.sources) + "</div>" +
        "</article>";
    }).join("");
  }

  function renderAlbumCards(items, q) {
    return items.filter((a) => {
      if (!q) return true;
      const hay = [a.name, artistNames(a.artists),
        (a.sources || []).map((s) => s.service).join(" ")]
        .join(" ").toLowerCase();
      return hay.indexOf(q) !== -1;
    }).map((a) => {
      const multi = (a.sources || []).length > 1;
      const artists = artistNames(a.artists);
      return '<article class="card">' +
        libraryCardHeader(a.name,
          multi ? '<span class="badge badge-blue">' + a.sources.length + " sources</span>" : "") +
        (artists ? '<div class="meta">Artists: ' + escapeHtml(artists) + "</div>" : "") +
        '<div class="scopes">' + sourceBadges(a.sources) + "</div>" +
        "</article>";
    }).join("");
  }

  function renderArtistCards(items, q) {
    return items.filter((a) => {
      if (!q) return true;
      const hay = [a.name, (a.sources || []).map((s) => s.service).join(" ")]
        .join(" ").toLowerCase();
      return hay.indexOf(q) !== -1;
    }).map((a) => {
      const multi = (a.sources || []).length > 1;
      return '<article class="card">' +
        libraryCardHeader(a.name,
          multi ? '<span class="badge badge-blue">' + a.sources.length + " sources</span>" : "") +
        '<div class="scopes">' + sourceBadges(a.sources) + "</div>" +
        "</article>";
    }).join("");
  }

  function renderLibrary(data) {
    const kind = currentLibraryKind();
    const meta = $("library-meta");
    const list = $("library-list");
    if (!data) {
      list.innerHTML = '<div class="empty">Loading library…</div>';
      meta.textContent = "";
      return;
    }
    const items = data[kind.key] || [];
    const statusBits = libraryStatusBadges(data.by_service);
    const endpoint = '<code>' + escapeHtml(kind.path) + "</code>";
    meta.innerHTML = "<strong>" + items.length + "</strong> " + kind.label +
      (data.synced ? " · synced across services" : "") +
      (data.truncated ? ' · <span class="badge badge-orange" title="page cap hit — more items remain upstream">truncated</span>' : "") +
      " · agents call " + endpoint +
      (statusBits ? '<div class="scopes" style="margin-top:0.4rem">' + statusBits + "</div>" : "");

    const q = ($("library-filter").value || "").trim().toLowerCase();
    let html = "";
    if (libraryKind === "tracks") html = renderTrackCards(items, q);
    else if (libraryKind === "albums") html = renderAlbumCards(items, q);
    else if (libraryKind === "artists") html = renderArtistCards(items, q);
    else if (libraryKind === "contacts") html = renderContactCards(items, q);
    else if (libraryKind === "photos") html = renderPhotoCards(items, q);
    else html = renderPlaylistCards(items, q);

    if (!html) {
      list.innerHTML = '<div class="empty">' +
        (q ? escapeHtml(kind.emptyFilter) : escapeHtml(kind.emptyAll)) +
        "</div>";
      return;
    }
    list.innerHTML = html;
  }

  async function loadLibrary(kindName) {
    const kind = LIBRARY_KINDS[kindName || libraryKind] || currentLibraryKind();
    const key = kindName || libraryKind;
    if (key === libraryKind) {
      $("library-list").innerHTML = '<div class="empty">Loading ' + escapeHtml(kind.label) + "…</div>";
    }
    try {
      const data = await api(kind.path);
      libraryDataByKind[key] = data;
      if (key === libraryKind) renderLibrary(data);
    } catch (err) {
      if (err.status === 401) {
        showAuthGate(true);
        return;
      }
      if (err.status === 404) {
        libraryDataByKind[key] = { [kind.key]: [], total: 0, by_service: {} };
        if (key === libraryKind) {
          $("library-meta").textContent = "";
          $("library-list").innerHTML = '<div class="empty">' +
            escapeHtml(err.message || ("No " + kind.label + " available.")) +
            "</div>";
        }
        return;
      }
      if (err.status === 403) {
        libraryDataByKind[key] = { [kind.key]: [], total: 0, by_service: {} };
        if (key === libraryKind) {
          $("library-meta").textContent = "";
          $("library-list").innerHTML = '<div class="error-box">' + escapeHtml(err.message) + "</div>";
        }
        return;
      }
      if (key === libraryKind) {
        $("library-list").innerHTML = '<div class="error-box">' + escapeHtml(err.message) + "</div>";
      }
    }
  }

  function loadAllLibraryKinds() {
    Object.keys(LIBRARY_KINDS).forEach((k) => loadLibrary(k));
  }

  async function loadCache() {
    try {
      const data = await api("/v1/admin/cache");
      $("cache-summary").textContent =
        data.live + " live · " + data.total + " total · " + formatBytes(data.bytes);
      const entries = data.entries || [];
      if (!entries.length) {
        $("cache-body").innerHTML = '<tr><td colspan="4" class="muted-num">Cache is empty.</td></tr>';
        return;
      }
      $("cache-body").innerHTML = entries.map((e) => {
        const status = e.expired
          ? '<span class="badge badge-gray">Expired</span>'
          : '<span class="badge badge-green">Live</span>';
        const exp = e.expires_at ? new Date(e.expires_at).toLocaleString() : "—";
        return "<tr>" +
          '<td class="key">' + escapeHtml(e.key) +
            (e.preview ? '<div class="meta" style="margin-top:0.25rem">' + escapeHtml(e.preview) + "</div>" : "") +
          "</td>" +
          '<td class="num">' + formatBytes(e.size) + "</td>" +
          '<td class="num">' + escapeHtml(exp) + "</td>" +
          "<td>" + status + "</td>" +
          "</tr>";
      }).join("");
    } catch (err) {
      if (err.status === 401) {
        showAuthGate(true);
        return;
      }
      $("cache-body").innerHTML = '<tr><td colspan="4"><div class="error-box">' + escapeHtml(err.message) + "</div></td></tr>";
    }
  }

  function renderRuntime(data) {
    const rt = data.runtime;
    const logins = (rt.allowed_logins || []).length
      ? rt.allowed_logins.map((l) => "<code>" + escapeHtml(l) + "</code>").join(" ")
      : "<em>any tailnet identity</em>";
    $("runtime-body").innerHTML =
      '<article class="card">' +
        '<div class="card-header"><h3>Runtime</h3></div>' +
        '<div class="meta">Auth mode: <code>' + escapeHtml(rt.auth_mode) + "</code></div>" +
        '<div class="meta">Base URL: <code>' + escapeHtml(rt.base_url) + "</code></div>" +
        '<div class="meta">API key configured: ' + (rt.api_key_configured ? "yes" : "no") + "</div>" +
        '<div class="meta">Allowed logins: ' + logins + "</div>" +
      "</article>" +
      '<article class="card">' +
        '<div class="card-header"><h3>Endpoints</h3></div>' +
        '<div class="meta">Agents use <code>/v1/library/playlists</code>, <code>/v1/library/tracks</code>, <code>/v1/library/albums</code>, <code>/v1/library/artists</code>, <code>/v1/library/contacts</code>, <code>/v1/library/photos</code>, and <code>/v1/services/{id}/…</code></div>' +
        '<div class="meta">Hook: <code>POST /v1/pre-request</code> · Setup: <code>/v1/setup</code></div>' +
        '<div class="meta">Admin APIs: <code>/v1/admin/summary</code>, <code>/v1/admin/flow</code>, <code>/v1/admin/cache</code>, <code>/v1/admin/services/{id}/permissions</code></div>' +
      "</article>";
  }

  async function loadSummary() {
    showGlobalError("");
    try {
      summary = await api("/v1/admin/summary");
      showAuthGate(false);
      $("dashboard").hidden = false;
      renderStatusStrip(summary);
      renderServices($("service-filter").value);
      renderRuntime(summary);
      loadFlow();
    } catch (err) {
      if (err.status === 401) {
        showAuthGate(true);
        return;
      }
      showGlobalError(err.message || "Failed to load admin summary");
    }
  }

  const FLOW_TABS = ["map", "services", "library", "cache", "runtime"];

  function selectTab(name) {
    FLOW_TABS.forEach((id) => {
      const tab = $("tab-" + id);
      const panel = $("panel-" + id);
      const on = id === name;
      tab.setAttribute("aria-selected", on ? "true" : "false");
      panel.hidden = !on;
    });
    if (name === "map") loadFlow();
    if (name === "library") loadAllLibraryKinds();
    if (name === "cache") loadCache();
  }

  FLOW_TABS.forEach((id) => {
    $("tab-" + id).addEventListener("click", () => selectTab(id));
  });

  $("auth-form").addEventListener("submit", (e) => {
    e.preventDefault();
    apiKey = $("api-key-input").value.trim();
    if (apiKey) sessionStorage.setItem(KEY_STORAGE, apiKey);
    loadSummary();
  });

  $("service-filter").addEventListener("input", (e) => renderServices(e.target.value));
  $("library-filter").addEventListener("input", () => {
    if (libraryDataByKind[libraryKind]) renderLibrary(libraryDataByKind[libraryKind]);
  });
  document.querySelectorAll("[data-library-kind]").forEach((btn) => {
    btn.addEventListener("click", () => setLibraryKind(btn.getAttribute("data-library-kind")));
  });
  $("reload-summary").addEventListener("click", () => loadSummary());
  $("reload-flow").addEventListener("click", () => loadFlow());
  $("reload-library").addEventListener("click", () => loadAllLibraryKinds());
  $("reload-cache").addEventListener("click", () => loadCache());
  $("clear-cache").addEventListener("click", async () => {
    if (!confirm("Clear all cached responses?")) return;
    $("clear-cache").disabled = true;
    try {
      await api("/v1/admin/cache/clear", { method: "POST" });
      libraryDataByKind.playlists = null;
      libraryDataByKind.tracks = null;
      libraryDataByKind.albums = null;
      libraryDataByKind.artists = null;
      libraryDataByKind.contacts = null;
      await loadCache();
      await loadSummary();
    } catch (err) {
      if (err.status === 401) showAuthGate(true);
      else showGlobalError(err.message || "Failed to clear cache");
    } finally {
      $("clear-cache").disabled = false;
    }
  });

  async function startPhotoPick(serviceId) {
    const btn = document.querySelector('button.photo-pick[data-service="' + serviceId + '"]');
    const statusEl = document.getElementById("photo-pick-status-" + serviceId);
    const setLabel = (text) => { if (btn) btn.textContent = text; };
    const setStatus = (html, kind) => {
      if (!statusEl) return;
      statusEl.innerHTML = html;
      statusEl.className = "perm-status photo-pick-status" + (kind === "ok" ? " ok" : kind === "err" ? " err" : "");
    };
    const setPlainStatus = (text, kind) => {
      if (!statusEl) return;
      statusEl.textContent = text;
      statusEl.className = "perm-status photo-pick-status" + (kind === "ok" ? " ok" : kind === "err" ? " err" : "");
    };
    if (btn) btn.disabled = true;
    setLabel("Starting…");
    setPlainStatus("Creating picker session…");
    try {
      const created = await api("/v1/services/" + encodeURIComponent(serviceId) + "/picker/sessions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      });
      if (!created || !created.id) {
        throw new Error("Picker session response missing id");
      }
      if (!created.picker_uri) {
        throw new Error("Picker session missing picker_uri");
      }
      const opened = window.open(created.picker_uri, "_blank", "noopener");
      if (!opened) {
        // Popup blocked — still show the link so the user can open it.
        setStatus("Popup blocked. Open the picker: <a href=\"" + escapeHtml(created.picker_uri) +
          "\" target=\"_blank\" rel=\"noopener\">picker_uri</a>", "err");
      } else {
        setPlainStatus("Waiting for you to select photos in the Google tab…");
      }
      const deadline = Date.now() + 5 * 60 * 1000;
      while (Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 2500));
        const sess = await api("/v1/services/" + encodeURIComponent(serviceId) +
          "/picker/sessions/" + encodeURIComponent(created.id));
        if (sess && sess.media_items_set) {
          setPlainStatus("Loading selected photos…");
          await api("/v1/services/" + encodeURIComponent(serviceId) +
            "/picker/mediaItems?sessionId=" + encodeURIComponent(created.id));
          libraryDataByKind.photos = null;
          if (libraryKind === "photos") await loadLibrary("photos");
          setLabel("Pick photos…");
          setPlainStatus("Picked photos are in Library → photos.", "ok");
          showGlobalError("");
          return;
        }
        setPlainStatus("Waiting for you to select photos in the Google tab…");
      }
      throw new Error("Timed out waiting for photo selection in the Google picker");
    } catch (err) {
      if (err && err.status === 401) {
        showAuthGate(true);
        return;
      }
      const msg = (err && err.message) || "Failed to start photo pick";
      const enableMatch = msg.match(/https:\/\/console\.developers\.google\.com\/[^\s]+/);
      if (enableMatch) {
        setStatus("Google rejected the session. Enable the API, then try again:<br><a href=\"" +
          escapeHtml(enableMatch[0]) + "\" target=\"_blank\" rel=\"noopener\">Enable Photos Picker API</a><br>" +
          escapeHtml(msg), "err");
        showGlobalError(msg);
      } else {
        setPlainStatus(msg, "err");
        showGlobalError(msg);
      }
    } finally {
      if (btn) {
        btn.disabled = false;
        btn.textContent = "Pick photos…";
      }
    }
  }

  $("services-list").addEventListener("click", (e) => {
    const btn = e.target.closest("button");
    if (!btn) return;
    const serviceId = btn.getAttribute("data-service");
    if (!serviceId) return;

    if (btn.classList.contains("photo-pick")) {
      startPhotoPick(serviceId);
      return;
    }
    if (btn.classList.contains("perm-toggle")) {
      if (openPermEditors.has(serviceId)) openPermEditors.delete(serviceId);
      else openPermEditors.add(serviceId);
      renderServices($("service-filter").value);
      return;
    }
    if (btn.classList.contains("perm-save")) {
      const map = collectPermEditor(serviceId);
      if (!map) return;
      // Unset rows that still show unrestricted default for non-default keys
      // are sent as-is so the operator can set them explicitly.
      savePermissions(serviceId, map, false);
      return;
    }
    if (btn.classList.contains("perm-clear")) {
      if (!confirm("Remove the permissions block for " + serviceId + "? Agents will have unrestricted access.")) return;
      savePermissions(serviceId, {}, true);
      return;
    }
    if (btn.classList.contains("perm-reset")) {
      renderServices($("service-filter").value);
      return;
    }
    if (btn.classList.contains("perm-remove")) {
      const row = btn.closest(".perm-row");
      if (row) row.remove();
      return;
    }
    if (btn.classList.contains("perm-add-btn")) {
      const root = document.getElementById("perm-editor-" + CSS.escape(serviceId));
      if (!root) return;
      const input = root.querySelector(".perm-custom-key");
      const levelSel = root.querySelector(".perm-custom-level");
      const key = (input && input.value || "").trim();
      if (!key) {
        if (input) input.focus();
        return;
      }
      if (key === "default") {
        // Update default row if present.
        const existing = root.querySelector('select[data-perm-key="default"]');
        if (existing && levelSel) {
          existing.value = levelSel.value;
          input.value = "";
          return;
        }
      }
      // Avoid duplicate rows.
      if (root.querySelector('select[data-perm-key="' + CSS.escape(key) + '"]')) {
        input.value = "";
        return;
      }
      const div = document.createElement("div");
      div.className = "perm-row";
      const label = document.createElement("label");
      label.textContent = key;
      const sel = document.createElement("select");
      sel.setAttribute("data-perm-key", key);
      ACCESS_LEVELS.forEach((lvl) => {
        const opt = document.createElement("option");
        opt.value = lvl;
        opt.textContent = lvl;
        if (levelSel && lvl === levelSel.value) opt.selected = true;
        sel.appendChild(opt);
      });
      const rm = document.createElement("button");
      rm.type = "button";
      rm.className = "btn btn-danger perm-remove";
      rm.setAttribute("data-service", serviceId);
      rm.setAttribute("data-key", key);
      rm.textContent = "Remove";
      div.appendChild(label);
      div.appendChild(sel);
      div.appendChild(rm);
      const addBox = root.querySelector(".perm-add");
      root.insertBefore(div, addBox);
      input.value = "";
    }
  });

  loadSummary();
})();
</script>
</body>
</html>`
