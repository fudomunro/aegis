package kinds

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

	"github.com/fudomunro/aegis/internal/core/audit"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/core/search"
	"github.com/fudomunro/aegis/internal/shared"
)

// StatusHooks customizes the source-overview enrichment for kinds that own
// collection sources beyond live connectors (issue #312). Kinds declare
// their local sources here instead of the pipeline branching on kind or
// source ids.
type StatusHooks struct {
	// SourceName overrides a source's display name. ok=false falls back
	// to Host.SourceName.
	SourceName func(id string) (string, bool)
	// SourceLastSyncedAt supplies the overview's last-sync timestamp for
	// sources the kind tracks itself (mail-archive cursors). ok=false
	// leaves the field empty.
	SourceLastSyncedAt func(id string) (string, bool)
	// Expand adds or adjusts overview rows after the generic enrichment
	// (the messages kind surfaces its archive ingest sources).
	Expand func(bySvc map[string]LibraryServiceStatus)
}

// Spec wires one kind's dataset into the shared aggregate pipeline.
type Spec[T any] struct {
	Kind LibraryKind
	// Targets returns the fetch targets that pass the permission check
	// plus the denied service ids.
	Targets func(r *http.Request) (targets []NamedLister[T], denied []string)
	// ServiceOf returns the first source service name for stable pre-sort.
	ServiceOf func(T) string
	// SortKey is the identity key used after service-name grouping.
	SortKey func(T) string
	Merge   func([]T) []T
	// Encode builds the JSON response from the full filtered list plus the
	// agent page window (issue #185). Implementations call SlicePage
	// on whatever final item shape they expose (events vs freebusy blocks).
	Encode func(items []T, page PageRequest, bySvc map[string]LibraryServiceStatus) any
	// MarkCanonical stamps Source.Canonical on items from a config-declared
	// canonical_for service (issue #16). Nil skips config stamping.
	MarkCanonical func(items []T, serviceID string)
	// StampCopies attaches copy-ledger freshness to sources[] (issue #76).
	StampCopies func(items []T)
	// Enrich optionally resolves durable IDs before merge (issue #4).
	Enrich func(ctx context.Context, items []T)
	// RedactSearch re-applies access-level redaction to index-hydrated
	// items (issue #178). Fetch-time provider redaction never sees stored
	// payloads, so level-sensitive kinds must redact again on the search
	// path. Nil skips (kinds without access levels).
	RedactSearch func(items []T, r *http.Request) []T
	// CacheKey overrides kind.CacheKey() when a second endpoint shares the
	// same dataset but a different response shape (freebusy view of events).
	CacheKey string
	// Status declares the kind's local-source overview hooks (issue #312).
	Status StatusHooks
}

// Fetch is the shared provider fan-out result (issue #178): list responses
// and search indexing both need merged items and service status. Exported
// fields with JSON tags make the merged aggregate cacheable so pages can be
// sliced without re-fanning-out (issue #273).
type Fetch[T any] struct {
	Items    []T                             `json:"items"`
	BySvc    map[string]LibraryServiceStatus `json:"by_service"`
	Denied   []string                        `json:"denied,omitempty"`
	NTargets int                             `json:"n_targets"`
}

// searchHitCap bounds how many index hits are loaded before grant filtering
// and paging, mirroring the mail-search merge cap.
const searchHitCap = 5000

// aggregateTTL is how long a merged aggregate stays fresh. Targeted
// invalidation (issue #273) covers known changes; the TTL bounds drift for
// upstream edits no invalidation site knows about.
const aggregateTTL = 60 * time.Second

// AnyServiceTruncated reports whether any source row is incomplete. Encode
// closures surface it as the list's truncated flag (issue #185).
func AnyServiceTruncated(bySvc map[string]LibraryServiceStatus) bool {
	for _, st := range bySvc {
		if st.Truncated {
			return true
		}
	}
	return false
}

// appCacheSuffix namespaces cache entries by app scope level so a
// freebusy-only app never reads a full-events cache entry (and vice versa).
func appCacheSuffix(level string, isApp bool) string {
	if !isApp {
		return ""
	}
	return "|app|" + policy.NormalizeAccess(level)
}

// appAllows reports whether an app at the given scope level may call the
// endpoint for kind under method.
func appAllows(level, kind, method string) bool {
	return policy.AllowsMethod(policy.NormalizeAccess(level), method)
}

// indexWriteable reports whether this request's fetch is already at the
// partition's owner-tier view and may therefore write the search index.
// App scopes and scope-grant tightening can redact or drop items below that
// view; storing those would overwrite fuller rows with reduced ones, and
// only another unredacted fetch would restore them (issue #272).
func indexWriteable(h Host, r *http.Request, kind string) bool {
	if _, isApp := h.AppLevel(r, kind); isApp {
		return false
	}
	sc := scopes.From(r)
	if sc == nil {
		return true
	}
	for _, svc := range h.Services() {
		if !scopes.ServiceVisible(svc.Dataset, sc) {
			continue
		}
		owner := policy.NormalizeAccess(h.OwnerLevel(svc.ID, kind))
		if policy.NormalizeAccess(h.RequestLevel(r, svc.ID, kind)) != owner {
			return false
		}
	}
	return true
}

// Aggregate is the shared GET /v1/library/{kind} pipeline: method check,
// cache, permission-aware fan-out, merge, and JSON response.
func Aggregate[T any](h Host, e *Engine, w http.ResponseWriter, r *http.Request, spec Spec[T]) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	kind := spec.Kind
	logger := h.Logger(r, "library_"+kind.ID)
	agent := h.Agent(r)

	pageReq, pageErr := ParsePageRequest(r.URL.Query().Get("limit"), r.URL.Query().Get("cursor"))
	if pageErr != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, pageErr.Error())
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q != "" && kind.Search == nil {
		shared.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("%s does not support search", kind.ID))
		return
	}

	// Third-party apps are scope-limited even when service grant_limits
	// would allow more (issue #131).
	appLevel, isApp := h.AppLevel(r, kind.PermType)
	if isApp && !appAllows(appLevel, kind.PermType, r.Method) {
		logger.Info("library "+kind.ID+" denied by app scope", "scope", appLevel)
		h.RecordAccess(r, AccessEvent{
			Agent:    agent,
			Surface:  SurfaceLibrary,
			Target:   kind.ID,
			Kind:     kind.ID,
			Cache:    CacheMiss,
			Decision: audit.DecisionDeny,
			Reason:   "app_scope",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		shared.WriteJSONError(w, http.StatusForbidden,
			fmt.Sprintf("denied: app data grant for %s is %q", kind.ID, appLevel))
		return
	}

	// Data-scope grants are deny-by-default (issue #253): an unlisted kind
	// is none for the principal, mirroring the app-scope gate above.
	if sc := scopes.From(r); sc != nil && !scopes.Allows(r, kind.PermType, r.Method) {
		level := policy.ScopeGrantsAccess(sc.Grants, kind.PermType, "")
		logger.Info("library "+kind.ID+" denied by scope grant", "scope", sc.ID, "grant", level)
		h.RecordAccess(r, AccessEvent{
			Agent:    agent,
			Surface:  SurfaceLibrary,
			Target:   kind.ID,
			Kind:     kind.ID,
			Cache:    CacheMiss,
			Decision: audit.DecisionDeny,
			Reason:   "scope_grant",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		shared.WriteJSONError(w, http.StatusForbidden,
			fmt.Sprintf("denied: data scope grant for %s is %q", kind.ID, level))
		return
	}

	if q != "" {
		runSearch(h, e, w, r, spec, q, pageReq, agent, appLevel, isApp)
		return
	}

	// One cached merged aggregate per kind+view (issue #273): pages are
	// sliced from it, so paging through N pages costs one provider fan-out
	// instead of N. The key keeps the app and data-scope suffixes and adds
	// "|agg" so it never collides with a page-shaped or search key.
	aggKey := kind.CacheKey()
	if spec.CacheKey != "" {
		aggKey = spec.CacheKey
	}
	aggKey += appCacheSuffix(appLevel, isApp)
	aggKey += scopes.CacheSuffix(scopes.From(r))
	aggKey += "|agg"
	if cached, ok := e.cache.Get(aggKey); ok {
		var agg Fetch[T]
		if err := json.Unmarshal([]byte(cached), &agg); err == nil {
			logger.Debug("cache hit")
			result, err := json.Marshal(spec.Encode(agg.Items, pageReq, agg.BySvc))
			if err != nil {
				logger.Error("failed to marshal library "+kind.ID, "error", err)
				shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build "+kind.Label+" list")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Aegis-Cache", "hit")
			w.Write(result)
			h.RecordAccess(r, AccessEvent{
				Agent:   agent,
				Surface: SurfaceLibrary,
				Target:  kind.ID,
				Kind:    kind.ID,
				Cache:   CacheHit,
			})
			return
		}
		// Corrupt entry: fall through and refetch (fail-open).
	}

	// Coalesce concurrent identical fetches (issue #273): agents commonly
	// fire parallel tool calls, and each waiter shares one provider
	// fan-out. A detached context keeps one caller's disconnect from
	// poisoning the shared result with a half-failed fan-out.
	flightReq := r.WithContext(context.WithoutCancel(r.Context()))
	val, _ := e.flight.Do(aggKey, func() (any, error) {
		fetched := fetch(h, e, flightReq, spec, logger, agent)
		if fetched.NTargets > 0 && !aggregateDegraded(fetched.BySvc) {
			if raw, err := json.Marshal(fetched); err == nil {
				e.cache.Set(aggKey, string(raw), aggregateTTL)
			}
		}
		// Write-behind: refresh the search index from this fetch (issue
		// #178), off the response path (issue #273). Fail-open: a broken
		// index must not fail the list response. Only an owner-tier fetch
		// may write — a reduced (app/clamped) view would store rows below
		// the detail the owner's query expects (issue #272).
		if kind.Search != nil && e.search.Enabled() && indexWriteable(h, r, kind.PermType) {
			indexAsync(h, e, spec, scopes.Dataset(r), fetched)
		}
		return fetched, nil
	})
	fetched := val.(Fetch[T])

	for _, id := range fetched.Denied {
		perm := h.RequestLevel(r, id, kind.PermType)
		logger.Info("library "+kind.ID+" denied by permission", "service", id, "permission", perm)
		// Per-service denial for the audit trail (issue #105). Even when
		// other providers still answer the aggregate request.
		h.RecordAccess(r, AccessEvent{
			Agent:    agent,
			Surface:  SurfaceService,
			Target:   id,
			Kind:     kind.ID,
			Decision: audit.DecisionDeny,
			Reason:   "permission",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
	}

	if fetched.NTargets == 0 {
		if len(fetched.Denied) > 0 {
			h.RecordAccess(r, AccessEvent{
				Agent:    agent,
				Surface:  SurfaceLibrary,
				Target:   kind.ID,
				Kind:     kind.ID,
				Decision: audit.DecisionDeny,
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

	result, err := json.Marshal(spec.Encode(fetched.Items, pageReq, fetched.BySvc))
	if err != nil {
		logger.Error("failed to marshal library "+kind.ID, "error", err)
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build "+kind.Label+" list")
		return
	}

	// Cache-only-healthy moved into the coalesced fetch: degenerate rows
	// are shared with in-flight waiters but never frozen (issue #104).
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Aegis-Cache", "miss")
	w.Write(result)
	h.RecordAccess(r, AccessEvent{
		Agent:   agent,
		Surface: SurfaceLibrary,
		Target:  kind.ID,
		Kind:    kind.ID,
		Cache:   CacheMiss,
	})
}

// fetch runs the permission-aware fan-out, merge, and post-merge stamping
// shared by list responses and search-index materialization. It does not
// encode or respond; callers own gates, caching, and payloads.
func fetch[T any](h Host, e *Engine, r *http.Request, spec Spec[T], logger *slog.Logger, agent string) Fetch[T] {
	kind := spec.Kind
	targets, denied := spec.Targets(r)
	out := Fetch[T]{Denied: denied, NTargets: len(targets), Items: []T{}}
	if len(targets) == 0 {
		out.BySvc = map[string]LibraryServiceStatus{}
		return out
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
		go func(t NamedLister[T]) {
			defer wg.Done()
			var items []T
			var truncated bool
			var err error
			// A misbehaving connector must degrade to status=error, not
			// take down the process (library fan-out runs outside net/http
			// recovery).
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						logger.Error("library "+kind.ID+" provider panicked",
							"service", t.ID, "panic", rec)
						err = fmt.Errorf("provider panic: %v", rec)
						items = nil
						truncated = false
					}
				}()
				items, truncated, err = t.List(ctx)
			}()
			var status LibraryServiceStatus
			switch {
			case err == nil:
				if spec.MarkCanonical != nil {
					spec.MarkCanonical(items, t.ID)
				}
				status = LibraryServiceStatus{
					Status:    LibraryStatusOK,
					Count:     len(items),
					Truncated: truncated,
				}
				h.RecordAccess(r, AccessEvent{
					Agent:   agent,
					Surface: SurfaceService,
					Target:  t.ID,
					Kind:    kind.ID,
				})
				if truncated {
					logger.Warn("library "+kind.ID+" truncated by page cap",
						"service", t.ID, "count", len(items))
				}
				mu.Lock()
				all = append(all, items...)
				mu.Unlock()
			case errors.Is(err, ErrUnauthorized):
				logger.Info("library "+kind.ID+" unauthorized", "service", t.ID)
				status = LibraryServiceStatus{
					Status: LibraryStatusUnauthorized,
					Error:  "not authorized — connect at /v1/auth/" + t.ID + "/login",
				}
			default:
				logger.Warn("library "+kind.ID+" failed", "service", t.ID, "error", err)
				status = LibraryServiceStatus{Status: LibraryStatusError, Error: kind.FailMsg}
			}
			mu.Lock()
			bySvc[t.ID] = status
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	EnrichStatuses(h, kind.ID, spec.Status, bySvc)

	sort.SliceStable(all, func(i, j int) bool {
		si, sj := spec.ServiceOf(all[i]), spec.ServiceOf(all[j])
		if si != sj {
			return si < sj
		}
		return spec.SortKey(all[i]) < spec.SortKey(all[j])
	})

	if spec.Enrich != nil {
		spec.Enrich(ctx, all)
	}

	merged := spec.Merge(all)
	if merged == nil {
		merged = []T{}
	}
	if spec.StampCopies != nil {
		spec.StampCopies(merged)
	}
	out.Items = merged
	out.BySvc = bySvc
	return out
}

// search answers GET /v1/library/{kind}?q= from the search index (issue
// #178). A cold index (nothing materialized for this kind+dataset) is
// bootstrapped from one live fetch first, so results are correct from the
// first query. Hits pass the same per-service read gate as the aggregate.
func runSearch[T any](h Host, e *Engine, w http.ResponseWriter, r *http.Request, spec Spec[T], q string, pageReq PageRequest, agent string, appLevel string, isApp bool) {
	kind := spec.Kind
	logger := h.Logger(r, "library_"+kind.ID+"_search")

	if !e.search.Enabled() {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "search index is disabled")
		return
	}
	dataset := scopes.Dataset(r)

	// Grant gate mirrors the aggregate: without any readable target, search
	// has nothing legitimate to return.
	targets, denied := spec.Targets(r)
	if len(targets) == 0 {
		if len(denied) > 0 {
			h.RecordAccess(r, AccessEvent{
				Agent:    agent,
				Surface:  SurfaceLibrary,
				Target:   kind.ID,
				Kind:     kind.ID,
				Decision: audit.DecisionDeny,
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
		readable[t.ID] = true
		if t.Local {
			// Local collection sources (mail archive) gate their own
			// readability when the target is registered (issue #312).
			localSource = true
		}
	}
	for _, svc := range h.Services() {
		configured[svc.ID] = true
	}

	cacheKey := kind.CacheKey() + "|q=" + q
	cacheKey += appCacheSuffix(appLevel, isApp)
	cacheKey += scopes.CacheSuffix(scopes.From(r))
	cacheKey += pageReq.CacheSuffix()
	if cached, ok := e.cache.Get(cacheKey); ok {
		logger.Debug("search cache hit")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Aegis-Cache", "hit")
		w.Write([]byte(cached))
		h.RecordAccess(r, AccessEvent{
			Agent:   agent,
			Surface: SurfaceLibrary,
			Target:  kind.ID,
			Kind:    kind.ID,
			Cache:   CacheHit,
		})
		return
	}

	n, err := e.search.Count(kind.ID, dataset)
	if err != nil {
		logger.Error("search index count failed", "error", err)
		shared.WriteJSONError(w, http.StatusInternalServerError, "search index unavailable")
		return
	}
	if n == 0 {
		// Cold index: materialize from one live fetch, then query. The
		// fetch runs at owner tier (issue #272) so a reduced caller's
		// first query still fills the index with the fullest rows;
		// RedactSearch narrows them on the way out.
		fetched := fetch(h, e, OwnerViewRequest(r), spec, logger, agent)
		if err := indexDocuments(e, spec, dataset, fetched); err != nil {
			logger.Warn("search index bootstrap failed", "error", err)
		}
	}

	// Clamp at the API boundary: the engine trusts its caller's limit.
	res, err := e.search.Search(search.Query{Kind: kind.ID, Dataset: dataset, Q: q, Limit: min(searchHitCap, MaxPageLimit)})
	if err != nil {
		logger.Error("search index query failed", "error", err)
		shared.WriteJSONError(w, http.StatusInternalServerError, "search index unavailable")
		return
	}
	hits := make([]T, 0, len(res.Docs))
	bySvc := make(map[string]LibraryServiceStatus)
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
			st.Status = LibraryStatusOK
			st.Count++
			bySvc[svc] = st
		}
	}
	if spec.RedactSearch != nil {
		hits = spec.RedactSearch(hits, r)
	}
	EnrichStatuses(h, kind.ID, spec.Status, bySvc)

	result, err := json.Marshal(spec.Encode(hits, pageReq, bySvc))
	if err != nil {
		logger.Error("failed to marshal library "+kind.ID+" search", "error", err)
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build "+kind.Label+" search results")
		return
	}
	e.cache.Set(cacheKey, string(result), 60*time.Second)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Aegis-Cache", "miss")
	w.Write(result)
	h.RecordAccess(r, AccessEvent{
		Agent:   agent,
		Surface: SurfaceLibrary,
		Target:  kind.ID,
		Kind:    kind.ID,
		Cache:   CacheMiss,
	})
}

// searchHitReadable applies the aggregate's per-service read gate to an
// indexed hit. Every source must be readable — a merged payload can carry
// source rows the fetch path would have dropped, so one unreadable source
// drops the hit. Sources no configured service claims (archive/import rows)
// stay visible while a declared local-source target is readable; that store
// already passed its own readability check when the target registered.
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

// indexDocuments flattens merged items into search documents and syncs them
// into the search index, dropping rows this fetch refuted (issue #272).
func indexDocuments[T any](e *Engine, spec Spec[T], dataset string, fetched Fetch[T]) error {
	if !e.search.Enabled() || spec.Kind.Search == nil {
		return nil
	}
	docs := make([]search.SearchDocument, 0, len(fetched.Items))
	for _, item := range fetched.Items {
		doc, err := BuildSearchDocument(spec.SortKey(item), item, *spec.Kind.Search)
		if err != nil {
			return err
		}
		docs = append(docs, doc)
	}
	return e.search.Sync(spec.Kind.ID, dataset, docs, completeServices(fetched.BySvc))
}

// indexAsync runs the search-index write-behind off the response path
// (issue #273). Fail-open: a broken index must not fail the list response,
// and latency on large lists must not land on the agent request.
func indexAsync[T any](h Host, e *Engine, spec Spec[T], dataset string, fetched Fetch[T]) {
	e.indexWG.Add(1)
	go func() {
		defer e.indexWG.Done()
		if err := indexDocuments(e, spec, dataset, fetched); err != nil {
			slog.Warn("search index update failed", "kind", spec.Kind.ID, "error", err)
		}
	}()
}

// completeServices lists the services whose fetch succeeded in full
// (status ok, not truncated). Only those can refute an indexed row; a
// degraded or truncated service leaves its rows untouched (issue #272).
func completeServices(bySvc map[string]LibraryServiceStatus) []string {
	out := make([]string, 0, len(bySvc))
	for id, st := range bySvc {
		if st.Status == LibraryStatusOK && !st.Truncated {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// aggregateDegraded reports whether any provider failed or was
// unauthorized. Degraded aggregates must not be cached.
func aggregateDegraded(bySvc map[string]LibraryServiceStatus) bool {
	for _, st := range bySvc {
		if st.Status != LibraryStatusOK {
			return true
		}
	}
	return false
}

// EnrichStatuses fills display name, canonical role, and last-sync fields
// used by the library source-overview header (issue #162). Fail-open:
// missing config or archive metadata leaves the original status. Kinds
// declare local-source naming and expansion through StatusHooks (issue
// #312) — no kind or source ids are named here.
func EnrichStatuses(h Host, kindID string, hooks StatusHooks, bySvc map[string]LibraryServiceStatus) {
	if len(bySvc) == 0 {
		return
	}
	canonicalID := ""
	for _, svc := range h.Services() {
		if svc.IsCanonicalFor(kindID) {
			canonicalID = svc.ID
			break
		}
	}
	mirrored := canonicalID != ""
	for id, st := range bySvc {
		st.Name = h.SourceName(id)
		if hooks.SourceName != nil {
			if name, ok := hooks.SourceName(id); ok {
				st.Name = name
			}
		}
		if mirrored {
			if id == canonicalID {
				st.Canonical = true
				st.Role = "canonical"
			} else {
				st.Role = "mirror"
			}
		}
		if hooks.SourceLastSyncedAt != nil {
			if ts, ok := hooks.SourceLastSyncedAt(id); ok {
				st.LastSyncedAt = ts
			}
		}
		bySvc[id] = st
	}
	if hooks.Expand != nil {
		hooks.Expand(bySvc)
	}
}
