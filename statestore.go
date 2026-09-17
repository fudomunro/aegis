package main

import (
	"sync"
	"time"
)

// StateStore is a simple in-memory TTL store for OAuth CSRF state tokens.
type StateStore struct {
	mu    sync.Mutex
	items map[string]stateEntry
}

type stateEntry struct {
	expires time.Time
}

func NewStateStore() *StateStore {
	return &StateStore{
		items: make(map[string]stateEntry),
	}
}

// Set stores a state key with a TTL. Expired entries are purged opportunistically
// so abandoned logins do not accumulate until restart.
func (s *StateStore) Set(key string, _ bool, ttl time.Duration) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.items {
		if now.After(e.expires) {
			delete(s.items, k)
		}
	}
	s.items[key] = stateEntry{expires: now.Add(ttl)}
}

// Delete removes and returns true if the key existed and was valid.
func (s *StateStore) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[key]
	if !ok {
		return false
	}
	delete(s.items, key)
	return time.Now().Before(entry.expires)
}

// Len returns the number of tracked states (including not-yet-purged expired ones).
func (s *StateStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}
