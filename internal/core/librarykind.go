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
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

// kindRuntimeEntry is core-owned visibility wiring for one library kind.
// Predicates stay here — not on types.LibraryKind (issue #15).
type kindRuntimeEntry struct {
	// Provides reports whether a single connector implements this kind's
	// provider interface. The flow map lists a service's kinds through it
	// (issue #254).
	Provides func(c connectors.ServiceConnector) bool
	// HasProviders reports whether any connector can feed this kind.
	HasProviders func(s *Server) bool
	// Readable reports whether any non-blocked service allows reads.
	Readable func(s *Server) bool
}

// newKindRuntime wires a kind whose live sources are exactly the connectors
// implementing P. Override HasProviders on the result when a local store can
// feed the kind without a connector (mail archive, Timeline import).
func newKindRuntime[P any](kind string) kindRuntimeEntry {
	provides := func(c connectors.ServiceConnector) bool {
		_, ok := any(c).(P)
		return ok
	}
	return kindRuntimeEntry{
		Provides: provides,
		HasProviders: func(s *Server) bool {
			for _, c := range s.connectors {
				if provides(c) {
					return true
				}
			}
			return false
		},
		Readable: func(s *Server) bool { return s.libraryReadableFor(kind) },
	}
}

// kindRuntime maps kind ID → visibility predicates. Keys must match
// types.BuiltinLibraryKinds (TestLibraryKinds_KindRuntimeInSync).
var kindRuntime = map[string]kindRuntimeEntry{
	"playlists":    newKindRuntime[types.PlaylistProvider]("playlists"),
	"tracks":       newKindRuntime[types.LikedTrackProvider]("tracks"),
	"albums":       newKindRuntime[types.AlbumProvider]("albums"),
	"artists":      newKindRuntime[types.ArtistProvider]("artists"),
	"contacts":     newKindRuntime[types.ContactProvider]("contacts"),
	"photos":       newKindRuntime[types.PhotoProvider]("photos"),
	"events":       newKindRuntime[types.CalendarEventProvider]("events"),
	"messages":     messagesKindRuntime(),
	"transactions": newKindRuntime[types.TransactionProvider]("transactions"),
	"places":       placesKindRuntime(),
	"documents":    newKindRuntime[types.DocumentProvider]("documents"),
}

// messagesKindRuntime adds the local mail archive (Gmail sync + Takeout) as a
// collection source even without a live connector (issue #139).
func messagesKindRuntime() kindRuntimeEntry {
	e := newKindRuntime[types.MessageProvider]("messages")
	live := e.HasProviders
	e.HasProviders = func(s *Server) bool {
		return live(s) || (s.mail != nil && s.mailReadable())
	}
	return e
}

// placesKindRuntime adds the local Timeline import store as a collection
// source even without a live connector (issue #142).
func placesKindRuntime() kindRuntimeEntry {
	e := newKindRuntime[types.PlaceVisitProvider]("places")
	live := e.HasProviders
	e.HasProviders = func(s *Server) bool {
		return live(s) || (s.timeline != nil && s.timeline.Enabled())
	}
	return e
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
// multi-source mutation rule. Kind-specific agent copy rides on
// LibraryKind.ExtraInstructions (issue #254); this only adds the runtime
// canonical hint.
func (s *Server) libraryInstructionsFor(kindID string) string {
	k := LibraryKindByID(kindID)
	if k == nil {
		return ""
	}
	text := k.Instructions(s.baseURL)
	if name := s.canonicalServiceName(kindID); name != "" {
		text += fmt.Sprintf(" Configured canonical service for %s: %s — treat it as the canonical home.", k.Label, name)
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
// that matches a service-less user question and is readable for this request
// (owner grants AND scope grants). Used by the pre-request hook.
func (s *Server) libraryHintsForMessage(r *http.Request, msg string) []string {
	var out []string
	for _, k := range s.allLibraryKinds() {
		if !k.Mentions(msg) {
			continue
		}
		if !kindHasProviders(k.ID, s) || !kindReadable(k.ID, s) {
			continue
		}
		if !scopeAllows(r, k.PermType, http.MethodGet) {
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

// ownerViewKey marks an internal owner-tier fetch (issue #272).
type ownerViewKey struct{}

// ownerViewRequest returns the request view used to materialize the search
// index: app identity and scope-grant tightening are ignored, so items come
// back at the partition's fullest detail. Data-scope dataset visibility is
// preserved (docs/data-scopes.md). Read-path redaction (redactSearch) stays
// the caller boundary — this view only decides what gets stored.
func ownerViewRequest(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ownerViewKey{}, true))
}

// ownerView reports whether r is an internal owner-tier fetch.
func ownerView(r *http.Request) bool {
	if r == nil {
		return false
	}
	v, _ := r.Context().Value(ownerViewKey{}).(bool)
	return v
}

// libraryIndexWriteable reports whether this request's fetch is already at
// the partition's owner-tier view and may therefore write the search index.
// App scopes and scope-grant tightening can redact or drop items below that
// view; storing those would overwrite fuller rows with reduced ones, and only
// another unredacted fetch would restore them (issue #272).
func (s *Server) libraryIndexWriteable(r *http.Request, permType string) bool {
	if AppIdentityFrom(r) != nil {
		return false
	}
	sc := DataScopeFrom(r)
	if sc == nil {
		return true
	}
	for _, svc := range s.config.ServiceSnapshots() {
		if !serviceVisibleToScope(&svc, sc) {
			continue
		}
		owner := types.NormalizeAccess(s.effectivePermissions(&svc).AccessForDataType(permType))
		if types.NormalizeAccess(s.requestLevel(r, &svc, permType)) != owner {
			return false
		}
	}
	return true
}

// collectProviders type-asserts each connector to P and builds fetch targets
// that pass the permission check for permType. Services outside the request's
// data-scope dataset partition are skipped silently (docs/data-scopes.md).
func collectProviders[T any, P any](
	s *Server,
	r *http.Request,
	permType string,
	lister func(P) func(ctx context.Context) ([]T, bool, error),
) (targets []namedLister[T], denied []string) {
	scope := DataScopeFrom(r)
	for id, connector := range s.connectors {
		svc := s.config.GetService(id)
		if svc == nil || svc.Action == "block" {
			continue
		}
		if !serviceVisibleToScope(svc, scope) {
			continue
		}
		provider, ok := any(connector).(P)
		if !ok {
			continue
		}
		if !types.AllowsMethod(s.requestLevel(r, svc, permType), http.MethodGet) {
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
	// redactSearch re-applies access-level redaction to index-hydrated items
	// (issue #178). Fetch-time provider redaction never sees stored payloads,
	// so level-sensitive kinds must redact again on the search path. Nil
	// skips (kinds without access levels).
	redactSearch func(items []T, r *http.Request) []T
	// cacheKey overrides kind.CacheKey() when a second endpoint shares the
	// same dataset but a different response shape (freebusy view of events).
	cacheKey string
}

// handleLibraryAggregate is the shared GET /v1/library/{kind} pipeline:
// method check, cache, permission-aware fan-out, merge, and JSON response.
func handleLibraryAggregate[T any](s *Server, w http.ResponseWriter, r *http.Request, spec libraryKindSpec[T]) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	kind := spec.kind
	logger := withPeerLog(slog.With("handler", "library_"+kind.ID), r)
	agent := agentForRequest(r)

	pageReq, pageErr := types.ParsePageRequest(r.URL.Query().Get("limit"), r.URL.Query().Get("cursor"))
	if pageErr != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, pageErr.Error())
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q != "" && kind.Search == nil {
		shared.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("%s does not support search", kind.ID))
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
		shared.WriteJSONError(w, http.StatusForbidden,
			fmt.Sprintf("denied: app data grant for %s is %q", kind.ID, app.Scopes.For(kind.PermType)))
		return
	}

	// Data-scope grants are deny-by-default (issue #253): an unlisted kind is
	// none for the principal, mirroring the app-scope gate above.
	if sc := DataScopeFrom(r); sc != nil && !scopeAllows(r, kind.PermType, r.Method) {
		level := types.ScopeGrantsAccess(sc.Grants, kind.PermType, "")
		logger.Info("library "+kind.ID+" denied by scope grant", "scope", sc.ID, "grant", level)
		s.recordAccessFrom(r, AccessEvent{
			Agent:    agent,
			Surface:  MetricSurfaceLibrary,
			Target:   kind.ID,
			DataType: kind.ID,
			Cache:    MetricCacheMiss,
			Decision: DecisionDeny,
			Reason:   "scope_grant",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		shared.WriteJSONError(w, http.StatusForbidden,
			fmt.Sprintf("denied: data scope grant for %s is %q", kind.ID, level))
		return
	}

	if q != "" {
		handleLibrarySearch(s, w, r, spec, q, pageReq, agent, app)
		return
	}

	// One cached merged aggregate per kind+view (issue #273): pages are
	// sliced from it, so paging through N pages costs one provider fan-out
	// instead of N. The key keeps the app and data-scope suffixes and adds
	// "|agg" so it never collides with a page-shaped or search key.
	aggKey := kind.CacheKey()
	if spec.cacheKey != "" {
		aggKey = spec.cacheKey
	}
	aggKey += appLibraryCacheSuffix(app, kind.PermType)
	aggKey += scopeCacheSuffix(DataScopeFrom(r))
	aggKey += "|agg"
	if cached, ok := s.cache.Get(aggKey); ok {
		var agg libraryFetch[T]
		if err := json.Unmarshal([]byte(cached), &agg); err == nil {
			logger.Debug("cache hit")
			result, err := json.Marshal(spec.encode(agg.Items, pageReq, agg.BySvc))
			if err != nil {
				logger.Error("failed to marshal library "+kind.ID, "error", err)
				shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build "+kind.Label+" list")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Aegis-Cache", "hit")
			w.Write(result)
			s.recordAccessFrom(r, AccessEvent{
				Agent:    agent,
				Surface:  MetricSurfaceLibrary,
				Target:   kind.ID,
				DataType: kind.ID,
				Cache:    MetricCacheHit,
			})
			return
		}
		// Corrupt entry: fall through and refetch (fail-open).
	}

	// Coalesce concurrent identical fetches (issue #273): agents commonly
	// fire parallel tool calls, and each waiter shares one provider fan-out.
	// A detached context keeps one caller's disconnect from poisoning the
	// shared result with a half-failed fan-out.
	flightReq := r.WithContext(context.WithoutCancel(r.Context()))
	val, _ := s.libraryFlight.Do(aggKey, func() (any, error) {
		fetched := fetchLibrary(s, flightReq, spec, logger, agent)
		if fetched.NTargets > 0 && !libraryAggregateDegraded(fetched.BySvc) {
			if raw, err := json.Marshal(fetched); err == nil {
				s.cache.Set(aggKey, string(raw), libraryAggregateTTL)
			}
		}
		// Write-behind: refresh the search index from this fetch (issue #178),
		// off the response path (issue #273). Fail-open: a broken index must
		// not fail the list response. Only an owner-tier fetch may write — a
		// reduced (app/clamped) view would store rows below the detail the
		// owner's query expects (issue #272).
		if kind.Search != nil && s.search.Enabled() && s.libraryIndexWriteable(r, kind.PermType) {
			indexLibraryAsync(s, spec, scopeDataset(r), fetched)
		}
		return fetched, nil
	})
	fetched := val.(libraryFetch[T])

	for _, id := range fetched.Denied {
		svc := s.config.GetService(id)
		perm := ""
		if svc != nil {
			perm = s.requestLevel(r, svc, kind.PermType)
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

	if fetched.NTargets == 0 {
		if len(fetched.Denied) > 0 {
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
			shared.WriteJSONError(w, http.StatusForbidden, kind.DenyMsg)
			return
		}
		shared.WriteJSONError(w, http.StatusNotFound, kind.EmptyMsg)
		return
	}

	result, err := json.Marshal(spec.encode(fetched.Items, pageReq, fetched.BySvc))
	if err != nil {
		logger.Error("failed to marshal library "+kind.ID, "error", err)
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build "+kind.Label+" list")
		return
	}

	// Cache-only-healthy moved into the coalesced fetch: degenerate rows are
	// shared with in-flight waiters but never frozen (issue #104).
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

// libraryFetch is the shared provider fan-out result (issue #178): list
// responses and search indexing both need merged items and service status.
// Exported fields with JSON tags make the merged aggregate cacheable so
// pages can be sliced without re-fanning-out (issue #273).
type libraryFetch[T any] struct {
	Items    []T                                   `json:"items"`
	BySvc    map[string]types.LibraryServiceStatus `json:"by_service"`
	Denied   []string                              `json:"denied,omitempty"`
	NTargets int                                   `json:"n_targets"`
}

// fetchLibrary runs the permission-aware fan-out, merge, and post-merge
// stamping shared by list responses and search-index materialization. It
// does not encode or respond; callers own gates, caching, and payloads.
func fetchLibrary[T any](s *Server, r *http.Request, spec libraryKindSpec[T], logger *slog.Logger, agent string) libraryFetch[T] {
	kind := spec.kind
	targets, denied := spec.targets(s, r)
	out := libraryFetch[T]{Denied: denied, NTargets: len(targets), Items: []T{}}
	if len(targets) == 0 {
		out.BySvc = map[string]types.LibraryServiceStatus{}
		return out
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
	out.Items = merged
	out.BySvc = bySvc
	return out
}

// searchHitCap bounds how many index hits are loaded before grant filtering
// and paging, mirroring the mail-search merge cap.
const searchHitCap = 5000

// libraryAggregateTTL is how long a merged aggregate stays fresh. Targeted
// invalidation (issue #273) covers known changes; the TTL bounds drift for
// upstream edits no invalidation site knows about.
const libraryAggregateTTL = 60 * time.Second

// handleLibrarySearch answers GET /v1/library/{kind}?q= from the search index
// (issue #178). A cold index (nothing materialized for this kind+dataset) is
// bootstrapped from one live fetch first, so results are correct from the
// first query. Hits pass the same per-service read gate as the aggregate.
func handleLibrarySearch[T any](s *Server, w http.ResponseWriter, r *http.Request, spec libraryKindSpec[T], q string, pageReq types.PageRequest, agent string, app *AppIdentity) {
	kind := spec.kind
	logger := withPeerLog(slog.With("handler", "library_"+kind.ID+"_search"), r)

	if !s.search.Enabled() {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "search index is disabled")
		return
	}
	dataset := scopeDataset(r)

	// Grant gate mirrors the aggregate: without any readable target, search
	// has nothing legitimate to return.
	targets, denied := spec.targets(s, r)
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
			shared.WriteJSONError(w, http.StatusForbidden, kind.DenyMsg)
			return
		}
		shared.WriteJSONError(w, http.StatusNotFound, kind.EmptyMsg)
		return
	}
	readable := map[string]bool{}
	configured := map[string]bool{}
	localSource := false
	for _, t := range targets {
		readable[t.id] = true
		if s.config.GetService(t.id) == nil {
			// Local collection sources (mail archive, imports) gate their own
			// readability when the target is registered.
			localSource = true
		}
	}
	for _, svc := range s.config.ServiceSnapshots() {
		configured[svc.ID] = true
	}

	cacheKey := kind.CacheKey() + "|q=" + q
	cacheKey += appLibraryCacheSuffix(app, kind.PermType)
	cacheKey += scopeCacheSuffix(DataScopeFrom(r))
	cacheKey += pageReq.CacheSuffix()
	if cached, ok := s.cache.Get(cacheKey); ok {
		logger.Debug("search cache hit")
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

	n, err := s.search.Count(kind.ID, dataset)
	if err != nil {
		logger.Error("search index count failed", "error", err)
		shared.WriteJSONError(w, http.StatusInternalServerError, "search index unavailable")
		return
	}
	if n == 0 {
		// Cold index: materialize from one live fetch, then query. The fetch
		// runs at owner tier (issue #272) so a reduced caller's first query
		// still fills the index with the fullest rows; redactSearch narrows
		// them on the way out.
		fetched := fetchLibrary(s, ownerViewRequest(r), spec, logger, agent)
		if err := indexLibraryDocuments(s, spec, dataset, fetched); err != nil {
			logger.Warn("search index bootstrap failed", "error", err)
		}
	}

	res, err := s.search.Search(SearchIndexQuery{Kind: kind.ID, Dataset: dataset, Q: q, Limit: searchHitCap})
	if err != nil {
		logger.Error("search index query failed", "error", err)
		shared.WriteJSONError(w, http.StatusInternalServerError, "search index unavailable")
		return
	}
	hits := make([]T, 0, len(res.Docs))
	bySvc := make(map[string]types.LibraryServiceStatus)
	for _, doc := range res.Docs {
		if !searchHitReadable(doc.Services, readable, configured, localSource) {
			continue
		}
		var item T
		if err := json.Unmarshal(doc.Payload, &item); err != nil {
			logger.Warn("search hit payload invalid", "item_id", doc.ItemID, "error", err)
			continue
		}
		hits = append(hits, item)
		for _, svc := range doc.Services {
			st := bySvc[svc]
			st.Status = types.LibraryStatusOK
			st.Count++
			bySvc[svc] = st
		}
	}
	if spec.redactSearch != nil {
		hits = spec.redactSearch(hits, r)
	}
	enrichLibraryServiceStatuses(s, kind.ID, bySvc)

	result, err := json.Marshal(spec.encode(hits, pageReq, bySvc))
	if err != nil {
		logger.Error("failed to marshal library "+kind.ID+" search", "error", err)
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build "+kind.Label+" search results")
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

// searchHitReadable applies the aggregate's per-service read gate to an
// indexed hit. Every source must be readable — a merged payload can carry
// source rows the fetch path would have dropped, so one unreadable source
// drops the hit. Sources no configured service claims (archive/import rows)
// stay visible while a local-source target is readable; that store already
// passed its own readability check when the target registered.
func searchHitReadable(services []string, readable, configured map[string]bool, localSource bool) bool {
	if len(services) == 0 {
		return false
	}
	for _, svc := range services {
		if readable[svc] {
			continue
		}
		if localSource && !configured[svc] {
			continue
		}
		return false
	}
	return true
}

// indexLibraryDocuments flattens merged items into search documents and syncs
// them into the search index, dropping rows this fetch refuted (issue #272).
func indexLibraryDocuments[T any](s *Server, spec libraryKindSpec[T], dataset string, fetched libraryFetch[T]) error {
	if !s.search.Enabled() || spec.kind.Search == nil {
		return nil
	}
	docs := make([]types.SearchDocument, 0, len(fetched.Items))
	for _, item := range fetched.Items {
		doc, err := types.BuildSearchDocument(spec.sortKey(item), item, *spec.kind.Search)
		if err != nil {
			return err
		}
		docs = append(docs, doc)
	}
	return s.search.Sync(spec.kind.ID, dataset, docs, completeServices(fetched.BySvc))
}

// indexLibraryAsync runs the search-index write-behind off the response
// path (issue #273). Fail-open: a broken index must not fail the list
// response, and latency on large lists must not land on the agent request.
func indexLibraryAsync[T any](s *Server, spec libraryKindSpec[T], dataset string, fetched libraryFetch[T]) {
	s.indexWG.Add(1)
	go func() {
		defer s.indexWG.Done()
		if err := indexLibraryDocuments(s, spec, dataset, fetched); err != nil {
			slog.Warn("search index update failed", "kind", spec.kind.ID, "error", err)
		}
	}()
}

// waitIndex blocks until scheduled search-index write-behind work finishes.
// Tests use it so a list fetch can materialize the index before querying.
func (s *Server) waitIndex() {
	s.indexWG.Wait()
}

// completeServices lists the services whose fetch succeeded in full (status
// ok, not truncated). Only those can refute an indexed row; a degraded or
// truncated service leaves its rows untouched (issue #272).
func completeServices(bySvc map[string]types.LibraryServiceStatus) []string {
	out := make([]string, 0, len(bySvc))
	for id, st := range bySvc {
		if st.Status == types.LibraryStatusOK && !st.Truncated {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
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
	case connectors.TakeoutServiceID:
		return connectors.TakeoutServiceName
	}
	if name, ok := connectors.ServiceName(id); ok {
		return name
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
	case "mail-archive", connectors.TakeoutServiceID, connectors.GoogleGmailServiceID:
		st, err := s.mail.Status()
		if err != nil {
			return ""
		}
		if id == connectors.TakeoutServiceID || id == connectors.GoogleGmailServiceID {
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
		if id == connectors.TakeoutServiceID {
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
