package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
)

// UserDefinedDeps are the constructor args for user-defined kinds (issue
// #15, issue #312): schema overlay resolution and local-file base paths.
type UserDefinedDeps struct {
	// Schema resolves the current schema for an id with the admin
	// file-selection overlay applied. Called per request so /v1/admin/udt
	// picks apply without a restart.
	Schema func(id string) UserDefinedTypeSchema
	// BaseDir returns the pack directory holding a kind's schema and its
	// relative source files.
	BaseDir func(id string) string
	// Readable reports whether the data grant allows GET on a UDT
	// permType. Packs never self-enable (issue #15 review).
	Readable func(permType string) bool
}

// userDefinedKind serves one schema pack through the shared aggregate
// pipeline (issue #15 Phase 6 PoC). Kind-owned: the schema declares
// sources, fields, identity, and merge.
type userDefinedKind struct {
	h      Host
	e      *Engine
	schema UserDefinedTypeSchema
	deps   UserDefinedDeps
	routes []Route
}

// UserDefined returns the Kind for one loaded schema pack (issue #312).
// The construction schema drives identity and routes; the serve path
// re-resolves it per request so admin file selection applies live.
func UserDefined(h Host, e *Engine, schema UserDefinedTypeSchema, d UserDefinedDeps) Kind {
	k := &userDefinedKind{h: h, e: e, schema: schema, deps: d}
	k.routes = []Route{{
		Pattern: schema.Path,
		Handler: k.serve,
		// User-defined kinds are not app-readable (issue #183).
	}}
	return k
}

func (k *userDefinedKind) Def() LibraryKind { return k.schema.LibraryKind() }

func (k *userDefinedKind) Routes() []Route { return k.routes }

// Provides reports whether a connector instance feeds this kind. UDTs are
// fed by schema sources, not OAuth connectors.
func (k *userDefinedKind) Provides(connector any) bool { return false }

// HasProviders reports whether the schema declares any source.
func (k *userDefinedKind) HasProviders(h Host) bool {
	return len(k.schema.Sources) > 0
}

// Readable reports whether the data grant allows GET on this kind.
func (k *userDefinedKind) Readable(h Host) bool {
	if k.deps.Readable == nil {
		return false
	}
	return k.deps.Readable(k.schema.PermType)
}

func (k *userDefinedKind) serve(w http.ResponseWriter, r *http.Request) {
	schema := k.schema
	if k.deps.Schema != nil {
		schema = k.deps.Schema(schema.ID)
	}
	kind := schema.LibraryKind()
	baseDir := ""
	if k.deps.BaseDir != nil {
		baseDir = k.deps.BaseDir(schema.ID)
	}
	Aggregate(k.h, k.e, w, r, Spec[UserDefinedItem]{
		Kind: kind,
		Targets: func(r *http.Request) (targets []NamedLister[UserDefinedItem], denied []string) {
			// Type-owned policy decides agent visibility (issue #148).
			// UDTs are opt-in: without an enabling policy entry this denies.
			if !k.Readable(k.h) {
				denied = append(denied, schema.ID)
				return targets, denied
			}
			for _, src := range schema.Sources {
				src := src
				targets = append(targets, NamedLister[UserDefinedItem]{
					ID: src.ID,
					List: func(ctx context.Context) ([]UserDefinedItem, bool, error) {
						// Fail-open per source: fetch errors surface as
						// by_service status, never a hard 500 for the list.
						return fetchUserDefinedSource(ctx, schema, src, baseDir)
					},
				})
			}
			return targets, nil
		},
		ServiceOf: UserDefinedServiceName,
		SortKey: func(item UserDefinedItem) string {
			if item.EntityID != "" {
				return "e:" + item.EntityID
			}
			return "n:" + schema.UserDefinedIdentityKey(item)
		},
		Merge: func(in []UserDefinedItem) []UserDefinedItem {
			return MergeUserDefinedItems(schema, in)
		},
		Encode: func(items []UserDefinedItem, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			return EncodeUserDefinedList(schema, items, page, bySvc)
		},
	})
}

// MergeUserDefined composes the kind registry: built-ins plus user-defined
// kinds, refusing any pack that shadows a built-in. ID collisions and
// ServeMux-pattern conflicts are rejected — registration itself is the
// shadow check (issue #312; docs/user-defined-types.md promises packs
// never shadow built-ins).
func MergeUserDefined(builtin []Kind, udts []Kind) (merged []Kind, skipped []string) {
	merged = append([]Kind(nil), builtin...)
	ids := map[string]bool{}
	mux := http.NewServeMux()
	for _, k := range builtin {
		ids[k.Def().ID] = true
		if !tryRegisterRoutes(mux, k.Routes()) {
			// Built-in self-conflict is a programming error; the full-mux
			// test covers it. Keep the kind so failure stays visible.
		}
	}
	for _, k := range udts {
		id := k.Def().ID
		if ids[id] || IsReservedUserDefinedID(id) || !tryRegisterRoutes(mux, k.Routes()) {
			skipped = append(skipped, id)
			continue
		}
		ids[id] = true
		merged = append(merged, k)
	}
	return merged, skipped
}

// tryRegisterRoutes reports whether every pattern registers on mux without
// conflicting with what is already there. ServeMux panics on conflicting
// patterns — that panic is the shadow check.
func tryRegisterRoutes(mux *http.ServeMux, routes []Route) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	for _, route := range routes {
		mux.HandleFunc(route.Pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return true
}

// maxUserDefinedItems caps one source page so a hostile or huge feed cannot
// pin memory (same spirit as shared.LibraryPageCap on named providers).
const maxUserDefinedItems = 500

// maxUserDefinedBytes caps file_json and http_json payloads before decode.
const maxUserDefinedBytes = 4 << 20

// fetchUserDefinedSource loads and maps one schema source. file_json paths
// are resolved relative to the schema directory when not absolute.
func fetchUserDefinedSource(ctx context.Context, schema UserDefinedTypeSchema, src UserDefinedSourceSpec, baseDir string) ([]UserDefinedItem, bool, error) {
	var body []byte
	var err error
	switch src.Kind {
	case "file_json":
		path := src.Path
		if !filepath.IsAbs(path) && baseDir != "" {
			path = filepath.Join(baseDir, path)
		}
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil, false, oerr
		}
		defer f.Close()
		body, err = io.ReadAll(io.LimitReader(f, maxUserDefinedBytes+1))
		if err != nil {
			return nil, false, err
		}
		if len(body) > maxUserDefinedBytes {
			return nil, false, fmt.Errorf("file_json %s: payload exceeds %d bytes", src.ID, maxUserDefinedBytes)
		}
	case "http_json":
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, src.Endpoint, nil)
		if reqErr != nil {
			return nil, false, reqErr
		}
		for k, v := range src.Headers {
			req.Header.Set(k, v)
		}
		resp, doErr := shared.HTTPClient.Do(req)
		if doErr != nil {
			return nil, false, doErr
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, false, fmt.Errorf("http_json %s: status %d", src.ID, resp.StatusCode)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxUserDefinedBytes+1))
		if err != nil {
			return nil, false, err
		}
		if len(body) > maxUserDefinedBytes {
			return nil, false, fmt.Errorf("http_json %s: payload exceeds %d bytes", src.ID, maxUserDefinedBytes)
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
func mapUserDefinedItem(schema UserDefinedTypeSchema, src UserDefinedSourceSpec, raw any) UserDefinedItem {
	fields := map[string]any{}
	for name, path := range src.FieldMap {
		if v := dottedPath(raw, path); v != nil {
			fields[name] = v
		}
	}
	item := UserDefinedItem{Fields: fields}
	if src.IDField != "" {
		if v := dottedPath(raw, src.IDField); v != nil {
			item.Sources = append(item.Sources, UserDefinedSource{})
			item.Sources[0].ID = fmt.Sprint(v)
		}
	}
	// Ensure one source entry even when IDField is absent.
	if len(item.Sources) == 0 {
		item.Sources = []UserDefinedSource{{}}
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
func mapUserDefinedDocument(schema UserDefinedTypeSchema, src UserDefinedSourceSpec, doc any) []UserDefinedItem {
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
	out := make([]UserDefinedItem, 0, len(list))
	for _, raw := range list {
		out = append(out, mapUserDefinedItem(schema, src, raw))
	}
	return out
}
