// Package connectors hosts named-service adapters (issue #15). Connectors
// depend on Host + internal/types only — never on package main / *Server.
package connectors

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// ServiceInfo is the configured surface a connector may read. Narrower than
// core config.Service so this package does not own OAuth/config file types.
type ServiceInfo struct {
	ID          string
	Action      string
	Endpoint    string
	Headers     map[string]string
	CacheTTL    time.Duration
	Permissions types.Permissions
}

// Host is the runtime surface connectors may use (issue #15).
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
	ServiceConfig(serviceID string) *ServiceInfo
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

// ServiceConnector is an optional interface for services that provide
// intelligent proxying beyond simple request forwarding.
type ServiceConnector interface {
	// Capabilities returns what data types and operations this connector supports.
	Capabilities() types.ServiceCapabilities
	// SystemInstructions returns the system message for agents referencing this service.
	SystemInstructions(baseURL string) string
	// HandleProxy handles a proxied request for this service.
	HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string)
}
