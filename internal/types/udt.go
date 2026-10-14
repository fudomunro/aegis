package types

import (
	"fmt"
	"strings"
)

// UserDefinedTypeSchema is the JSON description of a bring-your-own library
// kind (issue #15). Schemas are data, not compiled Go structs: a user (or an
// example pack in its own repo) can define a type and source mapping without
// changing Aegis.
//
// Schema files use this shape (versioned for forward compatibility):
//
//	{
//	  "version": "1",
//	  "id": "reading-list",
//	  "label": "reading list",
//	  "fields": { "title": {"type": "string", "required": true}, ... },
//	  "identity": { "fields": ["title", "author"], "entity_id_field": "id" },
//	  "sources": [ { "kind": "file_json", "path": "sample/books.json", ... } ]
//	}
type UserDefinedTypeSchema struct {
	// Version is the schema-format version. Empty is treated as "1".
	Version string `json:"version,omitempty"`
	// ID is the short kind name ("reading-list") used in cache keys, logs,
	// and /v1/library/{id}. Must be unique among built-in and user kinds.
	ID string `json:"id"`
	// Label is the human name used in agent instructions ("reading list").
	Label string `json:"label"`
	// PermType is the permissions map key. Defaults to ID.
	PermType string `json:"perm_type,omitempty"`
	// Path is the agent-facing endpoint. Defaults to "/v1/library/"+ID.
	Path string `json:"path,omitempty"`
	// Usage is how agents should think about the dataset.
	Usage string `json:"usage"`
	// Summary describes the neutral list shape for agents.
	Summary string `json:"summary"`
	// SyncRule explains cross-source merge behavior. Defaults to name match.
	SyncRule string `json:"sync_rule,omitempty"`
	// ExampleQuestion is a canonical user prompt for pre-request injection.
	ExampleQuestion string `json:"example_question,omitempty"`
	// Keywords detect service-less questions that should inject this endpoint.
	Keywords []string `json:"keywords,omitempty"`
	// EmptyMsg / DenyMsg / FailMsg override the default API error strings.
	EmptyMsg string `json:"empty_msg,omitempty"`
	DenyMsg  string `json:"deny_msg,omitempty"`
	FailMsg  string `json:"fail_msg,omitempty"`

	// Fields declares the user-defined item shape. Keys are field names.
	Fields map[string]UserDefinedField `json:"fields"`
	// Identity describes cross-source matching (issue #4 pattern).
	Identity UserDefinedIdentity `json:"identity,omitempty"`
	// Sources lists how to fetch items for this type.
	Sources []UserDefinedSourceSpec `json:"sources,omitempty"`
	// DefaultAccess is the TypePolicy default for this kind. Empty means
	// "none" — UDTs are opt-in until the admin enables them (places pattern).
	DefaultAccess string `json:"default_access,omitempty"`
}

// UserDefinedField is one schema-declared item field.
type UserDefinedField struct {
	// Type is a coarse JSON type: "string", "number", "bool", "date",
	// "string[]", or "any". Used for docs and future validation; values are
	// still passed through as JSON.
	Type string `json:"type,omitempty"`
	// Required marks fields that must be present after mapping.
	Required bool `json:"required,omitempty"`
	// Description is shown in agent-facing field docs.
	Description string `json:"description,omitempty"`
	// Sensitive marks fields for future attribute-level redaction (#148).
	Sensitive bool `json:"sensitive,omitempty"`
}

// UserDefinedIdentity describes how items from different sources fold into
// one neutral row.
type UserDefinedIdentity struct {
	// Fields lists schema field names that form the name-normalized identity
	// key when no durable EntityID is present (e.g. ["title","author"]).
	Fields []string `json:"fields,omitempty"`
	// EntityIDField names the schema field that carries a durable ID when the
	// source supplies one. Preferred over Fields matching.
	EntityIDField string `json:"entity_id_field,omitempty"`
}

// UserDefinedSourceSpec is one fetch mapping for a UDT. PoC supports
// "file_json" (local sample / export) and "http_json" (GET + field map).
// Named-service connectors stay the path for OAuth providers; UDT sources
// are user-supplied endpoints or files (issue #15).
type UserDefinedSourceSpec struct {
	// ID is the source / service id written into Sources[].Service.
	ID string `json:"id"`
	// Name is the human label for Sources[].ServiceName. Defaults to ID.
	Name string `json:"name,omitempty"`
	// Kind is "file_json" or "http_json".
	Kind string `json:"kind"`
	// Path is the file path for kind=file_json, relative to the schema file.
	Path string `json:"path,omitempty"`
	// Endpoint is the URL for kind=http_json.
	Endpoint string `json:"endpoint,omitempty"`
	// Headers are extra request headers for kind=http_json (non-secret).
	Headers map[string]string `json:"headers,omitempty"`
	// ItemsPath is a dotted path to the item array. Empty means the document
	// root is already an array (or the object itself is one item).
	ItemsPath string `json:"items_path,omitempty"`
	// FieldMap maps schema field name → dotted path on each source item.
	// Missing keys leave the field unset.
	FieldMap map[string]string `json:"field_map"`
	// IDField is the dotted path of the source-local id (Sources[].ID).
	IDField string `json:"id_field,omitempty"`
	// URLTemplate builds Sources[].URL, e.g. "https://wiki/{id}".
	// "{id}" is replaced with the mapped source id.
	URLTemplate string `json:"url_template,omitempty"`
}

// UserDefinedSource is a service-specific location of a user-defined item.
// Same product rules as built-in kinds: identity lives here, never top-level.
type UserDefinedSource struct {
	Service     string `json:"service"`
	ServiceName string `json:"service_name,omitempty"`
	ID          string `json:"id,omitempty"`
	URL         string `json:"url,omitempty"`
	// Canonical is true when this source is the configured home for the kind
	// (issue #16). Stamped by config canonical_for.
	Canonical bool `json:"canonical,omitempty"`
	// Copy is copy-ledger metadata when tracked (issue #76). Nil when untracked.
	Copy *CopyInfo `json:"copy,omitempty"`
	Extra map[string]any `json:"extra,omitempty"`
}

// UserDefinedItem is one neutral row of a user-defined kind. Schema-declared
// fields live under Fields so the envelope (name, entity_id, match, sources)
// stays the same as built-in kinds.
type UserDefinedItem struct {
	// Name is the display name. First required-ish field: Identity.Fields[0]
	// or the mapped "name"/"title" field, else the source id.
	Name string `json:"name"`
	// EntityID is the durable catalog id when the source supplies one.
	EntityID string `json:"entity_id,omitempty"`
	// Match records multi-source identity: "entity_id" or "name". Empty on
	// single-source items.
	Match string `json:"match,omitempty"`
	// Fields holds schema-declared values (author, status, …).
	Fields map[string]any `json:"fields,omitempty"`
	// Sources lists every source-local copy of this item.
	Sources []UserDefinedSource `json:"sources,omitempty"`
}

// UserDefinedList is the neutral list wrapper for a user-defined kind.
// The items key is the kind ID so agents see e.g. "reading-list": [...].
// Encode via UserDefinedItemList once the kind ID is known.
type UserDefinedList struct {
	// Items is the merged neutral list.
	Items []UserDefinedItem `json:"items"`
	// Total is len(Items).
	Total int `json:"total"`
	// SchemaID echoes the kind id that produced this list.
	SchemaID string `json:"schema_id"`
	// Fields describes the declared field shape (for agent self-description).
	Fields map[string]UserDefinedField `json:"fields,omitempty"`
	// Synced is true when multi-source merge ran.
	Synced bool `json:"synced,omitempty"`
	// Truncated is true when any source hit its item cap.
	Truncated bool `json:"truncated,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// Match methods for UserDefinedItem.
const (
	UserDefinedMatchEntityID = "entity_id"
	UserDefinedMatchName     = "name"
)

// userDefinedSchemaFormatVersions are schema-format versions this build reads.
// Bump and extend when the format changes; unknown versions fail load
// (fail-open at the server: the kind is skipped and logged).
var userDefinedSchemaFormatVersions = map[string]bool{
	"":  true, // treat missing as v1
	"1": true,
}

// Normalize fills defaults and validates required schema fields.
func (s *UserDefinedTypeSchema) Normalize() error {
	if s == nil {
		return fmt.Errorf("nil schema")
	}
	if !userDefinedSchemaFormatVersions[s.Version] {
		return fmt.Errorf("unsupported schema version %q", s.Version)
	}
	s.ID = strings.TrimSpace(s.ID)
	if s.ID == "" {
		return fmt.Errorf("schema id is required")
	}
	if strings.ContainsAny(s.ID, "/\\") {
		return fmt.Errorf("schema id %q must not contain path separators", s.ID)
	}
	if s.Label == "" {
		s.Label = s.ID
	}
	if s.PermType == "" {
		s.PermType = s.ID
	}
	if s.Path == "" {
		s.Path = "/v1/library/" + s.ID
	}
	if !strings.HasPrefix(s.Path, "/") {
		s.Path = "/" + s.Path
	}
	if s.Usage == "" {
		s.Usage = s.Label
	}
	if s.Summary == "" {
		s.Summary = fmt.Sprintf("user-defined %s items in one neutral list (name, fields, sources[])", s.Label)
	}
	if s.SyncRule == "" {
		s.SyncRule = "Same-identity items are synced into one entry with multiple sources[]."
	}
	if s.EmptyMsg == "" {
		s.EmptyMsg = "no " + s.ID + " sources configured"
	}
	if s.DenyMsg == "" {
		s.DenyMsg = "permission denied: agent access to " + s.ID + " is not allowed"
	}
	if s.FailMsg == "" {
		s.FailMsg = "failed to fetch " + s.ID
	}
	if s.DefaultAccess == "" {
		// UDTs are opt-in until enabled (places pattern, issue #15).
		s.DefaultAccess = AccessNone
	} else {
		s.DefaultAccess = NormalizeAccess(s.DefaultAccess)
	}
	if len(s.Fields) == 0 {
		return fmt.Errorf("schema %q: fields must not be empty", s.ID)
	}
	for name, f := range s.Fields {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("schema %q: empty field name", s.ID)
		}
		if f.Type == "" {
			f.Type = "any"
			s.Fields[name] = f
		}
	}
	if len(s.Identity.Fields) == 0 && s.Identity.EntityIDField == "" {
		// Default identity is the conventional display-name fields.
		for _, cand := range []string{"name", "title", "label"} {
			if _, ok := s.Fields[cand]; ok {
				s.Identity.Fields = []string{cand}
				break
			}
		}
		if len(s.Identity.Fields) == 0 {
			return fmt.Errorf("schema %q: identity.fields or identity.entity_id_field is required", s.ID)
		}
	}
	if s.Identity.EntityIDField != "" {
		if _, ok := s.Fields[s.Identity.EntityIDField]; !ok {
			return fmt.Errorf("schema %q: identity.entity_id_field %q is not in fields", s.ID, s.Identity.EntityIDField)
		}
	}
	for _, name := range s.Identity.Fields {
		if _, ok := s.Fields[name]; !ok {
			return fmt.Errorf("schema %q: identity field %q is not in fields", s.ID, name)
		}
	}
	for i := range s.Sources {
		src := &s.Sources[i]
		if strings.TrimSpace(src.ID) == "" {
			return fmt.Errorf("schema %q: sources[%d].id is required", s.ID, i)
		}
		if src.Name == "" {
			src.Name = src.ID
		}
		switch src.Kind {
		case "file_json":
			if strings.TrimSpace(src.Path) == "" {
				return fmt.Errorf("schema %q: source %q: path is required for file_json", s.ID, src.ID)
			}
		case "http_json":
			if strings.TrimSpace(src.Endpoint) == "" {
				return fmt.Errorf("schema %q: source %q: endpoint is required for http_json", s.ID, src.ID)
			}
		default:
			return fmt.Errorf("schema %q: source %q: unsupported kind %q (want file_json or http_json)", s.ID, src.ID, src.Kind)
		}
		if src.FieldMap == nil {
			return fmt.Errorf("schema %q: source %q: field_map is required", s.ID, src.ID)
		}
		for field := range src.FieldMap {
			if _, ok := s.Fields[field]; !ok {
				return fmt.Errorf("schema %q: source %q: field_map references unknown field %q", s.ID, src.ID, field)
			}
		}
	}
	return nil
}

// LibraryKind converts the schema into type-layer kind metadata. Runtime
// visibility predicates stay on the core registration wrapper (issue #15).
func (s UserDefinedTypeSchema) LibraryKind() LibraryKind {
	return LibraryKind{
		ID:              s.ID,
		Path:            s.Path,
		PermType:        s.PermType,
		Label:           s.Label,
		Usage:           s.Usage,
		Summary:         s.Summary,
		SyncRule:        s.SyncRule,
		ExampleQuestion: s.ExampleQuestion,
		Keywords:        s.Keywords,
		EmptyMsg:        s.EmptyMsg,
		DenyMsg:         s.DenyMsg,
		FailMsg:         s.FailMsg,
	}
}

// UserDefinedItemName picks the display name for an item: preferred identity
// field, then name/title/label, then source id.
func (s UserDefinedTypeSchema) UserDefinedItemName(item UserDefinedItem) string {
	if item.Name != "" {
		return item.Name
	}
	for _, key := range append(append([]string{}, s.Identity.Fields...), "name", "title", "label") {
		if v, ok := item.Fields[key]; ok {
			if str, ok := v.(string); ok && strings.TrimSpace(str) != "" {
				return strings.TrimSpace(str)
			}
		}
	}
	if len(item.Sources) > 0 && item.Sources[0].ID != "" {
		return item.Sources[0].ID
	}
	return ""
}

// UserDefinedEntityID returns the durable id when the schema declares one.
func (s UserDefinedTypeSchema) UserDefinedEntityID(item UserDefinedItem) string {
	if item.EntityID != "" {
		return item.EntityID
	}
	if s.Identity.EntityIDField == "" {
		return ""
	}
	v, ok := item.Fields[s.Identity.EntityIDField]
	if !ok {
		return ""
	}
	str, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(str)
}

// UserDefinedIdentityKey builds the name-normalized multi-field identity key
// used when no durable EntityID is present.
func (s UserDefinedTypeSchema) UserDefinedIdentityKey(item UserDefinedItem) string {
	parts := make([]string, 0, len(s.Identity.Fields))
	for _, name := range s.Identity.Fields {
		v, ok := item.Fields[name]
		if !ok {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, NormalizeIdentityKey(fmt.Sprint(v)))
	}
	return strings.Join(parts, "\x00")
}

// MergeUserDefinedItems folds source-local rows into neutral items:
//  1. durable EntityID when the schema supplies one (match=entity_id)
//  2. otherwise the name-normalized Identity.Fields key (match=name)
//
// Multi-source entries union Sources in first-seen order. Field conflicts keep
// the first non-empty value (same fail-open spirit as name merges elsewhere).
func MergeUserDefinedItems(schema UserDefinedTypeSchema, in []UserDefinedItem) []UserDefinedItem {
	type acc struct {
		item UserDefinedItem
		seen map[string]bool // source service+id already present
	}
	byEntity := map[string]*acc{}
	byName := map[string]*acc{}
	var order []*acc

	for _, raw := range in {
		name := schema.UserDefinedItemName(raw)
		entity := schema.UserDefinedEntityID(raw)
		nameKey := schema.UserDefinedIdentityKey(raw)

		var slot *acc
		var match string
		switch {
		case entity != "":
			if existing, ok := byEntity[entity]; ok {
				slot = existing
				match = UserDefinedMatchEntityID
			} else {
				slot = &acc{item: UserDefinedItem{
					Name:     name,
					EntityID: entity,
					Fields:   map[string]any{},
					Match:    "",
				}, seen: map[string]bool{}}
				byEntity[entity] = slot
				order = append(order, slot)
				match = UserDefinedMatchEntityID
			}
		case nameKey != "":
			if existing, ok := byName[nameKey]; ok {
				slot = existing
				match = UserDefinedMatchName
			} else {
				slot = &acc{item: UserDefinedItem{
					Name:   name,
					Fields: map[string]any{},
					Match:  "",
				}, seen: map[string]bool{}}
				byName[nameKey] = slot
				order = append(order, slot)
				match = UserDefinedMatchName
			}
		default:
			// No identity: keep as a standalone single-source row.
			item := raw
			if item.Name == "" {
				item.Name = name
			}
			if item.Fields == nil {
				item.Fields = map[string]any{}
			}
			if entity != "" {
				item.EntityID = entity
			}
			order = append(order, &acc{item: item, seen: map[string]bool{}})
			continue
		}

		// Merge fields (first non-empty wins).
		for k, v := range raw.Fields {
			if v == nil {
				continue
			}
			if _, exists := slot.item.Fields[k]; !exists || slot.item.Fields[k] == nil || slot.item.Fields[k] == "" {
				if s := fmt.Sprint(v); s != "" && s != "<nil>" {
					slot.item.Fields[k] = v
				}
			}
		}
		if slot.item.Name == "" {
			slot.item.Name = name
		}
		if slot.item.EntityID == "" {
			slot.item.EntityID = entity
		}
		// Record match only when more than one source will attach.
		if len(slot.item.Sources) > 0 || len(raw.Sources) > 0 {
			// Prefer durable entity_id label when we matched on it.
			if match == UserDefinedMatchEntityID {
				slot.item.Match = UserDefinedMatchEntityID
			} else if slot.item.Match == "" {
				slot.item.Match = match
			}
		}
		for _, src := range raw.Sources {
			key := src.Service + "\x00" + src.ID
			if slot.seen[key] {
				continue
			}
			slot.seen[key] = true
			slot.item.Sources = append(slot.item.Sources, src)
		}
	}

	out := make([]UserDefinedItem, 0, len(order))
	for _, a := range order {
		// Single-source items carry no match label (empty), matching built-ins.
		if len(a.item.Sources) <= 1 {
			a.item.Match = ""
		} else if a.item.Match == "" {
			a.item.Match = UserDefinedMatchName
		}
		if a.item.Fields != nil && len(a.item.Fields) == 0 {
			a.item.Fields = nil
		}
		out = append(out, a.item)
	}
	return out
}

// EncodeUserDefinedList builds the neutral list envelope for a schema.
func EncodeUserDefinedList(schema UserDefinedTypeSchema, items []UserDefinedItem, bySvc map[string]LibraryServiceStatus) UserDefinedList {
	truncated := false
	for _, st := range bySvc {
		if st.Truncated {
			truncated = true
			break
		}
	}
	return UserDefinedList{
		Items:     items,
		Total:     len(items),
		SchemaID:  schema.ID,
		Fields:    schema.Fields,
		Synced:    true,
		Truncated: truncated,
		ByService: bySvc,
	}
}

// UserDefinedServiceName returns the display name of the first source, or "".
func UserDefinedServiceName(item UserDefinedItem) string {
	if len(item.Sources) > 0 {
		return item.Sources[0].ServiceName
	}
	return ""
}
