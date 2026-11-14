package core

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/admin"
	"github.com/fudomunro/aegis/internal/admin/apps"
	"github.com/fudomunro/aegis/internal/admin/scopes"
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/audit"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/hookhealth"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/core/ownership"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/core/udk"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/library/links"
	"github.com/fudomunro/aegis/internal/library/search"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/syncjobs"
)

// AuthMode selects how Aegis authenticates inbound API requests.
const (
	// AuthModeAuto accepts Tailscale identity when present, otherwise an API key.
	// Recommended default for tailnet-only deployments that also keep a key
	// for external bridges.
	AuthModeAuto = "auto"
	// AuthModeTailnet requires Tailscale identity (Serve headers or WhoIs).
	// API key is not used. This is the recommended mode when Aegis is
	// only reachable inside the tailnet.
	AuthModeTailnet = "tailnet"
	// AuthModeAPIKey requires the shared API key only (legacy).
	AuthModeAPIKey = "api-key"
)

type Server struct {
	// apiKey authenticates LLM-agent / third-party callers (library reads,
	// pre-request hook, content transport). Never admin (issue #181).
	apiKey string
	// adminAPIKey authenticates admin principals (curl, scripts). Distinct
	// from apiKey so a prompt-injected agent cannot use its agent key to
	// rewrite data grants. Optional when admin logins cover human operators.
	adminAPIKey string
	// authMode is AuthModeAuto, AuthModeTailnet, or AuthModeAPIKey.
	authMode string
	// allowedLogins, when non-empty, restricts which tailnet identities may
	// call the API. Empty means any authenticated tailnet peer is allowed.
	allowedLogins []string
	// adminLogins, when non-empty, is the tailnet allowlist for admin
	// surfaces. Empty means every allowed *user* login is admin; tagged
	// device identities are agent-only unless listed here (issue #181).
	adminLogins []string
	// identity resolves Tailscale peer identity. Optional in api-key mode.
	identity IdentityResolver
	baseURL  string
	// lib is the agent data plane (issue #358): kind composition, library
	// routes, content transport, and agent-facing copy.
	lib         *library.Handlers
	libraryOnce sync.Once
	// adminHandlers is the management surface (issue #358).
	adminHandlers *admin.Handlers
	adminOnce     sync.Once
	// syncDomain is the background-consistency executor (issue #358).
	syncDomain     *syncjobs.Runners
	syncDomainOnce sync.Once
	// dataDir is the resolved local state directory (issue #74). Surfaced
	// on the admin Runtime panel so the layout is discoverable.
	dataDir string
	config  *Config
	cache   *shared.Cache
	// mbCache holds MusicBrainz MBID lookups (issue #273). Separate from
	// cache so library invalidation and aggregate churn never spend the
	// rate-limited lookup budget re-resolving the same tracks.
	mbCache     *shared.Cache
	tokens      *tokens.TokenStore
	oauthStates *shared.StateStore
	// rl damps credential guessing on auth/token surfaces (issue #253).
	rl *rateLimiter
	// refreshFlight coalesces concurrent OAuth refresh calls per connection.
	refreshFlight shared.FlightGroup[*tokens.OAuthToken]
	connectors    map[string]connectors.ServiceConnector
	// metrics is the in-memory access ring buffer for the admin map (issue #32).
	metrics *metrics.AccessMetrics
	// links holds user-confirmed cross-service entity links (issue #4).
	links *links.Store
	// ownership holds per-kind user confirmation that Aegis already manages
	// all the data of that kind the user cares about (issue #165).
	ownership *ownership.Store
	// copies is the durable copy ledger (issue #76). May be disabled.
	copyLedger *copies.Ledger
	// syncJobs is the durable sync job queue (issue #227). May be disabled.
	// Co-located with copies by default so completion and copy stamps share
	// one transaction.
	syncJobs *syncjobs.JobStore
	// syncRunners maps (kind, job_type) to a runner. Held here, not only on
	// the worker, so admin enqueue can validate a request even when
	// -sync-workers 0 leaves the pool off.
	syncRunners *syncjobs.RunnerRegistry
	// syncWorker drains syncJobs. Nil when the queue is disabled.
	syncWorker *syncjobs.SyncWorker
	// syncSchedules holds recurring sync job templates (issue #227 phase 2).
	// May be disabled.
	syncSchedules *syncjobs.ScheduleStore
	// syncConflicts is the durable conflict queue (issue #365 phase 3):
	// detected non-hub edits awaiting an accept/reject decision. May be
	// disabled.
	syncConflicts *syncjobs.ConflictStore
	// mail is the optional local compressed mail archive (the messages
	// kind's local source).
	mail *mail.Archive
	// mailIngests are the archive ingest sources keyed by service id
	// (issue #286). Wired beside connectors; see mail_ingest.go.
	mailIngests map[string]connectors.MailIngest
	// search is the local library search index (issue #178). May be disabled.
	search *search.Index
	// quicken is the optional local Quicken import store (issue #141).
	quicken *connectors.QuickenStore
	// timeline is the optional local Google Timeline import store (issue #142).
	timeline *connectors.TimelineStore
	// signal is the optional local Signal import store (issue #168).
	signal *connectors.SignalStore
	// linkedin is the optional local LinkedIn import store (issue #218).
	linkedin *connectors.LinkedInStore
	// hookHealth records Aperture pre-request outcomes for admin diagnostics.
	hookHealth *hookhealth.HookHealth
	// audit is the durable access audit log (issue #105). May be disabled.
	audit *audit.Log
	// prom holds process-lifetime counters for GET /metrics (issue #105).
	prom *metrics.PromMetrics
	// apertureURL is the optional public base URL of the Aperture gateway.
	// Used only for admin live links / setup guide — never required for proxying.
	apertureURL string
	// apps is the third-party application registry (issue #131). May be nil
	// in tests that never exercise app tokens.
	apps *apps.Store
	// scopes resolves data-scope API keys to principal → dataset partitions
	// (docs/data-scopes.md). May be nil (legacy single-user).
	scopes *scopes.Store
	// demoMode locks the instance to synthetic fixtures and a read-only
	// surface (issue #217). See demo.go.
	demoMode bool
	// grants holds short-lived consent codes from the authorize page.
	grants *apps.GrantStore
	// csrfTokens holds single-use consent-page CSRF tokens (issue #182).
	csrfTokens *shared.StateStore
	// udks holds user-defined kind schemas loaded from the user-kinds
	// directory (issue #15). May be nil when none are configured.
	udks *udk.Registry
}

// appAllowedPaths returns the exact paths a third-party app token may call.
// Derived from the kind Route declarations (issue #312): a route is
// app-readable exactly when its declaration says so. Apps are data readers
// only — never admin, OAuth, proxy, mail, or unexposed subroutes like
// document full text. User-defined kinds are not app-readable.
func appAllowedPaths() []string {
	out := map[string]bool{}
	for _, route := range kinds.RouteDecls() {
		if route.AppReadable {
			out[route.Pattern] = true
		}
	}
	paths := make([]string, 0, len(out))
	for p := range out {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// appPathAllowed reports whether a third-party app token may access path.
// Exact match or trailing slash only — no subpaths (links/, review, mirror).
func appPathAllowed(path string) bool {
	p := strings.TrimSuffix(path, "/")
	if p == "" {
		return false
	}
	for _, prefix := range appAllowedPaths() {
		if p == prefix {
			return true
		}
	}
	return false
}

// baseURLFor returns the public base URL of the listener that received r —
// the admin listener's own URL for OAuth redirects and CSRF origin checks
// when listeners are split, else the instance base URL.
func (s *Server) baseURLFor(r *http.Request) string {
	if u := listenerBaseURLFrom(r); u != "" {
		return u
	}
	return s.baseURL
}

// AuthMiddleware authenticates requests according to the configured auth mode.
// A valid third-party app token is accepted in every mode and yields a scoped
// identity (issue #131) limited to library data reads. Successful tailnet
// identity is attached to the request context for logging.
//
// Auth is the agent tier (issue #181): library reads, pre-request hook, and
// content transport. Admin credentials also pass here so operators can use
// the library; admin *surfaces* additionally require AdminMiddleware.
func (s *Server) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	// csrfGuard runs after auth so it can distinguish ambient tailnet
	// identity from explicit API-key / app-token credentials (issue #182).
	next = s.csrfGuard(next)
	return func(w http.ResponseWriter, r *http.Request) {
		// Brute-force damping (issue #253): a client IP or presented
		// credential over its failure cap is rejected before any credential
		// work.
		if s.authLockedOut(r) {
			s.rateLimited(w, r)
			return
		}
		// App tokens are intentional third-party credentials. When present
		// and valid, they win so scopes cannot be bypassed by a concurrent
		// tailnet identity.
		if app := s.authenticateApp(r); app != nil {
			if !appPathAllowed(r.URL.Path) {
				s.deny(w, r, nil, "app token not permitted on this endpoint")
				return
			}
			next(w, withAuthKind(apps.WithAppIdentity(r, app), authKindApp))
			return
		}

		// Data-scope API keys (docs/data-scopes.md): each key is one
		// principal with a dataset partition. They are agent-tier and never
		// admin. Prefer them over the shared -api-key when both could match.
		if sc := s.authenticateScope(r); sc != nil {
			r = scopes.WithDataScope(r, sc)
			if !s.allowScopeMethod(w, r, sc) {
				return
			}
			next(w, withAuthKind(r, authKindAPIKey))
			return
		}

		// External access (docs/external-access.md): the published API
		// listener accepts invite tokens and app tokens only. The shared
		// agent key never crosses the public bind (a transcript leak must
		// not become internet-wide read), and ambient identity is not
		// resolved at all — both are tailnet-listener credentials.
		if externalAPIFrom(r) {
			s.deny(w, r, nil, "external API accepts invite tokens and app tokens only")
			return
		}

		id, idErr := s.resolveIdentity(r)

		switch s.authMode {
		case AuthModeAPIKey:
			if !s.checkAPIKey(r) {
				s.deny(w, r, nil, "missing or invalid API key")
				return
			}
			next(w, withAuthKind(r, authKindAPIKey))
			return
		case AuthModeTailnet:
			// Prefer explicit API key when supplied so webhook clients that
			// also arrive via Serve are classified as non-ambient (CSRF-exempt).
			if s.checkAPIKey(r) {
				if id != nil {
					if !loginAllowed(id, s.allowedLogins) && !s.isAdminIdentity(id) {
						s.deny(w, r, id, "identity not in allowed logins")
						return
					}
					next(w, withAuthKind(withPeerIdentity(r, id), authKindAPIKey))
					return
				}
				next(w, withAuthKind(r, authKindAPIKey))
				return
			}
			if id == nil {
				if idErr != nil {
					slog.Warn("tailnet identity resolve failed", "error", idErr, "remote", r.RemoteAddr)
				}
				s.deny(w, r, nil, "tailnet identity required — connect through Tailscale Serve or run on a tailscaled host")
				return
			}
			if !loginAllowed(id, s.allowedLogins) && !s.isAdminIdentity(id) {
				s.deny(w, r, id, "identity not in allowed logins")
				return
			}
			next(w, withAuthKind(withPeerIdentity(r, id), authKindTailnet))
			return
		default: // AuthModeAuto
			if id != nil && (loginAllowed(id, s.allowedLogins) || s.isAdminIdentity(id)) {
				next(w, withAuthKind(withPeerIdentity(r, id), authKindTailnet))
				return
			}
			if s.checkAPIKey(r) {
				next(w, withAuthKind(r, authKindAPIKey))
				return
			}
			if id != nil {
				s.deny(w, r, id, "identity not in allowed logins")
				return
			}
			s.deny(w, r, nil, "unauthorized: provide a valid API key or connect from the tailnet")
			return
		}
	}
}

// AdminMiddleware authenticates requests and requires an admin principal
// (issue #181). Admin surfaces must not be reachable with an LLM-agent
// credential: data grants only constrain callers who cannot rewrite them.
//
// Admin principal is:
//   - the admin API key (-admin-api-key), or
//   - a Tailscale user login that passes isAdminIdentity (default: allowed
//     user logins; tagged devices need an explicit -admin-logins entry).
//
// Valid agent credentials receive 403; unauthenticated requests receive 401.
// App tokens are never admin. Data-scope keys are never admin.
func (s *Server) AdminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	// csrfGuard runs after auth so it can distinguish ambient tailnet
	// identity from explicit admin API-key credentials (issue #182).
	next = s.csrfGuard(next)
	return func(w http.ResponseWriter, r *http.Request) {
		// Defense in depth (docs/external-access.md): admin surfaces are
		// never served on the published API listener — the allowlist gate
		// answers 404 first, and this refuses even a mis-gated route.
		if externalAPIFrom(r) {
			shared.WriteJSONError(w, http.StatusNotFound, "not found")
			return
		}
		// Brute-force damping (issue #253): the admin key is the most
		// valuable credential and gets the same failure caps as agent auth.
		if s.authLockedOut(r) {
			s.rateLimited(w, r)
			return
		}
		if app := s.authenticateApp(r); app != nil {
			s.denyStatus(w, r, nil, http.StatusForbidden, "app token not permitted on admin endpoints")
			return
		}
		// Data-scope keys are agent-tier only (docs/data-scopes.md). Reject
		// before tailnet-admin fallback so a scope key on an admin peer is
		// still not an admin principal.
		if sc := s.authenticateScope(r); sc != nil {
			s.denyStatus(w, r, nil, http.StatusForbidden, "admin credential required")
			return
		}

		if s.checkAdminAPIKey(r) {
			next(w, withAuthKind(r, authKindAPIKey))
			return
		}

		id, idErr := s.resolveIdentity(r)

		switch s.authMode {
		case AuthModeAPIKey:
			// Shared -api-key is the agent credential; only -admin-api-key
			// may call admin routes in this mode.
			if s.checkAgentAPIKey(r) {
				s.denyStatus(w, r, nil, http.StatusForbidden, "admin credential required")
				return
			}
			if idErr != nil {
				slog.Warn("identity resolve failed", "error", idErr, "remote", r.RemoteAddr)
			}
			s.denyStatus(w, r, nil, http.StatusUnauthorized, "missing or invalid admin API key")
			return
		case AuthModeTailnet:
			if id == nil {
				if idErr != nil {
					slog.Warn("tailnet identity resolve failed", "error", idErr, "remote", r.RemoteAddr)
				}
				s.denyStatus(w, r, nil, http.StatusUnauthorized, "tailnet identity required — connect through Tailscale Serve or run on a tailscaled host")
				return
			}
			if !s.isAdminIdentity(id) {
				s.denyStatus(w, r, id, http.StatusForbidden, "admin privilege required")
				return
			}
			next(w, withAuthKind(withPeerIdentity(r, id), authKindTailnet))
			return
		default: // AuthModeAuto
			if id != nil && s.isAdminIdentity(id) {
				next(w, withAuthKind(withPeerIdentity(r, id), authKindTailnet))
				return
			}
			if s.checkAgentAPIKey(r) {
				s.denyStatus(w, r, id, http.StatusForbidden, "admin credential required")
				return
			}
			if id != nil {
				s.denyStatus(w, r, id, http.StatusForbidden, "admin privilege required")
				return
			}
			s.denyStatus(w, r, nil, http.StatusUnauthorized, "unauthorized: provide a valid admin API key or connect from the tailnet as an admin")
			return
		}
	}
}

// authenticateApp resolves Authorization/X-Api-Key to a registered app token.
// Returns nil when the credential is missing, unknown, or disabled — callers
// fall through to normal auth (the same header may be the shared API key).
func (s *Server) authenticateApp(r *http.Request) *apps.App {
	if s == nil || s.apps == nil {
		return nil
	}
	authz := r.Header.Get("Authorization")
	token := ""
	if strings.HasPrefix(authz, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if token == "" || !strings.HasPrefix(token, apps.TokenPrefix) {
		return nil
	}
	return s.apps.FindByToken(token)
}

func (s *Server) resolveIdentity(r *http.Request) (*PeerIdentity, error) {
	if s.identity == nil {
		return nil, nil
	}
	return s.identity.Resolve(r.Context(), r)
}

// checkAPIKey reports whether the request carries a valid API key credential
// (agent or admin). Admin keys also authorize agent surfaces so a sole
// admin-api-key deployment can still read the library.
func (s *Server) checkAPIKey(r *http.Request) bool {
	return s.checkAgentAPIKey(r) || s.checkAdminAPIKey(r)
}

// checkAgentAPIKey reports whether the request carries the shared agent API key.
func (s *Server) checkAgentAPIKey(r *http.Request) bool {
	if s.apiKey == "" {
		return false
	}
	return matchAPIKey(r, s.apiKey)
}

// checkAdminAPIKey reports whether the request carries the admin API key.
// The shared agent key is never admin (issue #181).
func (s *Server) checkAdminAPIKey(r *http.Request) bool {
	if s.adminAPIKey == "" {
		return false
	}
	return matchAPIKey(r, s.adminAPIKey)
}

func matchAPIKey(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	presented := presentedAPIKey(r)
	return presented != "" && constantTimeEqual(presented, key)
}

// presentedAPIKey returns the bearer / X-Api-Key credential on the request.
func presentedAPIKey(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// authenticateScope resolves a data-scope API key to its principal scope
// (docs/data-scopes.md). Nil when the store is missing or the key is not a
// scope key (legacy shared agent key falls through to checkAPIKey).
func (s *Server) authenticateScope(r *http.Request) *scopes.Scope {
	if s == nil || s.scopes == nil {
		return nil
	}
	return s.scopes.FindByAPIKey(presentedAPIKey(r))
}

// allowScopeMethod enforces read_only scopes. Mutating agent methods are
// rejected except the Aperture pre-request hook, which is a read-only
// classification webhook. Writes the deny response and returns false.
func (s *Server) allowScopeMethod(w http.ResponseWriter, r *http.Request, sc *scopes.Scope) bool {
	if sc == nil || !sc.ReadOnly {
		return true
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if p := r.URL.Path; p == hookhealth.HookPath || strings.HasPrefix(p, "/v1/pre-request") {
		return true
	}
	s.denyStatus(w, r, nil, http.StatusForbidden, "data scope "+sc.ID+" is read-only")
	return false
}

// isAdminIdentity reports whether a Tailscale peer is an admin principal
// (issue #181). Explicit -admin-logins is the allowlist when set. Otherwise
// any allowed *user* login is admin (browser operator on a solo tailnet);
// tagged device identities are agent-only unless listed in -admin-logins.
func (s *Server) isAdminIdentity(id *PeerIdentity) bool {
	if id == nil {
		return false
	}
	if len(s.adminLogins) > 0 {
		return loginAllowed(id, s.adminLogins)
	}
	if !loginAllowed(id, s.allowedLogins) {
		return false
	}
	return !isTagIdentity(id)
}

// isTagIdentity reports whether the peer is a tagged device (synthetic
// tag:… login) rather than a human user login.
func isTagIdentity(id *PeerIdentity) bool {
	if id == nil {
		return false
	}
	if strings.HasPrefix(id.LoginName, "tag:") {
		return true
	}
	if len(id.Tags) > 0 && id.LoginName == strings.Join(id.Tags, ",") {
		return true
	}
	return false
}

func (s *Server) deny(w http.ResponseWriter, r *http.Request, id *PeerIdentity, reason string) {
	s.denyStatus(w, r, id, http.StatusUnauthorized, reason)
}

func (s *Server) denyStatus(w http.ResponseWriter, r *http.Request, id *PeerIdentity, status int, reason string) {
	attrs := []any{"remote", s.clientIPFor(r), "peer", r.RemoteAddr, "path", r.URL.Path, "reason", reason, "status", status}
	agent := "api-key"
	if sc := scopes.From(r); sc != nil && sc.ID != "" {
		agent = "scope:" + sc.ID
		attrs = append(attrs, "scope", sc.ID)
	} else if id != nil {
		attrs = append(attrs, "login", id.LoginName, "identity_source", id.Source)
		if id.LoginName != "" {
			agent = id.LoginName
		}
	}
	slog.Warn("unauthorized request", attrs...)
	if r.URL != nil && r.URL.Path == hookhealth.HookPath {
		s.recordHook(hookhealth.HookObservation{Action: "auth_fail", Host: requestHost(r)})
	}
	// Failed authentication counts against the brute-force caps (issue #253).
	// Denials that came with a resolved tailnet identity are not guesses.
	if status == http.StatusUnauthorized && id == nil {
		s.recordAuthFailure(r)
	}
	// Durable auth denials (issue #105). Route family only — never the full path.
	s.recordAccess(metrics.AccessEvent{
		Agent:    agent,
		Surface:  metrics.MetricSurfaceAuth,
		Target:   auditRouteFamily(r),
		Decision: audit.DecisionDeny,
		Reason:   "auth",
		Method:   requestMethod(r),
		Status:   status,
		ClientIP: s.clientIPFor(r),
	})
	if status == http.StatusForbidden {
		shared.WriteJSONError(w, status, "forbidden: admin credential required")
		return
	}
	shared.WriteJSONError(w, status, "unauthorized")
}

func requestMethod(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Method
}

// auditRouteFamily maps a request path to a coarse redacted family for the
// audit log. Never stores full paths (issue #32).
func auditRouteFamily(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "other"
	}
	p := r.URL.Path
	switch {
	case p == hookhealth.HookPath || strings.HasPrefix(p, "/v1/pre-request"):
		return "hook"
	case strings.HasPrefix(p, "/v1/admin"):
		return "admin"
	case strings.HasPrefix(p, "/v1/library"):
		return "library"
	case strings.HasPrefix(p, "/v1/services"):
		return "service"
	case strings.HasPrefix(p, "/v1/auth"):
		return "auth"
	case strings.HasPrefix(p, "/v1/apps"):
		return "apps"
	case p == "/healthz" || p == "/readyz" || p == "/metrics":
		return "observability"
	default:
		return "other"
	}
}

// requestHost returns Host or X-Forwarded-Host for hook diagnostics.
func requestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return h
	}
	return r.Host
}

type peerIdentityKey struct{}

func withPeerIdentity(r *http.Request, id *PeerIdentity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), peerIdentityKey{}, id))
}

// PeerIdentityFrom returns the Tailscale identity attached by AuthMiddleware,
// or nil when the request was authenticated with an API key.
func PeerIdentityFrom(r *http.Request) *PeerIdentity {
	id, _ := r.Context().Value(peerIdentityKey{}).(*PeerIdentity)
	return id
}

// withPeerLog enriches a logger with the Tailscale peer identity, when present.
func withPeerLog(logger *slog.Logger, r *http.Request) *slog.Logger {
	id := PeerIdentityFrom(r)
	if id == nil {
		return logger
	}
	return logger.With("login", id.LoginName, "identity_source", id.Source)
}
