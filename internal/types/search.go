package types

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SearchSpec declares how one library kind is searched (issue #178). It is
// part of the type definition — builtin kinds declare it in the registry and
// user-defined packs declare it in the schema — so search stays pluggable
// instead of a per-handler afterthought.
type SearchSpec struct {
	// Fields lists neutral item JSON keys to full-text index. Values are
	// flattened to text (strings as-is; arrays and nested objects contribute
	// their display text, minus identity/transport keys).
	Fields []string
	// TitleField is the JSON key whose value titles a search hit. Defaults
	// to the first entry in Fields.
	TitleField string
}

// SearchDocument is one merged library item flattened for the search index.
// Payload carries the neutral item JSON so search hits can be returned
// without a live provider fetch. Metadata only — never media bytes.
type SearchDocument struct {
	// ItemID is the stable identity of the merged item within (kind, dataset).
	ItemID string
	// Title is the display title (TitleField value).
	Title string
	// Body is the concatenated indexed field text.
	Body string
	// Payload is the marshaled neutral item used to hydrate search results.
	Payload []byte
	// Services are the source service IDs of the item, used to drop hits the
	// request is not allowed to read.
	Services []string
}

// BuildSearchDocument flattens one neutral item into an index document.
// item is marshaled to JSON and each spec field contributes its display text
// to Body. itemID should be the kind's stable identity key.
func BuildSearchDocument(itemID string, item any, spec SearchSpec) (SearchDocument, error) {
	payload, err := json.Marshal(item)
	if err != nil {
		return SearchDocument{}, err
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		return SearchDocument{}, err
	}
	doc := SearchDocument{ItemID: itemID, Payload: payload}
	titleField := spec.TitleField
	if titleField == "" && len(spec.Fields) > 0 {
		titleField = spec.Fields[0]
	}
	var body []string
	for _, name := range spec.Fields {
		val, ok := fields[name]
		if !ok {
			// User-defined items keep schema values under "fields".
			if sub, ok := fields["fields"].(map[string]any); ok {
				val = sub[name]
			}
		}
		text := searchTextify(val)
		if text == "" {
			continue
		}
		if name == titleField {
			doc.Title = text
		}
		body = append(body, text)
	}
	if doc.Title == "" && len(body) > 0 {
		doc.Title = body[0]
	}
	doc.Body = strings.Join(body, " ")
	doc.Services = searchSourceServices(fields["sources"])
	return doc, nil
}

// searchSkipKeys are JSON keys that identify transport/identity rather than
// user-visible content. Nested values under these keys never enter the index,
// so a hit on "spotify" or an entity id cannot masquerade as a content match.
var searchSkipKeys = map[string]bool{
	"id": true, "entity_id": true, "service": true, "service_name": true,
	"url": true, "blob_url": true, "copy": true, "extra": true,
	"canonical": true, "match": true, "point_ids": true, "account_id": true,
	"calendar_id": true, "thread_id": true, "message_id": true,
	"sources": true, "payload_gzip": true,
	"lat": true, "lng": true, "width": true, "height": true,
}

// searchTextify flattens a decoded JSON value into indexable text.
func searchTextify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		var parts []string
		for _, elem := range t {
			if s := searchTextify(elem); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		var parts []string
		for key, val := range t {
			if searchSkipKeys[key] {
				continue
			}
			if s := searchTextify(val); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

// searchSourceServices collects source service IDs from a "sources" array.
func searchSourceServices(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, elem := range list {
		m, ok := elem.(map[string]any)
		if !ok {
			continue
		}
		svc, _ := m["service"].(string)
		svc = strings.TrimSpace(svc)
		if svc == "" || seen[svc] {
			continue
		}
		seen[svc] = true
		out = append(out, svc)
	}
	return out
}
