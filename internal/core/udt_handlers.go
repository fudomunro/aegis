package core

import (
	"context"
	"fmt"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

// HandleLibraryUserDefined serves GET /v1/library/{udt-id} for one loaded
// user-defined kind through the shared aggregate pipeline (cache, type
// policy, fan-out, merge, JSON) — issue #15 Phase 6 PoC.
// Schema is resolved with the admin file-selection overlay at request time
// so /v1/admin/udt picks apply without a restart.
func (s *Server) HandleLibraryUserDefined(schema types.UserDefinedTypeSchema) http.HandlerFunc {
	kindID := schema.ID
	kind := schema.LibraryKind()
	return func(w http.ResponseWriter, r *http.Request) {
		schema := schema
		kind := kind
		if s.udts != nil {
			schema = s.udts.SchemaWithOverlay(kindID)
			kind = schema.LibraryKind()
		}
		handleLibraryAggregate(s, w, r, libraryKindSpec[types.UserDefinedItem]{
			kind: kind,
			targets: func(s *Server, r *http.Request) (targets []namedLister[types.UserDefinedItem], denied []string) {
				// Type-owned policy decides agent visibility (issue #148).
				// UDTs are opt-in: without an enabling policy entry this denies.
				if !s.userDefinedReadable(kind.PermType) {
					denied = append(denied, schema.ID)
					return targets, denied
				}
				baseDir := ""
				if s.udts != nil {
					baseDir = s.udts.DirFor(schema.ID)
				}
				for _, src := range schema.Sources {
					src := src
					targets = append(targets, namedLister[types.UserDefinedItem]{
						id: src.ID,
						list: func(ctx context.Context) ([]types.UserDefinedItem, bool, error) {
							// Fail-open per source: fetch errors surface as
							// by_service status, never a hard 500 for the list.
							return fetchUserDefinedSource(ctx, schema, src, baseDir, aegisHTTPClient)
						},
					})
				}
				return targets, nil
			},
			serviceOf: types.UserDefinedServiceName,
			sortKey: func(item types.UserDefinedItem) string {
				if item.EntityID != "" {
					return "e:" + item.EntityID
				}
				return "n:" + schema.UserDefinedIdentityKey(item)
			},
			merge: func(in []types.UserDefinedItem) []types.UserDefinedItem {
				return types.MergeUserDefinedItems(schema, in)
			},
			encode: func(merged []types.UserDefinedItem, bySvc map[string]types.LibraryServiceStatus) any {
				return types.EncodeUserDefinedList(schema, merged, bySvc)
			},
		})
	}
}

// userDefinedReadable reports whether type policy allows GET on a UDT kind.
func (s *Server) userDefinedReadable(permType string) bool {
	if s == nil || s.config == nil {
		return false
	}
	return types.AllowsMethod(s.config.EffectiveAccess("", permType), http.MethodGet)
}

// registerUserDefinedKinds seeds type policy from schema.default_access.
// Kind metadata stays in the UDT registry (allLibraryKinds merges it); this
// does not mutate the built-in libraryKinds slice.
func (s *Server) registerUserDefinedKinds() {
	if s == nil || s.udts == nil || s.config == nil {
		return
	}
	for _, schema := range s.udts.All() {
		if schema.DefaultAccess != "" && schema.DefaultAccess != types.AccessNone {
			s.config.SeedTypePolicy(schema.PermType, types.TypePolicy{Default: schema.DefaultAccess})
		}
	}
}

// udtRouteHandlers maps user-defined kind paths to their HTTP handlers.
func (s *Server) udtRouteHandlers() map[string]http.HandlerFunc {
	out := map[string]http.HandlerFunc{}
	if s == nil || s.udts == nil {
		return out
	}
	for _, schema := range s.udts.All() {
		schema := schema
		out[schema.Path] = s.HandleLibraryUserDefined(schema)
	}
	return out
}

// userDefinedHint returns pre-request instructions for one UDT kind,
// including the schema-declared field list so agents know the shape.
func (s *Server) userDefinedHint(schema types.UserDefinedTypeSchema) string {
	kind := schema.LibraryKind()
	text := kind.Instructions(s.baseURL)
	if name := s.canonicalServiceName(kind.ID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the home copy.", kind.Label, name)
	}
	text += "\nSchema-declared fields for this user-defined type:"
	for name, f := range schema.Fields {
		desc := f.Description
		if desc == "" {
			desc = f.Type
		}
		if f.Required {
			desc += ", required"
		}
		text += fmt.Sprintf(" %s (%s)", name, desc)
	}
	return text
}
