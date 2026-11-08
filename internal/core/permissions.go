package core

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

// writableDataSet reports kinds the admin form may offer as Editable:
// agent-meaningful proxy write ops only. Mirror adapters and picker/search
// POSTs do not count — those are not "agents can edit this kind".
func (s *Server) writableDataSet(serviceID string, caps types.ServiceCapabilities) map[string]bool {
	set := map[string]bool{}
	for _, name := range types.WritableKindsFromCapabilities(caps) {
		set[name] = true
	}
	return set
}

// writableKindsFor returns the sorted list for admin payloads.
func (s *Server) writableKindsFor(serviceID string, caps types.ServiceCapabilities) []string {
	set := s.writableDataSet(serviceID, caps)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// clampPermissionsWithWritable further constrains write levels so a saved
// permission never exceeds types the form may offer as Editable. Known
// read-only types become read, so later agent-write support does not
// auto-open — the operator must enable it explicitly. Unknown keys and
// "default" are preserved.
func clampPermissionsWithWritable(p policy.Permissions, caps types.ServiceCapabilities, writable map[string]bool) policy.Permissions {
	if p == nil {
		return nil
	}
	known := map[string]bool{}
	for _, dt := range caps.Kinds {
		known[dt.Name] = true
	}
	out := make(policy.Permissions, len(p))
	for k, v := range p {
		level := policy.NormalizeAccess(v)
		if k != policy.PermissionDefaultKey && known[k] && !writable[k] &&
			(level == policy.AccessWrite || level == policy.AccessFull) {
			level = policy.AccessRead
		}
		out[k] = level
	}
	return out
}

// connectorWriteBlocked reports whether a mutating proxy call is blocked
// because the connector does not implement any mutating op for that data
// type. Picker/search POSTs still count here so a configured write level can
// reach them; the admin form simply does not offer Editable for those.
func (s *Server) connectorWriteBlocked(serviceID, kind, method string) bool {
	if !types.IsMutatingMethod(method) || kind == "" || serviceID == "" {
		return false
	}
	conn, ok := s.connectors[serviceID]
	if !ok || conn == nil {
		return false
	}
	caps := conn.Capabilities()
	if !types.KindKnown(caps, kind) {
		return false
	}
	return !types.KindHasWriteOp(caps, kind)
}

// effectivePermissions resolves agent access for a service from data grants
// plus legacy service permissions (tighten-only). Data grant is primary
// (issue #148); the service map remains a migration override.
func (s *Server) effectivePermissions(svc *Service) policy.Permissions {
	if svc == nil || s == nil || s.config == nil {
		return nil
	}
	return s.config.EffectivePermissions(svc.ID)
}

// requestLevel returns the effective access level for a kind on a
// service for this request: owner-tier effective permissions tightened by
// the principal's scope grants (issue #253). Scope grants never open what
// owner policy closed — the level is the stricter of both layers. Internal
// owner-tier fetches (search-index writes) skip the tightening so the index
// stores the partition's fullest rows (issue #272).
func (s *Server) requestLevel(r *http.Request, svc *Service, kind string) string {
	level := s.effectivePermissions(svc).AccessForKind(kind)
	sc := scopes.From(r)
	if sc == nil || kinds.OwnerView(r) {
		return level
	}
	provider := ""
	if svc != nil {
		provider = svc.ID
	}
	return policy.ScopeTighten(sc.Grants, level, kind, provider)
}

// requestPermissions is requestLevel's map view: owner-tier effective
// permissions AND scope grants, key by key. Used where a permission map
// feeds notices and all-denied checks.
func (s *Server) requestPermissions(r *http.Request, svc *Service) policy.Permissions {
	owner := s.effectivePermissions(svc)
	sc := scopes.From(r)
	if sc == nil {
		return owner
	}
	return policy.ScopeTightenPermissions(sc.Grants, owner)
}

// AllowDataPermission enforces the service's permission for a proxy request.
// Returns true when the request may proceed. On denial it writes a 403 JSON error.
// Permissions further constrain the connector: a write level never unlocks
// mutating calls the connector does not implement.
// remainingPath should already be canonical; unclassified paths use the
// strictest configured level, never "default" (issue #94).
func (s *Server) AllowDataPermission(w http.ResponseWriter, r *http.Request, svc *Service, remainingPath string) bool {
	if svc == nil {
		return false
	}
	kind := types.KindForPath(remainingPath)
	level := s.requestLevel(r, svc, kind)
	if s.connectorWriteBlocked(svc.ID, kind, r.Method) {
		label := kind
		if label == "" {
			label = "this data"
		}
		shared.WriteJSONError(w, http.StatusForbidden, fmt.Sprintf(
			"denied: %s does not support write access to %s (method %s)",
			svc.Name, label, r.Method,
		))
		return false
	}
	if policy.AllowsMethod(level, r.Method) {
		return true
	}
	label := kind
	if label == "" {
		label = "this data"
	}
	shared.WriteJSONError(w, http.StatusForbidden, fmt.Sprintf(
		"denied: data grant for agent access to %s on %s is %q (method %s)",
		label, svc.Name, level, r.Method,
	))
	return false
}

// permissionNotice returns a short system-instruction suffix listing denied
// kinds so agents do not waste turns on blocked endpoints. Also warns
// when a type is free/busy only (issue #128).
func permissionNotice(p policy.Permissions) string {
	var parts []string
	if denied := p.DeniedKinds(); len(denied) > 0 {
		parts = append(parts, "the following kinds are not available and must not be requested: "+
			strings.Join(denied, ", "))
	}
	if limited := p.FreeBusyKinds(); len(limited) > 0 {
		parts = append(parts, strings.Join(limited, ", ")+
			" is free/busy only — responses include start_at, end_at, all_day, and status; never request titles, attendees, locations, or descriptions. Prefer GET /v1/library/freebusy for scheduling.")
	}
	if len(parts) == 0 {
		return ""
	}
	return "Data grants: " + strings.Join(parts, ". ") + "."
}

// allDataDenied reports whether every listed kind (and the default)
// is "none", so there is nothing useful to inject for the service.
func allDataDenied(p policy.Permissions) bool {
	if p == nil {
		return false
	}
	if p.For("") != policy.AccessNone {
		// Unlisted types are not fully denied.
		return false
	}
	for k, v := range p {
		if k == policy.PermissionDefaultKey {
			continue
		}
		if policy.NormalizeAccess(v) != policy.AccessNone {
			return false
		}
	}
	return true
}
