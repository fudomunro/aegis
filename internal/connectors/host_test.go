package connectors

import (
	"log/slog"
	"strings"
	"time"
)

// testHost is a Host fake for connector unit tests.
type testHost struct {
	tokens    map[string]string
	endpoints map[string]string
	services  map[string]*ServiceInfo
	cache     map[string]string
	baseURL   string
	canonical map[string]string
	libraryOK map[string]bool
	secrets   map[string]string
	settings  map[string]map[string]string
}

func newTestHost(baseURL string) *testHost {
	return &testHost{
		tokens:    map[string]string{},
		endpoints: map[string]string{},
		services:  map[string]*ServiceInfo{},
		cache:     map[string]string{},
		baseURL:   baseURL,
		canonical: map[string]string{},
		libraryOK: map[string]bool{},
		secrets:   map[string]string{},
		settings:  map[string]map[string]string{},
	}
}

func (h *testHost) setService(id, endpoint string) {
	h.services[id] = &ServiceInfo{ID: id, Endpoint: endpoint}
	h.endpoints[id] = endpoint
}

// setCredentials attaches static connection credentials to a configured
// service (issue #420: protocol mail transports).
func (h *testHost) setCredentials(id string, creds map[string]string) {
	if svc := h.services[id]; svc != nil {
		svc.Credentials = creds
	}
}

func (h *testHost) setToken(id, token string) { h.tokens[id] = token }

func (h *testHost) GetValidToken(serviceID string, logger *slog.Logger) string {
	// Touch the logger as the production GetValidToken does, so a nil logger
	// passed by a connector write path panics in tests.
	logger.Debug("testHost token lookup", "service", serviceID)
	return h.tokens[serviceID]
}

func (h *testHost) UpstreamBaseFor(serviceID, fallback string) string {
	if e := h.endpoints[serviceID]; e != "" {
		return strings.TrimRight(e, "/")
	}
	return fallback
}

func (h *testHost) ServiceConfig(serviceID string) *ServiceInfo {
	return h.services[serviceID]
}

func (h *testHost) BaseURL() string { return h.baseURL }

func (h *testHost) CacheGet(key string) (string, bool) {
	v, ok := h.cache[key]
	return v, ok
}

func (h *testHost) CacheSet(key, value string, ttl time.Duration) {
	h.cache[key] = value
}

func (h *testHost) ObserveUpstream(serviceID string, elapsed time.Duration, status int) {}
func (h *testHost) LibraryReadable(kind string) bool                                    { return h.libraryOK[kind] }
func (h *testHost) CanonicalOwner(kind string) string                                   { return h.canonical[kind] }
func (h *testHost) SetupSecret(name string) string                                      { return h.secrets[name] }
func (h *testHost) SetupSettings(name string) map[string]string                         { return h.settings[name] }

func newTestHostWithService(id, endpoint, baseURL string) *testHost {
	h := newTestHost(baseURL)
	h.setService(id, endpoint)
	return h
}
