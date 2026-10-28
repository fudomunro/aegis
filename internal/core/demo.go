package core

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Demo mode (issue #217) is a deployment profile for a public synthetic
// instance: api-key + data scopes only, sample:// connectors only, and a
// read-only surface. It is not a second product — live OAuth, imports, and
// admin writes are refused so a demo host cannot leak or mutate real state.

// demoModeEnv enables demo mode when set to 1/true/yes.
const demoModeEnv = "AEGIS_DEMO"

// resolveDemoMode reports whether demo mode is requested via flag or env.
func resolveDemoMode(flagVal bool) bool {
	if flagVal {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoModeEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// isDemoServiceEndpoint reports whether a configured service is allowed in
// demo mode (synthetic fixtures only).
func isDemoServiceEndpoint(svc *Service) bool {
	if svc == nil {
		return false
	}
	if strings.HasPrefix(svc.Endpoint, "sample://") {
		return true
	}
	// Fixture library ids without an endpoint still map to sample://library.
	return IsSampleLibraryService(svc) && svc.Endpoint == ""
}

// validateDemoConfig enforces demo lockdown after config + scopes load.
// It returns a fatal error when the instance is not safe to expose.
func validateDemoConfig(cfg *Config, scopes *DataScopeStore) error {
	if cfg != nil {
		for _, svc := range cfg.ServiceSnapshots() {
			if svc.Action == "block" {
				continue
			}
			if !isDemoServiceEndpoint(&svc) {
				return fmt.Errorf("demo mode: service %q (endpoint %q) is not allowed — only sample:// fixtures may be enabled", svc.ID, svc.Endpoint)
			}
		}
	}
	if scopes == nil || len(scopes.All()) == 0 {
		return fmt.Errorf("demo mode: data_scopes.json must define at least one scope (agent principal)")
	}
	return nil
}

// ForceReadOnlyAll marks every data scope read-only (demo lockdown).
func (s *DataScopeStore) ForceReadOnlyAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.byID {
		sc.ReadOnly = true
	}
}

// demoLockdown refuses mutating admin/OAuth routes in demo mode. GET/HEAD
// stay available for health and read-only admin views.
func (s *Server) demoLockdown(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.demoMode {
			if p := r.URL.Path; strings.HasPrefix(p, "/v1/auth/") && p != "/v1/auth/status" {
				writeJSONError(w, http.StatusForbidden, "demo mode: OAuth is disabled")
				return
			}
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				if p := r.URL.Path; p == hookPath || strings.HasPrefix(p, "/v1/pre-request") {
					next.ServeHTTP(w, r)
					return
				}
				writeJSONError(w, http.StatusForbidden, "demo mode is read-only")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
