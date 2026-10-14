package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// EntityLink is a user-confirmed cross-service identity. Playlists have no
// catalog IDs, so the user explicitly says "these two playlists are the same"
// instead of relying on name equality (issue #4).
type EntityLink struct {
	ID        string             `json:"id"`
	Kind      string             `json:"kind"` // "playlist"
	Sources   []EntityLinkSource `json:"sources"`
	CreatedAt time.Time          `json:"created_at"`
	Note      string             `json:"note,omitempty"`
}

// EntityLinkSource pins one service-local object into a link group.
type EntityLinkSource struct {
	Service string `json:"service"`
	ID      string `json:"id"`
}

func (s EntityLinkSource) key() string {
	return s.Service + ":" + s.ID
}

// EntityLinkStore persists user-confirmed identity links on disk.
type EntityLinkStore struct {
	mu    sync.RWMutex
	path  string
	Links []EntityLink `json:"links"`
}

type entityLinkFile struct {
	Version int          `json:"version"`
	Links   []EntityLink `json:"links"`
}

const entityLinkFileVersion = 1

// NewEntityLinkStore loads or creates a link file (plain JSON; not secrets).
func NewEntityLinkStore(path string) (*EntityLinkStore, error) {
	st := &EntityLinkStore{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("created empty entity link store", "path", path)
			return st, nil
		}
		return nil, fmt.Errorf("reading entity link file: %w", err)
	}
	var f entityLinkFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing entity link file: %w", err)
	}
	st.Links = f.Links
	return st, nil
}

func (st *EntityLinkStore) saveLocked() error {
	if st.path == "" {
		return nil
	}
	f := entityLinkFile{Version: entityLinkFileVersion, Links: st.Links}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil && !os.IsExist(err) {
		// Dir may be "." — ignore.
		if filepath.Dir(st.path) != "." {
			return err
		}
	}
	return os.WriteFile(st.path, append(data, '\n'), 0o644)
}

// List returns a copy of all links.
func (st *EntityLinkStore) List() []EntityLink {
	if st == nil {
		return nil
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]EntityLink, len(st.Links))
	copy(out, st.Links)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Add creates a link group. Sources must be unique service:id pairs and
// include at least two entries.
func (st *EntityLinkStore) Add(kind string, sources []EntityLinkSource, note string) (EntityLink, error) {
	if st == nil {
		return EntityLink{}, fmt.Errorf("entity links not configured")
	}
	kind = strings.TrimSpace(kind)
	if kind != "playlist" {
		return EntityLink{}, fmt.Errorf("unsupported link kind %q (only playlist)", kind)
	}
	if len(sources) < 2 {
		return EntityLink{}, fmt.Errorf("need at least two sources to link")
	}
	seen := map[string]bool{}
	clean := make([]EntityLinkSource, 0, len(sources))
	for _, src := range sources {
		src.Service = strings.TrimSpace(src.Service)
		src.ID = strings.TrimSpace(src.ID)
		if src.Service == "" || src.ID == "" {
			return EntityLink{}, fmt.Errorf("source service and id are required")
		}
		k := src.key()
		if seen[k] {
			return EntityLink{}, fmt.Errorf("duplicate source %s", k)
		}
		seen[k] = true
		clean = append(clean, src)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	// Reject if any source already belongs to another link.
	for _, existing := range st.Links {
		for _, src := range existing.Sources {
			if seen[src.key()] {
				return EntityLink{}, fmt.Errorf("source %s already linked in %s", src.key(), existing.ID)
			}
		}
	}
	id := "lnk_" + randomHexID()
	link := EntityLink{
		ID:        id,
		Kind:      kind,
		Sources:   clean,
		CreatedAt: time.Now().UTC(),
		Note:      strings.TrimSpace(note),
	}
	st.Links = append(st.Links, link)
	if err := st.saveLocked(); err != nil {
		st.Links = st.Links[:len(st.Links)-1]
		return EntityLink{}, err
	}
	slog.Info("entity link created", "id", id, "kind", kind, "sources", len(clean))
	return link, nil
}

// Delete removes a link by id.
func (st *EntityLinkStore) Delete(id string) bool {
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for i, link := range st.Links {
		if link.ID != id {
			continue
		}
		st.Links = append(st.Links[:i], st.Links[i+1:]...)
		if err := st.saveLocked(); err != nil {
			slog.Error("failed to persist entity link delete", "id", id, "error", err)
		}
		return true
	}
	return false
}

// PlaylistLinkKey returns a stable merge key when any playlist source is in
// an explicit link group; otherwise "".
func (st *EntityLinkStore) PlaylistLinkKey(p types.Playlist) string {
	if st == nil || len(p.Sources) == 0 {
		return ""
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, src := range p.Sources {
		k := src.Service + ":" + src.ID
		for _, link := range st.Links {
			if link.Kind != "playlist" {
				continue
			}
			for _, ls := range link.Sources {
				if ls.key() == k {
					return "link:" + link.ID
				}
			}
		}
	}
	return ""
}

func randomHexID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
