package core

import (
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

// ObserveUpstream records upstream latency/status on the metrics ring.
func (s *Server) ObserveUpstream(serviceID string, elapsed time.Duration, status int) {
	if s == nil || s.prom == nil {
		return
	}
	s.prom.observeUpstream(serviceID, elapsed, status)
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
		ID:          svc.ID,
		Action:      svc.Action,
		Endpoint:    svc.Endpoint,
		Headers:     svc.Headers,
		CacheTTL:    svc.CacheTTL.Duration,
		Permissions: svc.Permissions,
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

// Server implements connectors.Host (compile-time check).
var _ connectors.Host = (*Server)(nil)
