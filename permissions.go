package main

import (
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

// DataTypeForPath classifies a service-proxy path into a Guardian data type.
// Unknown paths return "" so callers fall through to the default access level.
func DataTypeForPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// Drop query string if present.
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	// Normalize trailing slash (except root).
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimRight(p, "/")
	}

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
	case p == "/me/playlists" || strings.HasPrefix(p, "/playlists"):
		return "playlists"
	case p == "/me/contacts" || strings.HasPrefix(p, "/contacts"):
		return "contacts"
	case p == "/search":
		return "search"
	case p == "/me/subscriptions" || strings.HasPrefix(p, "/channels"):
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

// AllowDataPermission enforces the service's permission for a proxy request.
// Returns true when the request may proceed. On denial it writes a 403 JSON error.
func (s *Server) AllowDataPermission(w http.ResponseWriter, r *http.Request, svc *Service, remainingPath string) bool {
	if svc == nil {
		return false
	}
	// Capabilities metadata is always readable; it is filtered by permission.
	if remainingPath == "/capabilities" || remainingPath == "/capabilities/" {
		return true
	}
	dataType := DataTypeForPath(remainingPath)
	if svc.Permissions.Allows(dataType, r.Method) {
		return true
	}
	level := svc.Permissions.For(dataType)
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
