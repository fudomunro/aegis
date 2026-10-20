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

// kindRuntime maps kind ID → visibility predicates. Keys must match
// types.BuiltinLibraryKinds (TestLibraryKinds_KindRuntimeInSync).
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
	"documents": {
		HasProviders: func(s *Server) bool { return s.hasProvider[types.DocumentProvider]() },
		Readable:     func(s *Server) bool { return s.libraryReadableFor("documents") },
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

// libraryKinds is the built-in kind registry (issue #183). The slice lives
// in internal/types so reserved ids, app-readable paths, canonical_for keys,
// and access-level allowlists all derive from one source.
var libraryKinds = types.BuiltinLibraryKinds

// LibraryKindByID returns the registered built-in kind, or nil.
func LibraryKindByID(id string) *types.LibraryKind {
	return types.BuiltinKindByID(id)
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
		text += fmt.Sprintf(" For scheduling or availability-only questions use GET %s/v1/library/freebusy — busy intervals only (start_at, end_at, all_day); never includes titles or attendees.", root)
	}
	if name := s.canonicalServiceName(kindID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the canonical home.", k.Label, name)
	}
	if kindID == "messages" {
		text += " Prefer /v1/mail/search (FTS + from/to/after/before) to find specific mail; " +
			"library lists are capped and newest-first. Fetch one message at " +
			"/v1/services/{sources[].service}/messages/{sources[].id} or /v1/mail/messages/{service}/{id}."
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
		"/v1/library/playlists":                     s.HandleLibraryPlaylists,
		"/v1/library/tracks":                        s.HandleLibraryTracks,
		"/v1/library/albums":                        s.HandleLibraryAlbums,
		"/v1/library/artists":                       s.HandleLibraryArtists,
		"/v1/library/contacts":                      s.HandleLibraryContacts,
		"/v1/library/photos":                        s.HandleLibraryPhotos,
		"/v1/library/events":                        s.HandleLibraryEvents,
		"/v1/library/freebusy":                      s.HandleLibraryFreeBusy,
		"/v1/library/messages":                      s.HandleLibraryMessages,
		"/v1/library/transactions":                  s.HandleLibraryTransactions,
		"/v1/library/places":                        s.HandleLibraryPlaceVisits,
		"/v1/library/documents":                     s.HandleLibraryDocuments,
		"/v1/library/documents/{service}/{id}/text": s.HandleLibraryDocumentText,
		"/v1/library/copies":                        s.HandleLibraryCopies,
		"/v1/library/review":                        s.HandleMergeReview,
		"/v1/library/links":                         s.HandleEntityLinks,
		"/v1/library/links/":                        s.HandleEntityLinks,
	}
	// User-defined packs must never shadow a built-in route (issue #183).
	// Normalize rejects reserved ids; this is the defense-in-depth check
	// against registry/handler drift.
	mergeUDTRoutes(handlers, s.udtRouteHandlers(), slog.Default())
	return handlers
}

// mergeUDTRoutes copies UDT handlers into the built-in route map, refusing
// to overwrite an existing path. Returns the skipped colliding paths.
func mergeUDTRoutes(handlers map[string]http.HandlerFunc, udt map[string]http.HandlerFunc, logger *slog.Logger) []string {
	var skipped []string
	for path, handler := range udt {
		if _, exists := handlers[path]; exists {
			skipped = append(skipped, path)
			if logger != nil {
				logger.Warn("user-defined type path collides with built-in library route; skipping",
					"path", path)
			}
			continue
		}
		handlers[path] = handler
	}
	return skipped
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
	// encode builds the JSON response from the full filtered list plus the
	// agent page window (issue #185). Implementations call types.SlicePage
	// on whatever final item shape they expose (events vs freebusy blocks).
	encode func(items []T, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any
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

	pageReq, pageErr := types.ParsePageRequest(r.URL.Query().Get("limit"), r.URL.Query().Get("cursor"))
	if pageErr != nil {
		writeJSONError(w, http.StatusBadRequest, pageErr.Error())
		return
	}

	// Third-party apps are scope-limited even when service grant_limits would
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
			fmt.Sprintf("denied: app data grant for %s is %q", kind.ID, app.Scopes.For(kind.PermType)))
		return
	}

	cacheKey := kind.CacheKey()
	if spec.cacheKey != "" {
		cacheKey = spec.cacheKey
	}
	cacheKey += appLibraryCacheSuffix(app, kind.PermType)
	cacheKey += pageReq.CacheSuffix()
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

	result, err := json.Marshal(spec.encode(merged, pageReq, bySvc))
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
	case connectors.PaperlessServiceID:
		return connectors.PaperlessServiceName
	case connectors.GoogleDriveServiceID:
		return connectors.GoogleDriveServiceName
	case connectors.CardDAVServiceID:
		return connectors.CardDAVServiceName
	case connectors.CalDAVServiceID:
		return connectors.CalDAVServiceName
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
