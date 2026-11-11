package core

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
)

// registerRoutes wires every HTTP route on mux. Called once from Run after
// the server is built. Admin and auth surfaces use Go 1.22 pattern
// registrations so the API-listener allowlist can be checked against the
// real pattern set in tests (issue #274) and connectors can eventually
// contribute their own admin routes through the onboarding seam.
// A non-nil fakeUpstream mounts the dev-only injectable provider.
func registerRoutes(mux *http.ServeMux, srv *Server, fakeUpstream *connectors.FakeUpstream) {
	registerBrandRoutes(mux, srv)

	// Observability (issue #105). Health probes are unauthenticated so
	// supervisors can poll cheaply; /metrics is admin-scoped (issue #181)
	// because series include service ids and denial reasons.
	mux.HandleFunc("GET /healthz", srv.HandleHealthz)
	mux.HandleFunc("HEAD /healthz", srv.HandleHealthz)
	mux.HandleFunc("GET /readyz", srv.HandleReadyz)
	mux.HandleFunc("HEAD /readyz", srv.HandleReadyz)
	mux.HandleFunc("GET /metrics", srv.AdminMiddleware(srv.admin().HandleMetrics))
	mux.HandleFunc("HEAD /metrics", srv.AdminMiddleware(srv.admin().HandleMetrics))

	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(withBodyLimit(maxHookBodyBytes, srv.HandlePreRequest)))

	// Third-party grant demo (issue #131). Consent GET + demo page are public
	// like OAuth authorize; grant POST and token exchange have their own rules.
	mux.HandleFunc("GET /demo/third-party", srv.admin().HandleThirdPartyDemo)
	mux.HandleFunc("HEAD /demo/third-party", srv.admin().HandleThirdPartyDemo)

	// Dev-only injectable fake provider (issue #86) for driving the
	// sync-maintenance loop with synthetic drift. Data paths are open like
	// the rest of /demo; control is admin-scoped.
	if fakeUpstream != nil {
		mux.HandleFunc("/demo/fake-upstream/control", srv.AdminMiddleware(fakeUpstream.ServeControl))
		mux.HandleFunc("/demo/fake-upstream/", fakeUpstream.ServeHTTP)
	}

	// Auth/token routes carry credential exchange — brute-force damped
	// (issue #253) per client IP.
	mux.HandleFunc("GET /v1/apps/authorize", srv.rateLimitAuth(srv.admin().HandleAppAuthorize))
	// Minting an app token is admin-equivalent (issue #181).
	mux.HandleFunc("POST /v1/apps/authorize", srv.AdminMiddleware(withBodyLimit(shared.MaxAdminBodyBytes, srv.admin().HandleAppAuthorize)))
	mux.HandleFunc("POST /v1/apps/token", srv.rateLimitAuth(withBodyLimit(shared.MaxAdminBodyBytes, srv.admin().HandleAppToken)))
	mux.HandleFunc("POST /v1/apps/revoke", srv.rateLimitAuth(withBodyLimit(shared.MaxAdminBodyBytes, srv.admin().HandleAppRevoke)))

	for path, handler := range srv.library().RouteHandlers() {
		mux.HandleFunc(path, srv.AuthMiddleware(handler))
	}
	// Library writes are authorized by the request key's data grant (a
	// write level unlocks them); the admin credential stays reserved for
	// policy edits, OAuth, and app registration.
	mux.HandleFunc("POST /v1/library/mirror", srv.AuthMiddleware(withBodyLimit(shared.MaxAdminBodyBytes, srv.library().HandleLibraryMirror)))
	// Photo share / ownership-claim PoC (issue #20).
	mux.HandleFunc("POST /v1/library/photos/share", srv.AuthMiddleware(withBodyLimit(shared.MaxAdminBodyBytes, srv.library().HandlePhotoShare)))
	mux.HandleFunc("POST /v1/library/links", srv.AuthMiddleware(withBodyLimit(shared.MaxAdminBodyBytes, srv.library().HandleEntityLinks)))
	mux.HandleFunc("DELETE /v1/library/links/", srv.AuthMiddleware(withBodyLimit(shared.MaxAdminBodyBytes, srv.library().HandleEntityLinks)))

	mux.HandleFunc("/v1/services/", srv.AuthMiddleware(withBodyLimit(maxProxyBodyBytes, srv.library().HandleServiceProxy)))

	registerAuthRoutes(mux, srv)
	registerAdminRoutes(mux, srv)
}

// registerAuthRoutes registers /v1/auth/* with per-route patterns
// (issue #274). rateLimitAuth damps brute-force attempts on credential
// exchange; OAuth login/callback are reachable without an auth wrapper so
// browser redirects complete. The {service} catch-all keeps the DELETE /
// 404 split that handleAuthRoutes implemented in its default case.
func registerAuthRoutes(mux *http.ServeMux, srv *Server) {
	mux.HandleFunc("/v1/auth/status", srv.AuthMiddleware(srv.HandleAuthStatus))
	mux.HandleFunc("/v1/auth/{service}/login", srv.rateLimitAuth(srv.HandleAuthLogin))
	mux.HandleFunc("/v1/auth/{service}/callback", srv.rateLimitAuth(srv.HandleAuthCallback))
	mux.HandleFunc("/v1/auth/{service}", srv.rateLimitAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			srv.AdminMiddleware(srv.HandleAuthRevoke)(w, r)
			return
		}
		shared.WriteJSONError(w, http.StatusNotFound, "unknown auth endpoint")
	}))
	// Fallback: unlisted /v1/auth/* paths answer 404 JSON (the former
	// handleAuthRoutes default case).
	mux.HandleFunc("/v1/auth/", srv.rateLimitAuth(func(w http.ResponseWriter, r *http.Request) {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown auth endpoint")
	}))
}

// registerAdminRoutes registers /v1/admin/* with per-route patterns
// (issue #274), replacing the former hand-rolled handleAdminRoutes switch.
// Every route carries AdminMiddleware + body limit; the wildcard routes
// delegate sub-path parsing to their handlers exactly as before.
func registerAdminRoutes(mux *http.ServeMux, srv *Server) {
	adminOnly := func(h http.HandlerFunc) http.HandlerFunc {
		return srv.AdminMiddleware(withBodyLimit(shared.MaxAdminBodyBytes, h))
	}

	// Admin shell (issues #98, #181): the SPA is markup only, but
	// unauthenticated peers should not learn the API surface and agent
	// credentials must not open the admin UI.
	// Friendly SPA routes (issue #150) are the durable URLs the client uses
	// with history.pushState so refresh keeps the same section.
	for _, path := range []string{
		"/{$}",
		"/home",
		"/home/{$}",
		"/library",
		"/library/{$}",
		"/services",
		"/services/{$}",
		"/advanced",
		"/advanced/{$}",
	} {
		mux.HandleFunc("GET "+path, adminOnly(srv.admin().HandleAdmin))
		mux.HandleFunc("HEAD "+path, adminOnly(srv.admin().HandleAdmin))
	}

	// Admin shell at /v1/admin — registered without method constraints to
	// match the former registration (the handler returns 405 internally).
	mux.HandleFunc("/v1/admin", adminOnly(srv.admin().HandleAdmin))
	mux.HandleFunc("/v1/admin/{$}", adminOnly(srv.admin().HandleAdmin))

	// Fixed admin endpoints. Method constraints are omitted here because
	// the catch-all fallback below would swallow mux-level 405s; handlers
	// keep their internal method switches (issue #274).
	mux.HandleFunc("/v1/admin/summary", adminOnly(srv.admin().HandleAdminSummary))
	mux.HandleFunc("/v1/admin/flow", adminOnly(srv.admin().HandleAdminFlow))
	mux.HandleFunc("/v1/admin/cache", adminOnly(srv.admin().HandleAdminCache))
	mux.HandleFunc("/v1/admin/cache/clear", adminOnly(srv.admin().HandleAdminCacheClear))
	mux.HandleFunc("/v1/admin/hook-health", adminOnly(srv.admin().HandleAdminHookHealth))
	mux.HandleFunc("/v1/admin/maintenance/signals", adminOnly(srv.admin().HandleAdminMaintenanceSignals))
	mux.HandleFunc("/v1/admin/mail/status", adminOnly(srv.admin().HandleAdminMailStatus))
	mux.HandleFunc("/v1/admin/search/status", adminOnly(srv.admin().HandleAdminSearchStatus))
	mux.HandleFunc("/v1/admin/mail/sync", adminOnly(srv.admin().HandleAdminMailSync))
	mux.HandleFunc("/v1/admin/mail/import", adminOnly(srv.admin().HandleAdminMailImport))
	mux.HandleFunc("/v1/admin/sync", adminOnly(srv.admin().HandleAdminSync))
	mux.HandleFunc("/v1/admin/sync/pause", adminOnly(srv.admin().HandleAdminSyncPause))
	mux.HandleFunc("/v1/admin/sync/resume", adminOnly(srv.admin().HandleAdminSyncResume))
	mux.HandleFunc("/v1/admin/sync/schedules/{id}/enable", adminOnly(srv.admin().HandleAdminSyncSchedule))
	mux.HandleFunc("/v1/admin/sync/schedules/{id}/disable", adminOnly(srv.admin().HandleAdminSyncSchedule))
	mux.HandleFunc("/v1/admin/sync/{id}/cancel", adminOnly(srv.admin().HandleAdminSyncCancel))
	mux.HandleFunc("/v1/admin/library/{kind}", adminOnly(srv.admin().HandleAdminLibrary))
	mux.HandleFunc("/v1/admin/permissions", adminOnly(srv.admin().HandleAdminPermissionsOverview))
	mux.HandleFunc("/v1/admin/data-grants", adminOnly(srv.admin().HandleAdminDataGrants))
	mux.HandleFunc("POST /v1/admin/services/{id}/sync", adminOnly(srv.admin().HandleAdminServiceSync))
	mux.HandleFunc("/v1/admin/ownership", adminOnly(srv.admin().HandleAdminOwnership))
	mux.HandleFunc("/v1/admin/ownership/{path...}", adminOnly(srv.admin().HandleAdminOwnership))
	mux.HandleFunc("/v1/admin/data-scopes", adminOnly(srv.admin().HandleAdminDataScopes))
	mux.HandleFunc("/v1/admin/data-scopes/{path...}", adminOnly(srv.admin().HandleAdminDataScopes))
	mux.HandleFunc("/v1/admin/apps", adminOnly(srv.admin().HandleAdminApps))
	mux.HandleFunc("/v1/admin/apps/{path...}", adminOnly(srv.admin().HandleAdminApps))
	mux.HandleFunc("/v1/admin/setup/{path...}", adminOnly(srv.admin().HandleAdminOnboarding))
	mux.HandleFunc("/v1/admin/quicken", adminOnly(srv.admin().HandleAdminQuicken))
	mux.HandleFunc("/v1/admin/quicken/{path...}", adminOnly(srv.admin().HandleAdminQuicken))
	mux.HandleFunc("/v1/admin/timeline", adminOnly(srv.admin().HandleAdminTimeline))
	mux.HandleFunc("/v1/admin/timeline/{path...}", adminOnly(srv.admin().HandleAdminTimeline))
	mux.HandleFunc("/v1/admin/signal", adminOnly(srv.admin().HandleAdminSignal))
	mux.HandleFunc("/v1/admin/signal/{path...}", adminOnly(srv.admin().HandleAdminSignal))
	mux.HandleFunc("/v1/admin/udk", adminOnly(srv.admin().HandleAdminUDK))
	mux.HandleFunc("/v1/admin/udk/{path...}", adminOnly(srv.admin().HandleAdminUDK))

	// Fallback: unlisted /v1/admin/* paths answer 404 (the former
	// handleAdminRoutes default case). Pattern specificity ensures every
	// registered route above wins over this catch-all.
	mux.HandleFunc("/v1/admin/", adminOnly(func(w http.ResponseWriter, r *http.Request) {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}))
}
