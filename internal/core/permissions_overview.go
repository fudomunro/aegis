package core

import (
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Sync accessor IDs in the holistic permissions view (issue #160).
const (
	syncMailID     = "sync:mail-archive"
	syncCopyID     = "sync:copy-ledger"
	syncTimelineID = "sync:timeline"
	syncQuickenID  = "sync:quicken"
	syncSignalID   = "sync:signal"
)

// agentAccessorID is the primary LLM-agent row (type-owned policy).
const agentAccessorID = "agent"

// permissionsOverview builds the kind-centric permissions model
// (issue #160): one row per kind with the agent policy level plus
// only the other accessors (apps, sync pipelines) that apply to that kind.
// The admin map shows what each accessor has accessed; this view shows
// what each is allowed to access.
func (s *Server) permissionsOverview() policy.PermissionsOverview {
	preferred := make([]string, 0, 16)
	for _, kind := range s.allLibraryKinds() {
		key := kind.PermType
		if key == "" {
			key = kind.ID
		}
		preferred = append(preferred, key)
	}

	policies := s.config.DataGrants()
	var extra []string
	extra = append(extra, policy.ExtraPolicyKeys(policies)...)
	for _, svc := range s.config.ServiceSnapshots() {
		extra = append(extra, policy.ExtraPermissionKeys(svc.GrantLimits)...)
	}
	if s.apps != nil {
		for _, app := range s.apps.List() {
			extra = append(extra, policy.ExtraPermissionKeys(app.Scopes)...)
		}
	}

	kinds := policy.OverviewKinds(preferred, extra)
	accessors := make([]policy.Accessor, 0, 8)

	// Agents: type-owned policy is primary (issue #148).
	accessors = append(accessors, policy.Accessor{
		ID:     agentAccessorID,
		Kind:   policy.AccessorAgent,
		Label:  "LLM agents",
		Detail: "Agents reaching Aegis through Aperture or direct proxy/library calls. Data grants are primary; per-service forms only tighten.",
		Levels: policy.AgentOverviewLevels(policies, kinds),
		// Edited via the data-grant form in the data grants editor.
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
			scopes := make(policy.Permissions, len(app.Scopes))
			for k, v := range app.Scopes {
				scopes[k] = v
			}
			levels, explicit := policy.AppOverviewLevels(scopes, kinds)
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
	if s.mail != nil {
		accessors = append(accessors, policy.Accessor{
			ID:       syncMailID,
			Kind:     policy.AccessorSync,
			Label:    "Mail archive sync",
			Detail:   "Mail ingest into the local mail archive (live sync and export import). Message bodies stay on the source; Aegis keeps a compressed search copy.",
			Levels:   map[string]string{"messages": policy.AccessRead},
			Explicit: map[string]bool{"messages": true},
		})
	}
	if s.copyLedger != nil {
		// Copy ledger stamps provenance for mirror writes across types.
		// No fixed type list — show only when a kind has copy activity.
		levels := map[string]string{}
		explicit := map[string]bool{}
		for _, rec := range s.copyLedger.List(copies.Filter{}) {
			if rec.Kind == "" {
				continue
			}
			levels[rec.Kind] = policy.AccessWrite
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
	if s.timeline != nil {
		accessors = append(accessors, policy.Accessor{
			ID:       syncTimelineID,
			Kind:     policy.AccessorSync,
			Label:    "Timeline import",
			Detail:   "Google Timeline export ingest into the local places store.",
			Levels:   map[string]string{"places": policy.AccessRead},
			Explicit: map[string]bool{"places": true},
		})
	}
	if s.quicken != nil {
		accessors = append(accessors, policy.Accessor{
			ID:       syncQuickenID,
			Kind:     policy.AccessorSync,
			Label:    "Quicken import",
			Detail:   "QIF/QFX/OFX import into the local transaction store.",
			Levels:   map[string]string{"transactions": policy.AccessRead},
			Explicit: map[string]bool{"transactions": true},
		})
	}
	if s.signal != nil {
		accessors = append(accessors, policy.Accessor{
			ID:     syncSignalID,
			Kind:   policy.AccessorSync,
			Label:  "Signal import",
			Detail: "File-based Signal export ingest into the local contacts/messages store. Attachments are never stored.",
			Levels: map[string]string{
				"contacts": policy.AccessRead,
				"messages": policy.AccessRead,
			},
			Explicit: map[string]bool{"contacts": true, "messages": true},
		})
	}

	// Services Aegis syncs this data with (issue #160). Read-only rows so
	// operators can see Spotify / YouTube Music under playlists, etc.
	// Capability and provider interfaces decide relevance per type.
	for _, svc := range s.config.ServiceSnapshots() {
		if svc.Action == "block" {
			continue
		}
		conn, ok := s.connectors[svc.ID]
		if !ok || conn == nil {
			continue
		}
		provided := serviceProvidedTypes(conn)
		if len(provided) == 0 {
			continue
		}
		levels := make(map[string]string, len(provided))
		explicit := make(map[string]bool, len(provided))
		for _, dt := range provided {
			levels[dt] = policy.AccessRead
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
			// Service grant_limits are edited on the service card.
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
		for _, dt := range kinds {
			if _, ok := accessors[i].Levels[dt]; !ok {
				accessors[i].Levels[dt] = policy.AccessNone
			}
		}
	}

	return policy.PermissionsOverview{
		Kinds: kinds,
		Rows:  policy.BuildKindAccess(kinds, accessors),
	}
}

// serviceProvidedTypes lists library/permission keys a connector actually
// syncs (issue #160): unified-library provider interfaces first. Capability
// names are ignored except for those same library kinds, so proxy-only
// surfaces (player, search) do not invent service rows under a type.
func serviceProvidedTypes(conn any) []string {
	// Library kinds Aegis merges across services (permission keys).
	// Derived from the registry (issue #183).
	libraryTypes := map[string]bool{}
	for _, id := range kinds.BuiltinKindIDs() {
		libraryTypes[id] = true
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || name == policy.PermissionDefaultKey || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	if _, ok := conn.(kinds.PlaylistProvider); ok {
		add("playlists")
	}
	if _, ok := conn.(kinds.LikedTrackProvider); ok {
		add("tracks")
	}
	if _, ok := conn.(kinds.AlbumProvider); ok {
		add("albums")
	}
	if _, ok := conn.(kinds.ArtistProvider); ok {
		add("artists")
	}
	if _, ok := conn.(kinds.ContactProvider); ok {
		add("contacts")
	}
	if _, ok := conn.(kinds.PhotoProvider); ok {
		add("photos")
	}
	if _, ok := conn.(kinds.CalendarEventProvider); ok {
		add("events")
	}
	if _, ok := conn.(kinds.MessageProvider); ok {
		add("messages")
	}
	if _, ok := conn.(kinds.TransactionProvider); ok {
		add("transactions")
	}
	if _, ok := conn.(kinds.PlaceVisitProvider); ok {
		add("places")
	}
	if _, ok := conn.(kinds.DocumentProvider); ok {
		add("documents")
	}

	// Advertised capability names, limited to library kinds (issue #160).
	type named interface {
		Capabilities() shared.ServiceCapabilities
	}
	if c, ok := conn.(named); ok {
		for _, dt := range c.Capabilities().Kinds {
			if libraryTypes[dt.Name] {
				add(dt.Name)
			}
		}
	}

	sort.Strings(out)
	return out
}

// HandleAdminPermissionsOverview returns the kind-centric permissions
// editor model (issue #160).
//
//	GET /v1/admin/permissions
//
// One entry per kind with its agent level and the non-agent accessors
// (apps, sync) that are relevant to that kind. The admin map remains the
// "what they have accessed" view; this endpoint is "what they may access".
func (s *Server) HandleAdminPermissionsOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	shared.WriteJSON(w, s.permissionsOverview())
}
