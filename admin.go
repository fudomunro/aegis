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
	ProxyPath          string       `json:"proxy_path"`
	LoginPath          string       `json:"login_path,omitempty"`
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
			caps := connector.Capabilities()
			types := make([]string, 0, len(caps.DataTypes))
			for _, dt := range caps.DataTypes {
				types = append(types, dt.Name)
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
  .perm-row {
    display: grid; grid-template-columns: minmax(100px, 1.2fr) minmax(120px, 1fr) auto;
    gap: 0.5rem; align-items: center; margin-bottom: 0.45rem;
  }
  .perm-row label { font-size: 0.85rem; color: var(--dim); }
  .perm-row select, .perm-add input, .perm-add select {
    width: 100%; background: var(--surface2); border: 1px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 0.4rem 0.55rem; font: inherit; font-size: 0.85rem;
  }
  .perm-add { display: grid; grid-template-columns: 1fr 1fr auto; gap: 0.5rem; margin-top: 0.55rem; }
  .perm-actions { display: flex; flex-wrap: wrap; gap: 0.45rem; margin-top: 0.75rem; }
  .perm-status { font-size: 0.82rem; color: var(--dim); margin-top: 0.45rem; min-height: 1.1em; }
  .perm-status.ok { color: var(--green); }
  .perm-status.err { color: var(--red); }
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
      <button class="tab" role="tab" id="tab-services" aria-controls="panel-services" aria-selected="true">Services</button>
      <button class="tab" role="tab" id="tab-library" aria-controls="panel-library" aria-selected="false">Library</button>
      <button class="tab" role="tab" id="tab-cache" aria-controls="panel-cache" aria-selected="false">Cache</button>
      <button class="tab" role="tab" id="tab-runtime" aria-controls="panel-runtime" aria-selected="false">Runtime</button>
    </nav>

    <section id="panel-services" class="panel" role="tabpanel" aria-labelledby="tab-services">
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
        <div class="grow">
          <input type="search" id="library-filter" placeholder="Filter playlists…" aria-label="Filter playlists">
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
  const FALLBACK_DATA_TYPES = ["user", "tracks", "albums", "artists", "playlists", "search", "player", "channels", "library", "recommendations", "contacts"];
  const openPermEditors = new Set();

  function normalizeAccess(level) {
    const v = String(level || "").toLowerCase();
    if (v === "none" || v === "deny" || v === "off") return "none";
    if (v === "read" || v === "ro" || v === "readonly" || v === "read-only") return "read";
    if (v === "write" || v === "wo" || v === "writeonly" || v === "write-only") return "write";
    if (v === "read-write" || v === "full" || v === "rw" || v === "readwrite") return "read-write";
    return v || "read-write";
  }

  function accessSelect(name, selected, extraClass) {
    const cur = normalizeAccess(selected);
    return '<select class="' + (extraClass || "") + '" data-perm-key="' + escapeHtml(name) + '">' +
      ACCESS_LEVELS.map((lvl) =>
        '<option value="' + lvl + '"' + (lvl === cur ? " selected" : "") + ">" + lvl + "</option>"
      ).join("") +
      "</select>";
  }

  function collectPermEditor(serviceId) {
    const root = document.getElementById("perm-editor-" + CSS.escape(serviceId));
    if (!root) return null;
    const map = {};
    root.querySelectorAll("select[data-perm-key]").forEach((sel) => {
      const key = sel.getAttribute("data-perm-key");
      if (!key || key === "__new__") return;
      map[key] = sel.value;
    });
    return map;
  }

  function renderPermEditor(svc) {
    const id = svc.id;
    const perms = svc.permissions || {};
    const known = (svc.available_data_types && svc.available_data_types.length)
      ? svc.available_data_types
      : FALLBACK_DATA_TYPES;
    const keys = Object.keys(perms).sort((a, b) => {
      if (a === "default") return -1;
      if (b === "default") return 1;
      return a.localeCompare(b);
    });
    // Include default + known types even if unset (defaults to unrestricted on server).
    const show = ["default"].concat(known.filter((k) => k !== "default"));
    keys.forEach((k) => {
      if (show.indexOf(k) === -1) show.push(k);
    });

    const rows = show.map((key) => {
      const level = perms[key] != null ? perms[key] : (key === "default" ? "read-write" : "read-write");
      return '<div class="perm-row">' +
        "<label>" + escapeHtml(key) + (key === "default" ? " (fallback)" : "") + "</label>" +
        accessSelect(key, level) +
        (key === "default" ? "<span></span>" :
          '<button type="button" class="btn btn-danger perm-remove" data-service="' + escapeHtml(id) +
            '" data-key="' + escapeHtml(key) + '" title="Remove this row">Remove</button>') +
        "</div>";
    }).join("");

    return '<div class="perm-editor" id="perm-editor-' + escapeHtml(id) + '" data-service="' + escapeHtml(id) + '">' +
      "<h4>Agent data permissions</h4>" +
      '<p class="meta" style="margin-bottom:0.55rem">Levels: none · read · write · read-write. Saving writes <code>guardian.json</code> and clears the response cache.</p>' +
      rows +
      '<div class="perm-add">' +
        '<input type="text" class="perm-custom-key" placeholder="data type (e.g. contacts)" aria-label="Custom data type">' +
        accessSelect("__new__", "read", "perm-custom-level") +
        '<button type="button" class="btn perm-add-btn" data-service="' + escapeHtml(id) + '">Add</button>' +
      "</div>" +
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
      if (svc.has_playlist_provider) {
        actions.push('<span class="badge badge-blue">Library source</span>');
      }
      actions.push('<button type="button" class="btn perm-toggle" data-service="' + escapeHtml(svc.id) + '">' +
        (openPermEditors.has(svc.id) ? "Hide permissions" : "Edit permissions") + "</button>");

      const editor = openPermEditors.has(svc.id) ? renderPermEditor(svc) : "";

      return '<article class="card" data-service-card="' + escapeHtml(svc.id) + '">' +
        '<div class="card-header"><h3>' + escapeHtml(svc.name) + "</h3>" +
        actionBadge(svc.action) + tokenBadge(token, svc.has_oauth) + expires + "</div>" +
        '<div class="meta">' + escapeHtml(svc.description || svc.endpoint || "No description") + "</div>" +
        aliases + oauthBits +
        '<div class="meta">Cache TTL: <code>' + escapeHtml(svc.cache_ttl) + "</code></div>" +
        permBits +
        '<div class="actions">' + actions.join(" ") + "</div>" +
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

  async function loadLibrary() {
    const list = $("library-list");
    const meta = $("library-meta");
    list.innerHTML = '<div class="empty">Loading library…</div>';
    meta.textContent = "";
    try {
      const data = await api("/v1/library/playlists");
      libraryData = data;
      const playlists = data.playlists || [];
      const byService = data.by_service || {};
      const statusBits = Object.keys(byService).sort().map((id) => {
        const st = byService[id];
        const cls = st.status === "ok" ? "badge-green" : st.status === "unauthorized" ? "badge-orange" : "badge-red";
        const count = st.status === "ok" ? " · " + st.count : "";
        return '<span class="badge ' + cls + '">' + escapeHtml(id) + ": " + escapeHtml(st.status) + count + "</span>";
      }).join(" ");
      meta.innerHTML = "<strong>" + playlists.length + "</strong> playlists" +
        (data.synced ? " · name-synced across services" : "") +
        (statusBits ? '<div class="scopes" style="margin-top:0.4rem">' + statusBits + "</div>" : "");

      applyLibraryFilter($("library-filter").value, data);
    } catch (err) {
      if (err.status === 401) {
        showAuthGate(true);
        return;
      }
      list.innerHTML = '<div class="error-box">' + escapeHtml(err.message) + "</div>";
    }
  }

  let libraryData = null;

  function applyLibraryFilter(filterText, data) {
    const source = data || libraryData;
    if (!source) return;
    const q = (filterText || "").trim().toLowerCase();
    const playlists = (source.playlists || []).filter((p) => {
      if (!q) return true;
      const hay = [p.name, p.description, p.owner, (p.sources || []).map((s) => s.service).join(" ")]
        .join(" ").toLowerCase();
      return hay.indexOf(q) !== -1;
    });

    if (!playlists.length) {
      $("library-list").innerHTML = '<div class="empty">' +
        (q ? "No playlists match this filter." : "No playlists found. Connect a service, then reload.") +
        "</div>";
      return;
    }

    $("library-list").innerHTML = playlists.map((p) => {
      return '<article class="card">' +
        '<div class="card-header"><h3>' + escapeHtml(p.name) + "</h3>" +
        '<span class="badge badge-gray">' + (p.track_count || 0) + " tracks</span>" +
        ((p.sources || []).length > 1 ? '<span class="badge badge-blue">' + p.sources.length + " sources</span>" : "") +
        "</div>" +
        (p.description ? '<div class="meta">' + escapeHtml(p.description) + "</div>" : "") +
        (p.owner ? '<div class="meta">Owner: ' + escapeHtml(p.owner) + "</div>" : "") +
        '<div class="scopes">' + sourceBadges(p.sources) + "</div>" +
        "</article>";
    }).join("");
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
        '<div class="meta">Agents use <code>/v1/library/playlists</code> and <code>/v1/services/{id}/…</code></div>' +
        '<div class="meta">Hook: <code>POST /v1/pre-request</code> · Setup: <code>/v1/setup</code></div>' +
        '<div class="meta">Admin APIs: <code>/v1/admin/summary</code>, <code>/v1/admin/cache</code>, <code>/v1/admin/services/{id}/permissions</code></div>' +
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
    } catch (err) {
      if (err.status === 401) {
        showAuthGate(true);
        return;
      }
      showGlobalError(err.message || "Failed to load admin summary");
    }
  }

  function selectTab(name) {
    ["services", "library", "cache", "runtime"].forEach((id) => {
      const tab = $("tab-" + id);
      const panel = $("panel-" + id);
      const on = id === name;
      tab.setAttribute("aria-selected", on ? "true" : "false");
      panel.hidden = !on;
    });
    if (name === "library" && !libraryData) loadLibrary();
    if (name === "cache") loadCache();
  }

  ["services", "library", "cache", "runtime"].forEach((id) => {
    $("tab-" + id).addEventListener("click", () => selectTab(id));
  });

  $("auth-form").addEventListener("submit", (e) => {
    e.preventDefault();
    apiKey = $("api-key-input").value.trim();
    if (apiKey) sessionStorage.setItem(KEY_STORAGE, apiKey);
    loadSummary();
  });

  $("service-filter").addEventListener("input", (e) => renderServices(e.target.value));
  $("library-filter").addEventListener("input", (e) => applyLibraryFilter(e.target.value));
  $("reload-summary").addEventListener("click", () => loadSummary());
  $("reload-library").addEventListener("click", () => loadLibrary());
  $("reload-cache").addEventListener("click", () => loadCache());
  $("clear-cache").addEventListener("click", async () => {
    if (!confirm("Clear all cached responses?")) return;
    $("clear-cache").disabled = true;
    try {
      await api("/v1/admin/cache/clear", { method: "POST" });
      await loadCache();
      await loadSummary();
    } catch (err) {
      if (err.status === 401) showAuthGate(true);
      else showGlobalError(err.message || "Failed to clear cache");
    } finally {
      $("clear-cache").disabled = false;
    }
  });

  $("services-list").addEventListener("click", (e) => {
    const btn = e.target.closest("button");
    if (!btn) return;
    const serviceId = btn.getAttribute("data-service");
    if (!serviceId) return;

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
