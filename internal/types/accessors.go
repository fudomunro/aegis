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
	// AccessorService is a configured connector service Aegis syncs with
	// for a data type (e.g. Spotify / YouTube Music under playlists).
	// Read-only in the permissions editor for now (issue #160).
	AccessorService AccessorKind = "service"
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
	// Explicit marks data-type keys the operator named on the record
	// (app scope key, sync pipeline type). Used to decide relevance.
	Explicit map[string]bool `json:"explicit,omitempty"`
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

// RelevantTo reports whether this accessor should appear under a data-type
// row in the permissions editor (issue #160).
//
// Agents are always relevant (they own the editable policy). Apps appear
// when they name the type or their effective level is not "none". Sync
// pipelines appear only for the types they actually store.
func (a Accessor) RelevantTo(dataType string) bool {
	if dataType == "" || dataType == PermissionDefaultKey {
		return false
	}
	switch a.Kind {
	case AccessorAgent, AccessorUser:
		return true
	case AccessorApp:
		if a.Explicit[dataType] {
			return true
		}
		return a.AccessorLevel(dataType) != "" && a.AccessorLevel(dataType) != AccessNone
	case AccessorSync, AccessorService:
		// Sync pipelines and service peers only appear for types they
		// actually touch (issue #160).
		if a.Explicit[dataType] {
			return true
		}
		level := a.AccessorLevel(dataType)
		return level != "" && level != AccessNone
	default:
		return false
	}
}

// TypeAccessor is one non-agent accessor row under a data type (issue #160).
// The agent policy itself stays the editable control on that type.
type TypeAccessor struct {
	ID       string       `json:"id"`
	Kind     AccessorKind `json:"kind"`
	Label    string       `json:"label"`
	Detail   string       `json:"detail,omitempty"`
	Level    string       `json:"level"`
	Disabled bool         `json:"disabled,omitempty"`
}

// TypeAccess is the permissions-editor row for one data type: the agent
// level (editable in the admin form) plus only the other accessors that
// are relevant to this type (read-only there).
type TypeAccess struct {
	ID         string         `json:"id"`
	Label      string         `json:"label"`
	AgentLevel string         `json:"agent_level"`
	Accessors  []TypeAccessor `json:"accessors"`
}

// PermissionsOverview is the type-centric permissions-editor model
// (issue #160). One entry per data type with its agent policy level and
// the non-agent accessors that actually apply to that type.
type PermissionsOverview struct {
	// DataTypes lists every permission key in stable order.
	DataTypes []string `json:"data_types"`
	// Types is one row per data type, ready for the admin editor.
	Types []TypeAccess `json:"types"`
}

// BuildTypeAccess assembles type-centric rows from accessors. Agent levels
// come from the agent accessor; other accessors are included only when
// RelevantTo is true (issue #160).
func BuildTypeAccess(dataTypes []string, accessors []Accessor) []TypeAccess {
	out := make([]TypeAccess, 0, len(dataTypes))
	for _, dataType := range dataTypes {
		if dataType == "" || dataType == PermissionDefaultKey {
			continue
		}
		row := TypeAccess{ID: dataType}
		for _, acc := range accessors {
			level := acc.AccessorLevel(dataType)
			if acc.Kind == AccessorAgent {
				row.AgentLevel = level
				continue
			}
			if !acc.RelevantTo(dataType) {
				continue
			}
			row.Accessors = append(row.Accessors, TypeAccessor{
				ID:       acc.ID,
				Kind:     acc.Kind,
				Label:    acc.Label,
				Detail:   acc.Detail,
				Level:    level,
				Disabled: acc.Disabled,
			})
		}
		sort.SliceStable(row.Accessors, func(i, j int) bool {
			// Apps and sync first, then service peers, then anything else.
			rank := func(k AccessorKind) int {
				switch k {
				case AccessorApp:
					return 0
				case AccessorSync:
					return 1
				case AccessorService:
					return 2
				default:
					return 3
				}
			}
			if rank(row.Accessors[i].Kind) != rank(row.Accessors[j].Kind) {
				return rank(row.Accessors[i].Kind) < rank(row.Accessors[j].Kind)
			}
			return row.Accessors[i].Label < row.Accessors[j].Label
		})
		out = append(out, row)
	}
	return out
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
func ExtraPolicyKeys(policies DataGrants) []string {
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
// flat data-type → level map. Opt-in types (places) stay "none" unless
// the policy names them explicitly.
func AgentOverviewLevels(policies DataGrants, dataTypes []string) map[string]string {
	out := make(map[string]string, len(dataTypes))
	for _, dataType := range dataTypes {
		level := policies.Access(dataType, "")
		out[dataType] = NormalizeAccess(level)
	}
	return out
}

// AppOverviewLevels materializes app scopes. Apps fail closed: unspecified
// types are none, matching Create's default=none rule. Opt-in types
// (places) stay none unless the scope map names them. Explicit keys are
// reported so the editor can treat a deliberate "none" as relevant.
func AppOverviewLevels(scopes Permissions, dataTypes []string) (levels map[string]string, explicit map[string]bool) {
	levels = make(map[string]string, len(dataTypes))
	explicit = make(map[string]bool, len(scopes))
	for k := range scopes {
		if k == "" || k == PermissionDefaultKey {
			continue
		}
		explicit[k] = true
	}
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
		levels[dataType] = level
	}
	return levels, explicit
}
