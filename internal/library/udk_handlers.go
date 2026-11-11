package library

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/fudomunro/aegis/internal/kinds"
)

// userDefinedReadable reports whether the owner data grant allows GET on a
// UDK kind.
func (h *Handlers) UserDefinedReadable(permType string) bool {
	if h == nil {
		return false
	}
	return h.Host.OwnerGrants().AllowsKindMethod(permType, http.MethodGet)
}

// registerUserDefinedKinds prepares loaded UDK kinds for serving.
//
// Visibility is never taken from the pack: every UDK stays opt-in
// (shared.MarkKindDefaultNone at load) until an admin data grant names the
// kind (issue #15 review — packs must not self-enable, including
// default_access). Schema default_access is informational only.
func (h *Handlers) RegisterUserDefinedKinds() {
	if h == nil || h.UDKs == nil {
		return
	}
	// Intentionally no SeedDataGrant: default_access cannot grant access.
	// Re-applying after Config.Reload is unnecessary because nothing is seeded.
}

// userDefinedHint returns pre-request instructions for one UDK kind,
// including the schema-declared field list so agents know the shape.
func (h *Handlers) UserDefinedHint(schema kinds.UserDefinedKindSchema) string {
	kind := schema.LibraryKind()
	text := kind.Instructions(h.BaseURL)
	if name := h.CanonicalServiceName(kind.ID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the canonical home.", kind.Label, name)
	}
	text += "\nSchema-declared fields for this user-defined kind:"
	// Sorted for prompt-cache stability (issue #15 review).
	fieldNames := make([]string, 0, len(schema.Fields))
	for name := range schema.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	for _, name := range fieldNames {
		f := schema.Fields[name]
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
