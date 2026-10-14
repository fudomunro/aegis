package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/types"
)

// userDefinedRegistry holds loaded UDT schemas (issue #15 Phase 6 PoC).
// Schemas are data files under a user-types directory; this package maps
// them onto the type layer and the existing library aggregate pipeline.
type userDefinedRegistry struct {
	mu        sync.RWMutex
	byID      map[string]types.UserDefinedTypeSchema
	byPath    map[string]string // path → kind id
	schemaDir map[string]string // kind id → directory of its schema.json
	baseDir   string            // user-types root (discovery only)
}

func newUserDefinedRegistry() *userDefinedRegistry {
	return &userDefinedRegistry{
		byID:      map[string]types.UserDefinedTypeSchema{},
		byPath:    map[string]string{},
		schemaDir: map[string]string{},
	}
}

// DirFor returns the pack directory that holds a kind's schema and relative
// sample files.
func (reg *userDefinedRegistry) DirFor(id string) string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.schemaDir[id]
}

// LoadUserDefinedTypes reads user-defined schema packs from dir.
//
// Layout (pack model used by examples/user-types/):
//
//	dir/reading-list/schema.json
//	dir/reading-list/sample/books.json   # relative file_json paths
//	dir/standalone.json                  # also accepted at the top level
//
// Invalid schemas are skipped with a log line — fail-open for the server
// (issue #15: a bad UDT file must not take down built-in kinds).
func LoadUserDefinedTypes(dir string, logger *slog.Logger) (*userDefinedRegistry, []types.UserDefinedTypeSchema) {
	reg := newUserDefinedRegistry()
	reg.baseDir = dir
	if dir == "" {
		return reg, nil
	}
	var loaded []types.UserDefinedTypeSchema
	for _, path := range discoverSchemaFiles(dir, logger) {
		raw, err := os.ReadFile(path)
		if err != nil {
			if logger != nil {
				logger.Warn("user-defined types: read failed", "path", path, "error", err)
			}
			continue
		}
		var schema types.UserDefinedTypeSchema
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
		if _, exists := reg.byID[schema.ID]; exists {
			if logger != nil {
				logger.Warn("user-defined types: duplicate id", "path", path, "id", schema.ID)
			}
			continue
		}
		if LibraryKindByID(schema.ID) != nil {
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
		// UDTs are opt-in until type policy / permissions name them (places).
		types.MarkOptInDataType(schema.PermType)
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
func (reg *userDefinedRegistry) Get(id string) (types.UserDefinedTypeSchema, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	s, ok := reg.byID[id]
	return s, ok
}

// SchemaIDForPath returns the kind id registered for an agent path.
func (reg *userDefinedRegistry) SchemaIDForPath(path string) (string, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	id, ok := reg.byPath[path]
	return id, ok
}

// All returns every loaded schema (stable registration order is not
// guaranteed across map iteration; callers sort if needed).
func (reg *userDefinedRegistry) All() []types.UserDefinedTypeSchema {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]types.UserDefinedTypeSchema, 0, len(reg.byID))
	for _, s := range reg.byID {
		out = append(out, s)
	}
	return out
}

// dottedPath reads a value from a decoded JSON object using a dotted path.
// Array indexes are numeric segments: "items.0.name". Empty path returns v.
func dottedPath(v any, path string) any {
	if path == "" {
		return v
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		if cur == nil {
			return nil
		}
		switch node := cur.(type) {
		case map[string]any:
			cur = node[seg]
		case []any:
			idx := 0
			if _, err := fmt.Sscanf(seg, "%d", &idx); err != nil || idx < 0 || idx >= len(node) {
				return nil
			}
			cur = node[idx]
		default:
			return nil
		}
	}
	return cur
}

// mapUserDefinedItem turns one source JSON object into a neutral item using
// the schema field map. Missing paths leave fields unset; required-field
// validation is intentionally deferred (fail-open listing).
func mapUserDefinedItem(schema types.UserDefinedTypeSchema, src types.UserDefinedSourceSpec, raw any) types.UserDefinedItem {
	fields := map[string]any{}
	for name, path := range src.FieldMap {
		if v := dottedPath(raw, path); v != nil {
			fields[name] = v
		}
	}
	item := types.UserDefinedItem{Fields: fields}
	if src.IDField != "" {
		if v := dottedPath(raw, src.IDField); v != nil {
			item.Sources = append(item.Sources, types.UserDefinedSource{})
			item.Sources[0].ID = fmt.Sprint(v)
		}
	}
	// Ensure one source entry even when IDField is absent.
	if len(item.Sources) == 0 {
		item.Sources = []types.UserDefinedSource{{}}
	}
	svc := &item.Sources[0]
	svc.Service = src.ID
	svc.ServiceName = src.Name
	if svc.ID == "" {
		if v := dottedPath(raw, "id"); v != nil {
			svc.ID = fmt.Sprint(v)
		}
	}
	if src.URLTemplate != "" && svc.ID != "" {
		svc.URL = strings.ReplaceAll(src.URLTemplate, "{id}", svc.ID)
	}
	item.Name = schema.UserDefinedItemName(item)
	if entity := schema.UserDefinedEntityID(item); entity != "" {
		item.EntityID = entity
	}
	return item
}

// mapUserDefinedDocument maps a decoded source document into neutral items.
func mapUserDefinedDocument(schema types.UserDefinedTypeSchema, src types.UserDefinedSourceSpec, doc any) []types.UserDefinedItem {
	rows := any(doc)
	if src.ItemsPath != "" {
		rows = dottedPath(doc, src.ItemsPath)
	}
	var list []any
	switch node := rows.(type) {
	case []any:
		list = node
	case nil:
		return nil
	default:
		list = []any{node}
	}
	out := make([]types.UserDefinedItem, 0, len(list))
	for _, raw := range list {
		out = append(out, mapUserDefinedItem(schema, src, raw))
	}
	return out
}

// maxUserDefinedItems caps one source page so a hostile or huge feed cannot
// pin memory (same spirit as libraryPageCap on named providers).
const maxUserDefinedItems = 500

// fetchUserDefinedSource loads and maps one schema source. file_json paths
// are resolved relative to the schema directory when not absolute.
func fetchUserDefinedSource(ctx context.Context, schema types.UserDefinedTypeSchema, src types.UserDefinedSourceSpec, baseDir string, client *http.Client) ([]types.UserDefinedItem, bool, error) {
	var body []byte
	var err error
	switch src.Kind {
	case "file_json":
		path := src.Path
		if !filepath.IsAbs(path) && baseDir != "" {
			path = filepath.Join(baseDir, path)
		}
		body, err = os.ReadFile(path)
		if err != nil {
			return nil, false, err
		}
	case "http_json":
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, src.Endpoint, nil)
		if reqErr != nil {
			return nil, false, reqErr
		}
		for k, v := range src.Headers {
			req.Header.Set(k, v)
		}
		if client == nil {
			client = aegisHTTPClient
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			return nil, false, doErr
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, false, fmt.Errorf("http_json %s: status %d", src.ID, resp.StatusCode)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, false, err
		}
	default:
		return nil, false, fmt.Errorf("unsupported source kind %q", src.Kind)
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, false, err
	}
	items := mapUserDefinedDocument(schema, src, doc)
	truncated := false
	if len(items) > maxUserDefinedItems {
		items = items[:maxUserDefinedItems]
		truncated = true
	}
	return items, truncated, nil
}

// listUserDefined fetches every source for one schema and returns the raw
// pre-merge rows (the aggregate pipeline owns merge + encode).
func listUserDefined(ctx context.Context, schema types.UserDefinedTypeSchema, baseDir string, client *http.Client) (items []types.UserDefinedItem, truncated bool, err error) {
	// Preserve source declaration order for stable source stamps.
	srcs := schema.Sources
	if len(srcs) == 0 {
		return nil, false, fmt.Errorf("no sources configured")
	}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	errs := make([]error, 0, len(srcs))
	all := make([]types.UserDefinedItem, 0, len(srcs)*8)
	for _, src := range srcs {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A bad source must not fail the whole list (fail-open).
			items, trunc, ferr := fetchUserDefinedSource(ctx, schema, src, baseDir, client)
			mu.Lock()
			defer mu.Unlock()
			if ferr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", src.ID, ferr))
				return
			}
			if trunc {
				truncated = true
			}
			all = append(all, items...)
		}()
	}
	wg.Wait()
	// Fail-open: return partial rows even when some sources erred. Only
	// return an error when every source failed and nothing was collected.
	if len(all) == 0 && len(errs) > 0 {
		return nil, truncated, errorsJoin(errs...)
	}
	return all, truncated, nil
}

// errorsJoin joins source errors for the empty-result case.
func errorsJoin(errs ...error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}
