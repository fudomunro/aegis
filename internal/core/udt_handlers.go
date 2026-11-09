package core

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
)

// userDefinedReadable reports whether data grant allows GET on a UDT kind.
func (s *Server) userDefinedReadable(permType string) bool {
	if s == nil || s.config == nil {
		return false
	}
	return policy.AllowsMethod(s.config.EffectiveAccess("", permType), http.MethodGet)
}

// registerUserDefinedKinds prepares loaded UDT kinds for serving.
//
// Visibility is never taken from the pack: every UDT stays opt-in
// (shared.MarkKindDefaultNone at load) until an admin data grant names the
// kind (issue #15 review — packs must not self-enable, including
// default_access). Schema default_access is informational only.
func (s *Server) registerUserDefinedKinds() {
	if s == nil || s.udts == nil {
		return
	}
	// Intentionally no SeedDataGrant: default_access cannot grant access.
	// Re-applying after Config.Reload is unnecessary because nothing is seeded.
}

// userDefinedHint returns pre-request instructions for one UDT kind,
// including the schema-declared field list so agents know the shape.
func (s *Server) userDefinedHint(schema kinds.UserDefinedTypeSchema) string {
	kind := schema.LibraryKind()
	text := kind.Instructions(s.baseURL)
	if name := s.canonicalServiceName(kind.ID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the canonical home.", kind.Label, name)
	}
	text += "\nSchema-declared fields for this user-defined type:"
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
