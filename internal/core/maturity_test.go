package core

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
)

// maturityHost is the minimal connectors.Host the connector constructors
// need; Capabilities() and provider methods are pure declarations over it.
type maturityHost struct{}

func (maturityHost) GetValidToken(string, *slog.Logger) string    { return "" }
func (maturityHost) UpstreamBaseFor(_, fallback string) string    { return fallback }
func (maturityHost) ServiceConfig(string) *connectors.ServiceInfo { return nil }
func (maturityHost) RequestLevel(*http.Request, string) string    { return "" }
func (maturityHost) BaseURL() string                              { return "https://aegis.example.com" }
func (maturityHost) CacheGet(string) (string, bool)               { return "", false }
func (maturityHost) CacheSet(string, string, time.Duration)       {}
func (maturityHost) ObserveUpstream(string, time.Duration, int)   {}
func (maturityHost) LibraryReadable(string) bool                  { return false }
func (maturityHost) CanonicalOwner(string) string                 { return "" }
func (maturityHost) SetupSecret(string) string                    { return "" }
func (maturityHost) SetupSettings(string) map[string]string       { return nil }

// catalogTestConnector is one connector under consistency check: its
// registered ID, the instance, and the capability names it advertises.
type catalogTestConnector struct {
	id   string
	conn connectors.ServiceConnector
}

func maturityTestConnectors() []catalogTestConnector {
	host := maturityHost{}
	return []catalogTestConnector{
		{id: "spotify", conn: connectors.NewSpotifyConnector(host)},
		{id: "youtube-music", conn: connectors.NewYouTubeMusicConnector(host)},
		{id: "amazon-music", conn: connectors.NewAmazonMusicConnector(host)},
		{id: connectors.GoogleGmailServiceID, conn: connectors.NewGoogleGmailConnector(host)},
		{id: connectors.GooglePhotosServiceID, conn: connectors.NewGooglePhotosConnector(host)},
		{id: connectors.GoogleContactsServiceID, conn: connectors.NewGoogleContactsConnector(host)},
		{id: connectors.GoogleCalendarServiceID, conn: connectors.NewGoogleCalendarConnector(host)},
		{id: connectors.GoogleDriveServiceID, conn: connectors.NewGoogleDriveConnector(host)},
		{id: connectors.ImmichServiceID, conn: connectors.NewImmichConnector(host)},
		{id: connectors.BlueskyServiceID, conn: connectors.NewBlueskyConnector(host)},
		{id: connectors.YNABServiceID, conn: connectors.NewYNABConnector(host)},
		{id: connectors.OwnTracksServiceID, conn: connectors.NewOwnTracksConnector(host)},
		{id: connectors.CalDAVServiceID, conn: connectors.NewCalDAVConnector(host)},
		{id: connectors.CardDAVServiceID, conn: connectors.NewCardDAVConnector(host)},
		{id: connectors.PaperlessServiceID, conn: connectors.NewPaperlessConnector(host)},
		{id: connectors.SignalServiceID, conn: connectors.NewSignalConnector(nil)},
		{id: connectors.QuickenServiceID, conn: connectors.NewQuickenConnector(nil)},
		{id: connectors.TimelineServiceID, conn: connectors.NewTimelineConnector(nil)},
	}
}

// providerKindNames maps implemented provider interfaces to the library
// kind id the catalog uses for that feed.
func providerKindNames(v any) map[string]bool {
	out := map[string]bool{}
	if _, ok := v.(kinds.PlaylistProvider); ok {
		out["playlists"] = true
	}
	if _, ok := v.(kinds.LikedTrackProvider); ok {
		out["tracks"] = true
	}
	if _, ok := v.(kinds.AlbumProvider); ok {
		out["albums"] = true
	}
	if _, ok := v.(kinds.ArtistProvider); ok {
		out["artists"] = true
	}
	if _, ok := v.(kinds.ContactProvider); ok {
		out["contacts"] = true
	}
	if _, ok := v.(kinds.MessageProvider); ok {
		out["messages"] = true
	}
	if _, ok := v.(kinds.PhotoProvider); ok {
		out["photos"] = true
	}
	if _, ok := v.(kinds.CalendarEventProvider); ok {
		out["events"] = true
	}
	if _, ok := v.(kinds.TransactionProvider); ok {
		out["transactions"] = true
	}
	if _, ok := v.(kinds.PlaceVisitProvider); ok {
		out["places"] = true
	}
	if _, ok := v.(kinds.DocumentProvider); ok {
		out["documents"] = true
	}
	return out
}

func maturityByService(t *testing.T) map[string]connectors.ConnectionMaturity {
	t.Helper()
	out := MaturityByService()
	if len(out) != len(ConnectionMaturityCatalog()) {
		t.Fatalf("catalog has duplicate service IDs")
	}
	return out
}

// TestMaturityCatalogCoversEveryConnector pins the catalog to the
// registered connectors: each one has exactly one declaration, and every
// declaration describes a connector this binary ships. The gap rows
// ("shows", "todos", …) are free-form; the connection list is not.
func TestMaturityCatalogCoversEveryConnector(t *testing.T) {
	cat := maturityByService(t)
	for _, c := range maturityTestConnectors() {
		m, ok := cat[c.id]
		if !ok {
			t.Errorf("connector %q has no maturity catalog entry", c.id)
			continue
		}
		if m.Service != c.id {
			t.Errorf("catalog entry service = %q, want %q", m.Service, c.id)
		}
		if m.Transport == "" {
			t.Errorf("connector %q has no transport classification", c.id)
		}
		if len(m.Kinds) == 0 {
			t.Errorf("connector %q catalog entry lists no kinds", c.id)
		}
	}
}

// TestMaturityCatalogNeverUnderclaims is the "be aware / indicate clearly"
// floor of issue #247: anything the connector demonstrably serves — a
// callable capability operation or a library feed — must read as supported.
// A catalog that hides implemented support behind an unsupported row lies
// about the connection.
func TestMaturityCatalogNeverUnderclaims(t *testing.T) {
	cat := maturityByService(t)
	for _, c := range maturityTestConnectors() {
		m, ok := cat[c.id]
		if !ok {
			continue
		}
		supported := map[string]bool{}
		for _, dt := range m.Kinds {
			if dt.Supported {
				supported[dt.Name] = true
			}
		}
		for _, dt := range c.conn.Capabilities().Kinds {
			// Empty Operations means "recognized, not implemented" — the
			// catalog may call those either way. Callable ops must be
			// marked supported.
			if len(dt.Operations) == 0 {
				continue
			}
			if !supported[dt.Name] {
				t.Errorf("connector %q: capability %q has callable operations but the catalog marks it unsupported", c.id, dt.Name)
			}
		}
		for kind := range providerKindNames(c.conn) {
			if !supported[kind] {
				t.Errorf("connector %q: feeds library kind %q but the catalog marks it unsupported", c.id, kind)
			}
		}
	}
}

// TestMaturityCatalogNeverOverclaims is the other direction: no entry may
// claim support for a kind the connector does not advertise and does
// not feed. Gap rows stay unsupported; supported rows must be real.
func TestMaturityCatalogNeverOverclaims(t *testing.T) {
	cat := maturityByService(t)
	for _, c := range maturityTestConnectors() {
		m, ok := cat[c.id]
		if !ok {
			continue
		}
		known := map[string]bool{}
		for _, dt := range c.conn.Capabilities().Kinds {
			known[dt.Name] = true
		}
		for kind := range providerKindNames(c.conn) {
			known[kind] = true
		}
		for _, dt := range m.Kinds {
			if dt.Supported && !known[dt.Name] {
				t.Errorf("connector %q: catalog claims support for %q, which no capability or library feed recognizes", c.id, dt.Name)
			}
		}
	}
}

// TestMaturityCatalogFieldCoverageIsReal keeps declared field coverage
// inside the neutral-model content fields for that kind. Hand-built
// Fields slices must not invent field names; FieldCoverageFor already
// enforces this on the common path.
func TestMaturityCatalogFieldCoverageIsReal(t *testing.T) {
	for _, m := range ConnectionMaturityCatalog() {
		for _, dt := range m.Kinds {
			want := map[string]bool{}
			for _, f := range connectors.MaturityContentFields[dt.Name] {
				want[f] = true
			}
			if len(dt.Fields) > 0 && len(connectors.MaturityContentFields[dt.Name]) == 0 {
				t.Errorf("%s/%s: field coverage on a type with no neutral model", m.Service, dt.Name)
			}
			for _, f := range dt.Fields {
				if !want[f.Name] {
					t.Errorf("%s/%s: field %q is not a content field of that model", m.Service, dt.Name, f.Name)
				}
			}
		}
	}
}

// TestMaturityCatalogTransportsAreClassified pins the transport vocabulary
// the dashboard badges rely on.
func TestMaturityCatalogTransportsAreClassified(t *testing.T) {
	valid := map[connectors.Transport]bool{
		connectors.TransportAPI:               true,
		connectors.TransportProtocol:          true,
		connectors.TransportReverseEngineered: true,
		connectors.TransportArchive:           true,
	}
	for _, m := range ConnectionMaturityCatalog() {
		if !valid[m.Transport] {
			t.Errorf("connector %q: transport %q is not a classified transport", m.Service, m.Transport)
		}
	}
}

// TestMaturityCatalogCoversPublishedManifest keeps the release-published
// service list and the catalog aligned: every connection the published
// dashboard names (other than the drift-injection harness) has a maturity
// declaration. The catalog may name more — instance-only sources such as
// local imports are not in the published manifest.
func TestMaturityCatalogCoversPublishedManifest(t *testing.T) {
	path := filepath.Join("..", "..", "site", "maintenance", "services.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published services manifest: %v", err)
	}
	var entries []struct {
		ID               string `json:"id"`
		FailureInjection bool   `json:"failure_injection"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("parse published services manifest: %v", err)
	}
	cat := maturityByService(t)
	for _, e := range entries {
		if e.FailureInjection {
			continue
		}
		if _, ok := cat[e.ID]; !ok {
			t.Errorf("published service %q has no maturity catalog entry", e.ID)
		}
	}
}
