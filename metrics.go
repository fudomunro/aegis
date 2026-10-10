package main

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Metric surfaces for access stats.
const (
	MetricSurfaceService = "service"
	MetricSurfaceLibrary = "library"
	// MetricSurfaceAuth is used for authentication denials (issue #105).
	MetricSurfaceAuth = "auth"
)

// Cache outcome recorded with an access event.
const (
	MetricCacheHit  = "hit"
	MetricCacheMiss = "miss"
	MetricCacheNone = ""
)

// defaultMetricsCapacity is the in-memory ring buffer size for access events.
const defaultMetricsCapacity = 4096

// AccessEvent is one redacted agent access record. Never stores request
// bodies, tokens, raw query strings, or full paths (issue #32). Decision and
// Reason feed the durable audit sink (issue #105).
type AccessEvent struct {
	At       time.Time
	Agent    string
	Surface  string
	Target   string // service id or library kind id
	DataType string
	Cache    string // hit, miss, or empty
	// Decision is DecisionAllow or DecisionDeny. Empty is treated as allow
	// for compatibility with older in-memory events.
	Decision string
	// Reason is a short redacted denial reason (permission, app_scope,
	// service_blocked, auth). Never free-form user text.
	Reason string
	// Method is the HTTP method when known (audit only).
	Method string
	// Status is the HTTP response status when known (audit only).
	Status int
}

// AccessMetrics is a lock-protected ring buffer of recent access events.
type AccessMetrics struct {
	mu       sync.Mutex
	events   []AccessEvent
	start    int
	count    int
	capacity int
}

// NewAccessMetrics creates a ring buffer. Non-positive capacity uses default.
func NewAccessMetrics(capacity int) *AccessMetrics {
	if capacity <= 0 {
		capacity = defaultMetricsCapacity
	}
	return &AccessMetrics{
		events:   make([]AccessEvent, capacity),
		capacity: capacity,
	}
}

// Record appends an event, evicting the oldest when the ring is full.
func (m *AccessMetrics) Record(ev AccessEvent) {
	if m == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.count < m.capacity {
		idx := (m.start + m.count) % m.capacity
		m.events[idx] = ev
		m.count++
		return
	}
	m.events[m.start] = ev
	m.start = (m.start + 1) % m.capacity
}

// MetricsAgentDetail is one agent's access breakdown in the stats window
// (issue #114). Identifiers are service ids / library kind ids only — never
// paths or bodies.
type MetricsAgentDetail struct {
	ID   string `json:"id"`
	Hits int    `json:"hits"`
	// Services maps service id → hits on the proxy surface.
	Services map[string]int `json:"services,omitempty"`
	// Libraries maps library kind id → hits on the unified library surface.
	Libraries map[string]int `json:"libraries,omitempty"`
	// DataTypes maps recorded data type → hits across both surfaces.
	DataTypes map[string]int `json:"data_types,omitempty"`
	LastAt    time.Time      `json:"last_at,omitempty"`
}

// MetricsAgentStats summarizes distinct callers in a window.
type MetricsAgentStats struct {
	Distinct int      `json:"distinct"`
	Sample   []string `json:"sample,omitempty"`
	// Detail lists each agent with what they touched (issue #114).
	Detail []MetricsAgentDetail `json:"detail,omitempty"`
}

// MetricsEndpointStats is hit counts for one service or library kind.
type MetricsEndpointStats struct {
	Hits      int            `json:"hits"`
	Agents    int            `json:"agents"`
	CacheHits int            `json:"cache_hits,omitempty"`
	CacheMiss int            `json:"cache_misses,omitempty"`
	DataTypes map[string]int `json:"data_types,omitempty"`
}

// MetricsRecentAccess is a redacted sample row for admin detail views.
type MetricsRecentAccess struct {
	At       time.Time `json:"at"`
	Agent    string    `json:"agent"`
	Surface  string    `json:"surface"`
	Target   string    `json:"target"`
	DataType string    `json:"data_type,omitempty"`
	Cache    string    `json:"cache,omitempty"`
}

// MetricsSnapshot aggregates events inside a time window.
type MetricsSnapshot struct {
	Window    time.Duration
	Agents    MetricsAgentStats
	Services  map[string]MetricsEndpointStats
	Libraries map[string]MetricsEndpointStats
	Recent    []MetricsRecentAccess
	TotalHits int
}

const metricsSampleLimit = 8
const metricsRecentLimit = 12

// metricsAgentDetailLimit caps per-agent map nodes so a noisy window cannot
// explode the admin SVG (issue #114).
const metricsAgentDetailLimit = 8

// Snapshot aggregates ring-buffer events within the given window.
func (m *AccessMetrics) Snapshot(window time.Duration) MetricsSnapshot {
	snap := MetricsSnapshot{
		Window:    window,
		Services:  make(map[string]MetricsEndpointStats),
		Libraries: make(map[string]MetricsEndpointStats),
	}
	if m == nil {
		return snap
	}
	if window <= 0 {
		window = time.Hour
		snap.Window = window
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-window)
	agents := make(map[string]bool)
	agentDetail := make(map[string]*MetricsAgentDetail)
	recent := make([]MetricsRecentAccess, 0, metricsRecentLimit)

	for i := 0; i < m.count; i++ {
		ev := m.events[(m.start+i)%m.capacity]
		if ev.At.Before(cutoff) {
			continue
		}
		if ev.Agent != "" {
			agents[ev.Agent] = true
			d := agentDetail[ev.Agent]
			if d == nil {
				d = &MetricsAgentDetail{
					ID:        ev.Agent,
					Services:  make(map[string]int),
					Libraries: make(map[string]int),
					DataTypes: make(map[string]int),
				}
				agentDetail[ev.Agent] = d
			}
			d.Hits++
			if ev.Surface == MetricSurfaceLibrary {
				d.Libraries[ev.Target]++
			} else if ev.Target != "" {
				d.Services[ev.Target]++
			}
			if ev.DataType != "" {
				d.DataTypes[ev.DataType]++
			}
			if ev.At.After(d.LastAt) {
				d.LastAt = ev.At
			}
		}
		snap.TotalHits++

		var bucket map[string]MetricsEndpointStats
		if ev.Surface == MetricSurfaceLibrary {
			bucket = snap.Libraries
		} else {
			bucket = snap.Services
		}
		st := bucket[ev.Target]
		st.Hits++
		if ev.Cache == MetricCacheHit {
			st.CacheHits++
		} else if ev.Cache == MetricCacheMiss {
			st.CacheMiss++
		}
		if ev.DataType != "" {
			if st.DataTypes == nil {
				st.DataTypes = make(map[string]int)
			}
			st.DataTypes[ev.DataType]++
		}
		bucket[ev.Target] = st

		if len(recent) < metricsRecentLimit {
			recent = append(recent, MetricsRecentAccess{
				At:       ev.At,
				Agent:    ev.Agent,
				Surface:  ev.Surface,
				Target:   ev.Target,
				DataType: ev.DataType,
				Cache:    ev.Cache,
			})
		}
	}

	// Distinct agents per endpoint (second pass over in-window events).
	svcAgents := make(map[string]map[string]bool)
	libAgents := make(map[string]map[string]bool)
	for i := 0; i < m.count; i++ {
		ev := m.events[(m.start+i)%m.capacity]
		if ev.At.Before(cutoff) || ev.Agent == "" {
			continue
		}
		var set map[string]map[string]bool
		if ev.Surface == MetricSurfaceLibrary {
			set = libAgents
		} else {
			set = svcAgents
		}
		if set[ev.Target] == nil {
			set[ev.Target] = make(map[string]bool)
		}
		set[ev.Target][ev.Agent] = true
	}
	for id, set := range svcAgents {
		st := snap.Services[id]
		st.Agents = len(set)
		snap.Services[id] = st
	}
	for id, set := range libAgents {
		st := snap.Libraries[id]
		st.Agents = len(set)
		snap.Libraries[id] = st
	}

	names := make([]string, 0, len(agents))
	for a := range agents {
		names = append(names, a)
	}
	sort.Strings(names)
	snap.Agents.Distinct = len(names)
	if len(names) > metricsSampleLimit {
		names = names[:metricsSampleLimit]
	}
	snap.Agents.Sample = names

	// Per-agent breakdown for map nodes/arrows (issue #114). Busiest first
	// so the map draws the most active agents when the list is truncated.
	detail := make([]MetricsAgentDetail, 0, len(agentDetail))
	for _, d := range agentDetail {
		detail = append(detail, *d)
	}
	sort.Slice(detail, func(i, j int) bool {
		if detail[i].Hits != detail[j].Hits {
			return detail[i].Hits > detail[j].Hits
		}
		return detail[i].ID < detail[j].ID
	})
	if len(detail) > metricsAgentDetailLimit {
		detail = detail[:metricsAgentDetailLimit]
	}
	snap.Agents.Detail = detail

	// Oldest → newest for the recent sample.
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	snap.Recent = recent
	return snap
}

// agentForRequest returns a redacted agent id: tailnet login, app id, or
// "api-key". Never returns token material.
func agentForRequest(r *http.Request) string {
	if r != nil {
		if app := AppIdentityFrom(r); app != nil && app.ID != "" {
			return "app:" + app.ID
		}
		if id := PeerIdentityFrom(r); id != nil && id.LoginName != "" {
			return id.LoginName
		}
	}
	return "api-key"
}

// recordAccess appends a redacted access event to the in-memory ring, the
// durable audit log, and the Prometheus counters (issue #105). Fail-open:
// a disabled metrics/audit sink never blocks the request path.
func (s *Server) recordAccess(ev AccessEvent) {
	if s == nil {
		return
	}
	if ev.Agent == "" {
		ev.Agent = "api-key"
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	if ev.Decision == "" {
		ev.Decision = DecisionAllow
	}
	if s.metrics != nil {
		s.metrics.Record(ev)
	}
	if s.audit != nil {
		s.audit.Write(AuditEvent{
			At:       ev.At,
			Agent:    ev.Agent,
			Surface:  ev.Surface,
			Target:   ev.Target,
			DataType: ev.DataType,
			Decision: ev.Decision,
			Reason:   ev.Reason,
			Cache:    ev.Cache,
			Method:   ev.Method,
			Status:   ev.Status,
		})
	}
	if s.prom != nil {
		s.prom.observeAccess(ev)
	}
}

type skipAccessMetricsKey struct{}

// withSkipAccessMetrics marks a request as admin/preview traffic. Admin
// Library views share the agent aggregate pipeline but must not inflate
// Map access counts (issue #62).
func withSkipAccessMetrics(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), skipAccessMetricsKey{}, true))
}

func skipAccessMetrics(r *http.Request) bool {
	if r == nil {
		return false
	}
	skip, _ := r.Context().Value(skipAccessMetricsKey{}).(bool)
	return skip
}

// recordAccessFrom records agent access unless the request is admin UI
// preview traffic (issue #62).
func (s *Server) recordAccessFrom(r *http.Request, ev AccessEvent) {
	if skipAccessMetrics(r) {
		return
	}
	s.recordAccess(ev)
}

// metricsSnapshot returns aggregated stats; empty maps when metrics are off.
func (s *Server) metricsSnapshot(window time.Duration) MetricsSnapshot {
	if s == nil || s.metrics == nil {
		if window <= 0 {
			window = time.Hour
		}
		return MetricsSnapshot{
			Window:    window,
			Services:  make(map[string]MetricsEndpointStats),
			Libraries: make(map[string]MetricsEndpointStats),
		}
	}
	if window <= 0 {
		window = time.Hour
	}
	return s.metrics.Snapshot(window)
}
