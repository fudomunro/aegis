package policy

import (
	"sort"
)

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
	// for a kind (e.g. Spotify / YouTube Music under playlists).
	// Read-only in the data grants editor for now (issue #160).
	AccessorService AccessorKind = "service"
	// AccessorUser is reserved for multi-user principals (issue #149).
	AccessorUser AccessorKind = "user"
)

// Accessor is one principal's allowed access level per kind (issue #160).
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
	// Levels maps kind / permission keys to access levels. Missing
	// keys mean the kind-specific default (agents: DefaultAccessType /
	// opt-in none; apps: none).
	Levels map[string]string `json:"levels"`
	// Explicit marks kind keys the operator named on the record
	// (app scope key, sync pipeline type). Used to decide relevance.
	Explicit map[string]bool `json:"explicit,omitempty"`
	// Editable is true when the admin UI may change Levels.
	Editable bool `json:"editable"`
	// Disabled blocks the accessor without dropping its stored levels.
	Disabled bool `json:"disabled,omitempty"`
}

// AccessorLevel returns the normalized level for a kind ("" when unset).
func (a Accessor) AccessorLevel(kind string) string {
	if a.Levels == nil {
		return ""
	}
	return NormalizeAccess(a.Levels[kind])
}

// RelevantTo reports whether this accessor should appear under a kind
// row in the data grants editor (issue #160).
//
// Agents are always relevant (they own the editable policy). Apps appear
// when they name the type or their effective level is not "none". Sync
// pipelines appear only for the types they actually store.
func (a Accessor) RelevantTo(kind string) bool {
	if kind == "" || kind == PermissionDefaultKey {
		return false
	}
	switch a.Kind {
	case AccessorAgent, AccessorUser:
		return true
	case AccessorApp:
		if a.Explicit[kind] {
			return true
		}
		return a.AccessorLevel(kind) != "" && a.AccessorLevel(kind) != AccessNone
	case AccessorSync, AccessorService:
		// Sync pipelines and service peers only appear for types they
		// actually touch (issue #160).
		if a.Explicit[kind] {
			return true
		}
		level := a.AccessorLevel(kind)
		return level != "" && level != AccessNone
	default:
		return false
	}
}

// KindAccessor is one non-agent accessor row under a kind (issue #160).
// The agent policy itself stays the editable control on that kind.
type KindAccessor struct {
	ID       string       `json:"id"`
	Kind     AccessorKind `json:"kind"`
	Label    string       `json:"label"`
	Detail   string       `json:"detail,omitempty"`
	Level    string       `json:"level"`
	Disabled bool         `json:"disabled,omitempty"`
}

// KindAccess is the permissions-editor row for one kind: the agent
// level (editable in the admin form) plus only the other accessors that
// are relevant to this kind (read-only there).
type KindAccess struct {
	ID         string         `json:"id"`
	Label      string         `json:"label"`
	AgentLevel string         `json:"agent_level"`
	Accessors  []KindAccessor `json:"accessors"`
}

// PermissionsOverview is the kind-centric permissions-editor model
// (issue #160). One entry per kind with its agent policy level and
// the non-agent accessors that actually apply to that kind.
type PermissionsOverview struct {
	// Kinds lists every permission key in stable order.
	Kinds []string `json:"data_types"`
	// Rows is one row per kind, ready for the admin editor.
	Rows []KindAccess `json:"types"`
}

// BuildKindAccess assembles kind-centric rows from accessors. Agent levels
// come from the agent accessor; other accessors are included only when
// RelevantTo is true (issue #160).
func BuildKindAccess(kinds []string, accessors []Accessor) []KindAccess {
	out := make([]KindAccess, 0, len(kinds))
	for _, kind := range kinds {
		if kind == "" || kind == PermissionDefaultKey {
			continue
		}
		row := KindAccess{ID: kind}
		for _, acc := range accessors {
			level := acc.AccessorLevel(kind)
			if acc.Kind == AccessorAgent {
				row.AgentLevel = level
				continue
			}
			if !acc.RelevantTo(kind) {
				continue
			}
			row.Accessors = append(row.Accessors, KindAccessor{
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

// OverviewKinds unions the preferred order (library kind IDs) with any
// extra keys that appear on policies, app scopes, or service maps.
func OverviewKinds(preferred, extra []string) []string {
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

// ExtraPolicyKeys collects kind keys from a data-grant map.
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

// AgentOverviewLevels materializes the agent data grants into a flat
// kind → level map under the collapsed deny-by-default rule, with the
// owner prefill so the view matches what the runtime enforces.
func AgentOverviewLevels(policies DataGrants, kinds []string) map[string]string {
	rules := policies.Levels().PrefillDefault()
	out := make(map[string]string, len(kinds))
	for _, kind := range kinds {
		out[kind] = rules.Level(kind)
	}
	return out
}

// AppOverviewLevels materializes app scopes. Apps fail closed: unspecified
// types are none, matching Create's default=none rule. Opt-in types
// (places) stay none unless the scope map names them. Explicit keys are
// reported so the editor can treat a deliberate "none" as relevant.
func AppOverviewLevels(appScopes Permissions, kinds []string) (levels map[string]string, explicit map[string]bool) {
	levels = make(map[string]string, len(kinds))
	explicit = make(map[string]bool, len(appScopes))
	for k := range appScopes {
		if k == "" || k == PermissionDefaultKey {
			continue
		}
		explicit[k] = true
	}
	for _, kind := range kinds {
		levels[kind] = appScopes.Level(kind)
	}
	return levels, explicit
}
