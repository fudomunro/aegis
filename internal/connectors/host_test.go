package connectors

import (
	"log/slog"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// testHost is a Host fake for connector unit tests.
type testHost struct {
	tokens    map[string]string
	endpoints map[string]string
	services  map[string]*ServiceInfo
	cache     map[string]string
	baseURL   string
}

func newTestHost(baseURL string) *testHost {
	return &testHost{
		tokens:    map[string]string{},
		endpoints: map[string]string{},
		services:  map[string]*ServiceInfo{},
		cache:     map[string]string{},
		baseURL:   baseURL,
	}
}

func (h *testHost) setService(id, endpoint string) {
	h.services[id] = &ServiceInfo{ID: id, Endpoint: endpoint, Permissions: types.Permissions{}}
	h.endpoints[id] = endpoint
}

func (h *testHost) setToken(id, token string) { h.tokens[id] = token }

func (h *testHost) GetValidToken(serviceID string, logger *slog.Logger) string {
	return h.tokens[serviceID]
}

func (h *testHost) UpstreamBaseFor(serviceID, fallback string) string {
	if e := h.endpoints[serviceID]; e != "" {
		return TrimRightSlash(e)
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

func newTestHostWithService(id, endpoint, baseURL string) *testHost {
	h := newTestHost(baseURL)
	h.setService(id, endpoint)
	return h
}
