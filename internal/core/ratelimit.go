package core

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/core/audit"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/shared"
)

// Brute-force damping for authentication and token-bearing routes
// (issue #253). Per-IP request caps damp guessing volume; per-IP and
// per-token failure caps bound credential retries. The counters are
// fixed-window and best-effort — they protect auth surfaces, not the
// availability story (body caps + edge rate limits cover that).

// RateLimitConfig tunes auth damping (aegis.json `rate_limits`, issue #253).
// Zero fields use the defaults; negative disables that cap.
type RateLimitConfig struct {
	// AuthPerMinute caps requests per client IP on auth/token routes.
	AuthPerMinute int `json:"auth_per_minute,omitempty"`
	// AuthFailuresPerIP locks out a client IP after N failed authentications
	// in the window (0 default, negative disables).
	AuthFailuresPerIP int `json:"auth_failures_per_ip,omitempty"`
	// AuthFailuresPerToken locks out one presented credential after N
	// failures in the window, whatever the source IP.
	AuthFailuresPerToken int `json:"auth_failures_per_token,omitempty"`
	// WindowSeconds is the failure-cap window.
	WindowSeconds int `json:"window_seconds,omitempty"`
}

const (
	defaultAuthPerMinute        = 60
	defaultAuthFailuresPerIP    = 10
	defaultAuthFailuresPerToken = 10
	defaultRateWindowSeconds    = 300

	// maxRateTrackedKeys bounds limiter memory; stale windows are swept and
	// the oldest entries evicted past this point.
	maxRateTrackedKeys = 4096
)

// rateLimits is the effective (resolved) configuration.
type rateLimits struct {
	perMinute    int
	failPerIP    int
	failPerToken int
	window       time.Duration
}

func (c RateLimitConfig) effective() rateLimits {
	out := rateLimits{
		perMinute:    defaultAuthPerMinute,
		failPerIP:    defaultAuthFailuresPerIP,
		failPerToken: defaultAuthFailuresPerToken,
		window:       defaultRateWindowSeconds * time.Second,
	}
	if c.WindowSeconds > 0 {
		out.window = time.Duration(c.WindowSeconds) * time.Second
	} else if c.WindowSeconds < 0 {
		// Negative window disables the failure caps entirely (a zero
		// window would mean "never expires" — a permanent lockout).
		out.failPerIP = 0
		out.failPerToken = 0
	}
	if c.AuthPerMinute != 0 {
		out.perMinute = c.AuthPerMinute
	}
	if c.AuthFailuresPerIP != 0 {
		out.failPerIP = c.AuthFailuresPerIP
	}
	if c.AuthFailuresPerToken != 0 {
		out.failPerToken = c.AuthFailuresPerToken
	}
	if out.perMinute < 0 {
		out.perMinute = 0
	}
	if out.failPerIP < 0 {
		out.failPerIP = 0
	}
	if out.failPerToken < 0 {
		out.failPerToken = 0
	}
	return out
}

type rateCounters struct {
	windowStart time.Time
	hits        int
	failures    int
}

// rateLimiter is a mutex-guarded fixed-window counter store.
type rateLimiter struct {
	mu      sync.Mutex
	counts  map[string]*rateCounters
	audited map[string]time.Time
	now     func() time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		counts:  make(map[string]*rateCounters),
		audited: make(map[string]time.Time),
		now:     time.Now,
	}
}

func (l *rateLimiter) counter(key string, window time.Duration, now time.Time) *rateCounters {
	if l == nil {
		return nil
	}
	c := l.counts[key]
	if c == nil || (window > 0 && now.Sub(c.windowStart) >= window) {
		c = &rateCounters{windowStart: now}
		l.counts[key] = c
		l.evictLocked(now, window)
	}
	return c
}

// hit records one request against key and reports whether it is within
// limit for the current window (limit <= 0 disables the cap). Fixed
// windows allow a 2x burst across a window boundary — accepted for
// brute-force damping; volume control belongs to the edge.
func (l *rateLimiter) hit(key string, limit int, window time.Duration) bool {
	if l == nil || limit <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counter(key, window, now)
	c.hits++
	return c.hits <= limit
}

// recordFailure records one auth failure against key and reports whether
// the failure cap is now reached (limit <= 0 disables the cap).
func (l *rateLimiter) recordFailure(key string, limit int, window time.Duration) bool {
	if l == nil || limit <= 0 {
		return false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counter(key, window, now)
	c.failures++
	return c.failures >= limit
}

// locked reports whether key is currently at or over its failure cap.
func (l *rateLimiter) locked(key string, limit int, window time.Duration) bool {
	if l == nil || limit <= 0 {
		return false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counts[key]
	if c == nil {
		return false
	}
	if window > 0 && now.Sub(c.windowStart) >= window {
		return false
	}
	return c.failures >= limit
}

// evictLocked drops stale windows and, past the key cap, the oldest batch.
// Batch eviction keeps the amortized cost per insert O(1): a full pass runs
// once per eviction batch, never per key (issue #253).
func (l *rateLimiter) evictLocked(now time.Time, window time.Duration) {
	if len(l.counts) <= maxRateTrackedKeys {
		return
	}
	stale := window * 2
	if stale <= 0 {
		stale = defaultRateWindowSeconds * 2 * time.Second
	}
	for key, c := range l.counts {
		if now.Sub(c.windowStart) >= stale {
			delete(l.counts, key)
		}
	}
	over := len(l.counts) - maxRateTrackedKeys
	if over <= 0 {
		return
	}
	// Drop the oldest batch (~10% of the cap) so the next full pass is many
	// inserts away.
	drop := maxRateTrackedKeys / 10
	if drop < over {
		drop = over
	}
	type entry struct {
		key string
		at  time.Time
	}
	all := make([]entry, 0, len(l.counts))
	for key, c := range l.counts {
		all = append(all, entry{key, c.windowStart})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	for i := 0; i < drop && i < len(all); i++ {
		delete(l.counts, all[i].key)
	}
}

// auditOnce reports whether this is the first request for key in the
// current window — used to coalesce 429 audit rows so a flood cannot wash
// the audit trail (issue #253).
func (l *rateLimiter) auditOnce(key string, window time.Duration) bool {
	if l == nil {
		return true
	}
	if window <= 0 {
		window = defaultRateWindowSeconds * time.Second
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.audited[key]
	if !t.IsZero() && now.Sub(t) < window {
		return false
	}
	l.audited[key] = now
	if len(l.audited) > maxRateTrackedKeys {
		for k, v := range l.audited {
			if now.Sub(v) >= window {
				delete(l.audited, k)
			}
		}
	}
	return true
}

// rateLimited writes a 429 with Retry-After and records the damp in the
// audit trail (reason "rate_limited"). At most one audit row per key per
// window — every damp still bumps the metrics counter, but a flood must
// not wash real evidence out of the audit log (issue #253).
func (s *Server) rateLimited(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	ev := metrics.AccessEvent{
		Agent:    agentForRequest(r),
		Surface:  metrics.MetricSurfaceAuth,
		Target:   auditRouteFamily(r),
		Decision: audit.DecisionDeny,
		Reason:   "rate_limited",
		Method:   requestMethod(r),
		Status:   http.StatusTooManyRequests,
	}
	key := "429:" + ipBucket(s.clientIPFor(r))
	if s.rl == nil || s.rl.auditOnce(key, s.rateLimitPolicy().window) {
		s.recordAccessFrom(r, ev)
	} else if s.prom != nil {
		s.prom.ObserveAccess(ev)
	}
	shared.WriteJSONError(w, http.StatusTooManyRequests, "rate limited: too many requests")
}

// rateLimitAuth damps credential-guessing volume on auth and token routes.
func (s *Server) rateLimitAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lim := s.rateLimitPolicy()
		if lim.perMinute > 0 && s.rl != nil && !s.skipPerIPLimits(r) {
			if !s.rl.hit("route:"+ipBucket(s.clientIPFor(r)), lim.perMinute, time.Minute) {
				s.rateLimited(w, r)
				return
			}
		}
		next(w, r)
	}
}

// rateLimitPolicy resolves the configured caps (defaults when unset).
func (s *Server) rateLimitPolicy() rateLimits {
	if s == nil || s.config == nil {
		return RateLimitConfig{}.effective()
	}
	return s.config.RateLimits.effective()
}

// clientIPFor resolves the request's client address under the configured
// trusted-proxy list and forwarded header.
func (s *Server) clientIPFor(r *http.Request) string {
	var trusted []net.IPNet
	header := ClientIPHeaderXFF
	if s != nil && s.config != nil {
		trusted = s.config.trustedNets()
		if h := s.config.clientIPHeader(); h != "" {
			header = h
		}
	}
	return clientIP(r, trusted, header)
}

// skipPerIPLimits reports whether per-IP caps would be meaningless for this
// request (issue #253): a loopback peer with no trusted proxies is a
// same-host edge (tailscale serve / local nginx), so every caller shares one
// bucket — locking that bucket out would take down the whole deployment
// instead of damping one attacker. Per-token caps still apply.
func (s *Server) skipPerIPLimits(r *http.Request) bool {
	if s == nil || s.config == nil {
		return true
	}
	if len(s.config.trustedNets()) > 0 {
		return false
	}
	return isLoopbackHost(remoteHost(r.RemoteAddr))
}

// authLockedOut reports whether this request must be damped before any
// credential work: the client IP or the presented credential has exceeded
// its failure cap. Credential-less requests that resolved a tailnet
// identity are exempt — ambient identity is not a guessable credential, so
// damping it only locks out honest tailnet callers sharing an IP bucket.
// Keyed requests stay damped even with identity, or a locked bucket would
// become a validity oracle.
func (s *Server) authLockedOut(r *http.Request) bool {
	if s == nil || s.rl == nil {
		return false
	}
	if presentedAPIKey(r) == "" {
		if id, _ := s.resolveIdentity(r); id != nil {
			return false
		}
	}
	lim := s.rateLimitPolicy()
	if !s.skipPerIPLimits(r) {
		if s.rl.locked("ip:"+ipBucket(s.clientIPFor(r)), lim.failPerIP, lim.window) {
			return true
		}
	}
	if key := presentedAPIKey(r); key != "" {
		return s.rl.locked(tokenFailKey(key), lim.failPerToken, lim.window)
	}
	return false
}

// recordAuthFailure counts one failed authentication against the client IP
// and, when a credential was presented, its hash. Only presented-credential
// failures are guesswork — anonymous 401s ("tailnet identity required",
// unconfigured probes) must not burn the lockout budget (issue #253).
func (s *Server) recordAuthFailure(r *http.Request) {
	if s == nil || s.rl == nil {
		return
	}
	if presentedAPIKey(r) == "" {
		return
	}
	lim := s.rateLimitPolicy()
	if !s.skipPerIPLimits(r) {
		s.rl.recordFailure("ip:"+ipBucket(s.clientIPFor(r)), lim.failPerIP, lim.window)
	}
	s.rl.recordFailure(tokenFailKey(presentedAPIKey(r)), lim.failPerToken, lim.window)
}

// tokenFailKey is the per-credential failure counter key: the SHA-256 of
// the presented key, never the plaintext.
func tokenFailKey(presented string) string {
	sum := sha256.Sum256([]byte(presented))
	return "tok:" + hex.EncodeToString(sum[:8])
}
