package shared

import (
	"strconv"
	"testing"
	"time"
)

// Response-cache benchmarks (issue #75 measurement plan). The set-churn
// benchmark is the one that matters for the eviction concern: the cache is
// capped at cacheMaxEntries and every Set runs a full expiry scan plus a
// linear victim walk under the write lock. Run with:
//
//	go test -bench 'BenchmarkCache' -benchtime=2s ./internal/shared/

const benchCacheValue = "cache benchmark value payload"

// BenchmarkCacheGet measures lookups on a cache holding as many live
// entries as the cap allows.
func BenchmarkCacheGet(b *testing.B) {
	c := NewCache()
	keys := make([]string, cacheMaxEntries)
	for i := range keys {
		keys[i] = "bench-get-" + strconv.Itoa(i)
		c.Set(keys[i], benchCacheValue, time.Minute)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := c.Get(keys[i%len(keys)]); !ok {
			b.Fatal("expected cache hit")
		}
	}
}

// BenchmarkCacheSetChurn inserts unique keys against a full cache, so
// every Set has to evict. Per-op cost here should stay flat as the entry
// count grows — if it climbs toward O(n) per Set, eviction is scanning.
func BenchmarkCacheSetChurn(b *testing.B) {
	c := NewCache()
	for i := 0; i < cacheMaxEntries; i++ {
		c.Set("bench-fill-"+strconv.Itoa(i), benchCacheValue, time.Minute)
	}
	b.ReportAllocs()
	b.ResetTimer()
	var key []byte
	for i := 0; i < b.N; i++ {
		key = strconv.AppendInt(key[:0], int64(i), 10)
		c.Set("bench-churn-"+string(key), benchCacheValue, time.Minute)
	}
}

// BenchmarkCachePurgeExpired measures the expiry sweep Set runs on every
// insert: a full walk of the entry map under the write lock. The map is
// filled directly (same package) so the unmeasured setup does not itself
// pay for a purge per entry.
func BenchmarkCachePurgeExpired(b *testing.B) {
	c := NewCache()
	expired := time.Now().Add(-time.Hour)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c.mu.Lock()
		for j := 0; j < cacheMaxEntries; j++ {
			c.items["bench-purge-"+strconv.Itoa(j)] = cacheEntry{value: benchCacheValue, expires: expired}
		}
		c.bytes = cacheMaxEntries * len(benchCacheValue)
		c.mu.Unlock()
		b.StartTimer()

		c.mu.Lock()
		c.purgeExpiredLocked(time.Now())
		c.mu.Unlock()
	}
}
