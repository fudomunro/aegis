package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// Aperture request payload for pre_request hooks.
type PreRequestPayload struct {
	Metadata    RequestMetadata `json:"metadata"`
	RequestBody json.RawMessage `json:"request_body,omitempty"`
	UserMessage string          `json:"user_message,omitempty"`
}

type RequestMetadata struct {
	LoginName string `json:"login_name"`
	UserAgent string `json:"user_agent"`
	URL       string `json:"url"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Tailnet   string `json:"tailnet_name"`
	NodeID    string `json:"stable_node_id"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
}

// Guardrail response actions.
const (
	ActionAllow  = "allow"
	ActionBlock  = "block"
	ActionModify = "modify"
	// ActionSkip is a hook-health observation only (not sent to Aperture):
	// matching was skipped because the message looked like development work
	// or hit a configured skip pattern (issue #205). Aperture always sees allow.
	ActionSkip = "skip"
)

type GuardrailResponse struct {
	Action string `json:"action"`
	// RequestBody is the replacement request body for ActionModify. Aperture
	// requires this field name; a `body` key is treated as allow.
	RequestBody json.RawMessage `json:"request_body,omitempty"`
	Message     string          `json:"message,omitempty"` // human-readable reason
}

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
	// dataDir is the resolved local state directory (issue #74). Surfaced
	// on the admin Runtime panel so the layout is discoverable.
	dataDir     string
	config      *Config
	cache       *Cache
	tokens      *TokenStore
	oauthStates *StateStore
	// refreshFlight coalesces concurrent OAuth refresh calls per connection.
	refreshFlight tokenFlight
	connectors    map[string]connectors.ServiceConnector
	// metrics is the in-memory access ring buffer for the admin map (issue #32).
	metrics *AccessMetrics
	// links holds user-confirmed cross-service entity links (issue #4).
	links *EntityLinkStore
	// copies is the durable copy ledger (issue #76). May be disabled.
	copies *CopyLedger
	// mail is the optional local compressed mail archive (/v1/mail/*).
	mail *MailArchive
	// quicken is the optional local Quicken import store (issue #141).
	quicken *QuickenStore
	// timeline is the optional local Google Timeline import store (issue #142).
	timeline *TimelineStore
	// signal is the optional local Signal import store (issue #168).
	signal *SignalStore
	// mailSyncMu serializes archive ingest so admin UI and agents cannot
	// interleave two Gmail page cursors into the same store.
	mailSyncMu sync.Mutex
	// hookHealth records Aperture pre-request outcomes for admin diagnostics.
	hookHealth *HookHealth
	// audit is the durable access audit log (issue #105). May be disabled.
	audit *AuditLog
	// prom holds process-lifetime counters for GET /metrics (issue #105).
	prom *PromMetrics
	// apertureURL is the optional public base URL of the Aperture gateway.
	// Used only for admin live links / setup guide — never required for proxying.
	apertureURL string
	// apps is the third-party application registry (issue #131). May be nil
	// in tests that never exercise app tokens.
	apps *AppStore
	// scopes resolves data-scope API keys to principal → dataset partitions
	// (docs/data-scopes.md). May be nil (legacy single-user).
	scopes *DataScopeStore
	// grants holds short-lived consent codes from the authorize page.
	grants *GrantStore
	// csrfTokens holds single-use consent-page CSRF tokens (issue #182).
	csrfTokens *StateStore
	// udts holds user-defined type schemas loaded from the user-types
	// directory (issue #15). May be nil when none are configured.
	udts *userDefinedRegistry
}

// appExtraPaths are non-kind library paths a third-party app token may call
// (freebusy is the events availability view, not a LibraryKind).
var appExtraPaths = []string{
	"/v1/library/freebusy",
}

// appAllowedPaths returns path prefixes a third-party app token may call.
// Derived from LibraryKind.AppReadable plus appExtraPaths (issue #183).
// Apps are data readers only — never admin, OAuth, proxy, mail, or
// document full-text subpaths. User-defined kinds are not app-readable.
func appAllowedPaths() []string {
	out := append([]string{}, appExtraPaths...)
	out = append(out, types.AppReadableKindPaths()...)
	return out
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
		// App tokens are intentional third-party credentials. When present
		// and valid, they win so scopes cannot be bypassed by a concurrent
		// tailnet identity.
		if app := s.authenticateApp(r); app != nil {
			if !appPathAllowed(r.URL.Path) {
				s.deny(w, r, nil, "app token not permitted on this endpoint")
				return
			}
			next(w, withAuthKind(withAppIdentity(r, app), authKindApp))
			return
		}

		// Data-scope API keys (docs/data-scopes.md): each key is one
		// principal with a dataset partition. They are agent-tier and never
		// admin. Prefer them over the shared -api-key when both could match.
		if sc := s.authenticateScope(r); sc != nil {
			if !s.allowScopeMethod(w, r, sc) {
				return
			}
			next(w, withAuthKind(withDataScope(r, sc), authKindAPIKey))
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
// App tokens are never admin.
func (s *Server) AdminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	// csrfGuard runs after auth so it can distinguish ambient tailnet
	// identity from explicit admin API-key credentials (issue #182).
	next = s.csrfGuard(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if app := s.authenticateApp(r); app != nil {
			s.denyStatus(w, r, nil, http.StatusForbidden, "app token not permitted on admin endpoints")
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
func (s *Server) authenticateApp(r *http.Request) *App {
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
	if token == "" || !strings.HasPrefix(token, appTokenPrefix) {
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
func (s *Server) authenticateScope(r *http.Request) *DataScope {
	if s == nil || s.scopes == nil {
		return nil
	}
	return s.scopes.FindByAPIKey(presentedAPIKey(r))
}

// allowScopeMethod enforces read_only scopes. Mutating agent methods are
// rejected except the Aperture pre-request hook, which is a read-only
// classification webhook. Writes the deny response and returns false.
func (s *Server) allowScopeMethod(w http.ResponseWriter, r *http.Request, sc *DataScope) bool {
	if sc == nil || !sc.ReadOnly {
		return true
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if p := r.URL.Path; p == hookPath || strings.HasPrefix(p, "/v1/pre-request") {
		return true
	}
	s.deny(w, r, nil, "data scope "+sc.ID+" is read-only")
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
	attrs := []any{"remote", r.RemoteAddr, "path", r.URL.Path, "reason", reason, "status", status}
	agent := "api-key"
	if id != nil {
		attrs = append(attrs, "login", id.LoginName, "identity_source", id.Source)
		if id.LoginName != "" {
			agent = id.LoginName
		}
	}
	slog.Warn("unauthorized request", attrs...)
	if r.URL != nil && r.URL.Path == hookPath {
		s.recordHook(HookObservation{Action: "auth_fail", Host: requestHost(r)})
	}
	// Durable auth denials (issue #105). Route family only — never the full path.
	s.recordAccess(AccessEvent{
		Agent:    agent,
		Surface:  MetricSurfaceAuth,
		Target:   auditRouteFamily(r),
		Decision: DecisionDeny,
		Reason:   "auth",
		Method:   requestMethod(r),
		Status:   status,
	})
	if status == http.StatusForbidden {
		writeJSONError(w, status, "forbidden: admin credential required")
		return
	}
	writeJSONError(w, status, "unauthorized")
}

// recordHook records a pre-request hook outcome in both the admin ring and
// the Prometheus counters (issue #105).
func (s *Server) recordHook(obs HookObservation) {
	if s == nil {
		return
	}
	if s.hookHealth != nil {
		s.hookHealth.Record(obs)
	}
	if s.prom != nil {
		s.prom.observeHook(obs.Action)
	}
}

// requestMethod returns the HTTP method or empty.
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
	case p == hookPath || strings.HasPrefix(p, "/v1/pre-request"):
		return "hook"
	case strings.HasPrefix(p, "/v1/admin"):
		return "admin"
	case strings.HasPrefix(p, "/v1/library"):
		return "library"
	case strings.HasPrefix(p, "/v1/services"):
		return "service"
	case strings.HasPrefix(p, "/v1/mail"):
		return "mail"
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

// warnIdentityMismatch logs when Aperture's claimed user differs from the
// Tailscale peer that actually called Aegis (shared tailnet / bridge risk).
func warnIdentityMismatch(logger *slog.Logger, apertureLogin string, r *http.Request) {
	id := PeerIdentityFrom(r)
	if id == nil || apertureLogin == "" || id.LoginName == "" {
		return
	}
	if !strings.EqualFold(id.LoginName, apertureLogin) {
		logger.Warn("identity mismatch: aperture login does not match tailnet peer",
			"aperture_login", apertureLogin,
			"tailnet_login", id.LoginName,
			"identity_source", id.Source,
			"path", r.URL.Path,
		)
	}
}

func (s *Server) HandlePreRequest(w http.ResponseWriter, r *http.Request) {
	// Cap the hook body so a huge conversation cannot exhaust memory (issue #97).
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			slog.Error("pre-request body too large", "limit", maxErr.Limit)
			s.recordHook(HookObservation{Action: "parse_fail", Host: requestHost(r)})
			writeJSONError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", maxErr.Limit))
			return
		}
		slog.Error("failed to read request body", "error", err)
		s.recordHook(HookObservation{Action: "parse_fail", Host: requestHost(r)})
		writeGuardrailResponse(w, GuardrailResponse{
			Action:  ActionAllow,
			Message: "failed to read request body",
		})
		return
	}

	var payload PreRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		slog.Error("failed to parse payload", "error", err)
		s.recordHook(HookObservation{Action: "parse_fail", Host: requestHost(r)})
		writeGuardrailResponse(w, GuardrailResponse{
			Action:  ActionAllow,
			Message: "failed to parse payload",
		})
		return
	}
	obs := HookObservation{
		Host:           requestHost(r),
		Action:         ActionAllow,
		HasUserMessage: strings.TrimSpace(payload.UserMessage) != "",
		HasRequestBody: len(payload.RequestBody) > 0,
		Model:          payload.Metadata.Model,
		Provider:       payload.Metadata.Provider,
		Login:          payload.Metadata.LoginName,
	}
	defer func() { s.recordHook(obs) }()

	logger := slog.With(
		"request_id", payload.Metadata.RequestID,
		"user", payload.Metadata.LoginName,
		"model", payload.Metadata.Model,
	)
	logger = withPeerLog(logger, r)
	warnIdentityMismatch(logger, payload.Metadata.LoginName, r)

	// Skip matching for development work (issue #205). Coding agents that
	// mention service names as identifiers must not be redirected to the
	// library surface. Fail-open: the request passes through unchanged.
	if reason := s.config.shouldSkipMatching(payload.UserMessage); reason != "" {
		logger.Debug("skipping pre-request matching", "reason", reason)
		obs.Action = ActionSkip
		obs.SkipReason = reason
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	matched := s.config.FindMatchingServices(payload.UserMessage)

	// Surface matching unified library endpoints alongside service proxy hints.
	// Issue #125: photo questions that named a service still need /v1/library/photos
	// (and the no-bypass policy); do not gate library hints on zero service matches.
	libraryInstructions := s.libraryHintsForMessage(payload.UserMessage)
	// Local mail archive search is a separate surface from live provider listing.
	libraryInstructions = append(libraryInstructions, s.mailArchiveHints(payload.UserMessage)...)

	if len(matched) == 0 && len(libraryInstructions) == 0 {
		logger.Debug("no services matched, allowing")
		obs.Action = ActionAllow
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	serviceIDs := make([]string, len(matched))
	for i, svc := range matched {
		serviceIDs[i] = svc.ID
	}
	if len(matched) > 0 {
		logger.Info("services matched", "services", serviceIDs)
	}

	// If any matched service is blocked, reject the request.
	for _, svc := range matched {
		if svc.Action == "block" {
			logger.Warn("request blocked by service policy", "service", svc.ID, "reason", svc.Description)
			obs.Action = ActionBlock
			writeGuardrailResponse(w, GuardrailResponse{
				Action:  ActionBlock,
				Message: fmt.Sprintf("blocked: access to %s is not allowed. %s", svc.Name, svc.Description),
			})
			return
		}
	}

	// Build instructions pointing agents at Aegis's data surface (issue #119).
	// Library is primary; connector instructions only add content/ingest gaps.
	var instructions []string
	for _, svc := range matched {
		if svc.Action != "inject" {
			continue
		}
		// Skip services that deny every data type — nothing useful to inject.
		if allDataDenied(s.effectivePermissions(&svc)) {
			logger.Info("skipping inject: all data types denied by permission", "service", svc.ID)
			continue
		}
		// Use connector-specific instructions if available.
		if connector, ok := s.connectors[svc.ID]; ok {
			instructions = append(instructions, connector.SystemInstructions(s.baseURL))
		} else {
			instruction := fmt.Sprintf(
				"When working with %s, use Aegis's unified library endpoints "+
					"(/v1/library/{kind}) for the user's data. Do not call %s directly. "+
					"Service provenance lives in sources[] for sync — not a separate services API.",
				svc.Name, svc.Name,
			)
			instructions = append(instructions, instruction)
		}
		if notice := permissionNotice(s.effectivePermissions(&svc)); notice != "" {
			instructions = append(instructions, notice)
		}
	}

	instructions = append(instructions, s.writePolicyNotices(matched)...)
	instructions = append(instructions, libraryInstructions...)

	if len(instructions) == 0 {
		logger.Debug("no injectable services, allowing")
		obs.Action = ActionAllow
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	// Inject instructions into the request body.
	systemContent := strings.Join(instructions, "\n\n")
	result, err := injectRequestInstructions(payload.RequestBody, systemContent,
		payload.Metadata.Provider, payload.Metadata.Model)
	obs.BodyShape = result.Shape
	if err != nil {
		if errors.Is(err, errUnrecognizedBodyShape) {
			logger.Warn("unrecognized request body shape, allowing without injection",
				"provider", payload.Metadata.Provider, "model", payload.Metadata.Model)
			obs.Action = ActionAllow
			writeGuardrailResponse(w, GuardrailResponse{
				Action:  ActionAllow,
				Message: "unrecognized request body shape; instructions not injected",
			})
			return
		}
		logger.Error("failed to inject system message", "error", err)
		obs.Action = ActionAllow
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}
	if result.AlreadyPresent {
		logger.Debug("instructions already present, skipping injection")
		obs.Action = ActionAllow
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	logger.Info("request modified with proxy instructions",
		"services", len(instructions), "body_shape", result.Shape)
	obs.Action = ActionModify
	writeGuardrailResponse(w, GuardrailResponse{
		Action:      ActionModify,
		RequestBody: result.Body,
	})
}

// Request body wire shapes Aegis can augment (issue #103).
const (
	bodyShapeChat      = "chat"      // OpenAI chat completions: top-level messages[]
	bodyShapeAnthropic = "anthropic" // Anthropic Messages API: top-level system + messages[]
	bodyShapeResponses = "responses" // OpenAI Responses API: top-level input
	bodyShapeUnknown   = "unknown"
)

// errUnrecognizedBodyShape is returned when the request body is not one of the
// supported LLM wire formats. Callers fail open (allow) and record diagnostics.
var errUnrecognizedBodyShape = errors.New("unrecognized request body shape")

// injectResult is the outcome of an instruction-injection attempt.
type injectResult struct {
	Body json.RawMessage
	// Shape is bodyShapeChat, bodyShapeAnthropic, bodyShapeResponses, or bodyShapeUnknown.
	Shape string
	// AlreadyPresent is true when the instruction block was already in the body.
	AlreadyPresent bool
}

// jsonSafeText matches encoding/json's UTF-8 handling: invalid bytes become
// U+FFFD. Injection must dedupe against the text that will actually be
// written, or a second pass fails to see its own instructions (issue #107).
func jsonSafeText(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out string
	if err := json.Unmarshal(b, &out); err != nil {
		return s
	}
	return out
}

// injectRequestInstructions inserts system-level instructions into an LLM request
// body, choosing the correct carrier for the detected wire shape. Repeated calls
// with the same content are idempotent. provider/model come from Aperture
// metadata and are used when the body alone is ambiguous.
func injectRequestInstructions(original []byte, content, provider, model string) (injectResult, error) {
	if len(original) == 0 {
		return injectResult{Shape: bodyShapeUnknown}, errUnrecognizedBodyShape
	}
	// Normalize to the form encoding/json will persist so containsContent
	// sees the same bytes a prior inject wrote.
	content = jsonSafeText(content)
	var full map[string]json.RawMessage
	if err := json.Unmarshal(original, &full); err != nil {
		// Non-object JSON (or corrupt bytes) is not a wire shape we can augment.
		return injectResult{Shape: bodyShapeUnknown}, errUnrecognizedBodyShape
	}
	shape := detectBodyShape(full, provider, model)
	res := injectResult{Shape: shape}

	if containsContent(instructionCarrierText(full, shape), content) {
		res.Body = original
		res.AlreadyPresent = true
		return res, nil
	}

	var err error
	switch shape {
	case bodyShapeChat:
		err = injectChatMessages(full, content)
	case bodyShapeAnthropic:
		err = injectAnthropicSystem(full, content)
	case bodyShapeResponses:
		err = injectResponsesInput(full, content)
	default:
		return res, errUnrecognizedBodyShape
	}
	if err != nil {
		return res, err
	}
	out, err := json.Marshal(full)
	if err != nil {
		return res, err
	}
	res.Body = out
	return res, nil
}

// injectSystemMessage inserts a system message into an OpenAI chat-completions body.
func injectSystemMessage(original []byte, content string) (json.RawMessage, error) {
	res, err := injectRequestInstructions(original, content, "", "")
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// detectBodyShape prefers explicit Aperture metadata, then sniffs body keys.
func detectBodyShape(full map[string]json.RawMessage, provider, model string) string {
	_, hasMessages := full["messages"]
	_, hasInput := full["input"]
	_, hasSystem := full["system"]
	_, hasInstructions := full["instructions"]
	_, hasPrevResponse := full["previous_response_id"]

	metaAnthropic := metaLooksAnthropic(provider, model)

	// Responses API is the only supported shape that uses `input` (or
	// `instructions` / `previous_response_id`) as conversation carriers and
	// typically has no `messages` array.
	if (hasInput || hasInstructions || hasPrevResponse) && !hasMessages {
		return bodyShapeResponses
	}
	if hasMessages {
		// Top-level `system` is Anthropic's system prompt slot; OpenAI chat
		// keeps system content inside messages[]. Metadata disambiguates
		// Anthropic bodies that omit system on first turn.
		if hasSystem || metaAnthropic {
			return bodyShapeAnthropic
		}
		return bodyShapeChat
	}
	if metaAnthropic {
		return bodyShapeAnthropic
	}
	if hasInput {
		return bodyShapeResponses
	}
	return bodyShapeUnknown
}

func metaLooksAnthropic(provider, model string) bool {
	p := strings.ToLower(provider)
	m := strings.ToLower(model)
	return strings.Contains(p, "anthropic") ||
		strings.Contains(p, "claude") ||
		strings.Contains(m, "claude")
}

// containsContent reports whether needle is already carried in the instruction text.
func containsContent(haystack, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return true
	}
	return strings.Contains(haystack, needle)
}

// instructionCarrierText extracts existing system/instruction text for dedupe.
func instructionCarrierText(full map[string]json.RawMessage, shape string) string {
	switch shape {
	case bodyShapeAnthropic:
		return contentText(full["system"])
	case bodyShapeResponses:
		parts := []string{contentText(full["instructions"])}
		parts = append(parts, inputCarrierText(full["input"])...)
		return strings.Join(parts, "\n")
	default:
		raw, ok := full["messages"]
		if !ok {
			return ""
		}
		var msgs []json.RawMessage
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return ""
		}
		var parts []string
		for _, m := range msgs {
			var obj struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(m, &obj); err != nil {
				continue
			}
			if obj.Role == "system" || obj.Role == "developer" {
				parts = append(parts, contentText(obj.Content))
			}
		}
		return strings.Join(parts, "\n")
	}
}

func inputCarrierText(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return nil // user text, not instruction carrier
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var parts []string
	for _, item := range items {
		var obj struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			continue
		}
		if obj.Role == "system" || obj.Role == "developer" {
			parts = append(parts, contentText(obj.Content))
		}
	}
	return parts
}

// contentText flattens a JSON string or content-block array into plain text.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		var obj struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			continue
		}
		if obj.Text != "" {
			parts = append(parts, obj.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func injectChatMessages(full map[string]json.RawMessage, content string) error {
	raw, ok := full["messages"]
	if !ok {
		return errUnrecognizedBodyShape
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return errUnrecognizedBodyShape
	}
	sysMsg, err := json.Marshal(map[string]string{
		"role":    "system",
		"content": content,
	})
	if err != nil {
		return err
	}
	msgs = append([]json.RawMessage{sysMsg}, msgs...)
	out, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	full["messages"] = out
	return nil
}

func injectAnthropicSystem(full map[string]json.RawMessage, content string) error {
	raw, ok := full["system"]
	if !ok {
		out, err := json.Marshal(content)
		if err != nil {
			return err
		}
		full["system"] = out
		return nil
	}

	// Preserve Anthropic's system shapes: plain string or content-block array.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		merged := content
		if s != "" {
			merged = content + "\n\n" + s
		}
		out, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		full["system"] = out
		return nil
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return errUnrecognizedBodyShape
	}
	sysBlock, err := json.Marshal(map[string]string{
		"type": "text",
		"text": content,
	})
	if err != nil {
		return err
	}
	blocks = append([]json.RawMessage{sysBlock}, blocks...)
	out, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	full["system"] = out
	return nil
}

func injectResponsesInput(full map[string]json.RawMessage, content string) error {
	sysMsg, err := json.Marshal(map[string]string{
		"role":    "system",
		"content": content,
	})
	if err != nil {
		return err
	}

	raw, ok := full["input"]
	if !ok {
		// No conversation yet: instructions field is the Responses system slot.
		if err := mergeInstructions(full, content); err != nil {
			return err
		}
		return nil
	}

	// String input is shorthand for a single user message — expand to items.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		userMsg, err := json.Marshal(map[string]string{
			"role":    "user",
			"content": s,
		})
		if err != nil {
			return err
		}
		items := []json.RawMessage{sysMsg, userMsg}
		out, err := json.Marshal(items)
		if err != nil {
			return err
		}
		full["input"] = out
		return nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err == nil {
		items = append([]json.RawMessage{sysMsg}, items...)
		out, err := json.Marshal(items)
		if err != nil {
			return err
		}
		full["input"] = out
		return nil
	}

	// Single input item object.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return errUnrecognizedBodyShape
	}
	items = []json.RawMessage{sysMsg, raw}
	out, err := json.Marshal(items)
	if err != nil {
		return err
	}
	full["input"] = out
	return nil
}

// mergeInstructions sets or prepends the Responses API `instructions` field.
func mergeInstructions(full map[string]json.RawMessage, content string) error {
	raw, ok := full["instructions"]
	if !ok {
		out, err := json.Marshal(content)
		if err != nil {
			return err
		}
		full["instructions"] = out
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return errUnrecognizedBodyShape
	}
	merged := content
	if s != "" {
		merged = content + "\n\n" + s
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	full["instructions"] = out
	return nil
}

func writeGuardrailResponse(w http.ResponseWriter, resp GuardrailResponse) {
	w.Header().Set("Content-Type", "application/json")
	if resp.Action == ActionBlock {
		w.WriteHeader(http.StatusForbidden)
	}
	json.NewEncoder(w).Encode(resp)
}
