package types

import "sort"

// AccessorKind identifies who or what is accessing data through Aegis
// (issue #160). Permissions editing must show every accessor class, not
// only agents. Multi-user principals (issue #149) will add AccessorUser.
type AccessorKind string

const (
	// AccessorAgent is an LLM agent (or tailnet/API client) calling the
	// proxy and library APIs — the primary accessor today.
	AccessorAgent AccessorKind = "agent"
	// AccessorApp is a registered third-party application with scopes
	// (issue #131).
	AccessorApp AccessorKind = "app"
	// AccessorSync is an Aegis-internal sync pipeline that copies or
	// archives data (mail archive, copy ledger, timeline import, …).
	AccessorSync AccessorKind = "sync"
	// AccessorUser is reserved for multi-user principals (issue #149).
	AccessorUser AccessorKind = "user"
)

// Accessor is one principal's allowed access level per data type (issue #160).
// This is the "what each accessor is allowed to access" half of confidence;
// the admin map shows "what each accessor has accessed".
type Accessor struct {
	// ID is the accessor identity ("agent", "app:eco-demo", "sync:mail").
	ID   string       `json:"id"`
	Kind AccessorKind `json:"kind"`
	// Label is the admin UI display name.
	Label string `json:"label"`
	// Detail is a short description of how this accessor reaches data.
	Detail string `json:"detail,omitempty"`
	// Levels maps data-type / permission keys to access levels. Missing
	// keys mean the kind-specific default (agents: DefaultAccessType /
	// opt-in none; apps: none).
	Levels map[string]string `json:"levels"`
	// Editable is true when the admin UI may change Levels.
	Editable bool `json:"editable"`
	// Disabled blocks the accessor without dropping its stored levels.
	Disabled bool `json:"disabled,omitempty"`
}

// SortedLevels returns data-type keys in stable order for rendering.
func (a Accessor) SortedLevels() []string {
	if a.Levels == nil {
		return nil
	}
	keys := make([]string, 0, len(a.Levels))
	for k := range a.Levels {
		if k == PermissionDefaultKey {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AccessorLevel returns the normalized level for a data type ("" when unset).
func (a Accessor) AccessorLevel(dataType string) string {
	if a.Levels == nil {
		return ""
	}
	return NormalizeAccess(a.Levels[dataType])
}

// PermissionsOverview is the holistic accessor × data-type matrix
// (issue #160). Admin permissions editing renders this so operators can see
// agents, apps, and sync pipelines side by side.
type PermissionsOverview struct {
	// DataTypes lists every permission key the matrix should show
	// (library kinds plus any explicit keys on stored policies).
	DataTypes []string `json:"data_types"`
	// Accessors is every principal that can reach data through Aegis.
	Accessors []Accessor `json:"accessors"`
}

// OverviewDataTypes unions the preferred order (library kind IDs) with any
// extra keys that appear on policies, app scopes, or service maps.
func OverviewDataTypes(preferred, extra []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(preferred)+len(extra))
	for _, id := range preferred {
		if id == "" || id == PermissionDefaultKey || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	extraCopy := append([]string(nil), extra...)
	sort.Strings(extraCopy)
	for _, id := range extraCopy {
		if id == "" || id == PermissionDefaultKey || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// ExtraPermissionKeys collects non-default keys from permission maps.
func ExtraPermissionKeys(maps ...map[string]string) []string {
	var out []string
	for _, m := range maps {
		for k := range m {
			if k == "" || k == PermissionDefaultKey {
				continue
			}
			out = append(out, k)
		}
	}
	return out
}

// ExtraPolicyKeys collects data-type keys from a type-policy map.
func ExtraPolicyKeys(policies TypePolicies) []string {
	if policies == nil {
		return nil
	}
	out := make([]string, 0, len(policies))
	for k := range policies {
		if k == "" || k == PermissionDefaultKey {
			continue
		}
		out = append(out, k)
	}
	return out
}

// AgentOverviewLevels materializes the type-owned agent policy into a
// flat data-type → level map for the permissions matrix. Opt-in types
// (places) stay "none" unless the policy names them explicitly.
func AgentOverviewLevels(policies TypePolicies, dataTypes []string) map[string]string {
	out := make(map[string]string, len(dataTypes))
	for _, dataType := range dataTypes {
		level := policies.Access(dataType, "")
		// TypePolicies.Access already applies opt-in none and default-read
		// when the type has no policy entry.
		out[dataType] = NormalizeAccess(level)
	}
	return out
}

// AppOverviewLevels materializes app scopes for the matrix. Apps fail
// closed: unspecified types are none, matching Create's default=none rule.
// Opt-in types (places) stay none unless the scope map names them.
func AppOverviewLevels(scopes Permissions, dataTypes []string) map[string]string {
	out := make(map[string]string, len(dataTypes))
	for _, dataType := range dataTypes {
		level := AccessNone
		if scopes != nil {
			if v, ok := scopes[dataType]; ok {
				level = NormalizeAccess(v)
			} else if !optInDefaultNone[dataType] {
				if v, ok := scopes[PermissionDefaultKey]; ok {
					level = NormalizeAccess(v)
				}
			}
		}
		if level == "" {
			level = AccessNone
		}
		out[dataType] = level
	}
	return out
}
