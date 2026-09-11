package main

import (
	"sort"
	"sync"
	"time"
)

type Cache struct {
	mu    sync.RWMutex
	items map[string]cacheEntry
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheEntry{
		value:   value,
		expires: time.Now().Add(ttl),
	}
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
