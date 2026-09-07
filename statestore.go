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

// Set stores a state key with a TTL.
func (s *StateStore) Set(key string, _ bool, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = stateEntry{expires: time.Now().Add(ttl)}
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
