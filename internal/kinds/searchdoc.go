package kinds

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/core/search"
)

// BuildSearchDocument flattens one neutral item into a search index row.
// item is marshaled to JSON and each spec field contributes its display text
// to Body. itemID should be the kind's stable identity key.
//
// This is kind-domain knowledge — it understands neutral item JSON and the
// user-defined "fields" map — producing an engine-internal row, so it lives
// here rather than in the core/search FTS engine (issue #325).
func BuildSearchDocument(itemID string, item any, spec SearchSpec) (search.SearchDocument, error) {
	payload, err := json.Marshal(item)
	if err != nil {
		return search.SearchDocument{}, err
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		return search.SearchDocument{}, err
	}
	doc := search.SearchDocument{ItemID: itemID, Payload: payload}
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
