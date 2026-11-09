package metrics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAccessMetrics_AgentDetailBreakdown(t *testing.T) {
	m := NewAccessMetrics(16)
	now := time.Now()

	m.Record(AccessEvent{At: now.Add(-time.Minute), Agent: "alice@example.com", Surface: MetricSurfaceService, Target: "spotify", Kind: "tracks"})
	m.Record(AccessEvent{At: now.Add(-time.Minute), Agent: "alice@example.com", Surface: MetricSurfaceService, Target: "spotify", Kind: "tracks"})
	m.Record(AccessEvent{At: now.Add(-time.Minute), Agent: "alice@example.com", Surface: MetricSurfaceLibrary, Target: "playlists", Kind: "playlists"})
	m.Record(AccessEvent{At: now.Add(-time.Minute), Agent: "bob@example.com", Surface: MetricSurfaceService, Target: "youtube-music", Kind: "tracks"})

	snap := m.Snapshot(time.Hour)
	if snap.Agents.Distinct != 2 {
		t.Fatalf("distinct = %d, want 2", snap.Agents.Distinct)
	}
	if len(snap.Agents.Detail) != 2 {
		t.Fatalf("detail = %d agents, want 2", len(snap.Agents.Detail))
	}

	// Busiest first: alice has 3 hits, bob has 1.
	alice := snap.Agents.Detail[0]
	if alice.ID != "alice@example.com" {
		t.Fatalf("detail[0].ID = %q, want alice@example.com", alice.ID)
	}
	if alice.Hits != 3 {
		t.Fatalf("alice hits = %d, want 3", alice.Hits)
	}
	if alice.Services["spotify"] != 2 {
		t.Fatalf("alice spotify = %d, want 2", alice.Services["spotify"])
	}
	if alice.Libraries["playlists"] != 1 {
		t.Fatalf("alice playlists = %d, want 1", alice.Libraries["playlists"])
	}
	if alice.Kinds["tracks"] != 2 || alice.Kinds["playlists"] != 1 {
		t.Fatalf("alice kinds = %v, want tracks=2 playlists=1", alice.Kinds)
	}
	if alice.LastAt.IsZero() {
		t.Fatal("expected alice last_at")
	}

	bob := snap.Agents.Detail[1]
	if bob.ID != "bob@example.com" || bob.Hits != 1 {
		t.Fatalf("detail[1] = %+v, want bob 1 hit", bob)
	}
	if bob.Services["youtube-music"] != 1 {
		t.Fatalf("bob youtube-music = %d, want 1", bob.Services["youtube-music"])
	}
}

func TestAccessMetrics_AgentDetailLimit(t *testing.T) {
	m := NewAccessMetrics(64)
	now := time.Now()
	for i := 0; i < 12; i++ {
		m.Record(AccessEvent{
			At:      now,
			Agent:   "agent-" + string(rune('a'+i)),
			Surface: MetricSurfaceService,
			Target:  "spotify",
		})
	}
	snap := m.Snapshot(time.Hour)
	if snap.Agents.Distinct != 12 {
		t.Fatalf("distinct = %d, want 12", snap.Agents.Distinct)
	}
	if len(snap.Agents.Detail) != metricsAgentDetailLimit {
		t.Fatalf("detail = %d, want cap %d", len(snap.Agents.Detail), metricsAgentDetailLimit)
	}
}

func TestAccessMetrics_RecordAndSnapshotWindows(t *testing.T) {
	m := NewAccessMetrics(16)
	now := time.Now()

	m.Record(AccessEvent{At: now.Add(-2 * time.Minute), Agent: "alice@example.com", Surface: MetricSurfaceService, Target: "spotify", Kind: "tracks", Cache: MetricCacheMiss})
	m.Record(AccessEvent{At: now.Add(-10 * time.Minute), Agent: "bob@example.com", Surface: MetricSurfaceService, Target: "spotify", Kind: "playlists", Cache: MetricCacheHit})
	m.Record(AccessEvent{At: now.Add(-30 * time.Minute), Agent: "alice@example.com", Surface: MetricSurfaceLibrary, Target: "tracks", Kind: "tracks", Cache: MetricCacheMiss})
	m.Record(AccessEvent{At: now.Add(-2 * time.Hour), Agent: "carol@example.com", Surface: MetricSurfaceService, Target: "youtube-music", Kind: "tracks"})

	five := m.Snapshot(5 * time.Minute)
	if five.Agents.Distinct != 1 {
		t.Fatalf("5m agents = %d, want 1", five.Agents.Distinct)
	}
	if five.Services["spotify"].Hits != 1 {
		t.Fatalf("5m spotify hits = %d, want 1", five.Services["spotify"].Hits)
	}

	oneHour := m.Snapshot(time.Hour)
	if oneHour.Agents.Distinct != 2 {
		t.Fatalf("1h agents = %d, want 2", oneHour.Agents.Distinct)
	}
	if oneHour.Services["spotify"].Hits != 2 {
		t.Fatalf("1h spotify hits = %d, want 2", oneHour.Services["spotify"].Hits)
	}
	if oneHour.Services["spotify"].CacheHits != 1 || oneHour.Services["spotify"].CacheMiss != 1 {
		t.Fatalf("1h spotify cache = hits %d misses %d, want 1/1",
			oneHour.Services["spotify"].CacheHits, oneHour.Services["spotify"].CacheMiss)
	}
	if oneHour.Libraries["tracks"].Hits != 1 {
		t.Fatalf("1h tracks library hits = %d, want 1", oneHour.Libraries["tracks"].Hits)
	}
	if oneHour.Services["spotify"].Kinds["tracks"] != 1 {
		t.Fatalf("1h spotify tracks type count = %d, want 1", oneHour.Services["spotify"].Kinds["tracks"])
	}

	day := m.Snapshot(24 * time.Hour)
	if day.Services["youtube-music"].Hits != 1 {
		t.Fatalf("24h youtube hits = %d, want 1", day.Services["youtube-music"].Hits)
	}
	if day.Agents.Distinct != 3 {
		t.Fatalf("24h agents = %d, want 3", day.Agents.Distinct)
	}
}

func TestAccessMetrics_RingEviction(t *testing.T) {
	m := NewAccessMetrics(3)
	now := time.Now()
	for i := 0; i < 5; i++ {
		m.Record(AccessEvent{
			At:      now,
			Agent:   "alice@example.com",
			Surface: MetricSurfaceService,
			Target:  "svc",
		})
	}
	snap := m.Snapshot(time.Hour)
	if snap.Services["svc"].Hits != 3 {
		t.Fatalf("hits after eviction = %d, want 3", snap.Services["svc"].Hits)
	}
}

func TestAccessMetrics_NilSafe(t *testing.T) {
	var m *AccessMetrics
	m.Record(AccessEvent{Agent: "x"})
	snap := m.Snapshot(time.Hour)
	if snap.TotalHits != 0 || snap.Services == nil {
		t.Fatalf("nil metrics snapshot = %+v", snap)
	}
}

func TestAgentForRequest_APIKeyFallback(t *testing.T) {
	m := NewAccessMetrics(8)
	m.Record(AccessEvent{
		Agent:   "api-key",
		Surface: MetricSurfaceService,
		Target:  "spotify",
		Kind:    "tracks",
		Cache:   MetricCacheHit,
	})
	raw, err := json.Marshal(m.Snapshot(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, banned := range []string{"Bearer", "secret", "access_token", "Authorization", "/me/tracks?"} {
		if strings.Contains(s, banned) {
			t.Fatalf("stats leaked %q: %s", banned, s)
		}
	}
}
