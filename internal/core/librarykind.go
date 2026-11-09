package core

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/types"
)

// kindHasProviders reports whether any connector can feed the kind.
func kindHasProviders(id string, s *Server) bool {
	if schema, ok := s.udtSchemaForKind(id); ok {
		return len(schema.Sources) > 0
	}
	if k, ok := s.builtinKind(id); ok {
		return k.HasProviders(s)
	}
	return false
}

// kindReadable reports whether at least one non-blocked service allows reads.
func kindReadable(id string, s *Server) bool {
	if schema, ok := s.udtSchemaForKind(id); ok {
		return s.userDefinedReadable(schema.PermType)
	}
	if k, ok := s.builtinKind(id); ok {
		return k.Readable(s)
	}
	return false
}

// libraryKinds is the built-in kind registry (issue #183). The slice lives
// in internal/types so reserved ids, app-readable paths, canonical_for keys,
// and access-level allowlists all derive from one source.
var libraryKinds = types.BuiltinLibraryKinds

// LibraryKindByID returns the registered built-in kind, or nil.
func LibraryKindByID(id string) *types.LibraryKind {
	return types.BuiltinKindByID(id)
}

// allLibraryKinds returns built-in kinds plus any loaded user-defined kinds
// (issue #15). Prefer this over ranging libraryKinds directly so UDTs appear
// in pre-request injection, admin listings, and flow maps.
func (s *Server) allLibraryKinds() []types.LibraryKind {
	out := make([]types.LibraryKind, 0, len(libraryKinds))
	out = append(out, libraryKinds...)
	if s != nil && s.udts != nil {
		for _, schema := range s.udts.All() {
			if LibraryKindByID(schema.ID) == nil {
				out = append(out, schema.LibraryKind())
			}
		}
	}
	return out
}

// udtSchemaForKind returns the user-defined schema backing a kind id, if any.
func (s *Server) udtSchemaForKind(id string) (types.UserDefinedTypeSchema, bool) {
	if s == nil || s.udts == nil {
		return types.UserDefinedTypeSchema{}, false
	}
	return s.udts.Get(id)
}

// libraryInstructionsFor returns kind instructions plus a configured canonical
// service hint when the instance declares one for this kind, and the
// multi-source mutation rule. Kind-specific agent copy rides on
// LibraryKind.ExtraInstructions (issue #254); this only adds the runtime
// canonical hint.
func (s *Server) libraryInstructionsFor(kindID string) string {
	k := LibraryKindByID(kindID)
	if k == nil {
		return ""
	}
	text := k.Instructions(s.baseURL)
	if name := s.canonicalServiceName(kindID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the canonical home.", k.Label, name)
	}
	text += "\n" + multiSourceMutationRule()
	return text
}

// libraryReadableFor reports whether at least one non-blocked service allows
// read access to the given permission/kind.
func (s *Server) libraryReadableFor(permType string) bool {
	for _, svc := range s.config.ServiceSnapshots() {
		if svc.Action == "block" {
			continue
		}
		if s.effectivePermissions(&svc).AllowsRead(permType) {
			return true
		}
	}
	return false
}

// libraryHintsForMessage returns instruction blocks for every library kind
// that matches a service-less user question and is readable for this request
// (owner grants AND scope grants). Used by the pre-request hook.
func (s *Server) libraryHintsForMessage(r *http.Request, msg string) []string {
	var out []string
	for _, k := range s.allLibraryKinds() {
		if !k.Mentions(msg) {
			continue
		}
		if !kindHasProviders(k.ID, s) || !kindReadable(k.ID, s) {
			continue
		}
		if !scopes.Allows(r, k.PermType, http.MethodGet) {
			continue
		}
		if schema, ok := s.udtSchemaForKind(k.ID); ok {
			out = append(out, s.userDefinedHint(schema))
			continue
		}
		out = append(out, s.libraryInstructionsFor(k.ID))
	}
	return out
}

// libraryRouteHandlers maps each library route to its HTTP handler so main
// can register every dataset from the registry: the kinds package's
// declared routes plus the fixed cross-kind entries. User-defined kinds
// (issue #15) are merged in when schemas are loaded.
func (s *Server) libraryRouteHandlers() map[string]http.HandlerFunc {
	handlers := map[string]http.HandlerFunc{}
	for _, k := range s.builtinKinds() {
		for _, route := range k.Routes() {
			handlers[route.Pattern] = route.Handler
		}
	}
	// Cross-kind maintenance reads (copy ledger, merge review, entity
	// links) are not owned by any kind: fixed registrations here, and the
	// API-listener deny list names the admin-side ones explicitly.
	handlers["/v1/library/copies"] = s.HandleLibraryCopies
	handlers["/v1/library/review"] = s.HandleMergeReview
	handlers["/v1/library/links"] = s.HandleEntityLinks
	handlers["/v1/library/links/"] = s.HandleEntityLinks
	// User-defined packs must never shadow a built-in route (issue #183).
	// Normalize rejects reserved ids; this is the defense-in-depth check
	// against registry/handler drift.
	mergeUDTRoutes(handlers, s.udtRouteHandlers(), slog.Default())
	return handlers
}

// mergeUDTRoutes copies UDT handlers into the built-in route map, refusing
// to overwrite an existing path. Returns the skipped colliding paths.
func mergeUDTRoutes(handlers map[string]http.HandlerFunc, udt map[string]http.HandlerFunc, logger *slog.Logger) []string {
	var skipped []string
	for path, handler := range udt {
		if _, exists := handlers[path]; exists {
			skipped = append(skipped, path)
			if logger != nil {
				logger.Warn("user-defined type path collides with built-in library route; skipping",
					"path", path)
			}
			continue
		}
		handlers[path] = handler
	}
	return skipped
}
