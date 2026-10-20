package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// csrfTokenTTL bounds how long a rendered consent-page CSRF token stays valid.
const csrfTokenTTL = 15 * time.Minute

// csrfTokenPlaceholder is substituted once per GET /v1/apps/authorize render.
const csrfTokenPlaceholder = "{{CSRF_TOKEN}}"

// authKind records how AuthMiddleware / AdminMiddleware authenticated the
// request so write guards can tell ambient tailnet identity from explicit
// credentials (issue #182).
type authKind string

const (
	authKindNone    authKind = ""
	authKindApp     authKind = "app"
	authKindAPIKey  authKind = "api-key"
	authKindTailnet authKind = "tailnet"
)

type authKindKey struct{}

func withAuthKind(r *http.Request, kind authKind) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authKindKey{}, kind))
}

// authKindFrom returns the credential class attached by auth middleware.
func authKindFrom(r *http.Request) authKind {
	if r == nil {
		return authKindNone
	}
	kind, _ := r.Context().Value(authKindKey{}).(authKind)
	return kind
}

// isSafeMethod reports whether the method cannot change state (issue #182).
// OPTIONS is treated as safe: CORS preflight fails closed because Aegis never
// sends Access-Control-Allow-Origin.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// originMatchesBase reports whether a browser Origin header equals the
// configured -base-url origin (scheme + host, including non-default port).
func originMatchesBase(origin, baseURL string) bool {
	origin = strings.TrimSpace(origin)
	baseURL = strings.TrimSpace(baseURL)
	if origin == "" || baseURL == "" {
		return false
	}
	if strings.EqualFold(origin, "null") {
		return false
	}
	ou, err := url.Parse(origin)
	if err != nil || ou.Scheme == "" || ou.Host == "" {
		return false
	}
	bu, err := url.Parse(baseURL)
	if err != nil || bu.Scheme == "" || bu.Host == "" {
		return false
	}
	return strings.EqualFold(ou.Scheme, bu.Scheme) && strings.EqualFold(ou.Host, bu.Host)
}

// sameOriginRequest reports whether a browser write looks same-origin
// (issue #182). Sec-Fetch-Site is set by the browser and cannot be forged
// from page script; Origin is the classic check.
func (s *Server) sameOriginRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "same-origin") {
		return true
	}
	return originMatchesBase(r.Header.Get("Origin"), s.baseURL)
}

// isContentTransportPath reports whether the route accepts arbitrary upload
// media (issue #119). Those must not be forced to application/json.
func isContentTransportPath(path string) bool {
	return strings.HasPrefix(path, "/v1/services/")
}

// allowUnsafeContentType rejects CORS "simple request" bodies on JSON write
// surfaces (issue #182). A cross-site text/plain or form POST would otherwise
// skip preflight and still reach json.Unmarshal / ParseForm handlers.
// Empty Content-Type stays allowed for bodyless POSTs (cache/clear, rotate).
// Content transport keeps free-form media types.
func allowUnsafeContentType(w http.ResponseWriter, r *http.Request) bool {
	if isContentTransportPath(r.URL.Path) {
		return true
	}
	ct := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ct == "" {
		return true
	}
	media := strings.TrimSpace(strings.ToLower(strings.SplitN(ct, ";", 2)[0]))
	switch media {
	case "application/json":
		return true
	case "application/x-www-form-urlencoded", "multipart/form-data":
		// Consent page may still post a form; CSRF token is enforced in the
		// grant handler. No other JSON write surface accepts form encoding.
		if r.URL.Path == "/v1/apps/authorize" {
			return true
		}
		writeJSONError(w, http.StatusForbidden,
			"unsupported Content-Type for write request (use application/json)")
		return false
	default:
		writeJSONError(w, http.StatusForbidden,
			"unsupported Content-Type for write request (use application/json)")
		return false
	}
}

// csrfGuard rejects cross-site state-changing requests after authentication
// (issue #182). Tailnet identity is ambient (Tailscale Serve headers act like
// a cookie), so browser writes must prove same-origin. Explicit API key and
// app-token callers are non-browser clients and are not subject to Origin.
//
// Also enforces Content-Type on JSON write surfaces.
func (s *Server) csrfGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next(w, r)
			return
		}
		kind := authKindFrom(r)
		// Explicit credentials are not ambient — browsers do not attach them
		// automatically on a cross-site form POST.
		// The Aperture pre-request hook is a server-to-server webhook, not a
		// browser write: Serve identity (or an API key) is expected, and the
		// call never carries a page Origin. CSRF would brick every hook.
		if kind != authKindAPIKey && kind != authKindApp && !isHookPath(r) {
			if !s.sameOriginRequest(r) {
				s.denyStatus(w, r, PeerIdentityFrom(r), http.StatusForbidden,
					"cross-origin write rejected")
				return
			}
		}
		if !allowUnsafeContentType(w, r) {
			return
		}
		next(w, r)
	}
}

// isHookPath reports whether this is the Aperture pre-request webhook route.
// Content-transport and admin routes stay CSRF-guarded.
func isHookPath(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	p := r.URL.Path
	return p == hookPath || strings.HasPrefix(p, hookPath+"/")
}

// issueCSRFToken mints a single-use token for the consent page render.
func (s *Server) issueCSRFToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := "aeg_csrf_" + base64.RawURLEncoding.EncodeToString(raw)
	if s.csrfTokens == nil {
		s.csrfTokens = NewStateStore()
	}
	s.csrfTokens.Set(token, true, csrfTokenTTL)
	return token, nil
}

// consumeCSRFToken validates and retires a consent-page token.
func (s *Server) consumeCSRFToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if s.csrfTokens == nil {
		return false
	}
	return s.csrfTokens.Delete(token)
}

// requireFormCSRF checks the consent form token for form-encoded grant POSTs.
// JSON grant POSTs rely on Origin + Content-Type (issue #182).
func (s *Server) requireFormCSRF(w http.ResponseWriter, r *http.Request, q url.Values) bool {
	token := q.Get("csrf_token")
	if token == "" {
		token = r.Header.Get("X-CSRF-Token")
	}
	if !s.consumeCSRFToken(token) {
		writeJSONError(w, http.StatusForbidden, "missing or invalid CSRF token")
		return false
	}
	return true
}
