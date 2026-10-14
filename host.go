package main

import (
	"log/slog"
	"time"
)

// Host is the runtime surface connectors may use (issue #15). It is the seam
// that lets named-service adapters leave package main without importing
// *Server — the first step of the core / data-types / connectors split.
//
// Keep this interface narrow. Provider quirks and neutral models stay out;
// token storage, config file I/O, and admin stay on the core side.
type Host interface {
	// GetValidToken returns a valid access token for the service's provider
	// connection, refreshing if needed. Returns "" when there is no token or
	// the connection lacks scopes required by this surface.
	GetValidToken(serviceID string, logger *slog.Logger) string
	// UpstreamBaseFor returns the configured endpoint for a service, or fallback.
	UpstreamBaseFor(serviceID, fallback string) string
	// ServiceConfig returns the configured service surface, or nil.
	ServiceConfig(serviceID string) *Service
	// BaseURL is the public base URL of this Aegis instance.
	BaseURL() string
	// CacheGet returns a cached value when present and live.
	CacheGet(key string) (string, bool)
	// CacheSet stores a value under key for ttl.
	CacheSet(key, value string, ttl time.Duration)
	// ObserveUpstream records upstream latency/status (no-op when metrics
	// are disabled).
	ObserveUpstream(serviceID string, elapsed time.Duration, status int)
}

// ObserveUpstream records upstream latency/status on the metrics ring.
func (s *Server) ObserveUpstream(serviceID string, elapsed time.Duration, status int) {
	if s == nil || s.prom == nil {
		return
	}
	s.prom.observeUpstream(serviceID, elapsed, status)
}

// ServiceConfig returns the configured service surface for id, or nil.
func (s *Server) ServiceConfig(serviceID string) *Service {
	if s == nil || s.config == nil {
		return nil
	}
	return s.config.GetService(serviceID)
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
