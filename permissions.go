package main

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Access levels for agent access to a data type.
const (
	// AccessNone denies all operations on the data type.
	AccessNone = "none"
	// AccessRead allows safe methods (GET, HEAD).
	AccessRead = "read"
	// AccessWrite allows mutating methods (POST, PUT, PATCH, DELETE).
	AccessWrite = "write"
	// AccessFull allows both read and write methods.
	AccessFull = "read-write"
)

// DefaultAccessType is used when a service has no permissions block, or when
// a data type is not listed and no "default" key is set. Preserves pre-permission
// behavior: authenticated agents may call any operation.
const DefaultAccessType = AccessFull

// PermissionDefaultKey is the reserved permissions map key that sets the level
// for data types not listed explicitly.
const PermissionDefaultKey = "default"

// Permissions maps data-type names to access levels. The reserved key
// "default" applies to types not listed. A nil map means unrestricted access.
type Permissions map[string]string

// For returns the access level for a data type.
// Unknown/empty data types fall through to "default" (or DefaultAccessType).
// Proxy callers with an unclassified path must use Strictest instead so
// unknown surface cannot outrank a tighter type (issue #94).
func (p Permissions) For(dataType string) string {
	if p == nil {
		return DefaultAccessType
	}
	level, ok := p[dataType]
	if ok {
		return normalizeAccess(level)
	}
	if level, ok := p[PermissionDefaultKey]; ok {
		return normalizeAccess(level)
	}
	return DefaultAccessType
}

// Strictest returns the most restrictive access level configured on the map.
// Used when a proxy path does not map to a known data type: unknown endpoints
// must not inherit "default" if any type is tighter (issue #94). A nil or empty
// map is unrestricted. If reads and writes both appear, no single level is
// tighter than every known type for an unknown method class — deny.
func (p Permissions) Strictest() string {
	if len(p) == 0 {
		return DefaultAccessType
	}
	hasNone, hasRead, hasWrite, hasFull := false, false, false, false
	for _, v := range p {
		switch normalizeAccess(v) {
		case AccessNone:
			hasNone = true
		case AccessRead:
			hasRead = true
		case AccessWrite:
			hasWrite = true
		case AccessFull:
			hasFull = true
		}
	}
	if hasNone || (hasRead && hasWrite) {
		return AccessNone
	}
	if hasRead {
		return AccessRead
	}
	if hasWrite {
		return AccessWrite
	}
	if hasFull {
		return AccessFull
	}
	return DefaultAccessType
}

// accessForDataType resolves the effective level for a classified path.
// Unclassified paths use Strictest, never the default key (issue #94).
func (p Permissions) accessForDataType(dataType string) string {
	if dataType == "" {
		return p.Strictest()
	}
	return p.For(dataType)
}

// Allows reports whether the given HTTP method is permitted for the data type.
func (p Permissions) Allows(dataType, method string) bool {
	return allowsMethod(p.For(dataType), method)
}

// AllowsRead reports whether read (GET/HEAD) is permitted for the data type.
func (p Permissions) AllowsRead(dataType string) bool {
	return allowsMethod(p.For(dataType), http.MethodGet)
}

// AllowsWrite reports whether any mutating method is permitted for the data type.
func (p Permissions) AllowsWrite(dataType string) bool {
	return allowsMethod(p.For(dataType), http.MethodPost)
}

// DeniedDataTypes returns sorted data-type names explicitly set to "none".
// The reserved "default" key is omitted.
func (p Permissions) DeniedDataTypes() []string {
	if p == nil {
		return nil
	}
	var denied []string
	for k, v := range p {
		if k == PermissionDefaultKey {
			continue
		}
		if normalizeAccess(v) == AccessNone {
			denied = append(denied, k)
		}
	}
	sort.Strings(denied)
	return denied
}

// Clone returns a shallow copy suitable for admin/API responses.
func (p Permissions) Clone() map[string]string {
	if p == nil {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = normalizeAccess(v)
	}
	return out
}

// ValidatePermissions checks that every level is a known access type.
func ValidatePermissions(p Permissions) error {
	for k, v := range p {
		if k == "" {
			return fmt.Errorf("permissions: empty data type key")
		}
		switch normalizeAccess(v) {
		case AccessNone, AccessRead, AccessWrite, AccessFull:
		default:
			return fmt.Errorf("permissions[%s]: unknown access level %q (use none, read, write, or read-write)", k, v)
		}
	}
	return nil
}

func normalizeAccess(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case AccessNone, "deny", "off", "false":
		return AccessNone
	case AccessRead, "ro", "readonly", "read-only":
		return AccessRead
	case AccessWrite, "wo", "writeonly", "write-only", "mutate":
		return AccessWrite
	case AccessFull, "rw", "readwrite", "read_write":
		return AccessFull
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}

func allowsMethod(level, method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return level == AccessRead || level == AccessFull
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return level == AccessWrite || level == AccessFull
	default:
		// Unknown methods are treated as mutations.
		return level == AccessWrite || level == AccessFull
	}
}

// errNonCanonicalPath is returned for proxy paths that are not in canonical
// form (dot segments, backslashes, NUL). Callers must reject with 400 rather
// than classify or forward them (issue #94).
var errNonCanonicalPath = errors.New("non-canonical path")

// canonicalProxyPath normalizes a service-proxy remaining path so permission
// classification and upstream routing see the same string (issue #94).
// Duplicate slashes are collapsed. "." / ".." segments and other non-canonical
// forms are rejected. Empty means the service root.
func canonicalProxyPath(remaining string) (string, error) {
	if remaining == "" {
		return "", nil
	}
	if !strings.HasPrefix(remaining, "/") {
		remaining = "/" + remaining
	}
	// URL.Path is already percent-decoded (%2f → /). Reject leftover characters
	// that can still smuggle alternate path forms through to upstream.
	if strings.ContainsAny(remaining, "\\\x00") {
		return "", errNonCanonicalPath
	}
	segs := strings.Split(remaining, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch seg {
		case "":
			// Collapse leading/duplicate/trailing slashes.
			continue
		case ".", "..":
			return "", errNonCanonicalPath
		default:
			out = append(out, seg)
		}
	}
	if len(out) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(out, "/"), nil
}

// DataTypeForPath classifies a service-proxy path into a Aegis data type.
// Unknown or non-canonical paths return "" so callers must apply the
// strictest configured permission (not "default") — see accessForDataType.
func DataTypeForPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	// Drop query string if present.
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	clean, err := canonicalProxyPath(p)
	if err != nil {
		return ""
	}
	if clean == "" || clean == "/" {
		return ""
	}
	p = clean

	switch {
	case p == "/me":
		return "user"
	case strings.HasPrefix(p, "/me/player"):
		return "player"
	case p == "/me/tracks" || strings.HasPrefix(p, "/tracks"):
		return "tracks"
	case p == "/me/liked" || p == "/me/top/tracks":
		return "tracks"
	case strings.HasPrefix(p, "/videos"):
		return "tracks"
	case p == "/me/albums" || strings.HasPrefix(p, "/albums"):
		return "albums"
	case p == "/me/artists" || p == "/me/top/artists" || strings.HasPrefix(p, "/artists"):
		return "artists"
	// Followed artists: Spotify /me/following and YouTube subscriptions back
	// the unified artists library, so they share the artists permission key.
	case p == "/me/following":
		return "artists"
	case p == "/me/playlists" || strings.HasPrefix(p, "/playlists"):
		return "playlists"
	case p == "/me/contacts" || strings.HasPrefix(p, "/contacts") || strings.HasPrefix(p, "/people"):
		return "contacts"
	case p == "/me/events" || strings.HasPrefix(p, "/events") || strings.HasPrefix(p, "/calendars") ||
		p == "/me/calendars" || strings.HasPrefix(p, "/calendarList") || strings.HasPrefix(p, "/users/me/calendarList"):
		return "events"
	case p == "/me/messages" || strings.HasPrefix(p, "/messages") ||
		p == "/me/threads" || strings.HasPrefix(p, "/threads") ||
		strings.HasPrefix(p, "/me/mail") || strings.HasPrefix(p, "/gmail") ||
		strings.HasPrefix(p, "/users/me/messages") || strings.HasPrefix(p, "/users/me/threads"):
		return "messages"
	case p == "/me/photos" || strings.HasPrefix(p, "/photos") || strings.HasPrefix(p, "/mediaItems") || strings.HasPrefix(p, "/picker"):
		return "photos"
	// Self-hosted / foreign photo stores (Immich, …). /search/metadata is the
	// Immich asset search; keep it under photos so the permission key matches
	// the unified photos library.
	case strings.HasPrefix(p, "/assets") || p == "/search/metadata":
		return "photos"
	case p == "/search":
		return "search"
	case p == "/me/subscriptions":
		return "artists"
	case strings.HasPrefix(p, "/channels"):
		return "channels"
	case strings.HasPrefix(p, "/me/library"):
		return "library"
	case strings.HasPrefix(p, "/me/recommendations"):
		return "recommendations"
	default:
		return ""
	}
}

// FilterCapabilities removes operations the permission map does not allow.
// Data types set to "none" are dropped entirely; other types keep only
// operations whose HTTP method is permitted.
func FilterCapabilities(caps ServiceCapabilities, p Permissions) ServiceCapabilities {
	if p == nil {
		return caps
	}
	out := ServiceCapabilities{DataTypes: make([]DataTypeCapability, 0, len(caps.DataTypes))}
	for _, dt := range caps.DataTypes {
		level := p.For(dt.Name)
		if level == AccessNone {
			continue
		}
		ops := make([]Operation, 0, len(dt.Operations))
		for _, op := range dt.Operations {
			method := op.Method
			if method == "" {
				method = http.MethodGet
			}
			if allowsMethod(level, method) {
				ops = append(ops, op)
			}
		}
		dt.Operations = ops
		out.DataTypes = append(out.DataTypes, dt)
	}
	return out
}

// isMutatingMethod reports whether the HTTP method changes data.
func isMutatingMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// dataTypeKnown reports whether the connector advertises the data type.
func dataTypeKnown(caps ServiceCapabilities, dataType string) bool {
	if dataType == "" {
		return false
	}
	for _, dt := range caps.DataTypes {
		if dt.Name == dataType {
			return true
		}
	}
	return false
}

// dataTypeHasWriteOp reports whether the connector exposes any mutating
// operation for the data type (including session/search POSTs). Used for
// proxy enforcement: a permission write level never unlocks methods the
// connector does not implement at all.
func dataTypeHasWriteOp(caps ServiceCapabilities, dataType string) bool {
	if dataType == "" {
		return false
	}
	for _, dt := range caps.DataTypes {
		if dt.Name != dataType {
			continue
		}
		for _, op := range dt.Operations {
			method := op.Method
			if method == "" {
				method = http.MethodGet
			}
			if isMutatingMethod(method) {
				return true
			}
		}
	}
	return false
}

// isAgentDataWritePath filters mutating ops that are not "agents may change
// this data type". Picker session creates and metadata search POSTs are
// operational helpers, not library write surface for the permissions form.
func isAgentDataWritePath(path string) bool {
	p := strings.ToLower(path)
	switch {
	case strings.Contains(p, "/picker/"):
		return false
	case strings.Contains(p, "/search"):
		return false
	default:
		return true
	}
}

// dataTypeHasAgentWriteOp reports whether the connector exposes a mutating
// operation that actually changes data of this type (not picker/search).
func dataTypeHasAgentWriteOp(caps ServiceCapabilities, dataType string) bool {
	if dataType == "" {
		return false
	}
	for _, dt := range caps.DataTypes {
		if dt.Name != dataType {
			continue
		}
		for _, op := range dt.Operations {
			method := op.Method
			if method == "" {
				method = http.MethodGet
			}
			if isMutatingMethod(method) && isAgentDataWritePath(op.Path) {
				return true
			}
		}
	}
	return false
}

// writableDataTypesFromCapabilities lists data types with at least one
// agent-meaningful mutating connector operation.
func writableDataTypesFromCapabilities(caps ServiceCapabilities) []string {
	var out []string
	for _, dt := range caps.DataTypes {
		if dataTypeHasAgentWriteOp(caps, dt.Name) {
			out = append(out, dt.Name)
		}
	}
	sort.Strings(out)
	return out
}

// writableDataSet reports data types the admin form may offer as Editable:
// agent-meaningful proxy write ops only. Mirror adapters and picker/search
// POSTs do not count — those are not "agents can edit this data type".
func (s *Server) writableDataSet(serviceID string, caps ServiceCapabilities) map[string]bool {
	set := map[string]bool{}
	for _, name := range writableDataTypesFromCapabilities(caps) {
		set[name] = true
	}
	return set
}

// writableDataTypesFor returns the sorted list for admin payloads.
func (s *Server) writableDataTypesFor(serviceID string, caps ServiceCapabilities) []string {
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
func clampPermissionsWithWritable(p Permissions, caps ServiceCapabilities, writable map[string]bool) Permissions {
	if p == nil {
		return nil
	}
	known := map[string]bool{}
	for _, dt := range caps.DataTypes {
		known[dt.Name] = true
	}
	out := make(Permissions, len(p))
	for k, v := range p {
		level := normalizeAccess(v)
		if k != PermissionDefaultKey && known[k] && !writable[k] &&
			(level == AccessWrite || level == AccessFull) {
			level = AccessRead
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
	if !isMutatingMethod(method) || dataType == "" || serviceID == "" {
		return false
	}
	conn, ok := s.connectors[serviceID]
	if !ok || conn == nil {
		return false
	}
	caps := conn.Capabilities()
	if !dataTypeKnown(caps, dataType) {
		return false
	}
	return !dataTypeHasWriteOp(caps, dataType)
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
	// Capabilities metadata is always readable; it is filtered by permission.
	if remainingPath == "/capabilities" || remainingPath == "/capabilities/" {
		return true
	}
	dataType := DataTypeForPath(remainingPath)
	level := svc.Permissions.accessForDataType(dataType)
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
	if allowsMethod(level, r.Method) {
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
// data types so agents do not waste turns on blocked endpoints.
func permissionNotice(p Permissions) string {
	denied := p.DeniedDataTypes()
	if len(denied) == 0 {
		return ""
	}
	return "Access policy: the following data types are not available and must not be requested: " +
		strings.Join(denied, ", ") + "."
}

// allDataDenied reports whether every listed data type (and the default)
// is "none", so there is nothing useful to inject for the service.
func allDataDenied(p Permissions) bool {
	if p == nil {
		return false
	}
	if p.For("") != AccessNone {
		// Unlisted types are not fully denied.
		return false
	}
	for k, v := range p {
		if k == PermissionDefaultKey {
			continue
		}
		if normalizeAccess(v) != AccessNone {
			return false
		}
	}
	return true
}
