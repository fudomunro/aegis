package core

import (
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
)

// ObserveUpstream records upstream latency/status on the metrics ring.
func (s *Server) ObserveUpstream(serviceID string, elapsed time.Duration, status int) {
	if s == nil || s.prom == nil {
		return
	}
	s.prom.ObserveUpstream(serviceID, elapsed, status)
}

// ServiceConfig returns the configured service surface for id, or nil.
func (s *Server) ServiceConfig(serviceID string) *connectors.ServiceInfo {
	if s == nil || s.config == nil {
		return nil
	}
	svc := s.config.GetService(serviceID)
	if svc == nil {
		return nil
	}
	return &connectors.ServiceInfo{
		ID:           svc.ID,
		Action:       svc.Action,
		Endpoint:     svc.Endpoint,
		Headers:      svc.Headers,
		CacheTTL:     svc.CacheTTL.Duration,
		CanonicalFor: append([]string(nil), svc.CanonicalFor...),
	}
}

// BaseURL returns the public base URL of this Aegis instance.
func (s *Server) BaseURL() string {
	if s == nil {
		return ""
	}
	return s.baseURL
}

// CacheGet returns a cached value when present and live.
func (s *Server) CacheGet(key string) (string, bool) {
	if s == nil || s.cache == nil {
		return "", false
	}
	return s.cache.Get(key)
}

// CacheSet stores a value under key for ttl.
func (s *Server) CacheSet(key, value string, ttl time.Duration) {
	if s == nil || s.cache == nil {
		return
	}
	s.cache.Set(key, value, ttl)
}

// LibraryReadable reports whether agents can currently read the given library
// kind: a provider is registered and the kind policy allows reads. Backs
// connectors.Host for setup readiness reporting (issue #265).
func (s *Server) LibraryReadable(kind string) bool {
	if s == nil {
		return false
	}
	if k, ok := s.library().RegistryKind(kind); ok {
		return k.HasProviders(s) && k.Readable(s)
	}
	return false
}

// CanonicalOwner returns the service id claiming kind as its home copy.
// Backs connectors.Host for setup conflict reporting (issue #265).
func (s *Server) CanonicalOwner(kind string) string {
	if s == nil || s.config == nil {
		return ""
	}
	for _, svc := range s.config.ServiceSnapshots() {
		for _, k := range svc.CanonicalFor {
			if k == kind {
				return svc.ID
			}
		}
	}
	return ""
}

// SetupSecret returns a setup-time secret by name (issue #265). Only the
// Tailscale Admin API key is such a secret today; it lives in the encrypted
// token store and is never echoed by any API.
func (s *Server) SetupSecret(name string) string {
	if s == nil || s.config == nil {
		return ""
	}
	if name != shared.SetupSecretTailscaleAPIKey {
		return ""
	}
	return s.config.TailscaleAdminAPIKey()
}

// SetupSettings returns the non-secret settings recorded for a named setup
// flow (issue #265).
func (s *Server) SetupSettings(name string) map[string]string {
	if s == nil || s.config == nil || name != shared.SetupFlowTailscale {
		return nil
	}
	snap := s.config.TailscaleAdminSnapshot()
	return map[string]string{
		"tailnet":        snap.Tailnet,
		"host_device_id": snap.HostDeviceID,
		"host_tag":       snap.HostTag,
	}
}

// Server implements connectors.Host (compile-time check).
var _ connectors.Host = (*Server)(nil)
