package core

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

// promSyncKey labels sync job counters. No agent identities (cardinality).
type promSyncKey struct {
	jobType string
	state   string
}

// promSyncDurKey labels sync job duration histograms.
type promSyncDurKey struct {
	jobType string
	service string
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

	// Sync queue observability (issue #227).
	syncJobs     map[promSyncKey]uint64
	syncRetries  map[string]uint64 // job_type|error_class -> count
	syncDuration map[promSyncDurKey]*promHist
	syncQueue    map[string]int // state -> depth, refreshed by the worker
	syncLastOK   map[string]float64
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
		syncJobs:     make(map[promSyncKey]uint64),
		syncRetries:  make(map[string]uint64),
		syncDuration: make(map[promSyncDurKey]*promHist),
		syncQueue:    make(map[string]int),
		syncLastOK:   make(map[string]float64),
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

// observeSyncJob records one sync job step outcome (issue #227).
func (p *PromMetrics) observeSyncJob(jobType, service, state string, d time.Duration) {
	if p == nil {
		return
	}
	if jobType == "" {
		jobType = "unknown"
	}
	if state == "" {
		state = "unknown"
	}
	if service == "" {
		service = "none"
	}
	seconds := d.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	p.mu.Lock()
	p.syncJobs[promSyncKey{jobType: jobType, state: state}]++
	key := promSyncDurKey{jobType: jobType, service: service}
	h := p.syncDuration[key]
	if h == nil {
		h = newPromHist()
		p.syncDuration[key] = h
	}
	h.observe(seconds)
	p.mu.Unlock()
}

// observeSyncRetry records a retried or terminal sync failure.
func (p *PromMetrics) observeSyncRetry(jobType, class string) {
	if p == nil {
		return
	}
	if jobType == "" {
		jobType = "unknown"
	}
	if class == "" {
		class = "unknown"
	}
	p.mu.Lock()
	p.syncRetries[jobType+"|"+class]++
	p.mu.Unlock()
}

// setSyncQueueDepth refreshes the queue-depth gauges from the store rollup.
func (p *PromMetrics) setSyncQueueDepth(c SyncCounts) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.syncQueue[SyncJobQueued] = c.Queued
	p.syncQueue[SyncJobRunning] = c.Running
	p.syncQueue[SyncJobSucceeded] = c.Succeeded
	p.syncQueue[SyncJobFailed] = c.Failed
	p.syncQueue[SyncJobCanceled] = c.Canceled
	p.syncQueue[SyncJobPaused] = c.Paused
	p.mu.Unlock()
}

// noteSyncSuccess records the last successful finish per kind|service.
func (p *PromMetrics) noteSyncSuccess(kind, service string, at time.Time) {
	if p == nil {
		return
	}
	if kind == "" {
		kind = "unknown"
	}
	if service == "" {
		service = "none"
	}
	p.mu.Lock()
	p.syncLastOK[kind+"|"+service] = float64(at.Unix())
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

	// Help text is the /metrics surface operators read (attribution: issue #86).
	writeMeta(&b, "aegis_build_info", "gauge",
		"Build version and commit of the running binary.")
	fmt.Fprintf(&b, "aegis_build_info{version=%q,commit=%q} 1\n", Version, Commit)

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

	writeMeta(&b, "aegis_sync_jobs_total", "counter",
		"Sync job step outcomes by job type and result state.")
	syncJobKeys := make([]promSyncKey, 0, len(p.syncJobs))
	for k := range p.syncJobs {
		syncJobKeys = append(syncJobKeys, k)
	}
	sort.Slice(syncJobKeys, func(i, j int) bool {
		if syncJobKeys[i].jobType != syncJobKeys[j].jobType {
			return syncJobKeys[i].jobType < syncJobKeys[j].jobType
		}
		return syncJobKeys[i].state < syncJobKeys[j].state
	})
	for _, k := range syncJobKeys {
		fmt.Fprintf(&b, "aegis_sync_jobs_total{job_type=%q,state=%q} %d\n",
			k.jobType, k.state, p.syncJobs[k])
	}

	writeMeta(&b, "aegis_sync_retries_total", "counter",
		"Sync job retries and terminal failures by job type and error class.")
	for _, key := range sortedKeys(p.syncRetries) {
		parts := strings.SplitN(key, "|", 2)
		jobType, class := "unknown", "unknown"
		if len(parts) == 2 {
			jobType, class = parts[0], parts[1]
		}
		fmt.Fprintf(&b, "aegis_sync_retries_total{job_type=%q,error_class=%q} %d\n",
			jobType, class, p.syncRetries[key])
	}

	writeMeta(&b, "aegis_sync_job_duration_seconds", "histogram",
		"Sync job step wall time in seconds by job type and service.")
	syncDurKeys := make([]promSyncDurKey, 0, len(p.syncDuration))
	for k := range p.syncDuration {
		syncDurKeys = append(syncDurKeys, k)
	}
	sort.Slice(syncDurKeys, func(i, j int) bool {
		if syncDurKeys[i].jobType != syncDurKeys[j].jobType {
			return syncDurKeys[i].jobType < syncDurKeys[j].jobType
		}
		return syncDurKeys[i].service < syncDurKeys[j].service
	})
	for _, k := range syncDurKeys {
		h := p.syncDuration[k]
		var cumulative uint64
		for i, bound := range upstreamDurationBuckets {
			cumulative += h.counts[i]
			fmt.Fprintf(&b, "aegis_sync_job_duration_seconds_bucket{job_type=%q,service=%q,le=%q} %d\n",
				k.jobType, k.service, formatLE(bound), cumulative)
		}
		cumulative += h.counts[len(upstreamDurationBuckets)]
		fmt.Fprintf(&b, "aegis_sync_job_duration_seconds_bucket{job_type=%q,service=%q,le=\"+Inf\"} %d\n",
			k.jobType, k.service, cumulative)
		fmt.Fprintf(&b, "aegis_sync_job_duration_seconds_sum{job_type=%q,service=%q} %g\n",
			k.jobType, k.service, h.sum)
		fmt.Fprintf(&b, "aegis_sync_job_duration_seconds_count{job_type=%q,service=%q} %d\n",
			k.jobType, k.service, h.count)
	}

	writeMeta(&b, "aegis_sync_queue_depth", "gauge",
		"Sync jobs by state (refreshed by the sync worker).")
	for _, state := range []string{SyncJobQueued, SyncJobRunning, SyncJobSucceeded,
		SyncJobFailed, SyncJobCanceled, SyncJobPaused} {
		fmt.Fprintf(&b, "aegis_sync_queue_depth{state=%q} %d\n", state, p.syncQueue[state])
	}

	writeMeta(&b, "aegis_sync_last_success_timestamp_seconds", "gauge",
		"Unix time of the most recent successful sync job per kind and service.")
	lastKeys := make([]string, 0, len(p.syncLastOK))
	for k := range p.syncLastOK {
		lastKeys = append(lastKeys, k)
	}
	sort.Strings(lastKeys)
	for _, key := range lastKeys {
		parts := strings.SplitN(key, "|", 2)
		kind, service := "unknown", "none"
		if len(parts) == 2 {
			kind, service = parts[0], parts[1]
		}
		fmt.Fprintf(&b, "aegis_sync_last_success_timestamp_seconds{kind=%q,service=%q} %g\n",
			kind, service, p.syncLastOK[key])
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
