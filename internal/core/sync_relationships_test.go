package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// syncStub is the bare ServiceConnector that the typed stubs embed
// (issue #365). Provides/WriteCapable are type assertions, so each
// capability needs its own method set.
type syncStub struct{}

func (*syncStub) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (*syncStub) SystemInstructions(string) string         { return "stub" }
func (*syncStub) HandleProxy(http.ResponseWriter, *http.Request, string) {}

// contactsStub provides contacts only (read-only for the kind).
type contactsStub struct{ syncStub }

func (*contactsStub) ListUserContacts(context.Context) ([]kinds.Contact, bool, error) {
	return nil, false, nil
}

// contactsWriteStub also receives contact writes (write-capable).
type contactsWriteStub struct{ contactsStub }

func (*contactsWriteStub) PlaceContacts(context.Context, []kinds.Contact) (kinds.PlaceEntitiesResult, error) {
	return kinds.PlaceEntitiesResult{}, nil
}

// eventsWriteStub provides calendar events and receives event writes.
type eventsWriteStub struct{ syncStub }

func (*eventsWriteStub) ListCalendarEvents(context.Context) ([]kinds.CalendarEvent, bool, error) {
	return nil, false, nil
}

func (*eventsWriteStub) PlaceEvents(context.Context, []kinds.CalendarEvent) (kinds.PlaceEntitiesResult, error) {
	return kinds.PlaceEntitiesResult{}, nil
}

// photosStub provides photos — a kind outside the sync participant set.
type photosStub struct{ syncStub }

func (*photosStub) ListUserPhotos(context.Context) ([]kinds.Photo, bool, error) {
	return nil, false, nil
}

// contactsAlbumsStub provides both contacts and albums.
type contactsAlbumsStub struct{ syncStub }

func (*contactsAlbumsStub) ListUserContacts(context.Context) ([]kinds.Contact, bool, error) {
	return nil, false, nil
}
func (*contactsAlbumsStub) ListUserSavedAlbums(context.Context) ([]kinds.Album, bool, error) {
	return nil, false, nil
}

// syncTestServer builds a server over cfg with the given connectors and a
// throwaway overlay path so relationship writes persist only to the test.
func syncTestServer(t *testing.T, cfg *Config, conns map[string]connectors.ServiceConnector) *Server {
	t.Helper()
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = conns
	cfg.svcConnPath = filepath.Join(t.TempDir(), "service_connections.json")
	return srv
}

func syncRows(t *testing.T, srv *Server, id string) map[string]bool {
	t.Helper()
	return srv.config.SyncRelationshipsFor(id)
}

func TestEstablishSyncRelationships_CreatesRowsForProvidedKinds(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject", Endpoint: "https://contacts.example.com"},
		{ID: "photo-service", Name: "Photos", Action: "inject", Endpoint: "https://photos.example.com"},
	}}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		"google-contacts": &contactsAlbumsStub{},
		"photo-service":   &photosStub{},
	})

	srv.EstablishSyncRelationships()

	rows := syncRows(t, srv, "google-contacts")
	if _, ok := rows["contacts"]; !ok {
		t.Fatalf("expected a contacts row, got %v", rows)
	}
	if _, ok := rows["albums"]; !ok {
		t.Fatalf("albums is a sync participant; expected a row, got %v", rows)
	}
	if rows := syncRows(t, srv, "photo-service"); len(rows) != 0 {
		t.Fatalf("a source outside every participant kind joins no group: %v", rows)
	}
}

func TestEstablishSyncRelationships_DefaultHubIsWriteCapableSource(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "signal", Name: "Signal", Action: "inject"},
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject", Endpoint: "https://contacts.example.com"},
	}}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		// Read-only member first: it joins the group but never claims the hub.
		"signal":          &contactsStub{},
		"google-contacts": &contactsWriteStub{},
	})

	srv.EstablishSyncRelationships()

	if rows := syncRows(t, srv, "signal"); rows["contacts"] {
		t.Fatalf("read-only member must not claim the hub: %v", rows)
	}
	if rows := syncRows(t, srv, "google-contacts"); !rows["contacts"] {
		t.Fatalf("first write-capable source should claim the hub: %v", rows)
	}
	svc := cfg.GetService("google-contacts")
	if svc == nil || !svc.IsCanonicalFor("contacts") {
		t.Fatalf("hub designation must land in canonical_for, got %+v", svc)
	}
}

func TestEstablishSyncRelationships_SeedsHubFromCanonicalFor(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "dav", Name: "DAV", Action: "inject", Endpoint: "https://dav.example.com",
			CanonicalFor: []string{"contacts"}},
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject", Endpoint: "https://contacts.example.com"},
	}}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		"dav":             &contactsWriteStub{},
		"google-contacts": &contactsWriteStub{},
	})

	srv.EstablishSyncRelationships()

	if rows := syncRows(t, srv, "dav"); !rows["contacts"] {
		t.Fatalf("existing canonical_for must seed the hub flag: %v", rows)
	}
	if rows := syncRows(t, srv, "google-contacts"); rows["contacts"] {
		t.Fatalf("second source must not steal the hub: %v", rows)
	}
}

func TestEstablishSyncRelationships_SkipsSampleAndUnconnected(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "sample", Name: "Sample", Action: "inject", Endpoint: "sample://library"},
		{ID: "waiting", Name: "Waiting", Action: "inject", Connection: "google",
			Endpoint: "https://contacts.example.com"},
	}}
	cfg.Connections = map[string]Connection{}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		"sample":  &contactsWriteStub{},
		"waiting": &contactsWriteStub{},
	})

	srv.EstablishSyncRelationships()

	if rows := syncRows(t, srv, "sample"); len(rows) != 0 {
		t.Fatalf("sample fixtures are not user data sources: %v", rows)
	}
	if rows := syncRows(t, srv, "waiting"); len(rows) != 0 {
		t.Fatalf("a source awaiting OAuth is not connected yet: %v", rows)
	}
}

func TestEstablishSyncRelationships_Idempotent(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject", Endpoint: "https://contacts.example.com"},
	}}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		"google-contacts": &contactsWriteStub{},
	})

	srv.EstablishSyncRelationships()
	first := syncRows(t, srv, "google-contacts")
	srv.EstablishSyncRelationships()
	second := syncRows(t, srv, "google-contacts")
	if len(first) != 1 || len(second) != 1 || first["contacts"] != second["contacts"] {
		t.Fatalf("sweep must be idempotent: first=%v second=%v", first, second)
	}
}

func TestSetSyncRelationship_ReassignsHubAcrossGroup(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "dav", Name: "DAV", Action: "inject", Endpoint: "https://dav.example.com",
			CanonicalFor: []string{"contacts"}},
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject", Endpoint: "https://contacts.example.com"},
	}}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		"dav":             &contactsWriteStub{},
		"google-contacts": &contactsWriteStub{},
	})
	srv.EstablishSyncRelationships()

	if err := srv.config.SetSyncRelationship("google-contacts",
		shared.SyncRelationship{Kind: "contacts", Canonical: true}); err != nil {
		t.Fatalf("SetSyncRelationship: %v", err)
	}

	if rows := syncRows(t, srv, "google-contacts"); !rows["contacts"] {
		t.Fatalf("designated source should carry the hub flag: %v", rows)
	}
	if rows := syncRows(t, srv, "dav"); rows["contacts"] {
		t.Fatalf("hub must reassign across the group: %v", rows)
	}
	if svc := cfg.GetService("dav"); svc.IsCanonicalFor("contacts") {
		t.Fatalf("rival claim must be stripped, got %v", svc.CanonicalFor)
	}
	if svc := cfg.GetService("google-contacts"); !svc.IsCanonicalFor("contacts") {
		t.Fatalf("canonical_for must show the new hub, got %v", svc.CanonicalFor)
	}

	// Unset: nobody holds the designation.
	if err := srv.config.SetSyncRelationship("google-contacts",
		shared.SyncRelationship{Kind: "contacts", Canonical: false}); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if rows := syncRows(t, srv, "google-contacts"); rows["contacts"] {
		t.Fatalf("unset must clear the hub flag: %v", rows)
	}
	if svc := cfg.GetService("google-contacts"); svc.IsCanonicalFor("contacts") {
		t.Fatalf("unset must clear the claim too, got %v", svc.CanonicalFor)
	}
}

func TestSetServiceConnection_ClaimsReassignHubFlags(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "dav", Name: "DAV", Action: "inject", Endpoint: "https://dav.example.com",
			CanonicalFor: []string{"contacts"}},
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject", Endpoint: "https://contacts.example.com"},
	}}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		"dav":             &contactsWriteStub{},
		"google-contacts": &contactsWriteStub{},
	})
	srv.EstablishSyncRelationships()

	// Pointing canonical_for elsewhere is the bulk reassignment knob.
	if err := cfg.SetServiceConnection("google-contacts", serviceConnectionEntry{
		CanonicalFor: []string{"contacts"},
	}); err != nil {
		t.Fatalf("SetServiceConnection: %v", err)
	}

	if rows := syncRows(t, srv, "google-contacts"); !rows["contacts"] {
		t.Fatalf("claim must reassign the hub flag: %v", rows)
	}
	if rows := syncRows(t, srv, "dav"); rows["contacts"] {
		t.Fatalf("rival hub flag must clear: %v", rows)
	}
}

func TestSyncRelationships_PersistAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service_connections.json")
	cfg := &Config{Services: []Service{
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject", Endpoint: "https://contacts.example.com"},
	}}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		"google-contacts": &contactsWriteStub{},
	})
	cfg.svcConnPath = path
	srv.EstablishSyncRelationships()
	if rows := syncRows(t, srv, "google-contacts"); !rows["contacts"] {
		t.Fatalf("expected default hub on first write-capable source: %v", rows)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if !strings.Contains(string(raw), `"sync"`) {
		t.Fatalf("overlay must persist the sync block: %s", raw)
	}

	// Reload the overlay onto a fresh config and reconcile.
	cfg2 := &Config{Services: []Service{
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject", Endpoint: "https://contacts.example.com"},
	}}
	cfg2.svcConnPath = path
	if err := cfg2.InitServiceConnectionsStore(path, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rows := cfg2.SyncRelationshipsFor("google-contacts"); !rows["contacts"] {
		t.Fatalf("hub designation must survive reload: %v", rows)
	}
	if svc := cfg2.GetService("google-contacts"); svc == nil || !svc.IsCanonicalFor("contacts") {
		t.Fatalf("canonical_for must reflect the stored hub after reload, got %+v", svc)
	}
}

func TestAdminHTML_ShowsSyncRelationships(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.admin().HandleAdmin(w, req)
	body := w.Body.String()
	for _, want := range []string{"serviceSyncRows", "data-sync-toggle", "canonical home"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin HTML missing %q", want)
		}
	}
}

func TestSetSyncRelationship_ProjectionPersistsAndViews(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service_connections.json")
	cfg := &Config{Services: []Service{
		{ID: "microsoft-calendar", Name: "Microsoft Calendar", Action: "inject", Endpoint: "https://graph.example.com"},
		{ID: "google-calendar", Name: "Google Calendar", Action: "inject", Endpoint: "https://cal.example.com"},
	}}
	srv := syncTestServer(t, cfg, map[string]connectors.ServiceConnector{
		"microsoft-calendar": &eventsWriteStub{},
		"google-calendar":    &eventsWriteStub{},
	})
	cfg.svcConnPath = path
	srv.EstablishSyncRelationships()

	// Google is the hub; Microsoft is a member that receives busy blocks only.
	if err := cfg.SetSyncRelationship("google-calendar", shared.SyncRelationship{Kind: "events", Canonical: true}); err != nil {
		t.Fatalf("set hub: %v", err)
	}
	if err := cfg.SetSyncRelationship("microsoft-calendar", shared.SyncRelationship{
		Kind: "events", Projection: shared.SyncProjectionFreeBusy,
	}); err != nil {
		t.Fatalf("set projection: %v", err)
	}

	svc := cfg.GetService("microsoft-calendar")
	if svc == nil || svc.SyncProjections["events"] != shared.SyncProjectionFreeBusy {
		t.Fatalf("in-memory projections = %+v", svc)
	}
	// The kinds-layer view carries the projection (issue #145).
	if got := serviceViewOf(*svc).SyncProjection("events"); got != shared.SyncProjectionFreeBusy {
		t.Fatalf("ServiceView projection = %q", got)
	}
	// Unknown projection values fall back to a full copy.
	if err := cfg.SetSyncRelationship("microsoft-calendar", shared.SyncRelationship{Kind: "events", Projection: "nonsense"}); err != nil {
		t.Fatalf("set nonsense: %v", err)
	}
	if got := cfg.GetService("microsoft-calendar").SyncProjections["events"]; got != "" {
		t.Fatalf("nonsense projection should clear, got %q", got)
	}
	// Re-apply and reload the overlay onto a fresh config.
	if err := cfg.SetSyncRelationship("microsoft-calendar", shared.SyncRelationship{
		Kind: "events", Projection: shared.SyncProjectionFreeBusy,
	}); err != nil {
		t.Fatalf("re-set projection: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if !strings.Contains(string(raw), `"projection"`) || !strings.Contains(string(raw), "freebusy") {
		t.Fatalf("overlay must persist the projection: %s", raw)
	}
	cfg2 := &Config{Services: []Service{
		{ID: "microsoft-calendar", Name: "Microsoft Calendar", Action: "inject", Endpoint: "https://graph.example.com"},
	}}
	cfg2.svcConnPath = path
	if err := cfg2.InitServiceConnectionsStore(path, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := cfg2.GetService("microsoft-calendar").SyncProjections["events"]; got != shared.SyncProjectionFreeBusy {
		t.Fatalf("projection must survive reload, got %q", got)
	}
}
