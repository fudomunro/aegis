package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Split listeners (docs/external-access.md): the API listener carries the
// agent and external surfaces and may be published; the admin listener
// carries policy and setup surfaces and stays in-tailnet. Request context
// carries the per-listener posture so the shared middleware can enforce it.

type listenerBaseKey struct{}
type externalAPIKey struct{}

// withListenerBase marks r as arriving on a listener whose public base URL
// is base — used for OAuth redirects, CSRF origin checks, and injected
// agent URLs.
func withListenerBase(r *http.Request, base string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), listenerBaseKey{}, base))
}

// listenerBaseURLFrom returns the receiving listener's base URL, or "" when
// the request did not pass through a listener wrapper (single-listener mode).
func listenerBaseURLFrom(r *http.Request) string {
	if r == nil {
		return ""
	}
	s, _ := r.Context().Value(listenerBaseKey{}).(string)
	return s
}

// withExternalAPI marks r as arriving on the published API listener under
// external access: only invite tokens and app tokens are accepted there.
func withExternalAPI(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), externalAPIKey{}, true))
}

func externalAPIFrom(r *http.Request) bool {
	if r == nil {
		return false
	}
	b, _ := r.Context().Value(externalAPIKey{}).(bool)
	return b
}

// listenerMiddleware attaches the per-listener posture to every request
// before dispatch: base for URL construction, and (under external access)
// the published-API marker that narrows accepted credentials.
func listenerMiddleware(base string, external bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = withListenerBase(r, base)
		if external {
			r = withExternalAPI(r)
		}
		next.ServeHTTP(w, r)
	})
}

// apiAllowlistGate answers 404 for any route outside the API listener
// allowlist (apiRouteAllowed). The allowlist is the durable boundary: a
// newly registered route defaults to the admin side and cannot leak onto
// the published surface by forgetting a blocklist entry.
func apiAllowlistGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !apiRouteAllowed(r.Method, r.URL.Path) {
			shared.WriteJSONError(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiRouteAllowed is the API listener allowlist (docs/external-access.md
// "Model"). Everything else answers 404 there and is served only by the
// admin listener. Library entries are read-only: writes (photo share,
// entity-link mutations) stay admin-side even though they share the
// /v1/library prefix. Mail retrieval is part of the library surface (issue
// #311); mail ingest stays admin-side.
func apiRouteAllowed(method, path string) bool {
	switch {
	case path == "/healthz" || path == "/readyz":
		return method == http.MethodGet || method == http.MethodHead
	case path == "/v1/pre-request":
		return method == http.MethodPost
	case path == "/v1/auth/status":
		return method == http.MethodGet || method == http.MethodHead
	case path == "/v1/apps/token" || path == "/v1/apps/revoke":
		return method == http.MethodPost
	case strings.HasPrefix(path, "/v1/services/"):
		// Content transport: GET/HEAD for asset/message/playlist bytes,
		// POST for local import. IsAgentContentPath gates the surface
		// inside the proxy.
		return method == http.MethodGet || method == http.MethodHead ||
			method == http.MethodPost
	case strings.HasPrefix(path, "/v1/library/"):
		// Library writes are grant-decided: photo share and entity-link
		// mutations join the API surface and the request key's write data
		// grant authorizes them. Merge review and the copy ledger expose
		// owner-internal maintenance state that kind grants do not filter,
		// so they stay admin-side — the deny list stays explicit. The read
		// allow side derives from the kind Route declarations (issue #312);
		// an unlisted multi-segment path is not an agent route.
		switch {
		case path == "/v1/library/photos/share" && method == http.MethodPost,
			path == "/v1/library/links" && method == http.MethodPost,
			strings.HasPrefix(path, "/v1/library/links/") && method == http.MethodDelete:
			return true
		}
		if method != http.MethodGet && method != http.MethodHead {
			return false
		}
		if path == "/v1/library/review" || strings.HasPrefix(path, "/v1/library/review/") ||
			path == "/v1/library/copies" || strings.HasPrefix(path, "/v1/library/copies/") {
			return false
		}
		return libraryAgentReadable(path)
	}
	return false
}

// libraryAgentReadable reports whether a library GET/HEAD path is an
// agent-tier read on the API listener. The allowlist derives from the kind
// Route declarations; the cross-kind entity-link reads and single-segment
// user-defined kind paths (runtime-registered; packs never shadow a
// built-in) are the only other entries.
func libraryAgentReadable(path string) bool {
	for _, route := range kinds.RouteDecls() {
		if routePatternMatches(route.Pattern, path) {
			return true
		}
	}
	if path == "/v1/library/links" || strings.HasPrefix(path, "/v1/library/links/") {
		return true
	}
	seg := strings.TrimPrefix(path, "/v1/library/")
	if seg != "" && !strings.Contains(seg, "/") {
		return !kinds.IsReservedUserDefinedID(seg)
	}
	return false
}

// routePatternMatches reports whether a request path matches a declared
// ServeMux-style Route pattern ("{name}" matches exactly one segment).
func routePatternMatches(pattern, path string) bool {
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	qp := strings.Split(strings.Trim(path, "/"), "/")
	if len(pp) != len(qp) {
		return false
	}
	for i := range pp {
		if strings.HasPrefix(pp[i], "{") && strings.HasSuffix(pp[i], "}") {
			continue
		}
		if pp[i] != qp[i] {
			return false
		}
	}
	return true
}

// validateAdminListenAddr refuses admin-listener binds that are reachable
// beyond loopback or the tailnet. The admin listener relies on the tailnet
// as its perimeter (Serve/WhoIs identity or the admin key over an in-tailnet
// path); a wildcard or LAN bind would publish policy surfaces — OAuth,
// data-grant writes, app minting — to any peer on that network.
func validateAdminListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid -admin-addr %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("refusing -admin-addr %q: bind an IP literal on loopback or the Tailscale IP, not %q", addr, host)
	}
	if ip.IsLoopback() || isTailnetListenIP(ip) {
		return nil
	}
	return fmt.Errorf("refusing -admin-addr %q: the admin listener relies on the tailnet — bind loopback (SSH tunnel or tailscale serve) or the Tailscale IP", addr)
}

// validateSplitListeners checks the listener-split configuration:
// external access requires the split (admin must never share the published
// API listener), and the split requires its own base URL for OAuth
// redirects and CSRF origin checks.
func validateSplitListeners(adminAddr, adminBaseURL string, externalAccess bool) error {
	if adminAddr == "" {
		if externalAccess {
			return fmt.Errorf("-external-access requires -admin-addr: the admin surface must never share the published API listener (docs/external-access.md)")
		}
		return nil
	}
	if strings.TrimSpace(adminBaseURL) == "" {
		return fmt.Errorf("-admin-base-url is required with -admin-addr (OAuth redirects and CSRF origin checks on the admin listener)")
	}
	return validateAdminListenAddr(adminAddr)
}
