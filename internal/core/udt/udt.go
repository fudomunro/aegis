package udt

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Registry holds loaded UDT schemas (issue #15 Phase 6 PoC).
// Schemas are data files under a user-types directory; this package maps
// them onto the type layer and the existing library aggregate pipeline.
type Registry struct {
	mu        sync.RWMutex
	byID      map[string]kinds.UserDefinedTypeSchema
	byPath    map[string]string // path → kind id
	schemaDir map[string]string // kind id → directory of its schema.json
	baseDir   string            // user-types root (discovery only)
	// overlayPath is the admin-edited user_types.json (local file selection).
	overlayPath string
	// overlay is the cached overlay so library GETs do not re-read disk on
	// every request (issue #15 review). Invalidated on admin save.
	overlay   *UserTypesOverlay
	overlayMu sync.Mutex // serializes admin read-modify-write of user_types.json
}

func newRegistry() *Registry {
	return &Registry{
		byID:      map[string]kinds.UserDefinedTypeSchema{},
		byPath:    map[string]string{},
		schemaDir: map[string]string{},
	}
}

// OverlayPath returns the admin user_types.json path ("" when disabled).
func (reg *Registry) OverlayPath() string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.overlayPath
}

// BaseDir returns the user-types root directory ("" when disabled).
func (reg *Registry) BaseDir() string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.baseDir
}

// LockOverlayWrite / UnlockOverlayWrite expose the lock that serializes
// admin read-modify-write of user_types.json. The hub's admin handlers hold
// it across load → mutate → save → cache.
func (reg *Registry) LockOverlayWrite()   { reg.overlayMu.Lock() }
func (reg *Registry) UnlockOverlayWrite() { reg.overlayMu.Unlock() }

// SetOverlay caches a just-saved overlay (and clears it when nil).
func (reg *Registry) SetOverlay(ov *UserTypesOverlay) {
	if reg == nil {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.overlay = ov
}

// Overlay returns the cached overlay, loading from disk on first use.
func (reg *Registry) Overlay() *UserTypesOverlay {
	if reg == nil {
		return &UserTypesOverlay{Kinds: map[string]UDTKindOverlay{}}
	}
	reg.mu.RLock()
	cached := reg.overlay
	reg.mu.RUnlock()
	if cached != nil {
		return cached
	}
	ov, err := LoadOverlay(reg.OverlayPath())
	if err != nil {
		return &UserTypesOverlay{Kinds: map[string]UDTKindOverlay{}}
	}
	reg.SetOverlay(ov)
	return ov
}

// SchemaWithOverlay returns the kind schema with admin-selected local files
// applied (issue #15). Falls back to the bare schema when the overlay is
// missing or unreadable. Safe on a nil registry.
func (reg *Registry) SchemaWithOverlay(id string) kinds.UserDefinedTypeSchema {
	if reg == nil {
		return kinds.UserDefinedTypeSchema{}
	}
	schema, _ := reg.Get(id)
	ov := reg.Overlay()
	return applyUserTypesOverlay(schema, ov.Kinds[id])
}

// DirFor returns the pack directory that holds a kind's schema and relative
// sample files.
func (reg *Registry) DirFor(id string) string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.schemaDir[id]
}

// LoadUserDefinedTypes reads user-defined schema packs from dir.
// overlayPath (optional) is the admin user_types.json that selects local files.
//
// Layout (pack model used by examples/user-types/):
//
//	dir/reading-list/schema.json
//	dir/reading-list/sample/books.json   # relative file_json paths
//	dir/standalone.json                  # also accepted at the top level
//
// Invalid schemas are skipped with a log line — fail-open for the server
// (issue #15: a bad UDT file must not take down built-in kinds).
func LoadUserDefinedTypes(dir, overlayPath string, logger *slog.Logger) (*Registry, []kinds.UserDefinedTypeSchema) {
	reg := newRegistry()
	reg.baseDir = dir
	reg.overlayPath = overlayPath
	if dir == "" {
		return reg, nil
	}
	var loaded []kinds.UserDefinedTypeSchema
	for _, path := range discoverSchemaFiles(dir, logger) {
		raw, err := os.ReadFile(path)
		if err != nil {
			if logger != nil {
				logger.Warn("user-defined types: read failed", "path", path, "error", err)
			}
			continue
		}
		var schema kinds.UserDefinedTypeSchema
		if err := json.Unmarshal(raw, &schema); err != nil {
			if logger != nil {
				logger.Warn("user-defined types: parse failed", "path", path, "error", err)
			}
			continue
		}
		if err := schema.Normalize(); err != nil {
			if logger != nil {
				logger.Warn("user-defined types: invalid schema", "path", path, "error", err)
			}
			continue
		}
		// DefaultAccess is admin-display metadata only (packs cannot grant
		// agent access); canonicalize the level token here at the policy
		// boundary (issue #15 review).
		schema.DefaultAccess = policy.NormalizeAccess(schema.DefaultAccess)
		if schema.DefaultAccess == "" {
			schema.DefaultAccess = policy.AccessNone
		}
		if _, exists := reg.byID[schema.ID]; exists {
			if logger != nil {
				logger.Warn("user-defined types: duplicate id", "path", path, "id", schema.ID)
			}
			continue
		}
		if kinds.BuiltinKindByID(schema.ID) != nil {
			if logger != nil {
				logger.Warn("user-defined types: id collides with built-in kind", "path", path, "id", schema.ID)
			}
			continue
		}
		// Relative file_json paths resolve against the schema's own directory
		// (the pack folder), not the shared user-types root.
		schemaDir := filepath.Dir(path)
		reg.byID[schema.ID] = schema
		reg.schemaDir[schema.ID] = schemaDir
		reg.byPath[schema.Path] = schema.ID
		// UDTs are opt-in until data grant / permissions name them (places).
		shared.MarkKindDefaultNone(schema.PermType)
		loaded = append(loaded, schema)
		if logger != nil {
			logger.Info("user-defined type loaded",
				"id", schema.ID, "path", schema.Path, "sources", len(schema.Sources), "dir", schemaDir)
		}
	}
	return reg, loaded
}

// discoverSchemaFiles finds schema.json in each pack directory and *.json at
// the top level (non-recursive beyond one pack level).
func discoverSchemaFiles(dir string, logger *slog.Logger) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) && logger != nil {
			logger.Warn("user-defined types: read dir failed", "dir", dir, "error", err)
		}
		return nil
	}
	var paths []string
	for _, ent := range entries {
		if ent.IsDir() {
			pack := filepath.Join(dir, ent.Name(), "schema.json")
			if st, err := os.Stat(pack); err == nil && !st.IsDir() {
				paths = append(paths, pack)
			}
			continue
		}
		if strings.HasSuffix(ent.Name(), ".json") {
			paths = append(paths, filepath.Join(dir, ent.Name()))
		}
	}
	return paths
}

// Get returns a schema by kind id.
func (reg *Registry) Get(id string) (kinds.UserDefinedTypeSchema, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	s, ok := reg.byID[id]
	return s, ok
}

// SchemaIDForPath returns the kind id registered for an agent path.
func (reg *Registry) SchemaIDForPath(path string) (string, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	id, ok := reg.byPath[path]
	return id, ok
}

// All returns every loaded schema (stable registration order is not
// guaranteed across map iteration; callers sort if needed).
func (reg *Registry) All() []kinds.UserDefinedTypeSchema {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]kinds.UserDefinedTypeSchema, 0, len(reg.byID))
	for _, s := range reg.byID {
		out = append(out, s)
	}
	return out
}
