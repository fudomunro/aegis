package types

import (
	"net/http"
	"sort"
	"strings"
)

// ServiceCapabilities describes what a connector can do.
type ServiceCapabilities struct {
	DataTypes []DataTypeCapability `json:"data_types"`
}

// DataTypeCapability describes supported operations for a specific data type.
type DataTypeCapability struct {
	Name        string      `json:"name"`        // e.g. "tracks", "playlists", "search"
	Description string      `json:"description"` // human-readable description
	Operations  []Operation `json:"operations"`
}

// Operation describes a single callable operation on a data type.
type Operation struct {
	Name        string       `json:"name"`        // "read_saved", "search", "get_by_id"
	Description string       `json:"description"` // human-readable
	Method      string       `json:"method"`      // "GET", "POST", etc.
	Path        string       `json:"path"`        // "/me/tracks"
	Parameters  []Parameter  `json:"parameters,omitempty"`
	Response    ResponseSpec `json:"response"`
}

// Parameter describes a query or path parameter for an operation.
type Parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`   // "query" or "path"
	Type        string `json:"type"` // "string", "integer", "boolean"
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ResponseSpec describes what the operation returns.
type ResponseSpec struct {
	Description string   `json:"description"`
	Fields      []string `json:"fields,omitempty"` // top-level field names in the response
}

// IsMutatingMethod reports whether the HTTP method changes data.
func IsMutatingMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// DataTypeKnown reports whether the connector advertises the data type.
func DataTypeKnown(caps ServiceCapabilities, dataType string) bool {
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

// DataTypeHasWriteOp reports whether the connector exposes any mutating
// operation for the data type (including session/search POSTs). Used for
// proxy enforcement: a permission write level never unlocks methods the
// connector does not implement at all.
func DataTypeHasWriteOp(caps ServiceCapabilities, dataType string) bool {
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
			if IsMutatingMethod(method) {
				return true
			}
		}
	}
	return false
}

// DataTypeHasAgentWriteOp reports whether the connector exposes a mutating
// operation that actually changes data of this type (not picker/search).
func DataTypeHasAgentWriteOp(caps ServiceCapabilities, dataType string) bool {
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
			if IsMutatingMethod(method) && IsAgentDataWritePath(op.Path) {
				return true
			}
		}
	}
	return false
}

// WritableDataTypesFromCapabilities lists data types with at least one
// agent-meaningful mutating connector operation.
func WritableDataTypesFromCapabilities(caps ServiceCapabilities) []string {
	var out []string
	for _, dt := range caps.DataTypes {
		if DataTypeHasAgentWriteOp(caps, dt.Name) {
			out = append(out, dt.Name)
		}
	}
	sort.Strings(out)
	return out
}

// IsAgentDataWritePath filters mutating ops that are not "agents may change
// this data type". Picker session creates and metadata search POSTs are
// operational helpers, not library write surface for the permissions form.
func IsAgentDataWritePath(path string) bool {
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
