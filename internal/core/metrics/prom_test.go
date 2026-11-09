package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/audit"
)

func TestObserveUpstream_Histogram(t *testing.T) {
	p := NewPromMetrics("", "")
	p.ObserveUpstream("spotify", 3*time.Millisecond, 200)
	p.ObserveUpstream("spotify", 300*time.Millisecond, 200)
	p.ObserveUpstream("spotify", 0, 0) // error path
	p.ObserveUpstream("", 10*time.Millisecond, 200)
	var nilP *PromMetrics
	nilP.ObserveUpstream("x", time.Millisecond, 200)

	text := p.PromText()
	if !strings.Contains(text, `aegis_upstream_duration_seconds_count{service="spotify"} 3`) {
		t.Fatalf("missing spotify histogram count:\n%s", text)
	}
	if !strings.Contains(text, `aegis_upstream_duration_seconds_bucket{service="spotify",le="+Inf"} 3`) {
		t.Fatalf("missing +Inf bucket:\n%s", text)
	}
	if !strings.Contains(text, `aegis_upstream_duration_seconds_count{service="unknown"} 1`) {
		t.Fatalf("missing unknown service histogram:\n%s", text)
	}
}

func TestPromText_CacheAndAccessSeries(t *testing.T) {
	p := NewPromMetrics("", "")
	p.ObserveAccess(AccessEvent{Surface: "library", Target: "tracks", Cache: MetricCacheHit})
	p.ObserveAccess(AccessEvent{Surface: "library", Target: "tracks", Cache: MetricCacheMiss})
	p.ObserveAccess(AccessEvent{Surface: MetricSurfaceService, Target: "spotify", Decision: audit.DecisionDeny, Reason: "permission"})
	p.ObserveAccess(AccessEvent{}) // defaults

	text := p.PromText()
	for _, want := range []string{
		`aegis_access_total{surface="library",target="tracks",decision="allow"} 2`,
		`aegis_cache_events_total{result="hit"} 1`,
		`aegis_cache_events_total{result="miss"} 1`,
		`aegis_permission_denials_total{surface="service",target="spotify"} 1`,
		`aegis_access_total{surface="unknown",target="unknown",decision="allow"} 1`,
		`aegis_process_start_time_seconds`,
		`aegis_process_uptime_seconds`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
	if NewPromMetrics("", "").PromText() == "" {
		t.Fatal("empty registry should still emit process series")
	}
	var nilP *PromMetrics
	if nilP.PromText() != "" {
		t.Fatal("nil registry should emit empty text")
	}
}
