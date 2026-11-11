package library

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/kinds"
)

// builtinKinds returns the wired built-in kinds (issue #312). Built lazily
// so zero-value fixtures fail open.
func (h *Handlers) BuiltinKinds() []kinds.Kind {
	h.kindListOnce.Do(func() {
		h.kindList = kinds.Builtin(h.Host, h.Engine(), h.Deps)
	})
	return h.kindList
}

// KindRegistry returns the composed kind registry: the wired built-ins
// plus any loaded user-defined kinds, with the shadow check applied
// (issue #312). UDK kinds are composed per call so fixtures that load
// packs late still see them, matching the old per-request route merge.
func (h *Handlers) KindRegistry() []kinds.Kind {
	builtin := h.BuiltinKinds()
	if h == nil || h.UDKs == nil {
		return builtin
	}
	schemas := h.UDKs.All()
	udks := make([]kinds.Kind, 0, len(schemas))
	for _, schema := range schemas {
		udks = append(udks, kinds.UserDefined(h.Host, h.Engine(), schema, h.userDefinedDeps()))
	}
	merged, _ := kinds.MergeUserDefined(builtin, udks)
	return merged
}

// registryKind returns the registered kind for id (issue #312).
func (h *Handlers) RegistryKind(id string) (kinds.Kind, bool) {
	for _, k := range h.KindRegistry() {
		if k.Def().ID == id {
			return k, true
		}
	}
	return nil, false
}

// listHandler resolves a kind's list-route handler (its Def().Path route).
func ListHandler(k kinds.Kind) http.HandlerFunc {
	for _, route := range k.Routes() {
		if route.Pattern == k.Def().Path {
			return route.Handler
		}
	}
	return nil
}

// userDefinedDeps wires the user-defined kind constructor args (issue
// #15): overlay-resolved schemas, pack directories, and the data-grant
// gate.
func (h *Handlers) userDefinedDeps() kinds.UserDefinedDeps {
	return kinds.UserDefinedDeps{
		Schema: func(id string) kinds.UserDefinedKindSchema {
			if h.UDKs == nil {
				return kinds.UserDefinedKindSchema{}
			}
			return h.UDKs.SchemaWithOverlay(id)
		},
		BaseDir: func(id string) string {
			if h.UDKs == nil {
				return ""
			}
			return h.UDKs.DirFor(id)
		},
		Readable: h.UserDefinedReadable,
	}
}

// LibraryHandler resolves a kind's list handler through the route
// registry — the reach-through for tests and admin dispatch (issue #312).
func (h *Handlers) LibraryHandler(kindID string) http.HandlerFunc {
	k := LibraryKindByID(kindID)
	if k == nil {
		return nil
	}
	return h.RouteHandlers()[k.Path]
}

// RouteHandler resolves any registered library route pattern, including
// kind subroutes (issue #312 reach-through for tests).
func (h *Handlers) RouteHandler(pattern string) http.HandlerFunc {
	return h.RouteHandlers()[pattern]
}
