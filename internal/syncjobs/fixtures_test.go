package syncjobs

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/library"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Test fixtures: a small in-memory host that satisfies the three seams the
// background domain needs (connectors.Host, kinds.Host, syncjobs.Host), so
// runner tests can wire real connectors and the mirror glue without the
// hub's fixture.

type fixtureService struct {
	Name     string
	Action   string
	Endpoint string
}

type fixtureHost struct {
	baseURL     string
	services    map[string]fixtureService
	tokens      map[string]string
	grants      shared.GrantMap
	connectors  map[string]connectors.ServiceConnector
	mailIngests map[string]connectors.MailIngest
	syncGroups  map[string]fixtureSyncGroup
}

// fixtureSyncGroup is one kind's configured sync group: the designated
// canonical home and its member rows (issue #365).
type fixtureSyncGroup struct {
	hub     string
	members []string
}

func newFixtureHost(baseURL string) *fixtureHost {
	return &fixtureHost{
		baseURL:     baseURL,
		services:    map[string]fixtureService{},
		tokens:      map[string]string{},
		grants:      shared.GrantMap{},
		connectors:  map[string]connectors.ServiceConnector{},
		mailIngests: map[string]connectors.MailIngest{},
		syncGroups:  map[string]fixtureSyncGroup{},
	}
}

// --- connectors.Host ---

func (h *fixtureHost) GetValidToken(serviceID string, _ *slog.Logger) string {
	return h.tokens[serviceID]
}

func (h *fixtureHost) UpstreamBaseFor(serviceID, fallback string) string {
	if svc, ok := h.services[serviceID]; ok && svc.Endpoint != "" {
		return svc.Endpoint
	}
	return fallback
}

func (h *fixtureHost) ServiceConfig(serviceID string) *connectors.ServiceInfo {
	svc, ok := h.services[serviceID]
	if !ok {
		return nil
	}
	return &connectors.ServiceInfo{
		ID:       serviceID,
		Action:   svc.Action,
		Endpoint: svc.Endpoint,
	}
}

func (h *fixtureHost) BaseURL() string { return h.baseURL }

func (h *fixtureHost) CacheGet(key string) (string, bool) { return "", false }

func (h *fixtureHost) CacheSet(key, value string, ttl time.Duration) {}

func (h *fixtureHost) ObserveUpstream(serviceID string, elapsed time.Duration, status int) {}

func (h *fixtureHost) LibraryReadable(kind string) bool { return true }

func (h *fixtureHost) CanonicalOwner(kind string) string { return "" }

func (h *fixtureHost) SetupSecret(name string) string { return "" }

func (h *fixtureHost) SetupSettings(name string) map[string]string { return nil }

// --- kinds.Host ---

func (h *fixtureHost) Connectors() map[string]any {
	out := make(map[string]any, len(h.connectors))
	for id, c := range h.connectors {
		out[id] = c
	}
	return out
}

func (h *fixtureHost) Service(id string) (kinds.ServiceView, bool) {
	svc, ok := h.services[id]
	if !ok {
		return kinds.ServiceView{}, false
	}
	return kinds.ServiceView{ID: id, Name: svc.Name, Action: svc.Action}, true
}

func (h *fixtureHost) Services() []kinds.ServiceView {
	out := make([]kinds.ServiceView, 0, len(h.services))
	for id, svc := range h.services {
		out = append(out, kinds.ServiceView{ID: id, Name: svc.Name, Action: svc.Action})
	}
	return out
}

func (h *fixtureHost) Grants(r *http.Request) (shared.GrantMap, kinds.Principal) {
	return h.grants, kinds.PrincipalOwner
}

func (h *fixtureHost) OwnerGrants() shared.GrantMap { return h.grants }

func (h *fixtureHost) RecordAccess(r *http.Request, ev kinds.AccessEvent) {}

func (h *fixtureHost) Logger(r *http.Request, handler string) *slog.Logger {
	return slog.Default()
}

func (h *fixtureHost) Agent(r *http.Request) string { return "test" }

func (h *fixtureHost) SourceName(id string) string {
	if svc, ok := h.services[id]; ok && svc.Name != "" {
		return svc.Name
	}
	return id
}

func (h *fixtureHost) CopyInfo(kind, serviceID, externalID string) *kinds.CopyInfo { return nil }

// --- syncjobs.Host ---

func (h *fixtureHost) ServiceBlocked(serviceID string) bool {
	svc, ok := h.services[serviceID]
	return !ok || svc.Action == "block"
}

// SyncGroup resolves the fixture's configured group for kind (issue #365).
func (h *fixtureHost) SyncGroup(kind string) (string, []string) {
	g := h.syncGroups[kind]
	return g.hub, append([]string(nil), g.members...)
}

// --- ported test fixtures ---

// mirrorStubRunners wires a Runners with stub mirror adapters and a fresh
// copy ledger (the syncrunner_mirror fixture).
func mirrorStubRunners(t *testing.T, src library.MirrorSource, dst library.MirrorTarget) *Runners {
	t.Helper()
	h := newFixtureHost("https://aegis.example.com")
	h.grants = shared.GrantMap{
		"playlists": shared.AccessFull,
		"tracks":    shared.AccessFull,
	}
	h.services["spotify"] = fixtureService{Name: "Spotify", Action: "inject"}
	h.services["youtube-music"] = fixtureService{Name: "YouTube Music", Action: "inject"}
	ledger, err := copies.Open(t.TempDir() + "/copy_ledger.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	lib := &library.Handlers{Host: h, Connectors: h.connectors, Copies: ledger}
	if src != nil {
		lib.MirrorSourceFn = func(serviceID, kind string) (library.MirrorSource, error) { return src, nil }
	}
	lib.MirrorTargetFn = func(serviceID, kind string) (library.MirrorTarget, error) { return dst, nil }
	return &Runners{
		Host:        h,
		Mirror:      lib,
		Copies:      ledger,
		Connectors:  h.connectors,
		MailIngests: h.mailIngests,
	}
}

// mirrorTestRunners swaps in stub adapters, a fresh ledger, and a job store.
func mirrorTestRunners(t *testing.T, src library.MirrorSource, dst library.MirrorTarget) (*Runners, *JobStore, *SyncWorker) {
	t.Helper()
	run := mirrorStubRunners(t, src, dst)
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	registerMirrorRunners(reg, run)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	return run, store, w
}

// connectorTestRunners wires a real connector against a fake upstream (the
// canary and freshness-checker fixtures).
func connectorTestRunners(t *testing.T, upstream http.Handler, serviceID string) (*Runners, *fixtureHost, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	h := newFixtureHost("https://aegis.example.com")
	h.services[serviceID] = fixtureService{Name: serviceID, Action: "inject", Endpoint: up.URL}
	h.tokens[serviceID] = "tok"
	switch serviceID {
	case "spotify":
		h.connectors[serviceID] = connectors.NewSpotifyConnector(h)
	case "youtube-music":
		h.connectors[serviceID] = connectors.NewYouTubeMusicConnector(h)
	default:
		t.Fatalf("fixture has no connector for %s", serviceID)
	}
	h.mailIngests[connectors.TakeoutServiceID] = connectors.NewTakeoutIngest()
	lib := &library.Handlers{Host: h, Connectors: h.connectors}
	return &Runners{
		Host:        h,
		Mirror:      lib,
		Connectors:  h.connectors,
		MailIngests: h.mailIngests,
	}, h, up
}

// openTestMailArchive opens a fresh mail archive under the test temp dir.
func openTestMailArchive(t *testing.T) *mail.Archive {
	t.Helper()
	a, err := mail.Open(filepath.Join(t.TempDir(), "mail-archive.db"))
	if err != nil {
		t.Fatalf("mail.Open: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// disableGmailArchivePace removes the archive fetch pacing in tests.
func disableGmailArchivePace(t *testing.T) {
	t.Helper()
	prev := connectors.GmailArchiveGetInterval
	connectors.GmailArchiveGetInterval = 0
	t.Cleanup(func() { connectors.GmailArchiveGetInterval = prev })
}

// mailTestRunners wires a Runners over a Gmail connector and the given
// archive (the syncrunner_mail fixture).
func mailTestRunners(t *testing.T, archive *mail.Archive) *Runners {
	t.Helper()
	h := newFixtureHost("https://aegis.example.com")
	h.services[connectors.GoogleGmailServiceID] = fixtureService{
		Name:   connectors.GoogleGmailServiceName,
		Action: "inject",
	}
	h.connectors[connectors.GoogleGmailServiceID] = connectors.NewGoogleGmailConnector(h)
	h.mailIngests[connectors.GoogleGmailServiceID] = h.connectors[connectors.GoogleGmailServiceID].(connectors.MailIngest)
	h.mailIngests[connectors.TakeoutServiceID] = connectors.NewTakeoutIngest()
	lib := &library.Handlers{Host: h, Connectors: h.connectors}
	return &Runners{
		Host:        h,
		Mirror:      lib,
		Mail:        archive,
		Connectors:  h.connectors,
		MailIngests: h.mailIngests,
	}
}
func gmailPayloadJSON(id string) string {
	subject := "Hello " + id
	body := base64.RawURLEncoding.EncodeToString([]byte("body of " + id))
	return fmt.Sprintf(`{
		"id": %q,
		"threadId": "thread-1",
		"labelIds": ["INBOX", "UNREAD"],
		"snippet": "body of %s",
		"internalDate": "1717232400000",
		"payload": {
			"headers": [
				{"name":"From","value":"Ada <ada@example.com>"},
				{"name":"To","value":"me@example.com"},
				{"name":"Subject","value":%q},
				{"name":"Message-ID","value":"<%s@example.com>"}
			],
			"body": {"data": %q}
		}
	}`, id, id, subject, id, body)
}

// --- converge fixtures (issue #365) ---

// entityStubConn is a test connector for the entity kinds: it serves one
// contact/event collection and records placements. Place* upserts by name
// and never deletes — the converge contract. Each service ID gets its own
// instance in the fixture registry.
type entityStubConn struct {
	mu         sync.Mutex
	contacts   []kinds.Contact
	events     []kinds.CalendarEvent
	failPlace  error
	failList   error
	conflicts  []kinds.PlaceConflict
	skips      []kinds.PlaceSkip
	placeCalls int
	lastPlaced []string
}

// --- connectors.ServiceConnector (unused surfaces) ---

func (c *entityStubConn) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}

func (c *entityStubConn) SystemInstructions(string) string { return "" }

func (c *entityStubConn) HandleProxy(http.ResponseWriter, *http.Request, string) {}

// --- kinds.ContactProvider / ContactWriteProvider ---

func (c *entityStubConn) ListUserContacts(ctx context.Context) ([]kinds.Contact, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failList != nil {
		return nil, false, c.failList
	}
	return append([]kinds.Contact(nil), c.contacts...), false, nil
}

func (c *entityStubConn) PlaceContacts(ctx context.Context, in []kinds.Contact) (kinds.PlaceEntitiesResult, error) {
	names := make([]string, 0, len(in))
	for _, ct := range in {
		names = append(names, ct.Name)
	}
	return c.place(names, func(name string) {
		for i := range c.contacts {
			if c.contacts[i].Name == name {
				return
			}
		}
		c.contacts = append(c.contacts, kinds.Contact{
			Name:    name,
			Sources: []kinds.ContactSource{{Service: "stub", ID: "id-" + name}},
		})
	})
}

// --- kinds.CalendarEventProvider / CalendarEventWriteProvider ---

func (c *entityStubConn) ListCalendarEvents(ctx context.Context) ([]kinds.CalendarEvent, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failList != nil {
		return nil, false, c.failList
	}
	return append([]kinds.CalendarEvent(nil), c.events...), false, nil
}

func (c *entityStubConn) PlaceEvents(ctx context.Context, in []kinds.CalendarEvent) (kinds.PlaceEntitiesResult, error) {
	names := make([]string, 0, len(in))
	for _, ev := range in {
		names = append(names, ev.Title)
	}
	return c.place(names, func(name string) {
		for i := range c.events {
			if c.events[i].Title == name {
				return
			}
		}
		c.events = append(c.events, kinds.CalendarEvent{
			Title:   name,
			Sources: []kinds.CalendarEventSource{{Service: "stub", ID: "id-" + name}},
		})
	})
}

// place records one placement step: upsert each named item via put (never
// removing anything already held), reporting any armed failures, skips, and
// conflicts once.
func (c *entityStubConn) place(names []string, put func(name string)) (kinds.PlaceEntitiesResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.placeCalls++
	c.lastPlaced = append([]string(nil), names...)
	if c.failPlace != nil {
		err := c.failPlace
		c.failPlace = nil
		return kinds.PlaceEntitiesResult{}, err
	}
	res := kinds.PlaceEntitiesResult{Conflicts: c.conflicts, Skips: c.skips}
	c.conflicts, c.skips = nil, nil
	for i, name := range names {
		put(name)
		res.Copied++
		res.Placed = append(res.Placed, kinds.PlaceRecord{
			Name:       name,
			ExternalID: "id-" + name,
			ETag:       fmt.Sprintf("etag-%d-%d", c.placeCalls, i),
		})
	}
	return res, nil
}

// setFailPlace arms a one-shot placement failure.
func (c *entityStubConn) setFailPlace(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failPlace = err
}

// setConflicts arms conflicts (and optional skips) for the next placement.
func (c *entityStubConn) setConflicts(conflicts []kinds.PlaceConflict, skips []kinds.PlaceSkip) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conflicts, c.skips = conflicts, skips
}

// heldContacts returns the names the stub currently holds.
func (c *entityStubConn) heldContacts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.contacts))
	for _, ct := range c.contacts {
		out = append(out, ct.Name)
	}
	return out
}

// convergeStubRunners wires a Runners whose connectors are entity stubs and
// whose fixture host reports one sync group for kind (issue #365).
func convergeStubRunners(t *testing.T, kind, hub string, members []string, conns map[string]*entityStubConn) (*Runners, *fixtureHost) {
	t.Helper()
	h := newFixtureHost("https://aegis.example.com")
	h.grants = shared.GrantMap{
		"contacts": shared.AccessFull,
		"events":   shared.AccessFull,
	}
	for id, conn := range conns {
		h.services[id] = fixtureService{Name: id, Action: "inject"}
		h.connectors[id] = conn
	}
	h.syncGroups[kind] = fixtureSyncGroup{hub: hub, members: members}
	ledger, err := copies.Open(t.TempDir() + "/copy_ledger.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	lib := &library.Handlers{Host: h, Connectors: h.connectors, Copies: ledger}
	return &Runners{
		Host:       h,
		Mirror:     lib,
		Copies:     ledger,
		Connectors: h.connectors,
	}, h
}

// convergeTestRunners swaps in entity stubs and a job store.
func convergeTestRunners(t *testing.T, kind, hub string, members []string, conns map[string]*entityStubConn) (*Runners, *JobStore, *SyncWorker) {
	t.Helper()
	run, _ := convergeStubRunners(t, kind, hub, members, conns)
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	registerConvergeRunners(reg, run)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	return run, store, w
}
