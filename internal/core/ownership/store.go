package ownership

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/shared"
)

// Data-ownership state (issue #165): per-kind user confirmation that Aegis
// already holds all the data the user cares about, plus the derived
// evidence behind it — which sources are in use, which declared routes are
// still unused, and the copy-freshness rollup. Until a kind is confirmed,
// the admin surface keeps a call to action to manage more data of that kind.
//
// Confirmation is the only user-authored state (ownership.json). Sources and
// routes are derived from the connection-maturity catalog, ingest-source
// declarations, configured services, and local store counts — the same
// declared metadata the sync dashboard reads (issue #254).

// OwnershipEntry is the user's confirmation for one library kind — the
// store's record type (kept with the store, not in `internal/types`).
type OwnershipEntry struct {
	// Confirmed is the user's statement that Aegis holds all the data of
	// this kind they care about. False (or absent) keeps the call to action.
	Confirmed bool `json:"confirmed"`
	// ConfirmedAt is when the user confirmed (RFC3339). Empty when not
	// confirmed or when the entry was reopened.
	ConfirmedAt string `json:"confirmed_at,omitempty"`
	// Note is an optional user note ("work email only", …).
	Note string `json:"note,omitempty"`
}

// fileVersion is the on-disk schema version for ownership.json.
const fileVersion = 1

// file is the on-disk format.
type file struct {
	Version   int                       `json:"version"`
	Ownership map[string]OwnershipEntry `json:"ownership"`
}

// Store is a mutex-guarded map of kind id → user confirmation.
type Store struct {
	mu     sync.RWMutex
	path   string
	byKind map[string]OwnershipEntry
}

// NewStore loads ownership.json. A missing file yields an empty
// store — no kind is confirmed until the user says so. An empty path
// disables persistence (entries stay in memory for the process lifetime).
func NewStore(path string) (*Store, error) {
	s := &Store{
		path:   path,
		byKind: make(map[string]OwnershipEntry),
	}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var file file
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse ownership.json: %w", err)
	}
	if file.Version != 0 && file.Version != fileVersion {
		return nil, fmt.Errorf("unsupported ownership.json version %d", file.Version)
	}
	for kind, entry := range file.Ownership {
		if kind == "" {
			return nil, fmt.Errorf("ownership: entry missing kind id")
		}
		s.byKind[kind] = entry
	}
	return s, nil
}

// Get returns the confirmation entry for a kind. The zero value means
// unconfirmed.
func (s *Store) Get(kind string) OwnershipEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byKind[kind]
}

// List returns a copy of all entries keyed by kind id.
func (s *Store) List() map[string]OwnershipEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]OwnershipEntry, len(s.byKind))
	for k, v := range s.byKind {
		out[k] = v
	}
	return out
}

// Confirm records the user's statement that the kind is complete. A
// non-empty note is stored beside it; confirmed_at is stamped now.
func (s *Store) Confirm(kind, note string) (OwnershipEntry, error) {
	if strings.TrimSpace(kind) == "" {
		return OwnershipEntry{}, fmt.Errorf("kind is required")
	}
	entry := OwnershipEntry{
		Confirmed:   true,
		ConfirmedAt: time.Now().UTC().Format(time.RFC3339),
		Note:        strings.TrimSpace(note),
	}
	s.mu.Lock()
	prev, existed := s.byKind[kind]
	s.byKind[kind] = entry
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		// Roll back so memory and disk do not disagree.
		s.mu.Lock()
		if existed {
			s.byKind[kind] = prev
		} else {
			delete(s.byKind, kind)
		}
		s.mu.Unlock()
		return OwnershipEntry{}, err
	}
	return entry, nil
}

// Reopen clears a confirmation, returning the kind to the call-to-action
// state. The note is kept so the user's context survives a reopen.
func (s *Store) Reopen(kind string) (OwnershipEntry, error) {
	if strings.TrimSpace(kind) == "" {
		return OwnershipEntry{}, fmt.Errorf("kind is required")
	}
	s.mu.Lock()
	prev, existed := s.byKind[kind]
	entry := OwnershipEntry{Note: prev.Note}
	s.byKind[kind] = entry
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		if existed {
			s.byKind[kind] = prev
		} else {
			delete(s.byKind, kind)
		}
		s.mu.Unlock()
		return OwnershipEntry{}, err
	}
	return entry, nil
}

// persist writes ownership.json atomically. No-op when persistence is
// disabled (empty path).
func (s *Store) persist() error {
	if s.path == "" {
		return nil
	}
	s.mu.RLock()
	file := file{
		Version:   fileVersion,
		Ownership: make(map[string]OwnershipEntry, len(s.byKind)),
	}
	for k, v := range s.byKind {
		file.Ownership[k] = v
	}
	s.mu.RUnlock()
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return shared.AtomicWriteFile(s.path, append(raw, '\n'), 0o600)
}
