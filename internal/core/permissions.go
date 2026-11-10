package core

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/shared"
)

// connectorWriteBlocked reports whether a mutating proxy call is blocked
// because the connector does not implement any mutating op for that data
// type. Picker/search POSTs still count here so a configured write level can
// reach them; the admin form simply does not offer Editable for those.
func (s *Server) connectorWriteBlocked(serviceID, kind, method string) bool {
	if !shared.IsMutatingMethod(method) || kind == "" || serviceID == "" {
		return false
	}
	conn, ok := s.connectors[serviceID]
	if !ok || conn == nil {
		return false
	}
	caps := conn.Capabilities()
	if !shared.KindKnown(caps, kind) {
		return false
	}
	return !shared.KindHasWriteOp(caps, kind)
}

// ownerGrants is the owner-tier rule map: the configured data grants
// materialized as kind → level, with the permissive default prefill. The
// legacy "omit = unrestricted" semantics live in that prefill, not in the
// resolution rule, which stays deny-by-default.
func (s *Server) ownerGrants() policy.Permissions {
	if s == nil || s.config == nil {
		return nil
	}
	return s.config.DataGrants().Levels().PrefillDefault()
}

// requestGrants returns the rule map for the request's key (see Grants).
func (s *Server) requestGrants(r *http.Request) policy.Permissions {
	g, _ := s.Grants(r)
	return g
}

// requestLevel returns the level the request's key grants for kind.
func (s *Server) requestLevel(r *http.Request, kind string) string {
	return s.requestGrants(r).Level(kind)
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
	kind := KindForPath(remainingPath)
	level := s.requestLevel(r, kind)
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

// allDataDenied reports whether the rule map grants nothing to any kind,
// so there is nothing useful to inject for the service.
func allDataDenied(p policy.Permissions) bool {
	if p == nil {
		return true
	}
	if p.Level("") != policy.AccessNone {
		// Unlisted kinds are not fully denied.
		return false
	}
	for k := range p {
		if k == policy.PermissionDefaultKey {
			continue
		}
		if p.Level(k) != policy.AccessNone {
			return false
		}
	}
	return true
}
