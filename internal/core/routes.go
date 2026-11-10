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
	mux.HandleFunc("GET /metrics", srv.AdminMiddleware(srv.HandleMetrics))
	mux.HandleFunc("HEAD /metrics", srv.AdminMiddleware(srv.HandleMetrics))

	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(withBodyLimit(maxHookBodyBytes, srv.HandlePreRequest)))

	// Third-party grant demo (issue #131). Consent GET + demo page are public
	// like OAuth authorize; grant POST and token exchange have their own rules.
	mux.HandleFunc("GET /demo/third-party", srv.HandleThirdPartyDemo)
	mux.HandleFunc("HEAD /demo/third-party", srv.HandleThirdPartyDemo)

	// Dev-only injectable fake provider (issue #86) for driving the
	// sync-maintenance loop with synthetic drift. Data paths are open like
	// the rest of /demo; control is admin-scoped.
	if fakeUpstream != nil {
		mux.HandleFunc("/demo/fake-upstream/control", srv.AdminMiddleware(fakeUpstream.ServeControl))
		mux.HandleFunc("/demo/fake-upstream/", fakeUpstream.ServeHTTP)
	}

	// Auth/token routes carry credential exchange — brute-force damped
	// (issue #253) per client IP.
	mux.HandleFunc("GET /v1/apps/authorize", srv.rateLimitAuth(srv.HandleAppAuthorize))
	// Minting an app token is admin-equivalent (issue #181).
	mux.HandleFunc("POST /v1/apps/authorize", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleAppAuthorize)))
	mux.HandleFunc("POST /v1/apps/token", srv.rateLimitAuth(withBodyLimit(maxAdminBodyBytes, srv.HandleAppToken)))
	mux.HandleFunc("POST /v1/apps/revoke", srv.rateLimitAuth(withBodyLimit(maxAdminBodyBytes, srv.HandleAppRevoke)))

	for path, handler := range srv.libraryRouteHandlers() {
		mux.HandleFunc(path, srv.AuthMiddleware(handler))
	}
	// Library writes are admin (issue #181); GET stays on AuthMiddleware above.
	mux.HandleFunc("POST /v1/library/mirror", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleLibraryMirror)))
	// Photo share / ownership-claim PoC (issue #20).
	mux.HandleFunc("POST /v1/library/photos/share", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandlePhotoShare)))
	mux.HandleFunc("POST /v1/library/links", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleEntityLinks)))
	mux.HandleFunc("DELETE /v1/library/links/", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleEntityLinks)))

	mux.HandleFunc("/v1/services/", srv.AuthMiddleware(withBodyLimit(maxProxyBodyBytes, srv.HandleServiceProxy)))

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
	admin := func(h http.HandlerFunc) http.HandlerFunc {
		return srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, h))
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
		mux.HandleFunc("GET "+path, admin(srv.HandleAdmin))
		mux.HandleFunc("HEAD "+path, admin(srv.HandleAdmin))
	}

	// Admin shell at /v1/admin — registered without method constraints to
	// match the former registration (the handler returns 405 internally).
	mux.HandleFunc("/v1/admin", admin(srv.HandleAdmin))
	mux.HandleFunc("/v1/admin/{$}", admin(srv.HandleAdmin))

	// Fixed admin endpoints. Method constraints are omitted here because
	// the catch-all fallback below would swallow mux-level 405s; handlers
	// keep their internal method switches (issue #274).
	mux.HandleFunc("/v1/admin/summary", admin(srv.HandleAdminSummary))
	mux.HandleFunc("/v1/admin/flow", admin(srv.HandleAdminFlow))
	mux.HandleFunc("/v1/admin/cache", admin(srv.HandleAdminCache))
	mux.HandleFunc("/v1/admin/cache/clear", admin(srv.HandleAdminCacheClear))
	mux.HandleFunc("/v1/admin/hook-health", admin(srv.HandleAdminHookHealth))
	mux.HandleFunc("/v1/admin/maintenance/signals", admin(srv.HandleAdminMaintenanceSignals))
	mux.HandleFunc("/v1/admin/mail/status", admin(srv.HandleAdminMailStatus))
	mux.HandleFunc("/v1/admin/search/status", admin(srv.HandleAdminSearchStatus))
	mux.HandleFunc("/v1/admin/mail/sync", admin(srv.HandleAdminMailSync))
	mux.HandleFunc("/v1/admin/mail/import", admin(srv.HandleAdminMailImport))
	mux.HandleFunc("/v1/admin/sync", admin(srv.HandleAdminSync))
	mux.HandleFunc("/v1/admin/sync/pause", admin(srv.HandleAdminSyncPause))
	mux.HandleFunc("/v1/admin/sync/resume", admin(srv.HandleAdminSyncResume))
	mux.HandleFunc("/v1/admin/sync/schedules/{id}/enable", admin(srv.HandleAdminSyncSchedule))
	mux.HandleFunc("/v1/admin/sync/schedules/{id}/disable", admin(srv.HandleAdminSyncSchedule))
	mux.HandleFunc("/v1/admin/sync/{id}/cancel", admin(srv.HandleAdminSyncCancel))
	mux.HandleFunc("/v1/admin/library/{kind}", admin(srv.HandleAdminLibrary))
	mux.HandleFunc("/v1/admin/permissions", admin(srv.HandleAdminPermissionsOverview))
	mux.HandleFunc("/v1/admin/data-grants", admin(srv.HandleAdminDataGrants))
	mux.HandleFunc("/v1/admin/ownership", admin(srv.HandleAdminOwnership))
	mux.HandleFunc("/v1/admin/ownership/{path...}", admin(srv.HandleAdminOwnership))
	mux.HandleFunc("/v1/admin/data-scopes", admin(srv.HandleAdminDataScopes))
	mux.HandleFunc("/v1/admin/data-scopes/{path...}", admin(srv.HandleAdminDataScopes))
	mux.HandleFunc("/v1/admin/services/{id}/grant-limits", admin(srv.HandleAdminServiceGrantLimits))
	mux.HandleFunc("/v1/admin/apps", admin(srv.HandleAdminApps))
	mux.HandleFunc("/v1/admin/apps/{path...}", admin(srv.HandleAdminApps))
	mux.HandleFunc("/v1/admin/setup/{path...}", admin(srv.handleAdminOnboarding))
	mux.HandleFunc("/v1/admin/quicken", admin(srv.HandleAdminQuicken))
	mux.HandleFunc("/v1/admin/quicken/{path...}", admin(srv.HandleAdminQuicken))
	mux.HandleFunc("/v1/admin/timeline", admin(srv.HandleAdminTimeline))
	mux.HandleFunc("/v1/admin/timeline/{path...}", admin(srv.HandleAdminTimeline))
	mux.HandleFunc("/v1/admin/signal", admin(srv.HandleAdminSignal))
	mux.HandleFunc("/v1/admin/signal/{path...}", admin(srv.HandleAdminSignal))
	mux.HandleFunc("/v1/admin/udk", admin(srv.HandleAdminUDK))
	mux.HandleFunc("/v1/admin/udk/{path...}", admin(srv.HandleAdminUDK))

	// Fallback: unlisted /v1/admin/* paths answer 404 (the former
	// handleAdminRoutes default case). Pattern specificity ensures every
	// registered route above wins over this catch-all.
	mux.HandleFunc("/v1/admin/", admin(func(w http.ResponseWriter, r *http.Request) {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}))
}
