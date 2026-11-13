package syncjobs

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	mu        sync.Mutex
	contacts  []kinds.Contact
	events    []kinds.CalendarEvent
	failPlace error
	failList  error
	truncated bool
	conflicts []kinds.PlaceConflict
	skips     []kinds.PlaceSkip
	// placeCalls and lastInput capture what placement received — the
	// watermark-enrichment assertions read the input refs.
	placeCalls    int
	lastPlaced    []string
	lastContactIn []kinds.Contact
	lastEventIn   []kinds.CalendarEvent
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
	return append([]kinds.Contact(nil), c.contacts...), c.truncated, nil
}

func (c *entityStubConn) PlaceContacts(ctx context.Context, in []kinds.Contact) (kinds.PlaceEntitiesResult, error) {
	names := make([]string, 0, len(in))
	for _, ct := range in {
		names = append(names, ct.Name)
	}
	return c.place(names, func(i int) {
		ct := in[i]
		for j := range c.contacts {
			if c.contacts[j].Name == ct.Name {
				c.contacts[j] = ct // create-or-update: the edit lands
				return
			}
		}
		c.contacts = append(c.contacts, ct)
	}, func() {
		c.lastContactIn = append([]kinds.Contact(nil), in...)
	})
}

// --- kinds.CalendarEventProvider / CalendarEventWriteProvider ---

func (c *entityStubConn) ListCalendarEvents(ctx context.Context) ([]kinds.CalendarEvent, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failList != nil {
		return nil, false, c.failList
	}
	return append([]kinds.CalendarEvent(nil), c.events...), c.truncated, nil
}

func (c *entityStubConn) PlaceEvents(ctx context.Context, in []kinds.CalendarEvent) (kinds.PlaceEntitiesResult, error) {
	names := make([]string, 0, len(in))
	for _, ev := range in {
		names = append(names, ev.Title)
	}
	return c.place(names, func(i int) {
		ev := in[i]
		for j := range c.events {
			if c.events[j].Title == ev.Title {
				c.events[j] = ev // create-or-update: the edit lands
				return
			}
		}
		c.events = append(c.events, ev)
	}, func() {
		c.lastEventIn = append([]kinds.CalendarEvent(nil), in...)
	})
}

// place records one placement step: upsert each item via put (never
// removing anything already held), reporting any armed failures, skips,
// and conflicts once. Names armed as conflicts or skips are refused like
// the real write paths — reported, never placed, never counted.
func (c *entityStubConn) place(names []string, put func(i int), capture func()) (kinds.PlaceEntitiesResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.placeCalls++
	c.lastPlaced = append([]string(nil), names...)
	if capture != nil {
		capture()
	}
	if c.failPlace != nil {
		err := c.failPlace
		c.failPlace = nil
		return kinds.PlaceEntitiesResult{}, err
	}
	res := kinds.PlaceEntitiesResult{Conflicts: c.conflicts, Skips: c.skips}
	refused := map[string]bool{}
	for _, cf := range c.conflicts {
		refused[cf.Name] = true
	}
	for _, sk := range c.skips {
		refused[sk.Name] = true
	}
	c.conflicts, c.skips = nil, nil
	for i, name := range names {
		if refused[name] {
			continue
		}
		put(i)
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

// contact returns the stored contact with the given name (zero if absent).
func (c *entityStubConn) contact(name string) kinds.Contact {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ct := range c.contacts {
		if ct.Name == name {
			return ct
		}
	}
	return kinds.Contact{}
}

// convergeStubRunners wires a Runners whose connectors are entity or
// playlist stubs and whose fixture host reports one sync group for kind
// (issue #365).
func convergeStubRunners(t *testing.T, kind, hub string, members []string, conns map[string]connectors.ServiceConnector) (*Runners, *fixtureHost) {
	t.Helper()
	h := newFixtureHost("https://aegis.example.com")
	h.grants = shared.GrantMap{
		"contacts":  shared.AccessFull,
		"events":    shared.AccessFull,
		"playlists": shared.AccessFull,
		"tracks":    shared.AccessFull,
		"albums":    shared.AccessFull,
		"artists":   shared.AccessFull,
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
	conflicts, err := OpenConflictStore(nil, t.TempDir()+"/sync_jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conflicts.Close() })
	lib := &library.Handlers{Host: h, Connectors: h.connectors, Copies: ledger}
	return &Runners{
		Host:       h,
		Mirror:     lib,
		Copies:     ledger,
		Conflicts:  conflicts,
		Connectors: h.connectors,
	}, h
}

// convergeTestRunners swaps in entity or playlist stubs and a job store.
func convergeTestRunners(t *testing.T, kind, hub string, members []string, conns map[string]connectors.ServiceConnector) (*Runners, *JobStore, *SyncWorker) {
	t.Helper()
	run, _ := convergeStubRunners(t, kind, hub, members, conns)
	store := testJobStore(t)
	run.Jobs = store
	reg := NewRunnerRegistry()
	registerConvergeRunners(reg, run)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	return run, store, w
}

// --- playlist converge fixtures (issue #365) ---

// playlistStubConn is a test connector for playlist convergence: a small
// playlist store implementing the placement contract — create, append, and
// the title write — with no delete surface at all (issue #365). Items page
// on offset cursors like Spotify; pageLimit (when set) forces small pages.
type playlistStubConn struct {
	mu         sync.Mutex
	svc        string
	pageLimit  int
	order      []string
	byID       map[string]*playlistStub
	nextID     int
	failPlace  error
	failList   error
	created    int
	placeCalls int
}

type playlistStub struct {
	id     string
	name   string
	tracks []kinds.Track
}

func newPlaylistStubConn(svc string) *playlistStubConn {
	return &playlistStubConn{svc: svc, byID: map[string]*playlistStub{}}
}

// seed adds a playlist holding one track per name, in order. Returns its id.
func (c *playlistStubConn) seed(name string, trackNames ...string) string {
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprintf("%s-pl-%d", c.svc, c.nextID)
	c.mu.Unlock()
	return c.seedAs(id, name, trackNames...)
}

// seedAs adds a playlist with a caller-chosen id (a pre-existing member
// copy that a copy-ledger row already points at).
func (c *playlistStubConn) seedAs(id, name string, trackNames ...string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	pl := &playlistStub{id: id, name: name}
	for _, n := range trackNames {
		pl.tracks = append(pl.tracks, stubTrack(c.svc, n))
	}
	c.byID[id] = pl
	c.order = append(c.order, id)
	return id
}

// addTrack appends one track to a playlist (a source-side edit between runs).
func (c *playlistStubConn) addTrack(id, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id].tracks = append(c.byID[id].tracks, stubTrack(c.svc, name))
}

// renameTo sets a playlist's title (a user-side edit between runs).
func (c *playlistStubConn) renameTo(id, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id].name = name
}

// trackNames returns a playlist's track names in order.
func (c *playlistStubConn) trackNames(id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	pl := c.byID[id]
	if pl == nil {
		return nil
	}
	out := make([]string, 0, len(pl.tracks))
	for _, t := range pl.tracks {
		out = append(out, t.Name)
	}
	return out
}

// title returns a playlist's current title.
func (c *playlistStubConn) title(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pl := c.byID[id]; pl != nil {
		return pl.name
	}
	return ""
}

// ids returns the playlist ids in listing order.
func (c *playlistStubConn) ids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

func stubTrack(svc, name string) kinds.Track {
	return kinds.Track{Name: name, Sources: []kinds.TrackSource{{Service: svc, ID: "t-" + name}}}
}

// --- connectors.ServiceConnector (unused surfaces) ---

func (c *playlistStubConn) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}

func (c *playlistStubConn) SystemInstructions(string) string { return "" }

func (c *playlistStubConn) HandleProxy(http.ResponseWriter, *http.Request, string) {}

// --- kinds.PlaylistProvider ---

func (c *playlistStubConn) ListUserPlaylists(ctx context.Context) ([]kinds.Playlist, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failList != nil {
		return nil, false, c.failList
	}
	out := make([]kinds.Playlist, 0, len(c.order))
	for _, id := range c.order {
		pl := c.byID[id]
		out = append(out, kinds.Playlist{
			Name:       pl.name,
			TrackCount: len(pl.tracks),
			Sources:    []kinds.PlaylistSource{{Service: c.svc, ID: pl.id}},
		})
	}
	return out, false, nil
}

// --- kinds.PlaylistItemsProvider ---

func (c *playlistStubConn) ListPlaylistItems(ctx context.Context, playlistID, cursor string, limit int) (kinds.Playlist, kinds.TrackPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := kinds.Playlist{Sources: []kinds.PlaylistSource{{Service: c.svc, ID: playlistID}}}
	pl := c.byID[playlistID]
	if pl == nil {
		return ref, kinds.TrackPage{}, fmt.Errorf("playlist %q not found", playlistID)
	}
	if c.failList != nil {
		return ref, kinds.TrackPage{}, c.failList
	}
	ref.Name = pl.name
	ref.TrackCount = len(pl.tracks)
	offset := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &offset)
	}
	if limit <= 0 {
		limit = 2
	}
	if c.pageLimit > 0 && limit > c.pageLimit {
		limit = c.pageLimit
	}
	end := offset + limit
	if end > len(pl.tracks) {
		end = len(pl.tracks)
	}
	if offset > len(pl.tracks) {
		offset = len(pl.tracks)
	}
	page := kinds.TrackPage{Tracks: pl.tracks[offset:end], Total: len(pl.tracks)}
	if end < len(pl.tracks) {
		page.Next = fmt.Sprint(end)
	}
	return ref, page, nil
}

// --- kinds.PlaylistItemWriteProvider ---

func (c *playlistStubConn) PlacePlaylistItems(ctx context.Context, playlistID, name string, tracks []kinds.Track) (kinds.PlacePlaylistItemsResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.placeCalls++
	if c.failPlace != nil {
		err := c.failPlace
		c.failPlace = nil
		return kinds.PlacePlaylistItemsResult{}, err
	}
	if playlistID == "" {
		c.nextID++
		id := fmt.Sprintf("%s-pl-%d", c.svc, c.nextID)
		pl := &playlistStub{id: id, name: name, tracks: append([]kinds.Track(nil), tracks...)}
		c.byID[id] = pl
		c.order = append(c.order, id)
		c.created++
		return kinds.PlacePlaylistItemsResult{
			PlaylistID: id,
			URL:        "https://example.com/" + id,
			Copied:     len(tracks),
		}, nil
	}
	pl := c.byID[playlistID]
	if pl == nil {
		return kinds.PlacePlaylistItemsResult{PlaylistID: playlistID}, fmt.Errorf("playlist %q not found", playlistID)
	}
	// Append only — nothing on the member is ever removed. The title write
	// on the append path is the contract rename propagation relies on.
	pl.tracks = append(pl.tracks, tracks...)
	if strings.TrimSpace(name) != "" {
		pl.name = name
	}
	return kinds.PlacePlaylistItemsResult{
		PlaylistID: playlistID,
		URL:        "https://example.com/" + playlistID,
		Copied:     len(tracks),
	}, nil
}

// setFailPlace arms a one-shot placement failure.
func (c *playlistStubConn) setFailPlace(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failPlace = err
}

// --- kinds.PlaylistMetaProvider (the freshness checker's probe) ---

func (c *playlistStubConn) GetPlaylistMeta(ctx context.Context, playlistID string) (kinds.PlaylistMeta, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pl := c.byID[playlistID]
	if pl == nil {
		return kinds.PlaylistMeta{}, false, nil
	}
	return kinds.PlaylistMeta{Name: pl.name, ItemCount: len(pl.tracks)}, true, nil
}

func (c *playlistStubConn) SavedTrackCount(ctx context.Context) (int, error) { return 0, nil }

// --- liked-tracks converge fixtures (issue #365) ---

// likedStubConn is a test connector for liked-tracks convergence: a liked
// list implementing the placement contract — saving is create-or-update and
// never un-likes. Items page on offset cursors; pageLimit (when set) forces
// small pages.
type likedStubConn struct {
	mu         sync.Mutex
	svc        string
	pageLimit  int
	liked      []kinds.Track
	failPlace  error
	failList   error
	placeCalls int
	lastPlaced []string
}

func newLikedStubConn(svc string) *likedStubConn {
	return &likedStubConn{svc: svc}
}

// seed adds held likes, one per name, in order.
func (c *likedStubConn) seed(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range names {
		c.liked = append(c.liked, stubTrack(c.svc, n))
	}
}

// setFailPlace arms a one-shot placement failure.
func (c *likedStubConn) setFailPlace(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failPlace = err
}

// heldNames returns the liked track names in order.
func (c *likedStubConn) heldNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.liked))
	for _, t := range c.liked {
		out = append(out, t.Name)
	}
	return out
}

// --- connectors.ServiceConnector (unused surfaces) ---

func (c *likedStubConn) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}

func (c *likedStubConn) SystemInstructions(string) string { return "" }

func (c *likedStubConn) HandleProxy(http.ResponseWriter, *http.Request, string) {}

// --- kinds.LikedTrackProvider / LikedTrackPageProvider ---

func (c *likedStubConn) ListUserLikedTracks(ctx context.Context) ([]kinds.Track, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failList != nil {
		return nil, false, c.failList
	}
	return append([]kinds.Track(nil), c.liked...), false, nil
}

func (c *likedStubConn) ListUserLikedTracksPage(ctx context.Context, cursor string, limit int) (kinds.TrackPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failList != nil {
		return kinds.TrackPage{}, c.failList
	}
	offset := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &offset)
	}
	if limit <= 0 {
		limit = 2
	}
	if c.pageLimit > 0 && limit > c.pageLimit {
		limit = c.pageLimit
	}
	end := offset + limit
	if end > len(c.liked) {
		end = len(c.liked)
	}
	if offset > len(c.liked) {
		offset = len(c.liked)
	}
	page := kinds.TrackPage{Tracks: c.liked[offset:end], Total: len(c.liked)}
	if end < len(c.liked) {
		page.Next = fmt.Sprint(end)
	}
	return page, nil
}

// --- kinds.LikedTrackWriteProvider ---

func (c *likedStubConn) SaveLikedTracks(ctx context.Context, tracks []kinds.Track) (kinds.SaveLikedTracksResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.placeCalls++
	c.lastPlaced = nil
	if c.failPlace != nil {
		err := c.failPlace
		c.failPlace = nil
		return kinds.SaveLikedTracksResult{}, err
	}
	held := kinds.NewTrackMatcher(c.liked)
	res := kinds.SaveLikedTracksResult{}
	for _, t := range tracks {
		c.lastPlaced = append(c.lastPlaced, t.Name)
		if held.Has(t) {
			continue // liking again is a no-op — and never an un-like
		}
		c.liked = append(c.liked, t)
		held.Add(t)
		res.Copied++
		res.Placed = append(res.Placed, kinds.PlaceRecord{
			Name:      t.Name,
			MatchedBy: kinds.TrackMatchName,
		})
	}
	return res, nil
}

// SavedTrackCount implements the freshness checker's liked-list probe.
func (c *likedStubConn) SavedTrackCount(ctx context.Context) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.liked), nil
}

// --- albums/artists converge fixtures (issue #375) ---

// saveStubConn is a test connector for saved-albums and followed-artists
// convergence: the two library-save lists implementing their placement
// contracts — saving and following are create-only and never un-saves or
// unfollows. Lists are small and whole-list, like the providers.
type saveStubConn struct {
	mu         sync.Mutex
	svc        string
	albums     []kinds.Album
	artists    []kinds.Artist
	failPlace  error
	failList   error
	placeCalls int
	lastPlaced []string
}

func newSaveStubConn(svc string) *saveStubConn {
	return &saveStubConn{svc: svc}
}

// seedAlbums adds held saves, one per name, in order.
func (c *saveStubConn) seedAlbums(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range names {
		c.albums = append(c.albums, stubAlbum(c.svc, n))
	}
}

// seedArtists adds held follows, one per name, in order.
func (c *saveStubConn) seedArtists(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range names {
		c.artists = append(c.artists, stubArtist(c.svc, n))
	}
}

// setFailPlace arms a one-shot placement failure.
func (c *saveStubConn) setFailPlace(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failPlace = err
}

// heldAlbumNames returns the saved album names in order.
func (c *saveStubConn) heldAlbumNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.albums))
	for _, a := range c.albums {
		out = append(out, a.Name)
	}
	return out
}

// heldArtistNames returns the followed artist names in order.
func (c *saveStubConn) heldArtistNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.artists))
	for _, a := range c.artists {
		out = append(out, a.Name)
	}
	return out
}

// --- connectors.ServiceConnector (unused surfaces) ---

func (c *saveStubConn) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}

func (c *saveStubConn) SystemInstructions(string) string { return "" }

func (c *saveStubConn) HandleProxy(http.ResponseWriter, *http.Request, string) {}

// --- kinds.AlbumProvider / ArtistProvider ---

func (c *saveStubConn) ListUserSavedAlbums(ctx context.Context) ([]kinds.Album, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failList != nil {
		return nil, false, c.failList
	}
	return append([]kinds.Album(nil), c.albums...), false, nil
}

func (c *saveStubConn) ListUserFollowedArtists(ctx context.Context) ([]kinds.Artist, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failList != nil {
		return nil, false, c.failList
	}
	return append([]kinds.Artist(nil), c.artists...), false, nil
}

// --- kinds.AlbumWriteProvider / ArtistWriteProvider ---

func (c *saveStubConn) SaveAlbums(ctx context.Context, albums []kinds.Album) (kinds.SaveAlbumsResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.placeCalls++
	c.lastPlaced = nil
	if c.failPlace != nil {
		err := c.failPlace
		c.failPlace = nil
		return kinds.SaveAlbumsResult{}, err
	}
	held := kinds.NewAlbumMatcher(c.albums)
	res := kinds.SaveAlbumsResult{}
	for _, a := range albums {
		c.lastPlaced = append(c.lastPlaced, a.Name)
		if held.Has(a) {
			continue // saving again is a no-op — and never an un-save
		}
		c.albums = append(c.albums, a)
		held.Add(a)
		res.Copied++
		res.Placed = append(res.Placed, kinds.PlaceRecord{
			Name:      a.Name,
			MatchedBy: kinds.TrackMatchName,
		})
	}
	return res, nil
}

func (c *saveStubConn) FollowArtists(ctx context.Context, artists []kinds.Artist) (kinds.FollowArtistsResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.placeCalls++
	c.lastPlaced = nil
	if c.failPlace != nil {
		err := c.failPlace
		c.failPlace = nil
		return kinds.FollowArtistsResult{}, err
	}
	held := kinds.NewArtistMatcher(c.artists)
	res := kinds.FollowArtistsResult{}
	for _, a := range artists {
		c.lastPlaced = append(c.lastPlaced, a.Name)
		if held.Has(a) {
			continue // following again is a no-op — and never an unfollow
		}
		c.artists = append(c.artists, a)
		held.Add(a)
		res.Copied++
		res.Placed = append(res.Placed, kinds.PlaceRecord{
			Name:      a.Name,
			MatchedBy: kinds.TrackMatchName,
		})
	}
	return res, nil
}

func stubAlbum(svc, name string) kinds.Album {
	return kinds.Album{Name: name, Sources: []kinds.AlbumSource{{Service: svc, ID: "al-" + name}}}
}

func stubArtist(svc, name string) kinds.Artist {
	return kinds.Artist{Name: name, Sources: []kinds.ArtistSource{{Service: svc, ID: "ar-" + name, Name: name}}}
}
