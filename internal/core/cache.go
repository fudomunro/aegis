package core

import (
	"sort"
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
