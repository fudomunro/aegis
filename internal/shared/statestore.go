package shared

import (
	"sync"
	"time"
)

// StateStore is a simple in-memory TTL store for OAuth CSRF state tokens.
// Each entry carries a small string value: the PKCE code verifier bound to
// that state, when the connection uses PKCE (issue #145).
type StateStore struct {
	mu    sync.Mutex
	items map[string]stateEntry
}

type stateEntry struct {
	value   string
	expires time.Time
}

func NewStateStore() *StateStore {
	return &StateStore{
		items: make(map[string]stateEntry),
	}
}

// Set stores a state key with a value and TTL. Expired entries are purged
// opportunistically so abandoned logins do not accumulate until restart.
func (s *StateStore) Set(key, value string, ttl time.Duration) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.items {
		if now.After(e.expires) {
			delete(s.items, k)
		}
	}
	s.items[key] = stateEntry{value: value, expires: now.Add(ttl)}
}

// Delete removes the key and reports whether it existed and was valid,
// returning the value stored at Set time.
func (s *StateStore) Delete(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[key]
	if !ok {
		return "", false
	}
	delete(s.items, key)
	if !time.Now().Before(entry.expires) {
		return "", false
	}
	return entry.value, true
}

// Len returns the number of tracked states (including not-yet-purged expired ones).
func (s *StateStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}
