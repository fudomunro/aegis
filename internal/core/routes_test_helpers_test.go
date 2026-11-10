package core

import (
	"net/http"
)

// dispatchAdmin sends req through the registered admin route patterns with
// the production middleware chain (AdminMiddleware + body limit). Replaces
// direct handleAdminRoutes calls in tests (issue #274).
func dispatchAdmin(srv *Server, w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	registerAdminRoutes(mux, srv)
	mux.ServeHTTP(w, r)
}

// dispatchAuth sends req through the registered auth route patterns with
// the production middleware chain (rateLimitAuth). Replaces direct
// handleAuthRoutes calls in tests (issue #274).
func dispatchAuth(srv *Server, w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	registerAuthRoutes(mux, srv)
	mux.ServeHTTP(w, r)
}

// routeTestCase is one admin/auth path exercised through the pattern
// registrations (issue #274).
type routeTestCase struct {
	name     string
	method   string
	path     string
	auth     bool // true = must 401 without credentials (AdminMiddleware / AuthMiddleware)
	public   bool // true = no auth wrapper (OAuth login/callback)
	dispatch func(*Server, http.ResponseWriter, *http.Request)
}

func adminAuthPathTests() []routeTestCase {
	return []routeTestCase{
		// Admin endpoints — all AdminMiddleware.
		{"summary", "GET", "/v1/admin/summary", true, false, dispatchAdmin},
		{"flow", "GET", "/v1/admin/flow", true, false, dispatchAdmin},
		{"cache", "GET", "/v1/admin/cache", true, false, dispatchAdmin},
		{"cache-clear", "POST", "/v1/admin/cache/clear", true, false, dispatchAdmin},
		{"hook-health", "GET", "/v1/admin/hook-health", true, false, dispatchAdmin},
		{"maintenance-signals", "GET", "/v1/admin/maintenance/signals", true, false, dispatchAdmin},
		{"maintenance-scan", "POST", "/v1/admin/maintenance/scan", true, false, dispatchAdmin},
		{"mail-status", "GET", "/v1/admin/mail/status", true, false, dispatchAdmin},
		{"search-status", "GET", "/v1/admin/search/status", true, false, dispatchAdmin},
		{"mail-sync", "POST", "/v1/admin/mail/sync", true, false, dispatchAdmin},
		{"mail-import", "POST", "/v1/admin/mail/import", true, false, dispatchAdmin},
		{"sync", "GET", "/v1/admin/sync", true, false, dispatchAdmin},
		{"sync-pause", "POST", "/v1/admin/sync/pause", true, false, dispatchAdmin},
		{"sync-resume", "POST", "/v1/admin/sync/resume", true, false, dispatchAdmin},
		{"sync-schedule-enable", "POST", "/v1/admin/sync/schedules/1/enable", true, false, dispatchAdmin},
		{"sync-schedule-disable", "POST", "/v1/admin/sync/schedules/1/disable", true, false, dispatchAdmin},
		{"sync-cancel", "POST", "/v1/admin/sync/job123/cancel", true, false, dispatchAdmin},
		{"library", "GET", "/v1/admin/library/playlists", true, false, dispatchAdmin},
		{"permissions", "GET", "/v1/admin/permissions", true, false, dispatchAdmin},
		{"data-grants-get", "GET", "/v1/admin/data-grants", true, false, dispatchAdmin},
		{"data-grants-put", "PUT", "/v1/admin/data-grants", true, false, dispatchAdmin},
		{"ownership", "GET", "/v1/admin/ownership", true, false, dispatchAdmin},
		{"ownership-item", "POST", "/v1/admin/ownership/messages", true, false, dispatchAdmin},
		{"ownership-delete", "DELETE", "/v1/admin/ownership/messages", true, false, dispatchAdmin},
		{"data-scopes", "GET", "/v1/admin/data-scopes", true, false, dispatchAdmin},
		{"data-scopes-item", "GET", "/v1/admin/data-scopes/scope1", true, false, dispatchAdmin},
		{"service-grant-limits", "GET", "/v1/admin/services/spotify/grant-limits", true, false, dispatchAdmin},
		{"apps", "GET", "/v1/admin/apps", true, false, dispatchAdmin},
		{"apps-item", "GET", "/v1/admin/apps/app1", true, false, dispatchAdmin},
		{"setup", "GET", "/v1/admin/setup/immich", true, false, dispatchAdmin},
		{"setup-action", "POST", "/v1/admin/setup/immich/save", true, false, dispatchAdmin},
		{"quicken", "GET", "/v1/admin/quicken", true, false, dispatchAdmin},
		{"quicken-sub", "GET", "/v1/admin/quicken/status", true, false, dispatchAdmin},
		{"timeline", "GET", "/v1/admin/timeline", true, false, dispatchAdmin},
		{"timeline-sub", "GET", "/v1/admin/timeline/status", true, false, dispatchAdmin},
		{"signal", "GET", "/v1/admin/signal", true, false, dispatchAdmin},
		{"signal-sub", "GET", "/v1/admin/signal/status", true, false, dispatchAdmin},
		{"udk", "GET", "/v1/admin/udk", true, false, dispatchAdmin},
		{"udk-sub", "GET", "/v1/admin/udk/kinds", true, false, dispatchAdmin},
		{"admin-shell", "GET", "/v1/admin", true, false, dispatchAdmin},
		{"admin-shell-slash", "GET", "/v1/admin/", true, false, dispatchAdmin},
		{"admin-shell-head", "HEAD", "/v1/admin", true, false, dispatchAdmin},
		// SPA shells — AdminMiddleware.
		{"spa-root", "GET", "/", true, false, dispatchAdmin},
		{"spa-home", "GET", "/home", true, false, dispatchAdmin},
		{"spa-library", "GET", "/library", true, false, dispatchAdmin},
		{"spa-services", "GET", "/services", true, false, dispatchAdmin},
		{"spa-advanced", "GET", "/advanced", true, false, dispatchAdmin},
		// Auth routes.
		{"auth-status", "GET", "/v1/auth/status", true, false, dispatchAuth},
		{"auth-login", "GET", "/v1/auth/spotify/login", false, true, dispatchAuth},
		{"auth-callback", "GET", "/v1/auth/spotify/callback", false, true, dispatchAuth},
		{"auth-revoke", "DELETE", "/v1/auth/spotify", true, false, dispatchAuth},
	}
}
