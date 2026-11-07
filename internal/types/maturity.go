package types

// Connection maturity (issue #247): how completely one connection covers the
// data its service offers. Distinct from the sync dashboard's operational
// marks (success / stability / confidence), which measure maintenance
// responsiveness — maturity is a coverage statement about the connector
// itself and moves only when code moves.
//
// Three axes, from the issue:
//
//  1. Awareness — the catalog names every data type the service offers,
//     not only the ones Aegis implements.
//  2. Indication — each type is marked supported or not, so the gap is
//     visible instead of implied.
//  3. Field coverage — supported types that map to a neutral model declare
//     which content fields the connector populates.
//
// Transport is shown alongside the grade: API-based connections read as
// more mature than ones that reach the service through exported files or
// reverse-engineered surfaces.

// Transport classifies how a connection reaches its service.
type Transport string

const (
	// TransportAPI is an HTTP API of the service — public developer API or
	// a self-hosted service's own REST surface (Spotify Web API, Google
	// APIs, YNAB, Paperless, Immich, OwnTracks Recorder).
	TransportAPI Transport = "api"
	// TransportProtocol is a documented open interoperability protocol
	// (CalDAV, CardDAV, AT Protocol).
	TransportProtocol Transport = "protocol"
	// TransportReverseEngineered is an undocumented private API surface
	// (YouTube Music InnerTube, Amazon Music).
	TransportReverseEngineered Transport = "reverse-engineered"
	// TransportArchive is an import of exported files or a local store
	// (Quicken QIF, Google Timeline export, Signal Desktop database).
	TransportArchive Transport = "archive"
)

// FieldCoverage is one neutral-model content field's coverage on a
// connection. Service identity lives in sources[] and is never graded.
type FieldCoverage struct {
	Name string `json:"name"`
	// Supported is false when the model has the field but this connection
	// does not populate it.
	Supported bool `json:"supported"`
}

// DataMaturity is one data type's coverage on one connection: every type
// the service offers in theory, marked with whether Aegis makes it
// available here.
type DataMaturity struct {
	// Name is the data type as Aegis names it: a library kind id
	// (playlists, tracks, …) or connector capability name (user, search,
	// shows, …). Names may carry provider-local meaning per connection —
	// "albums" is music albums on Spotify and photo albums on Immich.
	Name string `json:"name"`
	// Description is what the service offers under this name.
	Description string `json:"description,omitempty"`
	// Supported is true when the connection serves the type: a unified-
	// library feed, a callable connector operation, or a handled proxy
	// path. Types the service offers but the connection does not serve
	// stay false so the gap is visible.
	Supported bool `json:"supported"`
	// Fields grades content-field coverage for supported types that map
	// to a neutral model. Nil for operational types (user, search, …).
	Fields []FieldCoverage `json:"fields,omitempty"`
}

// ConnectionMaturity is the coverage assessment of one connection.
type ConnectionMaturity struct {
	// Service is the connector ID: "spotify", "google-gmail", ….
	Service   string         `json:"service"`
	Transport Transport      `json:"transport"`
	DataTypes []DataMaturity `json:"data_types"`
	// Start is how a user brings this connection's data under management
	// (issue #165). Declared beside the connector; core never derives it
	// from interfaces or transport class.
	Start OwnershipAction `json:"start"`
}

// MaturityGrade is the derived mark shown on the dashboard.
type MaturityGrade struct {
	// Level is complete | substantial | partial | basic.
	Level string `json:"level"`
	// CoveragePct is supported data types over offered, percent 0-100.
	CoveragePct float64 `json:"coverage_pct"`
	// FieldPct is populated content fields over graded, percent 0-100
	// across every supported model-backed type. Nil when no supported
	// type maps to a neutral model.
	FieldPct *float64 `json:"field_pct,omitempty"`
	// Transport is carried through so the page can show it beside the level.
	Transport Transport `json:"transport"`
	Offered   int       `json:"offered"`
	Supported int       `json:"supported"`
}

// MaturityContentFields maps a data type name to the neutral-model content
// fields field coverage grades. sources[], extra, and match are structural
// — filled by construction or merge — and are never graded. A type absent
// from this map has no neutral model (user, search, shows, …) and takes no
// field coverage.
var MaturityContentFields = map[string][]string{
	"playlists":    {"name", "description", "owner", "track_count"},
	"tracks":       {"name", "artists", "album", "duration_ms", "duration", "entity_id"},
	"albums":       {"name", "artists", "entity_id"},
	"artists":      {"name", "entity_id"},
	"contacts":     {"name", "emails", "phones", "handles", "organization"},
	"photos":       {"taken_at", "width", "height", "mime_type", "filename", "caption", "people"},
	"events":       {"title", "description", "start_at", "end_at", "all_day", "location", "status", "calendar_id", "calendar_name", "organizer", "attendees", "entity_id"},
	"messages":     {"channel", "subject", "snippet", "body", "from", "to", "cc", "sent_at", "received_at", "direction", "read", "thread_id", "entity_id", "labels"},
	"documents":    {"title", "mime_type", "created_at", "modified_at", "correspondent", "document_type", "tags", "excerpt", "people", "entity_id"},
	"transactions": {"posted_at", "authorized_at", "amount", "currency", "description", "merchant", "category", "memo", "account", "status", "entity_id"},
	"places":       {"start_at", "end_at", "place", "kind", "mode", "confidence", "entity_id"},
}

// FieldCoverageFor grades one data type against the content fields the
// connector populates. Populated names outside the type's content fields
// are ignored; unlisted content fields grade as unsupported. Returns nil
// for types with no neutral model.
func FieldCoverageFor(dataType string, populated ...string) []FieldCoverage {
	want, ok := MaturityContentFields[dataType]
	if !ok {
		return nil
	}
	have := make(map[string]bool, len(populated))
	for _, f := range populated {
		have[f] = true
	}
	out := make([]FieldCoverage, 0, len(want))
	for _, f := range want {
		out = append(out, FieldCoverage{Name: f, Supported: have[f]})
	}
	return out
}

// GradeConnectionMaturity derives the dashboard mark from the declaration.
// Coverage and fields are graded separately; the level bands on the lower
// of the two so a connection cannot read as mature by supporting one
// complete type out of ten. Field coverage counts only supported types
// that map to a neutral model.
func GradeConnectionMaturity(m ConnectionMaturity) MaturityGrade {
	g := MaturityGrade{Transport: m.Transport, Offered: len(m.DataTypes)}
	if len(m.DataTypes) == 0 {
		g.Level = "basic"
		return g
	}
	populated, graded := 0, 0
	for _, dt := range m.DataTypes {
		if !dt.Supported {
			continue
		}
		g.Supported++
		for _, f := range dt.Fields {
			graded++
			if f.Supported {
				populated++
			}
		}
	}
	g.CoveragePct = 100 * float64(g.Supported) / float64(g.Offered)
	combined := g.CoveragePct
	if graded > 0 {
		pct := 100 * float64(populated) / float64(graded)
		g.FieldPct = &pct
		if pct < combined {
			combined = pct
		}
	}
	g.Level = maturityLevel(combined)
	return g
}

// maturityLevel bands a 0-100 combined coverage figure. "complete" is
// reserved for the issue's bar: every offered type supported and every
// graded content field populated.
func maturityLevel(pct float64) string {
	switch {
	case pct >= 99.999:
		return "complete"
	case pct >= 75:
		return "substantial"
	case pct >= 40:
		return "partial"
	default:
		return "basic"
	}
}
