package core

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

// documentServiceName returns the first source service name for stable pre-sort.
func documentServiceName(d types.Document) string {
	return types.DocumentServiceName(d)
}

// DocumentFilters are agent-facing list filters for GET /v1/library/documents.
type DocumentFilters struct {
	Q      string // free-text on title/correspondent/document_type/tags/excerpt
	Tag    string // exact tag name (case-insensitive)
	After  string // inclusive lower bound on created_at (date or RFC3339)
	Before string // exclusive upper bound on created_at (date or RFC3339)
}

// ParseDocumentFilters reads query parameters from the request.
// The issue's API shape is GET /v1/library/documents?q=&tag=&after=&before=.
func ParseDocumentFilters(r *http.Request) DocumentFilters {
	q := r.URL.Query()
	return DocumentFilters{
		Q:      strings.TrimSpace(q.Get("q")),
		Tag:    strings.TrimSpace(q.Get("tag")),
		After:  strings.TrimSpace(q.Get("after")),
		Before: strings.TrimSpace(q.Get("before")),
	}
}

// Empty reports whether no filter is active.
func (f DocumentFilters) Empty() bool {
	return f.Q == "" && f.Tag == "" && f.After == "" && f.Before == ""
}

// cacheKeySuffix so filtered requests do not share unfiltered cache entries.
func (f DocumentFilters) cacheKeySuffix() string {
	if f.Empty() {
		return ""
	}
	var b strings.Builder
	write := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
	}
	write("q", strings.ToLower(f.Q))
	write("tag", strings.ToLower(f.Tag))
	write("after", f.After)
	write("before", f.Before)
	return b.String()
}

func documentDay(raw string) string {
	return types.TransactionDay(raw)
}

// matches reports whether a document passes every active filter.
func (f DocumentFilters) matches(d types.Document) bool {
	if f.Empty() {
		return true
	}
	day := documentDay(d.CreatedAt)
	if f.After != "" {
		if day == "" || !types.DateAtOrAfter(day, f.After) {
			return false
		}
	}
	if f.Before != "" {
		if day == "" || !types.DateBefore(day, f.Before) {
			return false
		}
	}
	if f.Tag != "" {
		need := strings.ToLower(f.Tag)
		found := false
		for _, t := range d.Tags {
			if strings.ToLower(strings.TrimSpace(t)) == need {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.Q != "" {
		need := strings.ToLower(f.Q)
		hay := strings.ToLower(strings.Join([]string{
			d.Title, d.Correspondent, d.DocumentType, d.Excerpt,
		}, " ") + " " + strings.Join(d.Tags, " "))
		if !strings.Contains(hay, need) {
			return false
		}
	}
	return true
}

func filterDocuments(in []types.Document, f DocumentFilters) []types.Document {
	if f.Empty() {
		return in
	}
	out := make([]types.Document, 0, len(in))
	for _, d := range in {
		if f.matches(d) {
			out = append(out, d)
		}
	}
	return out
}

// RedactDocumentMetadata strips excerpt/text from a document while keeping
// titles, dates, tags, and sources (issue #143).
func RedactDocumentMetadata(d types.Document) types.Document {
	d.Excerpt = ""
	return d
}

func redactDocumentsForLevel(items []types.Document, level string) []types.Document {
	switch types.DocumentsLevelForResponse(level) {
	case types.AccessMetadata:
		out := make([]types.Document, len(items))
		for i, d := range items {
			out[i] = RedactDocumentMetadata(d)
		}
		return out
	case types.AccessNone:
		return nil
	default:
		return items
	}
}

// documentsProviderTargets collects providers, applying per-service and
// app-level metadata/read redaction so a metadata-only surface never
// contributes excerpts (issue #143).
func documentsProviderTargets(s *Server, r *http.Request, permType string, appLevel string) ([]namedLister[types.Document], []string) {
	targets, denied := collectProviders[types.Document, types.DocumentProvider](s, r, permType,
		func(p types.DocumentProvider) func(context.Context) ([]types.Document, bool, error) {
			return p.ListUserDocuments
		})
	appMeta := types.IsMetadataAccess(appLevel)
	for i := range targets {
		id := targets[i].id
		inner := targets[i].list
		level := ""
		if svc := s.config.GetService(id); svc != nil {
			level = s.requestLevel(r, svc, permType)
		}
		effective := level
		if appMeta {
			effective = types.AccessMetadata
		}
		if effective == level && !types.IsMetadataAccess(level) {
			continue
		}
		targets[i].list = func(ctx context.Context) ([]types.Document, bool, error) {
			items, truncated, err := inner(ctx)
			return redactDocumentsForLevel(items, effective), truncated, err
		}
	}
	return targets, denied
}

// HandleLibraryDocuments returns the user's documents from every connected
// source: GET /v1/library/documents
// Query filters: q, tag, after, before (issue #143).
// Permission key: documents. Access metadata returns titles/dates/tags only.
func (s *Server) HandleLibraryDocuments(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("documents")
	filters := ParseDocumentFilters(r)
	if kind.Search != nil {
		// Free-text q is owned by the search index (issue #178). tag/after/
		// before stay handler-side filters over whatever produced the items.
		filters.Q = ""
	}
	appLevel := ""
	if app := AppIdentityFrom(r); app != nil {
		appLevel = app.Scopes.For(kind.PermType)
	}
	// Encode-time redaction is a second belt: even if a service returned an
	// excerpt, metadata-only surfaces must not ship it.
	metaOnly := types.IsMetadataAccess(appLevel)
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Document]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Document], []string) {
			return documentsProviderTargets(s, r, kind.PermType, appLevel)
		},
		serviceOf: documentServiceName,
		sortKey:   types.DocumentIdentityKey,
		merge:     types.MergeDocuments,
		encode: func(items []types.Document, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			filtered := filterDocuments(items, filters)
			if metaOnly {
				filtered = redactDocumentsForLevel(filtered, types.AccessMetadata)
			}
			pageItems, pr := types.SlicePage(filtered, page)
			return types.DocumentList{
				Documents:       pageItems,
				LibraryListMeta: types.MetaForPage(pr, true, anyServiceTruncated(bySvc), bySvc),
			}
		},
		redactSearch: func(items []types.Document, r *http.Request) []types.Document {
			return s.searchRedactDocuments(items, r)
		},
		markCanonical: s.stampDocumentCanonical,
		stampCopies:   s.stampDocumentCopies,
		cacheKey:      "library:documents" + filters.cacheKeySuffix() + "|meta:" + boolKey(metaOnly),
	})
}

// searchRedactDocuments re-applies per-source metadata redaction to
// index-hydrated documents (issue #178). Redactions only remove data, so
// applying every source's level in turn settles on the most restrictive one
// (mixed-source hits may over-redact; never under-redact).
func (s *Server) searchRedactDocuments(items []types.Document, r *http.Request) []types.Document {
	appLevel := ""
	if app := AppIdentityFrom(r); app != nil {
		appLevel = app.Scopes.For("documents")
	}
	out := make([]types.Document, 0, len(items))
	for _, d := range items {
		skip := false
		for _, src := range d.Sources {
			svc := s.config.GetService(src.Service)
			if svc == nil {
				// Local/import sources gate their own content.
				continue
			}
			level := s.requestLevel(r, svc, "documents")
			red := redactDocumentsForLevel([]types.Document{d}, types.DocumentsLevelForResponse(level))
			if len(red) == 0 {
				skip = true
				break
			}
			d = red[0]
		}
		if skip {
			continue
		}
		if types.IsMetadataAccess(appLevel) {
			d = RedactDocumentMetadata(d)
		}
		out = append(out, d)
	}
	return out
}

func boolKey(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// HandleLibraryDocumentText returns full extracted text for one document:
// GET /v1/library/documents/{service}/{id}/text
// Gated separately from list metadata: metadata access cannot read content
// (issue #143).
func (s *Server) HandleLibraryDocumentText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	kind := *LibraryKindByID("documents")
	logger := withPeerLog(slog.With("handler", "library_documents_text"), r)
	agent := agentForRequest(r)

	serviceID := r.PathValue("service")
	id := r.PathValue("id")
	if serviceID == "" || id == "" {
		shared.WriteJSONError(w, http.StatusNotFound, "expected /v1/library/documents/{service}/{id}/text")
		return
	}

	app := AppIdentityFrom(r)
	if app != nil && !appAllowsLibrary(app, kind.PermType, r.Method) {
		shared.WriteJSONError(w, http.StatusForbidden,
			"denied: app data grant for documents is "+app.Scopes.For(kind.PermType))
		return
	}

	level := types.DefaultAccessType
	svc := s.config.GetService(serviceID)
	if svc != nil {
		// Data-scope isolation: foreign partitions look missing (docs/data-scopes.md).
		if !serviceVisibleToScope(svc, DataScopeFrom(r)) {
			shared.WriteJSONError(w, http.StatusNotFound, "unknown service "+serviceID)
			return
		}
		level = s.requestLevel(r, svc, kind.PermType)
	} else if app != nil {
		level = app.Scopes.For(kind.PermType)
	}
	// Metadata is list-only: full text requires read (or unrestricted write/full).
	if !types.AllowsMethod(level, http.MethodGet) || types.IsMetadataAccess(level) || types.DocumentsLevelForResponse(level) == types.AccessNone {
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
		shared.WriteJSONError(w, http.StatusForbidden,
			"denied: document text requires documents=read (metadata is list-only)")
		return
	}

	connector, ok := s.connectors[serviceID]
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown service "+serviceID)
		return
	}
	provider, ok := connector.(types.DocumentProvider)
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "service does not provide documents")
		return
	}
	if svc != nil && svc.Action == "block" {
		shared.WriteJSONError(w, http.StatusForbidden, "denied: service is blocked")
		return
	}

	text, truncated, err := provider.FetchDocumentText(r.Context(), id)
	switch {
	case err == nil:
		// ok
	case errors.Is(err, types.ErrUnauthorized) || strings.Contains(strings.ToLower(err.Error()), "unauthorized"):
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"not authorized — connect at /v1/auth/"+serviceID+"/login")
		return
	default:
		logger.Warn("document text failed", "service", serviceID, "id", id, "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch document text")
		return
	}

	result := types.DocumentText{
		Service:   serviceID,
		ID:        id,
		Text:      text,
		Truncated: truncated,
	}
	shared.WriteJSON(w, result)
	s.recordAccessFrom(r, AccessEvent{
		Agent:    agent,
		Surface:  MetricSurfaceLibrary,
		Target:   kind.ID,
		DataType: kind.ID,
		Cache:    MetricCacheMiss,
	})
}
