package admin

import (
	"net/http"
	"sort"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Sync accessor IDs in the holistic permissions view (issue #160).
// Local import stores use "sync:<stem>" derived from their spec
// (issue #393).
const (
	SyncMailID = "sync:mail-archive"
	syncCopyID = "sync:copy-ledger"
)

// agentAccessorID is the primary LLM-agent row (type-owned policy).
const agentAccessorID = "agent"

// permissionsOverview builds the kind-centric permissions model
// (issue #160): one row per kind with the agent policy level plus
// only the other accessors (apps, sync pipelines) that apply to that kind.
// The admin map shows what each accessor has accessed; this view shows
// what each is allowed to access.
func (h *Handlers) PermissionsOverview() policy.PermissionsOverview {
	preferred := make([]string, 0, 16)
	for _, kind := range h.KindCatalog.AllLibraryKinds() {
		key := kind.PermType
		if key == "" {
			key = kind.ID
		}
		preferred = append(preferred, key)
	}

	policies := h.Host.DataGrants()
	var extra []string
	extra = append(extra, policy.ExtraPolicyKeys(policies)...)
	if h.Apps != nil {
		for _, app := range h.Apps.List() {
			extra = append(extra, policy.ExtraPermissionKeys(app.Scopes)...)
		}
	}

	kindList := policy.OverviewKinds(preferred, extra)
	accessors := make([]policy.Accessor, 0, 8)

	// Agents: type-owned policy is primary (issue #148).
	accessors = append(accessors, policy.Accessor{
		ID:     agentAccessorID,
		Kind:   policy.AccessorAgent,
		Label:  "LLM agents",
		Detail: "Agents reaching Aegis through Aperture or direct proxy/library calls. One grant map per key, deny-by-default.",
		Levels: policy.AgentOverviewLevels(policies, kindList),
		// Edited via the data-grant form in the data grants editor.
		Editable: true,
	})

	// Third-party apps (issue #131).
	if h.Apps != nil {
		apps := h.Apps.List()
		sort.Slice(apps, func(i, j int) bool {
			if apps[i].Name != apps[j].Name {
				return apps[i].Name < apps[j].Name
			}
			return apps[i].ID < apps[j].ID
		})
		for _, app := range apps {
			scopes := make(shared.GrantMap, len(app.Scopes))
			for k, v := range app.Scopes {
				scopes[k] = v
			}
			levels, explicit := policy.AppOverviewLevels(scopes, kindList)
			accessors = append(accessors, policy.Accessor{
				ID:       "app:" + app.ID,
				Kind:     policy.AccessorApp,
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
	// relevant under a kind row.
	if h.Mail != nil {
		accessors = append(accessors, policy.Accessor{
			ID:       SyncMailID,
			Kind:     policy.AccessorSync,
			Label:    "Mail archive sync",
			Detail:   "Mail ingest into the local mail archive (live sync and export import). Message bodies stay on the source; Aegis keeps a compressed search copy.",
			Levels:   map[string]string{"messages": shared.AccessRead},
			Explicit: map[string]bool{"messages": true},
		})
	}
	if h.Copies != nil {
		// Copy ledger stamps provenance for mirror writes across types.
		// No fixed type list — show only when a kind has copy activity.
		levels := map[string]string{}
		explicit := map[string]bool{}
		for _, rec := range h.Copies.List(copies.Filter{}) {
			if rec.Kind == "" {
				continue
			}
			levels[rec.Kind] = shared.AccessWrite
			explicit[rec.Kind] = true
		}
		accessors = append(accessors, policy.Accessor{
			ID:       syncCopyID,
			Kind:     policy.AccessorSync,
			Label:    "Copy ledger",
			Detail:   "Mirror/copy jobs stamp provenance when Aegis writes copies to other services. Aegis does not store media.",
			Levels:   levels,
			Explicit: explicit,
		})
	}
	// Local import stores (issue #393): one sync accessor per enabled
	// store. Kinds derive from the connector's ProvidedTypes; the detail
	// copy is the store's declared admin surface.
	for _, spec := range connectors.LocalImportStoreSpecs() {
		store := h.LocalStores[spec.Stem]
		if store == nil || !store.Enabled() {
			continue
		}
		levels := map[string]string{}
		explicit := map[string]bool{}
		for _, kind := range kinds.ProvidedTypes(spec.NewConnector(store)) {
			levels[kind] = shared.AccessRead
			explicit[kind] = true
		}
		accessors = append(accessors, policy.Accessor{
			ID:       "sync:" + spec.Stem,
			Kind:     policy.AccessorSync,
			Label:    spec.Service.Name + " import",
			Detail:   ImportStoreAdmin{Spec: spec, Store: store}.Admin().AccessorDetail,
			Levels:   levels,
			Explicit: explicit,
		})
	}

	// Services Aegis syncs this data with (issue #160). Read-only rows so
	// operators can see Spotify / YouTube Music under playlists, etc.
	// Capability and provider interfaces decide relevance per type.
	for _, svc := range h.Host.ServiceRows() {
		if svc.Action == "block" {
			continue
		}
		conn, ok := h.Connectors[svc.ID]
		if !ok || conn == nil {
			continue
		}
		provided := kinds.ProvidedTypes(conn)
		if len(provided) == 0 {
			continue
		}
		levels := make(map[string]string, len(provided))
		explicit := make(map[string]bool, len(provided))
		for _, dt := range provided {
			levels[dt] = shared.AccessRead
			explicit[dt] = true
		}
		name := svc.Name
		if name == "" {
			name = svc.ID
		}
		accessors = append(accessors, policy.Accessor{
			ID:       "service:" + svc.ID,
			Kind:     policy.AccessorService,
			Label:    name,
			Detail:   "Service Aegis syncs with for this kind (read-only here).",
			Levels:   levels,
			Explicit: explicit,
			// Informational only — data grants own access.
			Editable: false,
		})
	}

	// Fill non-agent missing levels with none so BuildKindAccess can read
	// a concrete level; relevance still uses Explicit / non-none checks.
	for i := range accessors {
		if accessors[i].Kind == policy.AccessorAgent {
			continue
		}
		if accessors[i].Levels == nil {
			accessors[i].Levels = map[string]string{}
		}
		for _, dt := range kindList {
			if _, ok := accessors[i].Levels[dt]; !ok {
				accessors[i].Levels[dt] = shared.AccessNone
			}
		}
	}

	return policy.PermissionsOverview{
		Kinds: kindList,
		Rows:  policy.BuildKindAccess(kindList, accessors),
	}
}

// serviceProvidedTypes lists library/permission keys a connector actually
// syncs (issue #160): unified-library provider interfaces first. Capability
// names are ignored except for those same library kinds, so proxy-only
// surfaces (player, search) do not invent service rows under a type.

// HandleAdminPermissionsOverview returns the kind-centric permissions
// editor model (issue #160).
//
//	GET /v1/admin/permissions
//
// One entry per kind with its agent level and the non-agent accessors
// (apps, sync) that are relevant to that kind. The admin map remains the
// "what they have accessed" view; this endpoint is "what they may access".
func (h *Handlers) HandleAdminPermissionsOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	shared.WriteJSON(w, h.PermissionsOverview())
}
