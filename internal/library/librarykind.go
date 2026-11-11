package library

import (
	"fmt"
	"net/http"

	"github.com/fudomunro/aegis/internal/kinds"
)

// kindHasProviders reports whether any connector or declared local source
// can feed the kind.
func KindHasProviders(id string, h *Handlers) bool {
	if k, ok := h.RegistryKind(id); ok {
		return k.HasProviders(h.Host)
	}
	return false
}

// kindReadable reports whether at least one non-blocked service allows reads.
func KindReadable(id string, h *Handlers) bool {
	if k, ok := h.RegistryKind(id); ok {
		return k.Readable(h.Host)
	}
	return false
}

// libraryKinds is the built-in kind registry (issue #183). The slice lives
// in internal/kinds so reserved ids, app-readable paths, canonical_for keys,
// and access-level allowlists all derive from one source.
// LibraryKinds is the built-in kind registry slice.
var LibraryKinds = kinds.BuiltinLibraryKinds

// LibraryKindByID returns the registered built-in kind, or nil.
func LibraryKindByID(id string) *kinds.LibraryKind {
	return kinds.BuiltinKindByID(id)
}

// allLibraryKinds returns every registered kind — built-in and
// user-defined (issue #15). Prefer this over ranging libraryKinds directly
// so UDKs appear in pre-request injection, admin listings, and flow maps.
func (h *Handlers) AllLibraryKinds() []kinds.LibraryKind {
	registry := h.KindRegistry()
	out := make([]kinds.LibraryKind, 0, len(registry))
	for _, k := range registry {
		out = append(out, k.Def())
	}
	return out
}

// udkSchemaForKind returns the user-defined schema backing a kind id, if any.
func (h *Handlers) UDKSchemaForKind(id string) (kinds.UserDefinedKindSchema, bool) {
	if h == nil || h.UDKs == nil {
		return kinds.UserDefinedKindSchema{}, false
	}
	return h.UDKs.Get(id)
}

// libraryInstructionsFor returns kind instructions plus a configured canonical
// service hint when the instance declares one for this kind, and the
// multi-source mutation rule. Kind-specific agent copy rides on
// LibraryKind.ExtraInstructions (issue #254); this only adds the runtime
// canonical hint.
func (h *Handlers) LibraryInstructionsFor(kindID string) string {
	k := LibraryKindByID(kindID)
	if k == nil {
		return ""
	}
	text := k.Instructions(h.BaseURL)
	if name := h.CanonicalServiceName(kindID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the canonical home.", k.Label, name)
	}
	text += "\n" + MultiSourceMutationRule()
	return text
}

// libraryReadableFor reports whether at least one non-blocked service allows
// read access to the given permission/kind.
func (h *Handlers) LibraryReadableFor(permType string) bool {
	for _, svc := range h.Host.Services() {
		if svc.Action == "block" {
			continue
		}
		if h.Host.OwnerGrants().AllowsRead(permType) {
			return true
		}
	}
	return false
}

// libraryHintsForMessage returns instruction blocks for every library kind
// that matches a service-less user question and is readable for this request
// (the request key's data grant). Used by the pre-request hook.
func (h *Handlers) LibraryHintsForMessage(r *http.Request, msg string) []string {
	var out []string
	for _, k := range h.AllLibraryKinds() {
		if !k.Mentions(msg) {
			continue
		}
		if !KindHasProviders(k.ID, h) || !KindReadable(k.ID, h) {
			continue
		}
		if !h.requestGrants(r).AllowsKindMethod(k.PermType, http.MethodGet) {
			continue
		}
		if schema, ok := h.UDKSchemaForKind(k.ID); ok {
			out = append(out, h.UserDefinedHint(schema))
			continue
		}
		out = append(out, h.LibraryInstructionsFor(k.ID))
	}
	return out
}
