package links

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

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Link is a user-confirmed cross-service identity. Playlists have no
// catalog IDs, so the user explicitly says "these two playlists are the same"
// instead of relying on name equality (issue #4).
type Link struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // "playlist"
	Sources   []Source  `json:"sources"`
	CreatedAt time.Time `json:"created_at"`
	Note      string    `json:"note,omitempty"`
}

// Source pins one service-local object into a link group.
type Source struct {
	Service string `json:"service"`
	ID      string `json:"id"`
}

func (s Source) key() string {
	return s.Service + ":" + s.ID
}

// Store persists user-confirmed identity links on disk.
type Store struct {
	mu    sync.RWMutex
	path  string
	Links []Link `json:"links"`
}

type file struct {
	Version int    `json:"version"`
	Links   []Link `json:"links"`
}

const entityLinkFileVersion = 1

// NewStore loads or creates a link file (plain JSON; not secrets).
func NewStore(path string) (*Store, error) {
	st := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("created empty entity link store", "path", path)
			return st, nil
		}
		return nil, fmt.Errorf("reading entity link file: %w", err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing entity link file: %w", err)
	}
	st.Links = f.Links
	return st, nil
}

func (st *Store) saveLocked() error {
	if st.path == "" {
		return nil
	}
	f := file{Version: entityLinkFileVersion, Links: st.Links}
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
	// Atomic: a crash mid-write must never leave a truncated link file,
	// which would silently drop the user's confirmed identities.
	return shared.AtomicWriteFile(st.path, append(data, '\n'), 0o644)
}

// List returns a copy of all links.
func (st *Store) List() []Link {
	if st == nil {
		return nil
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]Link, len(st.Links))
	copy(out, st.Links)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Add creates a link group. Sources must be unique service:id pairs and
// include at least two entries.
func (st *Store) Add(kind string, sources []Source, note string) (Link, error) {
	if st == nil {
		return Link{}, fmt.Errorf("entity links not configured")
	}
	kind = strings.TrimSpace(kind)
	if kind != "playlist" {
		return Link{}, fmt.Errorf("unsupported link kind %q (only playlist)", kind)
	}
	if len(sources) < 2 {
		return Link{}, fmt.Errorf("need at least two sources to link")
	}
	seen := map[string]bool{}
	clean := make([]Source, 0, len(sources))
	for _, src := range sources {
		src.Service = strings.TrimSpace(src.Service)
		src.ID = strings.TrimSpace(src.ID)
		if src.Service == "" || src.ID == "" {
			return Link{}, fmt.Errorf("source service and id are required")
		}
		k := src.key()
		if seen[k] {
			return Link{}, fmt.Errorf("duplicate source %s", k)
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
				return Link{}, fmt.Errorf("source %s already linked in %s", src.key(), existing.ID)
			}
		}
	}
	id := "lnk_" + randomHexID()
	link := Link{
		ID:        id,
		Kind:      kind,
		Sources:   clean,
		CreatedAt: time.Now().UTC(),
		Note:      strings.TrimSpace(note),
	}
	st.Links = append(st.Links, link)
	if err := st.saveLocked(); err != nil {
		st.Links = st.Links[:len(st.Links)-1]
		return Link{}, err
	}
	slog.Info("entity link created", "id", id, "kind", kind, "sources", len(clean))
	return link, nil
}

// Delete removes a link by id.
func (st *Store) Delete(id string) bool {
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
func (st *Store) PlaylistLinkKey(p kinds.Playlist) string {
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
