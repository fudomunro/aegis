package core

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Cache size caps keep a long-lived single-instance process from growing without
// bound. Values larger than cacheMaxBytes are not stored.
const (
	cacheMaxEntries = 256
	cacheMaxBytes   = 32 << 20 // 32 MiB of live payload
)

type Cache struct {
	mu    sync.RWMutex
	items map[string]cacheEntry
	bytes int // sum of len(value) over stored entries
}

type cacheEntry struct {
	value   string
	expires time.Time
}

func NewCache() *Cache {
	return &Cache{
		items: make(map[string]cacheEntry),
	}
}

func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.items[key]
	if !ok || time.Now().After(entry.expires) {
		return "", false
	}
	return entry.value, true
}

func (c *Cache) Set(key, value string, ttl time.Duration) {
	if key == "" || len(value) > cacheMaxBytes {
		return
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.purgeExpiredLocked(now)

	if old, ok := c.items[key]; ok {
		c.bytes -= len(old.value)
		delete(c.items, key)
	}

	for len(c.items) >= cacheMaxEntries {
		if !c.evictSoonestLocked() {
			break
		}
	}
	for c.bytes+len(value) > cacheMaxBytes && len(c.items) > 0 {
		if !c.evictSoonestLocked() {
			break
		}
	}

	// Non-positive TTL stores an already-expired entry (admin snapshot / tests);
	// Get still misses and the next Set purges it.
	c.items[key] = cacheEntry{
		value:   value,
		expires: now.Add(ttl),
	}
	c.bytes += len(value)
}

// CacheItem is a snapshot of one cache entry for admin views.
type CacheItem struct {
	Key       string    `json:"key"`
	Size      int       `json:"size"`
	ExpiresAt time.Time `json:"expires_at"`
	Expired   bool      `json:"expired"`
	Preview   string    `json:"preview,omitempty"`
}

// Snapshot returns all cache entries, including expired ones, sorted by key.
func (c *Cache) Snapshot() []CacheItem {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	items := make([]CacheItem, 0, len(c.items))
	for key, entry := range c.items {
		preview := entry.value
		if len(preview) > 120 {
			preview = preview[:120]
		}
		items = append(items, CacheItem{
			Key:       key,
			Size:      len(entry.value),
			ExpiresAt: entry.expires,
			Expired:   now.After(entry.expires),
			Preview:   preview,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}

// Clear removes every cache entry.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]cacheEntry)
	c.bytes = 0
}

// ClearPrefix removes every entry whose key starts with prefix. Targeted
// invalidation (issue #273) uses it so a scoped change does not wipe
// unrelated caches (e.g. MusicBrainz MBIDs survive library invalidation).
func (c *Cache) ClearPrefix(prefix string) {
	if prefix == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.items {
		if strings.HasPrefix(key, prefix) {
			c.bytes -= len(entry.value)
			if c.bytes < 0 {
				c.bytes = 0
			}
			delete(c.items, key)
		}
	}
}

// invalidateLibrary drops cached library aggregates and search views.
// MusicBrainz MBID lookups live in s.mbCache and survive (issue #273).
func (s *Server) invalidateLibrary() {
	s.cache.ClearPrefix("library:")
}

// invalidateLibraryKind drops cached aggregates for one library kind (its
// list and search views). Cache keys for a kind all start with
// "library:{id}" followed by "|", so the prefix cannot bleed into a longer
// kind id; the freebusy view ("library:freebusy") is only dropped by
// invalidateLibrary.
func (s *Server) invalidateLibraryKind(kindID string) {
	s.cache.ClearPrefix("library:" + kindID)
}

// invalidateService drops library aggregates plus one service's content
// transport entries. Used when a connection, token, or ingest for that
// service changes what cached responses would say.
func (s *Server) invalidateService(serviceID string) {
	s.invalidateLibrary()
	if serviceID != "" {
		s.cache.ClearPrefix(serviceID + ":")
	}
}

// invalidateCaches drops every transport and library entry. Used where the
// change (permission edits, app/scope visibility) is not scoped to one kind
// or service. MBID lookups survive: they are resolution metadata, not data.
func (s *Server) invalidateCaches() {
	s.cache.Clear()
}

// invalidateAllCaches drops every cache including MBID lookups. The admin
// cache-clear endpoint is an explicit flush-all.
func (s *Server) invalidateAllCaches() {
	s.invalidateCaches()
	s.mbCache.Clear()
}

// cacheStats sums the response cache and the MBID cache for admin views.
func (s *Server) cacheStats() (live, total, bytes int) {
	l1, t1, b1 := s.cache.Stats()
	l2, t2, b2 := s.mbCache.Stats()
	return l1 + l2, t1 + t2, b1 + b2
}

// cacheSnapshot lists both caches' entries (MBID keys keep their "mbid:"
// prefix) so the admin cache view stays complete after the split (issue #273).
func (s *Server) cacheSnapshot() []CacheItem {
	items := append(s.cache.Snapshot(), s.mbCache.Snapshot()...)
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}

// Stats counts live and total entries plus total live payload bytes.
func (c *Cache) Stats() (live, total, bytes int) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	for _, entry := range c.items {
		total++
		if now.After(entry.expires) {
			continue
		}
		live++
		bytes += len(entry.value)
	}
	return live, total, bytes
}

func (c *Cache) purgeExpiredLocked(now time.Time) {
	for key, entry := range c.items {
		if now.After(entry.expires) {
			c.bytes -= len(entry.value)
			if c.bytes < 0 {
				c.bytes = 0
			}
			delete(c.items, key)
		}
	}
}

func (c *Cache) evictSoonestLocked() bool {
	var victim string
	var victimExpiry time.Time
	for key, entry := range c.items {
		if victim == "" || entry.expires.Before(victimExpiry) {
			victim = key
			victimExpiry = entry.expires
		}
	}
	if victim == "" {
		return false
	}
	entry := c.items[victim]
	c.bytes -= len(entry.value)
	if c.bytes < 0 {
		c.bytes = 0
	}
	delete(c.items, victim)
	return true
}
