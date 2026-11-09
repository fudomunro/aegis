// Package connectors hosts named-service adapters (issue #15). Connectors
// depend on Host + internal/kinds + internal/core/policy only — never on
// package main / *Server.
package connectors

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/shared"
)

// ServiceInfo is the configured surface a connector may read. Narrower than
// core config.Service so this package does not own OAuth/config file types.
type ServiceInfo struct {
	ID          string
	Action      string
	Endpoint    string
	Headers     map[string]string
	CacheTTL    time.Duration
	GrantLimits policy.Permissions
	// CanonicalFor names the library kinds this service is the home copy
	// for. Aegis stores metadata only; canonical content stays on the
	// source service.
	CanonicalFor []string
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
	// LibraryReadable reports whether agents can currently read the given
	// library kind — a provider is registered and the kind policy allows it.
	// Setup flows use it to report readiness without reaching into core's
	// registries.
	LibraryReadable(kind string) bool
	// CanonicalOwner returns the service id that claims the given kind as
	// its home copy, or "" when none does. Setup flows use it for the
	// single-owner conflict copy.
	CanonicalOwner(kind string) string
	// SetupSecret returns a setup-time secret by name (e.g.
	// shared.SetupSecretTailscaleAPIKey), or "" when unset. Values reach only
	// connector verify/apply actions and are never included in responses.
	SetupSecret(name string) string
	// SetupSettings returns the non-secret settings recorded for a named
	// setup flow (e.g. shared.SetupFlowTailscale → tailnet, host device,
	// host tag). Empty when the flow has none.
	SetupSettings(name string) map[string]string
}

// ServiceConnector is an optional interface for services that provide
// intelligent proxying beyond simple request forwarding.
type ServiceConnector interface {
	// Capabilities returns what kinds and operations this connector supports.
	Capabilities() shared.ServiceCapabilities
	// SystemInstructions returns the system message for agents referencing this service.
	SystemInstructions(baseURL string) string
	// HandleProxy handles a proxied request for this service.
	HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string)
}

// Onboarder is the optional interface for connectors that drive their own
// advanced setup (issue #265): credential/config inputs, verification probes,
// external-tool guidance, status reporting. Core hosts the flow generically —
// admin routes and rendering, token/secret writes, config overlay persistence,
// the permissions perimeter — and the connector owns every provider-specific
// string, rule, and probe.
//
// The split is the maturity catalog's (#247): declare, don't render. A
// connector never ships HTML or JS, and never writes Aegis tokens or config —
// save/apply verbs return shared.StateUpdate values that core alone persists.
type Onboarder interface {
	// Onboarding returns the declared contract: fields, copyable guidance,
	// and the verbs this flow runs.
	Onboarding() shared.OnboardingSpec
	// OnboardingStatus derives the live view. Read-only and fail-open: a
	// broken provider reports failed rows and hints, never an error page.
	OnboardingStatus(ctx context.Context) shared.OnboardingStatus
	// OnboardingRun executes one declared verb. Probe verbs are read-only;
	// save/apply verbs return StateUpdates for core to persist and may mutate
	// external systems.
	OnboardingRun(ctx context.Context, action string, in shared.OnboardingInput) (shared.OnboardingResult, error)
}
