package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// kindRuntimeEntry is core-owned visibility wiring for one library kind.
// Predicates stay here — not on types.LibraryKind (issue #15).
type kindRuntimeEntry struct {
	// HasProviders reports whether any connector can feed this kind.
	HasProviders func(s *Server) bool
	// Readable reports whether any non-blocked service allows reads.
	Readable func(s *Server) bool
}

// kindRuntime maps kind ID → visibility predicates. Keep keys in sync with
// libraryKinds (TestLibraryKinds_KindRuntimeInSync).
var kindRuntime = map[string]kindRuntimeEntry{
	"playlists": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.PlaylistProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("playlists") },
	},
	"tracks": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.LikedTrackProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("tracks") },
	},
	"albums": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.AlbumProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("albums") },
	},
	"artists": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.ArtistProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("artists") },
	},
	"contacts": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.ContactProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("contacts") },
	},
	"photos": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.PhotoProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("photos") },
	},
	"events": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.CalendarEventProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("events") },
	},
	"messages": {
		HasProviders: func(s *Server) bool {
			if s.hasProvider[types.MessageProvider]() {
				return true
			}
			// Local mail archive (Gmail sync + Takeout) is a collection source
			// even without a live connector (issue #139).
			return s.mail != nil && s.mailReadable()
		},
		Readable: func(s *Server) bool { return s.libraryReadableFor("messages") },
	},
	"transactions": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.TransactionProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("transactions") },
	},
	"places": {
		HasProviders: func(s *Server) bool {
			if s.hasProvider[types.PlaceVisitProvider]() {
				return true
			}
			// Local Timeline import is a collection source even without a
			// live connector (issue #142).
			return s.timeline != nil && s.timeline.Enabled()
		},
		Readable: func(s *Server) bool { return s.libraryReadableFor("places") },
	},
}

// kindHasProviders reports whether any connector can feed the kind.
func kindHasProviders(id string, s *Server) bool {
	if schema, ok := s.udtSchemaForKind(id); ok {
		return len(schema.Sources) > 0
	}
	rt := kindRuntime[id]
	if rt.HasProviders == nil {
		return false
	}
	return rt.HasProviders(s)
}

// kindReadable reports whether at least one non-blocked service allows reads.
func kindReadable(id string, s *Server) bool {
	if schema, ok := s.udtSchemaForKind(id); ok {
		return s.userDefinedReadable(schema.PermType)
	}
	rt := kindRuntime[id]
	if rt.Readable == nil {
		return false
	}
	return rt.Readable(s)
}

// libraryKinds is the registry for every unified library dataset. Order is
// stable and used for route registration and pre-request injection priority.
var libraryKinds = []types.LibraryKind{
	{
		ID:              "playlists",
		Path:            "/v1/library/playlists",
		PermType:        "playlists",
		Label:           "playlists",
		Usage:           "playlists or their music library",
		Summary:         "all user playlists from every connected service in one neutral list (name, description, owner, track_count, sources[])",
		SyncRule:        "Same-named playlists are synced into one entry with multiple sources[].",
		ExampleQuestion: "what playlists do I have?",
		Keywords: []string{
			"playlist", "play list", "music library", "my library", "saved playlists",
		},
		EmptyMsg: "no playlist-capable services configured",
		DenyMsg:  "permission denied: agent access to playlists is not allowed",
		FailMsg:  "failed to fetch playlists",
	},
	{
		ID:              "tracks",
		Path:            "/v1/library/tracks",
		PermType:        "tracks",
		Label:           "liked tracks",
		Usage:           "songs they like, saved tracks, or favorites",
		Summary:         "all liked/saved tracks from every connected music service in one neutral list (name, artists, album, duration, entity_id, sources[])",
		SyncRule:        "Tracks merge on durable entity_id (ISRC or MusicBrainz recording MBID) when present; otherwise same title + primary artist when durations agree (match=name).",
		ExampleQuestion: "what songs have I liked?",
		Keywords: []string{
			"liked song", "liked track", "liked music",
			"saved song", "saved track",
			"favorite song", "favourite song", "favorite track", "favourite track",
			"have i liked", "songs i like", "tracks i like",
			"songs i've liked", "tracks i've liked",
			"songs have i liked", "tracks have i liked",
			"my likes", "my favorites", "my favourites",
		},
		EmptyMsg: "no liked-track-capable services configured",
		DenyMsg:  "permission denied: agent access to tracks is not allowed",
		FailMsg:  "failed to fetch liked tracks",
	},
	{
		ID:              "albums",
		Path:            "/v1/library/albums",
		PermType:        "albums",
		Label:           "saved albums",
		Usage:           "albums they saved or their album library",
		Summary:         "all saved albums from every connected music service in one neutral list (name, artists, entity_id, sources[])",
		SyncRule:        "Albums merge on durable entity_id (UPC or MusicBrainz release-group MBID) when present; otherwise same title + primary artist (match=name).",
		ExampleQuestion: "what albums have I saved?",
		Keywords: []string{
			"saved album", "liked album", "favorite album", "favourite album",
			"albums i've saved", "albums have i saved", "albums i saved",
			"my albums", "album library",
		},
		EmptyMsg: "no album-capable services configured",
		DenyMsg:  "permission denied: agent access to albums is not allowed",
		FailMsg:  "failed to fetch albums",
	},
	{
		ID:              "artists",
		Path:            "/v1/library/artists",
		PermType:        "artists",
		Label:           "followed artists",
		Usage:           "artists they follow or favorite artists",
		Summary:         "all followed/top artists from every connected music service in one neutral list (name, entity_id, sources[])",
		SyncRule:        "Artists merge on durable entity_id (MusicBrainz MBID) when resolved (match=mbid); otherwise same normalized name (match=name).",
		ExampleQuestion: "what artists do I follow?",
		Keywords: []string{
			"followed artist", "artists i follow", "artists do i follow",
			"who do i follow", "do i follow", "artists i've followed",
			"favorite artist", "favourite artist", "my artists",
			"top artists", "artists i like",
		},
		EmptyMsg: "no artist-capable services configured",
		DenyMsg:  "permission denied: agent access to artists is not allowed",
		FailMsg:  "failed to fetch artists",
	},
	{
		ID:              "contacts",
		Path:            "/v1/library/contacts",
		PermType:        "contacts",
		Label:           "contacts",
		Usage:           "people, contacts, or their address book",
		Summary:         "all user contacts from every connected service in one neutral list (name, emails, phones, handles, organization, sources[])",
		SyncRule:        "Contacts that share an email, phone, handle, or name are synced into one entry with multiple sources[].",
		ExampleQuestion: "who is in my contacts?",
		Keywords: []string{
			"my contacts", "address book", "contact list", "contacts list",
			"who is in my contacts", "people i know",
		},
		EmptyMsg: "no contact-capable services configured",
		DenyMsg:  "permission denied: agent access to contacts is not allowed",
		FailMsg:  "failed to fetch contacts",
	},
	{
		ID:              "photos",
		Path:            "/v1/library/photos",
		PermType:        "photos",
		Label:           "photos",
		Usage:           "photos, pictures, or their photo library",
		Summary:         "all user photos from every connected photo service in one neutral list (taken_at, dimensions, mime_type, filename, people[] as Contact identities, sources[] with canonical blob location). Google Photos contributes the user's last picker selection.",
		SyncRule:        "Photos with the same content hash or the same taken_at plus dimensions are synced into one entry with multiple sources[]. people[] entries merge with the same contact identity matching used by /v1/library/contacts. Photo bytes are never stored in Aegis. Google Photos uses the Picker API: the user must pick photos once before they appear here.",
		ExampleQuestion: "what photos do I have?",
		Keywords: []string{
			"my photos", "photo library", "my pictures", "picture library",
			"photos from", "pictures from", "photos do i have",
			"photos i have", "list my photos", "list my pictures",
			"google photos", "camera roll",
			// Singular / collection phrasing missed before issue #125
			// ("oldest photo in my collection" matched nothing).
			"photo", "picture",
			"photo collection", "photos in my", "pictures in my",
		},
		EmptyMsg: "no photo-capable services configured",
		DenyMsg:  "permission denied: agent access to photos is not allowed",
		FailMsg:  "failed to fetch photos",
	},
	{
		ID:              "events",
		Path:            "/v1/library/events",
		PermType:        "events",
		Label:           "calendar events",
		Usage:           "calendar events, meetings, appointments, or their schedule",
		Summary:         "all calendar events from every connected calendar service in one neutral list (title, description, start_at, end_at, all_day, location, status, organizer, attendees as Contact identities, sources[])",
		SyncRule:        "Events merge on durable iCalUID when present (match=ical_uid); otherwise same title + start time + all-day flag (match=title_start). Event content stays on the source service.",
		ExampleQuestion: "what is on my calendar?",
		Keywords: []string{
			"my calendar", "my schedule", "on my calendar",
			"calendar events", "upcoming events", "upcoming meetings",
			"what meetings", "meetings do i have", "meetings i have",
			"appointments", "my events", "events this week",
			"google calendar", "what's on my calendar", "whats on my calendar",
		},
		EmptyMsg: "no calendar-capable services configured",
		DenyMsg:  "permission denied: agent access to events is not allowed",
		FailMsg:  "failed to fetch calendar events",
	},
	{
		ID:              "messages",
		Path:            "/v1/library/messages",
		PermType:        "messages",
		Label:           "messages",
		Usage:           "emails, messages, inbox, mail, or recent correspondence between people (not app notifications)",
		Summary:         "the user's mail collection from every message source in one neutral list (channel, subject, snippet, from/to/cc as Contact identities, sent_at, direction, read, entity_id, sources[]). Sources include live Gmail and the local archive (Gmail sync + Google Takeout). Messages are person-to-person text — not app/service notifications.",
		SyncRule:        "Gmail and Google Takeout are sources of one mail collection, synchronized on RFC Message-ID (match=message_id); otherwise same subject + from + sent_at (match=subject_from_time). Message bodies stay on the source service. Messages are person-to-person text (sent by or to the user) — app/service notifications are out of scope (future Notifications type). SMS/WhatsApp connectors are not implemented yet. Bluesky contributes the user's own posts and replies only.",
		ExampleQuestion: "what emails have I received recently?",
		Keywords: []string{
			"my email", "my emails", "my mail", "my inbox",
			"recent email", "recent emails", "recent messages",
			"emails have i", "emails i have", "emails i received",
			"check my email", "check my mail", "check my inbox",
			"messages in my inbox", "unread email", "unread emails",
			"google mail", "gmail inbox", "correspondence",
		},
		EmptyMsg: "no message-capable services configured",
		DenyMsg:  "permission denied: agent access to messages is not allowed",
		FailMsg:  "failed to fetch messages",
	},
	{
		ID:              "transactions",
		Path:            "/v1/library/transactions",
		PermType:        "transactions",
		Label:           "transactions",
		Usage:           "transactions, charges, spending, bank activity, budget line items, or money history",
		Summary:         "all financial transactions from every connected source in one neutral list (posted_at, authorized_at, amount as a signed decimal string, currency, description, merchant, category[] (multi-valued for YNAB splits), status, sources[]). Account fields never include full account numbers.",
		SyncRule:        "Transactions merge on durable provider_id (YNAB import_id, Quicken FITID) when present; otherwise same amount + posted day + normalized merchant (match=amount_date_merchant), with an adjacent-day (±1) pass for cross-provider duplicates. No Aegis category taxonomy — provider category only.",
		ExampleQuestion: "did we go over on groceries this month?",
		Keywords: []string{
			"transaction", "transactions", "my transactions",
			"spending", "my spending", "how much did i spend",
			"how much have i spent", "what did i spend",
			"charge", "charges", "card charge", "bank charge",
			"bank", "banking", "bank account", "bank activity",
			"card", "credit card", "debit card",
			"budget", "budgeting", "my budget",
			"money", "expenses", "expense", "spent",
			"groceries", "subscription", "subscriptions",
			"ynab", "quicken", "mint",
		},
		EmptyMsg: "no transaction-capable services configured",
		DenyMsg:  "permission denied: agent access to transactions is not allowed",
		FailMsg:  "failed to fetch transactions",
	},
	{
		ID:              "places",
		Path:            "/v1/library/places",
		PermType:        "places",
		Label:           "places",
		Usage:           "where I was, location history, visits, trips, places I've been, travel chronology, time at a place, or residence/absence timelines",
		Summary:         "visits and trips from every connected source in one neutral list (start_at, end_at, place{name,address,locality,country,lat,lng}, kind=visit|trip, mode for trips, confidence, sources[]). Models visit windows — not raw GPS streams. Coordinates require the precise permission.",
		SyncRule:        "Visits merge on durable provider_id when present; otherwise same start/end window + normalized place (match=window_place). Photos can be joined to visits by taken_at inside the window.",
		ExampleQuestion: "where was I last summer?",
		Keywords: []string{
			"where was i", "where was i?", "where have i been",
			"where did i go", "where did we go", "where were we",
			"location history", "my locations", "places i've been",
			"places i have been", "visit", "visits", "trip", "trips",
			"travel history", "travel chronology", "my travel",
			"was i out of town", "out of town", "was i away",
			"when was i in", "when did i visit", "when did we visit",
			"how long was i in", "how long did we stay",
			"timeline", "google timeline", "maps timeline",
			"owntracks", "dawarich", "significant locations",
			"residence", "absence timeline", "when was i home",
			"which beach was that", "where was that photo taken",
		},
		EmptyMsg: "no places-capable services configured (enable places permissions and connect OwnTracks or import a Google Timeline export)",
		DenyMsg:  "permission denied: agent access to places is not allowed (places defaults to none — enable city, read, or precise in service permissions)",
		FailMsg:  "failed to fetch places",
	},
}

// LibraryKindByID returns the registered kind, or nil.
func LibraryKindByID(id string) *types.LibraryKind {
	for i := range libraryKinds {
		if libraryKinds[i].ID == id {
			return &libraryKinds[i]
		}
	}
	return nil
}

// allLibraryKinds returns built-in kinds plus any loaded user-defined kinds
// (issue #15). Prefer this over ranging libraryKinds directly so UDTs appear
// in pre-request injection, admin listings, and flow maps.
func (s *Server) allLibraryKinds() []types.LibraryKind {
	out := make([]types.LibraryKind, 0, len(libraryKinds))
	out = append(out, libraryKinds...)
	if s != nil && s.udts != nil {
		for _, schema := range s.udts.All() {
			if LibraryKindByID(schema.ID) == nil {
				out = append(out, schema.LibraryKind())
			}
		}
	}
	return out
}

// udtSchemaForKind returns the user-defined schema backing a kind id, if any.
func (s *Server) udtSchemaForKind(id string) (types.UserDefinedTypeSchema, bool) {
	if s == nil || s.udts == nil {
		return types.UserDefinedTypeSchema{}, false
	}
	return s.udts.Get(id)
}

// libraryInstructionsFor returns kind instructions plus a configured canonical
// service hint when the instance declares one for this kind, and the
// multi-source mutation rule.
func (s *Server) libraryInstructionsFor(kindID string) string {
	k := LibraryKindByID(kindID)
	if k == nil {
		return ""
	}
	text := k.Instructions(s.baseURL)
	if kindID == "events" {
		root := strings.TrimRight(s.baseURL, "/")
		text += fmt.Sprintf(" For scheduling or availability-only questions use GET %s/v1/library/freebusy — busy intervals only (start_at, end_at, all_day); never includes titles or attendees (issue #128).", root)
	}
	if name := s.canonicalServiceName(kindID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the home copy.", k.Label, name)
	}
	if kindID == "photos" {
		text += "\n" + types.PhotoSourceDiscipline()
	}
	text += "\n" + multiSourceMutationRule()
	return text
}

// anyServiceTruncated reports whether any per-service status hit a page cap
// or left an unfollowed continuation. Used for the top-level truncated flag.
func anyServiceTruncated(bySvc map[string]types.LibraryServiceStatus) bool {
	for _, st := range bySvc {
		if st.Truncated {
			return true
		}
	}
	return false
}

// hasProvider reports whether any registered connector implements P.
func (s *Server) hasProvider[P any]() bool {
	for _, connector := range s.connectors {
		if _, ok := any(connector).(P); ok {
			return true
		}
	}
	return false
}

// libraryReadableFor reports whether at least one non-blocked service allows
// read access to the given permission/data type.
func (s *Server) libraryReadableFor(permType string) bool {
	for _, svc := range s.config.ServiceSnapshots() {
		if svc.Action == "block" {
			continue
		}
		if s.effectivePermissions(&svc).AllowsRead(permType) {
			return true
		}
	}
	return false
}

// libraryHintsForMessage returns instruction blocks for every library kind
// that matches a service-less user question. Used by the pre-request hook.
func (s *Server) libraryHintsForMessage(msg string) []string {
	var out []string
	for _, k := range s.allLibraryKinds() {
		if !k.Mentions(msg) {
			continue
		}
		if !kindHasProviders(k.ID, s) || !kindReadable(k.ID, s) {
			continue
		}
		if schema, ok := s.udtSchemaForKind(k.ID); ok {
			out = append(out, s.userDefinedHint(schema))
			continue
		}
		out = append(out, s.libraryInstructionsFor(k.ID))
	}
	return out
}

// libraryRouteHandlers maps each kind path to its HTTP handler so main can
// register every dataset from the registry. User-defined kinds (issue #15)
// are merged in when schemas are loaded.
func (s *Server) libraryRouteHandlers() map[string]http.HandlerFunc {
	handlers := map[string]http.HandlerFunc{
		"/v1/library/playlists":    s.HandleLibraryPlaylists,
		"/v1/library/tracks":       s.HandleLibraryTracks,
		"/v1/library/albums":       s.HandleLibraryAlbums,
		"/v1/library/artists":      s.HandleLibraryArtists,
		"/v1/library/contacts":     s.HandleLibraryContacts,
		"/v1/library/photos":       s.HandleLibraryPhotos,
		"/v1/library/events":       s.HandleLibraryEvents,
		"/v1/library/freebusy":     s.HandleLibraryFreeBusy,
		"/v1/library/messages":     s.HandleLibraryMessages,
		"/v1/library/transactions": s.HandleLibraryTransactions,
		"/v1/library/places":       s.HandleLibraryPlaceVisits,
		"/v1/library/copies":       s.HandleLibraryCopies,
		"/v1/library/review":       s.HandleMergeReview,
		"/v1/library/links":        s.HandleEntityLinks,
		"/v1/library/links/":       s.HandleEntityLinks,
	}
	for path, handler := range s.udtRouteHandlers() {
		handlers[path] = handler
	}
	return handlers
}

// namedLister is one service's typed fetch for a library aggregate.
type namedLister[T any] struct {
	id   string
	list func(ctx context.Context) (items []T, truncated bool, err error)
}

// collectProviders type-asserts each connector to P and builds fetch targets
// that pass the permission check for permType.
func collectProviders[T any, P any](
	s *Server,
	permType string,
	lister func(P) func(ctx context.Context) ([]T, bool, error),
) (targets []namedLister[T], denied []string) {
	for id, connector := range s.connectors {
		svc := s.config.GetService(id)
		if svc == nil || svc.Action == "block" {
			continue
		}
		provider, ok := any(connector).(P)
		if !ok {
			continue
		}
		if !s.effectivePermissions(svc).AllowsRead(permType) {
			denied = append(denied, id)
			continue
		}
		targets = append(targets, namedLister[T]{id: id, list: lister(provider)})
	}
	return targets, denied
}

// libraryKindSpec wires one typed dataset into the shared aggregate pipeline.
type libraryKindSpec[T any] struct {
	kind    types.LibraryKind
	targets func(s *Server, r *http.Request) (targets []namedLister[T], denied []string)
	// serviceOf returns the first source service name for stable pre-sort.
	serviceOf func(T) string
	// sortKey is the identity key used after service-name grouping.
	sortKey func(T) string
	merge   func([]T) []T
	encode  func(merged []T, bySvc map[string]types.LibraryServiceStatus) any
	// markCanonical stamps Source.Canonical on items from a config-declared
	// canonical_for service (issue #16). Nil skips config stamping.
	markCanonical func(items []T, serviceID string)
	// stampCopies attaches copy-ledger freshness to sources[] (issue #76).
	// Method values from s are bound; signature is items-only.
	stampCopies func(items []T)
	// enrich optionally resolves durable IDs before merge (issue #4).
	enrich func(ctx context.Context, s *Server, items []T)
	// cacheKey overrides kind.CacheKey() when a second endpoint shares the
	// same dataset but a different response shape (freebusy view of events).
	cacheKey string
}

// handleLibraryAggregate is the shared GET /v1/library/{kind} pipeline:
// method check, cache, permission-aware fan-out, merge, and JSON response.
func handleLibraryAggregate[T any](s *Server, w http.ResponseWriter, r *http.Request, spec libraryKindSpec[T]) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	kind := spec.kind
	logger := withPeerLog(slog.With("handler", "library_"+kind.ID), r)
	agent := agentForRequest(r)

	// Third-party apps are scope-limited even when service permissions would
	// allow more (issue #131).
	app := AppIdentityFrom(r)
	if app != nil && !appAllowsLibrary(app, kind.PermType, r.Method) {
		logger.Info("library "+kind.ID+" denied by app scope",
			"app_id", app.ID, "scope", app.Scopes.For(kind.PermType))
		s.recordAccessFrom(r, AccessEvent{
			Agent:    agent,
			Surface:  MetricSurfaceLibrary,
			Target:   kind.ID,
			DataType: kind.ID,
			Cache:    MetricCacheMiss,
			Decision: DecisionDeny,
			Reason:   "app_scope",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		writeJSONError(w, http.StatusForbidden,
			fmt.Sprintf("permission denied: app access to %s is %q", kind.ID, app.Scopes.For(kind.PermType)))
		return
	}

	cacheKey := kind.CacheKey()
	if spec.cacheKey != "" {
		cacheKey = spec.cacheKey
	}
	cacheKey += appLibraryCacheSuffix(app, kind.PermType)
	if cached, ok := s.cache.Get(cacheKey); ok {
		logger.Debug("cache hit")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Aegis-Cache", "hit")
		w.Write([]byte(cached))
		s.recordAccessFrom(r, AccessEvent{
			Agent:    agent,
			Surface:  MetricSurfaceLibrary,
			Target:   kind.ID,
			DataType: kind.ID,
			Cache:    MetricCacheHit,
		})
		return
	}

	targets, denied := spec.targets(s, r)
	for _, id := range denied {
		svc := s.config.GetService(id)
		perm := ""
		if svc != nil {
			perm = s.effectivePermissions(svc).For(kind.PermType)
		}
		logger.Info("library "+kind.ID+" denied by permission", "service", id, "permission", perm)
		// Per-service denial for the audit trail (issue #105). Even when
		// other providers still answer the aggregate request.
		s.recordAccessFrom(r, AccessEvent{
			Agent:    agent,
			Surface:  MetricSurfaceService,
			Target:   id,
			DataType: kind.ID,
			Decision: DecisionDeny,
			Reason:   "permission",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
	}

	if len(targets) == 0 {
		if len(denied) > 0 {
			s.recordAccessFrom(r, AccessEvent{
				Agent:    agent,
				Surface:  MetricSurfaceLibrary,
				Target:   kind.ID,
				DataType: kind.ID,
				Decision: DecisionDeny,
				Reason:   "permission",
				Method:   r.Method,
				Status:   http.StatusForbidden,
			})
			writeJSONError(w, http.StatusForbidden, kind.DenyMsg)
			return
		}
		writeJSONError(w, http.StatusNotFound, kind.EmptyMsg)
		return
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		all   []T
		bySvc = make(map[string]types.LibraryServiceStatus, len(targets))
	)

	ctx := r.Context()
	for _, t := range targets {
		wg.Add(1)
		go func(t namedLister[T]) {
			defer wg.Done()
			var items []T
			var truncated bool
			var err error
			// A misbehaving connector must degrade to status=error, not take
			// down the process (library fan-out runs outside net/http recovery).
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						logger.Error("library "+kind.ID+" provider panicked",
							"service", t.id, "panic", rec)
						err = fmt.Errorf("provider panic: %v", rec)
						items = nil
						truncated = false
					}
				}()
				items, truncated, err = t.list(ctx)
			}()
			var status types.LibraryServiceStatus
			switch {
			case err == nil:
				if spec.markCanonical != nil {
					spec.markCanonical(items, t.id)
				}
				status = types.LibraryServiceStatus{
					Status:    types.LibraryStatusOK,
					Count:     len(items),
					Truncated: truncated,
				}
				s.recordAccessFrom(r, AccessEvent{
					Agent:    agent,
					Surface:  MetricSurfaceService,
					Target:   t.id,
					DataType: kind.ID,
				})
				if truncated {
					logger.Warn("library "+kind.ID+" truncated by page cap",
						"service", t.id, "count", len(items))
				}
				mu.Lock()
				all = append(all, items...)
				mu.Unlock()
			case errors.Is(err, types.ErrUnauthorized):
				logger.Info("library "+kind.ID+" unauthorized", "service", t.id)
				status = types.LibraryServiceStatus{
					Status: types.LibraryStatusUnauthorized,
					Error:  "not authorized — connect at /v1/auth/" + t.id + "/login",
				}
			default:
				logger.Warn("library "+kind.ID+" failed", "service", t.id, "error", err)
				status = types.LibraryServiceStatus{Status: types.LibraryStatusError, Error: kind.FailMsg}
			}
			mu.Lock()
			bySvc[t.id] = status
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	enrichLibraryServiceStatuses(s, kind.ID, bySvc)

	sort.SliceStable(all, func(i, j int) bool {
		si, sj := spec.serviceOf(all[i]), spec.serviceOf(all[j])
		if si != sj {
			return si < sj
		}
		return spec.sortKey(all[i]) < spec.sortKey(all[j])
	})

	if spec.enrich != nil {
		spec.enrich(ctx, s, all)
	}

	merged := spec.merge(all)
	if merged == nil {
		merged = []T{}
	}

	if spec.stampCopies != nil {
		spec.stampCopies(merged)
	}

	result, err := json.Marshal(spec.encode(merged, bySvc))
	if err != nil {
		logger.Error("failed to marshal library "+kind.ID, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build "+kind.Label+" list")
		return
	}

	// Cache only healthy aggregates. Freezing by_service error/unauthorized
	// rows would keep a just-connected service looking disconnected (issue #104).
	if !libraryAggregateDegraded(bySvc) {
		s.cache.Set(cacheKey, string(result), 60*time.Second)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Aegis-Cache", "miss")
	w.Write(result)
	s.recordAccessFrom(r, AccessEvent{
		Agent:    agent,
		Surface:  MetricSurfaceLibrary,
		Target:   kind.ID,
		DataType: kind.ID,
		Cache:    MetricCacheMiss,
	})
}

// libraryAggregateDegraded reports whether any provider failed or was
// unauthorized. Degraded aggregates must not be cached.
func libraryAggregateDegraded(bySvc map[string]types.LibraryServiceStatus) bool {
	for _, st := range bySvc {
		if st.Status != types.LibraryStatusOK {
			return true
		}
	}
	return false
}

// enrichLibraryServiceStatuses fills display name, canonical role, and
// last-sync fields used by the library source-overview header (issue #162).
// Fail-open: missing config or archive metadata leaves the original status.
func enrichLibraryServiceStatuses(s *Server, kindID string, bySvc map[string]types.LibraryServiceStatus) {
	if len(bySvc) == 0 {
		return
	}
	canonicalID := ""
	for _, svc := range s.config.ServiceSnapshots() {
		if svc.IsCanonicalFor(kindID) {
			canonicalID = svc.ID
			break
		}
	}
	mirrored := canonicalID != ""
	for id, st := range bySvc {
		st.Name = librarySourceDisplayName(s, id)
		if mirrored {
			if id == canonicalID {
				st.Canonical = true
				st.Role = "canonical"
			} else {
				st.Role = "mirror"
			}
		}
		if ts := librarySourceLastSyncedAt(s, id); ts != "" {
			st.LastSyncedAt = ts
		}
		bySvc[id] = st
	}
	// Messages: surface Gmail + Google Takeout as separate overview sources
	// (issue #162) when the local archive knows both. Keep the mail-archive
	// fetch row so by_service still reports the aggregate target.
	if kindID == "messages" && s.mail != nil {
		expandMailArchiveSources(s, bySvc)
	}
}

func librarySourceDisplayName(s *Server, id string) string {
	switch id {
	case "mail-archive":
		return "Mail archive"
	case takeoutServiceID:
		return takeoutServiceName
	case connectors.GoogleGmailServiceID:
		return connectors.GoogleGmailServiceName
	case connectors.GoogleCalendarServiceID:
		return connectors.GoogleCalendarServiceName
	case connectors.GoogleContactsServiceID:
		return connectors.GoogleContactsServiceName
	case connectors.GooglePhotosServiceID:
		return connectors.GooglePhotosServiceName
	case connectors.ImmichServiceID:
		return connectors.ImmichServiceName
	case connectors.YNABServiceID:
		return connectors.YNABServiceName
	case connectors.OwnTracksServiceID:
		return connectors.OwnTracksServiceName
	}
	if svc := s.config.GetService(id); svc != nil && svc.Name != "" {
		return svc.Name
	}
	return id
}

func librarySourceLastSyncedAt(s *Server, id string) string {
	if s.mail == nil {
		return ""
	}
	switch id {
	case "mail-archive", takeoutServiceID, connectors.GoogleGmailServiceID:
		st, err := s.mail.Status()
		if err != nil {
			return ""
		}
		if id == takeoutServiceID || id == connectors.GoogleGmailServiceID {
			if cur, err := s.mail.LoadSyncCursor(id); err == nil && cur.LastSyncAt != "" {
				return cur.LastSyncAt
			}
		}
		return st.LastSyncAt
	}
	return ""
}

// expandMailArchiveSources adds per-source rows for Gmail-synced and Takeout
// mail when the archive has both. Counts come from the archive inventory
// (the affirmative understanding of those sources), not the library page cap.
func expandMailArchiveSources(s *Server, bySvc map[string]types.LibraryServiceStatus) {
	st, err := s.mail.Status()
	if err != nil {
		return
	}
	for svc, n := range st.ByService {
		if n <= 0 {
			continue
		}
		id := svc
		name := mailServiceName(svc)
		last := ""
		if cur, err := s.mail.LoadSyncCursor(svc); err == nil {
			last = cur.LastSyncAt
		}
		if last == "" {
			last = st.LastSyncAt
		}
		role := ""
		canonical := false
		// Takeout is a historical export nested under the live Gmail home when
		// Gmail is present; otherwise it stands alone.
		parent := ""
		if id == takeoutServiceID {
			if _, ok := bySvc[connectors.GoogleGmailServiceID]; ok {
				parent = connectors.GoogleGmailServiceID
			} else if _, ok := bySvc["mail-archive"]; ok {
				parent = "mail-archive"
			}
		}
		existing, exists := bySvc[id]
		if exists && existing.Count >= n {
			// Keep the live-fetch row; just fill overview metadata.
			existing.Name = name
			if existing.LastSyncedAt == "" {
				existing.LastSyncedAt = last
			}
			if parent != "" && existing.Parent == "" {
				existing.Parent = parent
			}
			bySvc[id] = existing
			continue
		}
		bySvc[id] = types.LibraryServiceStatus{
			Status:       types.LibraryStatusOK,
			Count:        n,
			Name:         name,
			LastSyncedAt: last,
			Canonical:    canonical,
			Role:         role,
			Parent:       parent,
		}
	}
}
