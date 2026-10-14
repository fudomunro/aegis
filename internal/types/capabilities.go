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
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Operations  []Operation `json:"operations"`
}

// Operation describes a single callable operation on a data type.
type Operation struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Method      string       `json:"method"`
	Path        string       `json:"path"`
	Parameters  []Parameter  `json:"parameters,omitempty"`
	Response    ResponseSpec `json:"response"`
}

// Parameter describes a query or path parameter for an operation.
type Parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ResponseSpec describes what the operation returns.
type ResponseSpec struct {
	Description string   `json:"description"`
	Fields      []string `json:"fields,omitempty"`
}

// FilterCapabilities removes operations the permission map does not allow.
// Data types set to "none" are dropped entirely; other types keep only
// operations whose HTTP method is permitted.
// isMutatingMethod reports whether the HTTP method changes data.
// dataTypeKnown reports whether the connector advertises the data type.
// dataTypeHasWriteOp reports whether the connector exposes any mutating
// operation for the data type (including session/search POSTs). Used for
// proxy enforcement: a permission write level never unlocks methods the
// connector does not implement at all.
// dataTypeHasAgentWriteOp reports whether the connector exposes a mutating
// operation that actually changes data of this type (not picker/search).
// writableDataTypesFromCapabilities lists data types with at least one
// agent-meaningful mutating connector operation.

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
			if AllowsMethod(level, method) {
				ops = append(ops, op)
			}
		}
		dt.Operations = ops
		out.DataTypes = append(out.DataTypes, dt)
	}
	return out
}

// isMutatingMethod reports whether the HTTP method changes data.
func IsMutatingMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// dataTypeKnown reports whether the connector advertises the data type.
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

// dataTypeHasWriteOp reports whether the connector exposes any mutating
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

// dataTypeHasAgentWriteOp reports whether the connector exposes a mutating
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

// writableDataTypesFromCapabilities lists data types with at least one
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

// isAgentDataWritePath filters mutating ops that are not "agents may change
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
