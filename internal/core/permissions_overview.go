package core

import (
	"net/http"
	"sort"

	"github.com/fudomunro/aegis/internal/types"
)

// Sync accessor IDs in the holistic permissions view (issue #160).
const (
	syncMailID     = "sync:mail-archive"
	syncCopyID     = "sync:copy-ledger"
	syncTimelineID = "sync:timeline"
	syncQuickenID  = "sync:quicken"
)

// agentAccessorID is the primary LLM-agent row (type-owned policy).
const agentAccessorID = "agent"

// permissionsOverview builds the type-centric permissions model
// (issue #160): one row per data type with the agent policy level plus
// only the other accessors (apps, sync pipelines) that apply to that type.
// The admin map shows what each accessor has accessed; this view shows
// what each is allowed to access.
func (s *Server) permissionsOverview() types.PermissionsOverview {
	preferred := make([]string, 0, 16)
	for _, kind := range s.allLibraryKinds() {
		key := kind.PermType
		if key == "" {
			key = kind.ID
		}
		preferred = append(preferred, key)
	}

	policies := s.config.TypePolicies()
	var extra []string
	extra = append(extra, types.ExtraPolicyKeys(policies)...)
	for _, svc := range s.config.ServiceSnapshots() {
		extra = append(extra, types.ExtraPermissionKeys(svc.Permissions)...)
	}
	if s.apps != nil {
		for _, app := range s.apps.List() {
			extra = append(extra, types.ExtraPermissionKeys(app.Scopes)...)
		}
	}

	dataTypes := types.OverviewDataTypes(preferred, extra)
	accessors := make([]types.Accessor, 0, 8)

	// Agents: type-owned policy is primary (issue #148).
	accessors = append(accessors, types.Accessor{
		ID:     agentAccessorID,
		Kind:   types.AccessorAgent,
		Label:  "LLM agents",
		Detail: "Agents reaching Aegis through Aperture or direct proxy/library calls. Type policy is primary; per-service forms only tighten.",
		Levels: types.AgentOverviewLevels(policies, dataTypes),
		// Edited via the type-policy form in the permissions editor.
		Editable: true,
	})

	// Third-party apps (issue #131).
	if s.apps != nil {
		apps := s.apps.List()
		sort.Slice(apps, func(i, j int) bool {
			if apps[i].Name != apps[j].Name {
				return apps[i].Name < apps[j].Name
			}
			return apps[i].ID < apps[j].ID
		})
		for _, app := range apps {
			scopes := make(types.Permissions, len(app.Scopes))
			for k, v := range app.Scopes {
				scopes[k] = v
			}
			levels, explicit := types.AppOverviewLevels(scopes, dataTypes)
			accessors = append(accessors, types.Accessor{
				ID:       "app:" + app.ID,
				Kind:     types.AccessorApp,
				Label:    app.Name,
				Detail:   "Third-party application (" + app.ID + "). Scopes fail closed — unnamed types are off.",
				Levels:   levels,
				Explicit: explicit,
				// Scopes are managed on the app record (/v1/admin/apps).
				Editable: true,
				Disabled: app.Disabled,
			})
		}
	}

	// Aegis-internal sync pipelines (issue #160). Levels describe what the
	// pipeline may store — not agent visibility. Only named types are
	// relevant under a data-type row.
	if s.mail != nil {
		accessors = append(accessors, types.Accessor{
			ID:       syncMailID,
			Kind:     types.AccessorSync,
			Label:    "Mail archive sync",
			Detail:   "Gmail sync and Takeout import into the local mail archive. Message bodies stay on the source; Aegis keeps a compressed search copy.",
			Levels:   map[string]string{"messages": types.AccessRead},
			Explicit: map[string]bool{"messages": true},
		})
	}
	if s.copies != nil {
		// Copy ledger stamps provenance for mirror writes across types.
		// No fixed type list — show only when a kind has copy activity.
		levels := map[string]string{}
		explicit := map[string]bool{}
		for _, rec := range s.copies.List(CopyFilter{}) {
			if rec.Kind == "" {
				continue
			}
			levels[rec.Kind] = types.AccessWrite
			explicit[rec.Kind] = true
		}
		accessors = append(accessors, types.Accessor{
			ID:       syncCopyID,
			Kind:     types.AccessorSync,
			Label:    "Copy ledger",
			Detail:   "Mirror/copy jobs stamp provenance when Aegis writes copies to other services. Aegis does not store media.",
			Levels:   levels,
			Explicit: explicit,
		})
	}
	if s.timeline != nil {
		accessors = append(accessors, types.Accessor{
			ID:       syncTimelineID,
			Kind:     types.AccessorSync,
			Label:    "Timeline import",
			Detail:   "Google Timeline export ingest into the local places store.",
			Levels:   map[string]string{"places": types.AccessRead},
			Explicit: map[string]bool{"places": true},
		})
	}
	if s.quicken != nil {
		accessors = append(accessors, types.Accessor{
			ID:       syncQuickenID,
			Kind:     types.AccessorSync,
			Label:    "Quicken import",
			Detail:   "QIF/QFX/OFX import into the local transaction store.",
			Levels:   map[string]string{"transactions": types.AccessRead},
			Explicit: map[string]bool{"transactions": true},
		})
	}

	// Fill non-agent missing levels with none so BuildTypeAccess can read
	// a concrete level; relevance still uses Explicit / non-none checks.
	for i := range accessors {
		if accessors[i].Kind == types.AccessorAgent {
			continue
		}
		if accessors[i].Levels == nil {
			accessors[i].Levels = map[string]string{}
		}
		for _, dt := range dataTypes {
			if _, ok := accessors[i].Levels[dt]; !ok {
				accessors[i].Levels[dt] = types.AccessNone
			}
		}
	}

	return types.PermissionsOverview{
		DataTypes: dataTypes,
		Types:     types.BuildTypeAccess(dataTypes, accessors),
	}
}

// HandleAdminPermissionsOverview returns the type-centric permissions
// editor model (issue #160).
//
//	GET /v1/admin/permissions
//
// One entry per data type with its agent level and the non-agent accessors
// (apps, sync) that are relevant to that type. The admin map remains the
// "what they have accessed" view; this endpoint is "what they may access".
func (s *Server) HandleAdminPermissionsOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, s.permissionsOverview())
}
