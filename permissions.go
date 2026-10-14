package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// writableDataSet reports data types the admin form may offer as Editable:
// agent-meaningful proxy write ops only. Mirror adapters and picker/search
// POSTs do not count — those are not "agents can edit this data type".
func (s *Server) writableDataSet(serviceID string, caps types.ServiceCapabilities) map[string]bool {
	set := map[string]bool{}
	for _, name := range types.WritableDataTypesFromCapabilities(caps) {
		set[name] = true
	}
	return set
}

// writableDataTypesFor returns the sorted list for admin payloads.
func (s *Server) writableDataTypesFor(serviceID string, caps types.ServiceCapabilities) []string {
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
func clampPermissionsWithWritable(p types.Permissions, caps types.ServiceCapabilities, writable map[string]bool) types.Permissions {
	if p == nil {
		return nil
	}
	known := map[string]bool{}
	for _, dt := range caps.DataTypes {
		known[dt.Name] = true
	}
	out := make(types.Permissions, len(p))
	for k, v := range p {
		level := types.NormalizeAccess(v)
		if k != types.PermissionDefaultKey && known[k] && !writable[k] &&
			(level == types.AccessWrite || level == types.AccessFull) {
			level = types.AccessRead
		}
		out[k] = level
	}
	return out
}

// connectorWriteBlocked reports whether a mutating proxy call is blocked
// because the connector does not implement any mutating op for that data
// type. Picker/search POSTs still count here so a configured write level can
// reach them; the admin form simply does not offer Editable for those.
func (s *Server) connectorWriteBlocked(serviceID, dataType, method string) bool {
	if !types.IsMutatingMethod(method) || dataType == "" || serviceID == "" {
		return false
	}
	conn, ok := s.connectors[serviceID]
	if !ok || conn == nil {
		return false
	}
	caps := conn.Capabilities()
	if !types.DataTypeKnown(caps, dataType) {
		return false
	}
	return !types.DataTypeHasWriteOp(caps, dataType)
}

// AllowDataPermission enforces the service's permission for a proxy request.
// Returns true when the request may proceed. On denial it writes a 403 JSON error.
// types.Permissions further constrain the connector: a write level never unlocks
// mutating calls the connector does not implement.
// remainingPath should already be canonical; unclassified paths use the
// strictest configured level, never "default" (issue #94).
func (s *Server) AllowDataPermission(w http.ResponseWriter, r *http.Request, svc *Service, remainingPath string) bool {
	if svc == nil {
		return false
	}
	// Capabilities metadata is always readable; it is filtered by permission.
	if remainingPath == "/capabilities" || remainingPath == "/capabilities/" {
		return true
	}
	dataType := types.DataTypeForPath(remainingPath)
	level := svc.Permissions.AccessForDataType(dataType)
	if s.connectorWriteBlocked(svc.ID, dataType, r.Method) {
		label := dataType
		if label == "" {
			label = "this data"
		}
		writeJSONError(w, http.StatusForbidden, fmt.Sprintf(
			"permission denied: %s does not support write access to %s (method %s)",
			svc.Name, label, r.Method,
		))
		return false
	}
	if types.AllowsMethod(level, r.Method) {
		return true
	}
	label := dataType
	if label == "" {
		label = "this data"
	}
	writeJSONError(w, http.StatusForbidden, fmt.Sprintf(
		"permission denied: agent access to %s on %s is %q (method %s)",
		label, svc.Name, level, r.Method,
	))
	return false
}

// permissionNotice returns a short system-instruction suffix listing denied
// data types so agents do not waste turns on blocked endpoints. Also warns
// when a type is free/busy only (issue #128).
func permissionNotice(p types.Permissions) string {
	var parts []string
	if denied := p.DeniedDataTypes(); len(denied) > 0 {
		parts = append(parts, "the following data types are not available and must not be requested: "+
			strings.Join(denied, ", "))
	}
	if limited := p.FreeBusyDataTypes(); len(limited) > 0 {
		parts = append(parts, strings.Join(limited, ", ")+
			" is free/busy only — responses include start_at, end_at, all_day, and status; never request titles, attendees, locations, or descriptions. Prefer GET /v1/library/freebusy for scheduling.")
	}
	if len(parts) == 0 {
		return ""
	}
	return "Access policy: " + strings.Join(parts, ". ") + "."
}

// allDataDenied reports whether every listed data type (and the default)
// is "none", so there is nothing useful to inject for the service.
func allDataDenied(p types.Permissions) bool {
	if p == nil {
		return false
	}
	if p.For("") != types.AccessNone {
		// Unlisted types are not fully denied.
		return false
	}
	for k, v := range p {
		if k == types.PermissionDefaultKey {
			continue
		}
		if types.NormalizeAccess(v) != types.AccessNone {
			return false
		}
	}
	return true
}
