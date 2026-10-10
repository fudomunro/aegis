package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Prometheus-style metrics export (issue #105). Process-lifetime counters
// rendered in text exposition format. Values reset on restart; Prometheus
// rate()/increase() treat that as a counter reset, so scrapes stay
// reset-safe. Labels are service/library kind ids and redacted reasons only
// — never agent identities (cardinality + privacy).

// upstreamDurationBuckets are histogram bucket upper bounds in seconds.
var upstreamDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type promHist struct {
	// counts[i] is observations <= upstreamDurationBuckets[i].
	// An extra slot after the last bound holds the +Inf bucket.
	counts []uint64
	sum    float64
	count  uint64
}

func newPromHist() *promHist {
	return &promHist{counts: make([]uint64, len(upstreamDurationBuckets)+1)}
}

func (h *promHist) observe(seconds float64) {
	if h == nil {
		return
	}
	h.sum += seconds
	h.count++
	for i, bound := range upstreamDurationBuckets {
		if seconds <= bound {
			h.counts[i]++
			return
		}
	}
	h.counts[len(upstreamDurationBuckets)]++
}

type promAccessKey struct {
	surface  string
	target   string
	decision string
}

// PromMetrics accumulates exportable counters. Safe for concurrent use.
// Nil-safe: every observe method is a no-op on a nil receiver.
type PromMetrics struct {
	mu    sync.Mutex
	start time.Time

	hookOutcomes map[string]uint64
	access       map[promAccessKey]uint64
	cache        map[string]uint64
	denials      map[string]uint64 // surface|target -> count
	upstream     map[string]*promHist
	audit        map[string]uint64 // decision -> count
}

// NewPromMetrics creates an empty metrics registry.
func NewPromMetrics() *PromMetrics {
	return &PromMetrics{
		start:        time.Now(),
		hookOutcomes: make(map[string]uint64),
		access:       make(map[promAccessKey]uint64),
		cache:        make(map[string]uint64),
		denials:      make(map[string]uint64),
		upstream:     make(map[string]*promHist),
		audit:        make(map[string]uint64),
	}
}

func (p *PromMetrics) observeHook(action string) {
	if p == nil {
		return
	}
	if action == "" {
		action = "unknown"
	}
	p.mu.Lock()
	p.hookOutcomes[action]++
	p.mu.Unlock()
}

// observeAccess records one redacted access/denial event for export.
// Cache is optional (hit/miss/empty). Decision empty is treated as allow.
func (p *PromMetrics) observeAccess(ev AccessEvent) {
	if p == nil {
		return
	}
	decision := ev.Decision
	if decision == "" {
		decision = DecisionAllow
	}
	surface := ev.Surface
	if surface == "" {
		surface = "unknown"
	}
	target := ev.Target
	if target == "" {
		target = "unknown"
	}
	p.mu.Lock()
	p.access[promAccessKey{surface: surface, target: target, decision: decision}]++
	switch ev.Cache {
	case MetricCacheHit:
		p.cache[MetricCacheHit]++
	case MetricCacheMiss:
		p.cache[MetricCacheMiss]++
	}
	if decision == DecisionDeny {
		p.denials[surface+"|"+target]++
	}
	p.audit[decision]++
	p.mu.Unlock()
}

// observeUpstream records one upstream request latency sample.
func (p *PromMetrics) observeUpstream(service string, d time.Duration, status int) {
	if p == nil {
		return
	}
	if service == "" {
		service = "unknown"
	}
	seconds := d.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	p.mu.Lock()
	h := p.upstream[service]
	if h == nil {
		h = newPromHist()
		p.upstream[service] = h
	}
	h.observe(seconds)
	p.mu.Unlock()
}

// HandleMetrics renders Prometheus text exposition format.
// Auth-scoped like admin: series include service ids and denial reasons.
func (s *Server) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body := s.prom.promText()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(body))
}

// promText renders the current registry. Exported for tests.
func (p *PromMetrics) promText() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var b strings.Builder
	startSec := float64(0)
	if !p.start.IsZero() {
		startSec = float64(p.start.Unix())
	}
	writeMeta(&b, "aegis_process_start_time_seconds", "gauge",
		"Unix time when this Aegis process started (metrics reset on restart).")
	fmt.Fprintf(&b, "aegis_process_start_time_seconds %g\n", startSec)
	writeMeta(&b, "aegis_process_uptime_seconds", "gauge",
		"Seconds since process start.")
	fmt.Fprintf(&b, "aegis_process_uptime_seconds %g\n", time.Since(p.start).Seconds())

	writeMeta(&b, "aegis_hook_outcomes_total", "counter",
		"Pre-request hook outcomes by action (allow, modify, block, auth_fail, parse_fail).")
	for _, action := range sortedKeys(p.hookOutcomes) {
		fmt.Fprintf(&b, "aegis_hook_outcomes_total{action=%q} %d\n", action, p.hookOutcomes[action])
	}

	writeMeta(&b, "aegis_access_total", "counter",
		"Redacted agent data access events by surface, target, and decision.")
	keys := make([]promAccessKey, 0, len(p.access))
	for k := range p.access {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.surface != b.surface {
			return a.surface < b.surface
		}
		if a.target != b.target {
			return a.target < b.target
		}
		return a.decision < b.decision
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "aegis_access_total{surface=%q,target=%q,decision=%q} %d\n",
			k.surface, k.target, k.decision, p.access[k])
	}

	writeMeta(&b, "aegis_cache_events_total", "counter",
		"Response cache outcomes for agent traffic.")
	for _, result := range []string{MetricCacheHit, MetricCacheMiss} {
		if n, ok := p.cache[result]; ok || result == MetricCacheHit || result == MetricCacheMiss {
			fmt.Fprintf(&b, "aegis_cache_events_total{result=%q} %d\n", result, n)
		}
	}

	writeMeta(&b, "aegis_permission_denials_total", "counter",
		"Permission and auth denials by surface and target.")
	for _, key := range sortedKeys(p.denials) {
		parts := strings.SplitN(key, "|", 2)
		surface, target := "unknown", "unknown"
		if len(parts) == 2 {
			surface, target = parts[0], parts[1]
		}
		fmt.Fprintf(&b, "aegis_permission_denials_total{surface=%q,target=%q} %d\n",
			surface, target, p.denials[key])
	}

	writeMeta(&b, "aegis_audit_events_total", "counter",
		"Audit sink writes by decision.")
	for _, decision := range []string{DecisionAllow, DecisionDeny} {
		fmt.Fprintf(&b, "aegis_audit_events_total{decision=%q} %d\n", decision, p.audit[decision])
	}

	writeMeta(&b, "aegis_upstream_duration_seconds", "histogram",
		"Upstream provider request latency in seconds by service.")
	for _, service := range sortedHistKeys(p.upstream) {
		h := p.upstream[service]
		var cumulative uint64
		for i, bound := range upstreamDurationBuckets {
			cumulative += h.counts[i]
			fmt.Fprintf(&b, "aegis_upstream_duration_seconds_bucket{service=%q,le=%q} %d\n",
				service, formatLE(bound), cumulative)
		}
		cumulative += h.counts[len(upstreamDurationBuckets)]
		fmt.Fprintf(&b, "aegis_upstream_duration_seconds_bucket{service=%q,le=\"+Inf\"} %d\n",
			service, cumulative)
		fmt.Fprintf(&b, "aegis_upstream_duration_seconds_sum{service=%q} %g\n", service, h.sum)
		fmt.Fprintf(&b, "aegis_upstream_duration_seconds_count{service=%q} %d\n", service, h.count)
	}
	return b.String()
}

// statusOrZero returns the upstream status code, or 0 when the call failed
// before a status arrived (issue #105 latency metrics).
func statusOrZero(status int, err error) int {
	if err != nil {
		return 0
	}
	return status
}

func writeMeta(b *strings.Builder, name, typ, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, typ)
}

func formatLE(v float64) string {
	// Prometheus uses compact decimal form; strconv would also work but
	// %g matches common exporter output for these bucket bounds.
	return fmt.Sprintf("%g", v)
}

func sortedKeys(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedHistKeys(m map[string]*promHist) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
