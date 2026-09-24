package main

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
)

// LibraryKind describes one unified library dataset. Adding a data type for
// scale means registering a kind here and a thin typed handler — not cloning
// another aggregate pipeline.
type LibraryKind struct {
	// ID is the short name ("tracks") used in cache keys and logs.
	ID string
	// Path is the agent-facing endpoint ("/v1/library/tracks").
	Path string
	// PermType is the permissions map key and path classifier type.
	PermType string
	// Label is the human name used in messages ("liked tracks").
	Label string
	// Usage is how agents should think about the dataset (instructions).
	Usage string
	// Summary describes the neutral list shape.
	Summary string
	// SyncRule explains cross-service merge behavior.
	SyncRule string
	// ExampleQuestion is a canonical user prompt.
	ExampleQuestion string
	// Keywords detect service-less questions that should inject this endpoint.
	Keywords []string
	// EmptyMsg / DenyMsg / FailMsg are API error strings.
	EmptyMsg string
	DenyMsg  string
	FailMsg  string

	// HasProviders reports whether any connector can feed this kind.
	HasProviders func(s *Server) bool
	// Readable reports whether any non-blocked service allows reads.
	Readable func(s *Server) bool
}

// libraryKinds is the registry for every unified library dataset. Order is
// stable and used for route registration and pre-request injection priority.
var libraryKinds = []LibraryKind{
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
		EmptyMsg:     "no playlist-capable services configured",
		DenyMsg:      "permission denied: agent access to playlists is not allowed",
		FailMsg:      "failed to fetch playlists",
		HasProviders: func(s *Server) bool { return s.hasProvider[PlaylistProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("playlists") },
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
		EmptyMsg:     "no liked-track-capable services configured",
		DenyMsg:      "permission denied: agent access to tracks is not allowed",
		FailMsg:      "failed to fetch liked tracks",
		HasProviders: func(s *Server) bool { return s.hasProvider[LikedTrackProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("tracks") },
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
		EmptyMsg:     "no album-capable services configured",
		DenyMsg:      "permission denied: agent access to albums is not allowed",
		FailMsg:      "failed to fetch albums",
		HasProviders: func(s *Server) bool { return s.hasProvider[AlbumProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("albums") },
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
		EmptyMsg:     "no artist-capable services configured",
		DenyMsg:      "permission denied: agent access to artists is not allowed",
		FailMsg:      "failed to fetch artists",
		HasProviders: func(s *Server) bool { return s.hasProvider[ArtistProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("artists") },
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
		EmptyMsg:     "no contact-capable services configured",
		DenyMsg:      "permission denied: agent access to contacts is not allowed",
		FailMsg:      "failed to fetch contacts",
		HasProviders: func(s *Server) bool { return s.hasProvider[ContactProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("contacts") },
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
		},
		EmptyMsg:     "no photo-capable services configured",
		DenyMsg:      "permission denied: agent access to photos is not allowed",
		FailMsg:      "failed to fetch photos",
		HasProviders: func(s *Server) bool { return s.hasProvider[PhotoProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("photos") },
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
		EmptyMsg:     "no calendar-capable services configured",
		DenyMsg:      "permission denied: agent access to events is not allowed",
		FailMsg:      "failed to fetch calendar events",
		HasProviders: func(s *Server) bool { return s.hasProvider[CalendarEventProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("events") },
	},
	{
		ID:              "messages",
		Path:            "/v1/library/messages",
		PermType:        "messages",
		Label:           "messages",
		Usage:           "emails, messages, inbox, mail, or recent correspondence",
		Summary:         "all messages from every connected messaging/mail service in one neutral list (channel, subject, snippet, from/to/cc as Contact identities, sent_at, direction, read, entity_id, sources[])",
		SyncRule:        "Messages merge on durable Message-ID when present (match=message_id); otherwise same subject + from + sent_at (match=subject_from_time). Message bodies stay on the source service. SMS/WhatsApp connectors are not implemented yet.",
		ExampleQuestion: "what emails have I received recently?",
		Keywords: []string{
			"my email", "my emails", "my mail", "my inbox",
			"recent email", "recent emails", "recent messages",
			"emails have i", "emails i have", "emails i received",
			"check my email", "check my mail", "check my inbox",
			"messages in my inbox", "unread email", "unread emails",
			"google mail", "gmail inbox", "correspondence",
		},
		EmptyMsg:     "no message-capable services configured",
		DenyMsg:      "permission denied: agent access to messages is not allowed",
		FailMsg:      "failed to fetch messages",
		HasProviders: func(s *Server) bool { return s.hasProvider[MessageProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("messages") },
	},
}

// LibraryKindByID returns the registered kind, or nil.
func LibraryKindByID(id string) *LibraryKind {
	for i := range libraryKinds {
		if libraryKinds[i].ID == id {
			return &libraryKinds[i]
		}
	}
	return nil
}

// Mentions reports whether the user message looks like a question about this
// dataset without naming a service.
func (k LibraryKind) Mentions(msg string) bool {
	lower := strings.ToLower(msg)
	for _, keyword := range k.Keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// Instructions builds the system-message hint for this library endpoint.
func (k LibraryKind) Instructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(
		"Unified %s library (use when the user asks about %s):\n"+
			"GET %s%s — %s.\n"+
			"%s Prefer this endpoint for questions like %q so the answer includes "+
			"every connected service.\n"+
			"Check response.truncated (and by_service[].truncated) before claiming the list is complete; "+
			"when true, say so and note that some items may still be missing upstream.\n"+
			"sources[].canonical marks the authoritative non-Aegis location for that item "+
			"(issue #16); prefer that service for writes or mirrors.",
		k.Label, k.Usage, root, k.Path, k.Summary, k.SyncRule, k.ExampleQuestion,
	)
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
	if name := s.canonicalServiceName(kindID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the home copy.", k.Label, name)
	}
	text += "\n" + multiSourceMutationRule()
	return text
}

// anyServiceTruncated reports whether any per-service status hit a page cap
// or left an unfollowed continuation. Used for the top-level truncated flag.
func anyServiceTruncated(bySvc map[string]LibraryServiceStatus) bool {
	for _, st := range bySvc {
		if st.Truncated {
			return true
		}
	}
	return false
}

// CacheKey is the in-memory cache key for the aggregate response.
func (k LibraryKind) CacheKey() string { return "library:" + k.ID }

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
	for _, svc := range s.config.Services {
		if svc.Action == "block" {
			continue
		}
		if svc.Permissions.AllowsRead(permType) {
			return true
		}
	}
	return false
}

// libraryHintsForMessage returns instruction blocks for every library kind
// that matches a service-less user question. Used by the pre-request hook.
func (s *Server) libraryHintsForMessage(msg string) []string {
	var out []string
	for _, k := range libraryKinds {
		if !k.Mentions(msg) || k.HasProviders == nil || k.Readable == nil {
			continue
		}
		if !k.HasProviders(s) || !k.Readable(s) {
			continue
		}
		out = append(out, s.libraryInstructionsFor(k.ID))
	}
	return out
}

// libraryRouteHandlers maps each kind path to its HTTP handler so main can
// register every dataset from the registry.
func (s *Server) libraryRouteHandlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/v1/library/playlists": s.HandleLibraryPlaylists,
		"/v1/library/tracks":    s.HandleLibraryTracks,
		"/v1/library/albums":    s.HandleLibraryAlbums,
		"/v1/library/artists":   s.HandleLibraryArtists,
		"/v1/library/contacts":  s.HandleLibraryContacts,
		"/v1/library/photos":    s.HandleLibraryPhotos,
		"/v1/library/events":    s.HandleLibraryEvents,
		"/v1/library/messages":  s.HandleLibraryMessages,
		"/v1/library/review":    s.HandleMergeReview,
		"/v1/library/links":     s.HandleEntityLinks,
		"/v1/library/links/":    s.HandleEntityLinks,
	}
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
		if !svc.Permissions.AllowsRead(permType) {
			denied = append(denied, id)
			continue
		}
		targets = append(targets, namedLister[T]{id: id, list: lister(provider)})
	}
	return targets, denied
}

// libraryKindSpec wires one typed dataset into the shared aggregate pipeline.
type libraryKindSpec[T any] struct {
	kind    LibraryKind
	targets func(s *Server) (targets []namedLister[T], denied []string)
	// serviceOf returns the first source service name for stable pre-sort.
	serviceOf func(T) string
	// sortKey is the identity key used after service-name grouping.
	sortKey func(T) string
	merge   func([]T) []T
	encode  func(merged []T, bySvc map[string]LibraryServiceStatus) any
	// markCanonical stamps Source.Canonical on items from a config-declared
	// canonical_for service (issue #16). Nil skips config stamping.
	markCanonical func(items []T, serviceID string)
	// enrich optionally resolves durable IDs before merge (issue #4).
	enrich func(ctx context.Context, s *Server, items []T)
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

	cacheKey := kind.CacheKey()
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

	targets, denied := spec.targets(s)
	for _, id := range denied {
		svc := s.config.GetService(id)
		perm := ""
		if svc != nil {
			perm = svc.Permissions.For(kind.PermType)
		}
		logger.Info("library "+kind.ID+" denied by permission", "service", id, "permission", perm)
	}

	if len(targets) == 0 {
		if len(denied) > 0 {
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
		bySvc = make(map[string]LibraryServiceStatus, len(targets))
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
			var status LibraryServiceStatus
			switch {
			case err == nil:
				if spec.markCanonical != nil {
					spec.markCanonical(items, t.id)
				}
				status = LibraryServiceStatus{
					Status:    LibraryStatusOK,
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
			case errors.Is(err, ErrUnauthorized):
				logger.Info("library "+kind.ID+" unauthorized", "service", t.id)
				status = LibraryServiceStatus{
					Status: LibraryStatusUnauthorized,
					Error:  "not authorized — connect at /v1/auth/" + t.id + "/login",
				}
			default:
				logger.Warn("library "+kind.ID+" failed", "service", t.id, "error", err)
				status = LibraryServiceStatus{Status: LibraryStatusError, Error: kind.FailMsg}
			}
			mu.Lock()
			bySvc[t.id] = status
			mu.Unlock()
		}(t)
	}
	wg.Wait()

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

	result, err := json.Marshal(spec.encode(merged, bySvc))
	if err != nil {
		logger.Error("failed to marshal library "+kind.ID, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build "+kind.Label+" list")
		return
	}

	s.cache.Set(cacheKey, string(result), 60*time.Second)
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
