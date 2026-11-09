package kinds

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/core/audit"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/shared"
)

// documentsKind owns the documents aggregate and its full-text subroute
// (issue #143): list filters, metadata-only redaction, and a separately
// gated text read that metadata access cannot use.
type documentsKind struct {
	providerKind
	h      Host
	e      *Engine
	mark   func([]Document, string)
	copies func([]Document)
}

func newDocumentsKind(h Host, e *Engine, d Deps) Kind {
	k := &documentsKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "documents", forEachDocumentSource)
	def := *BuiltinKindByID("documents")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[DocumentProvider],
		routes: []Route{
			{Pattern: def.Path, AppReadable: def.AppReadable, Handler: k.serve},
			// Full text is a separate read: app tokens and metadata-only
			// surfaces never see it (issue #143).
			{Pattern: "/v1/library/documents/{service}/{id}/text", Handler: k.serveText},
		},
	}
	return k
}

// serve returns the user's documents from every connected source:
// GET /v1/library/documents. Query filters: q, tag, after, before (issue
// #143). Permission key: documents. Access metadata returns titles/dates/
// tags only.
func (k *documentsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	filters := parseDocumentFilters(r)
	if kind.Search != nil {
		// Free-text q is owned by the search index (issue #178). tag/after/
		// before stay handler-side filters over whatever produced the items.
		filters.Q = ""
	}
	appLevel, _ := k.h.AppLevel(r, kind.PermType)
	// Encode-time redaction is a second belt: even if a service returned an
	// excerpt, metadata-only surfaces must not ship it.
	metaOnly := policy.IsMetadataAccess(appLevel)
	Aggregate(k.h, k.e, w, r, Spec[Document]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Document], []string) {
			return documentsProviderTargets(k.h, r, kind.PermType, appLevel)
		},
		ServiceOf: documentServiceName,
		SortKey:   DocumentIdentityKey,
		Merge:     MergeDocuments,
		Encode: func(items []Document, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			filtered := filterDocuments(items, filters)
			if metaOnly {
				filtered = redactDocumentsForLevel(filtered, policy.AccessMetadata)
			}
			pageItems, pr := SlicePage(filtered, page)
			return DocumentList{
				Documents:       pageItems,
				LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc),
			}
		},
		RedactSearch: func(items []Document, r *http.Request) []Document {
			return searchRedactDocuments(k.h, items, r)
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
		CacheKey:      "library:documents" + filters.cacheKeySuffix() + "|meta:" + boolKey(metaOnly),
	})
}

// serveText returns full extracted text for one document:
// GET /v1/library/documents/{service}/{id}/text. Gated separately from
// list metadata: metadata access cannot read content (issue #143).
func (k *documentsKind) serveText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	kind := k.def
	logger := k.h.Logger(r, "library_documents_text")
	agent := k.h.Agent(r)

	serviceID := r.PathValue("service")
	id := r.PathValue("id")
	if serviceID == "" || id == "" {
		shared.WriteJSONError(w, http.StatusNotFound, "expected /v1/library/documents/{service}/{id}/text")
		return
	}

	appLevel, isApp := k.h.AppLevel(r, kind.PermType)
	if isApp && !appAllows(appLevel, kind.PermType, r.Method) {
		shared.WriteJSONError(w, http.StatusForbidden,
			"denied: app data grant for documents is "+appLevel)
		return
	}

	level := policy.DefaultAccessType
	svc, known := k.h.Service(serviceID)
	if known {
		// Data-scope isolation: foreign partitions look missing (docs/data-scopes.md).
		if !scopes.ServiceVisible(svc.Dataset, scopes.From(r)) {
			shared.WriteJSONError(w, http.StatusNotFound, "unknown service "+serviceID)
			return
		}
		level = k.h.RequestLevel(r, serviceID, kind.PermType)
	} else if isApp {
		level = appLevel
	}
	// Metadata is list-only: full text requires read (or unrestricted write/full).
	if !policy.AllowsMethod(level, http.MethodGet) || policy.IsMetadataAccess(level) || policy.DocumentsLevelForResponse(level) == policy.AccessNone {
		k.h.RecordAccess(r, AccessEvent{
			Agent:    agent,
			Surface:  SurfaceLibrary,
			Target:   kind.ID,
			Kind:     kind.ID,
			Decision: audit.DecisionDeny,
			Reason:   "permission",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		shared.WriteJSONError(w, http.StatusForbidden,
			"denied: document text requires documents=read (metadata is list-only)")
		return
	}

	connector, ok := k.h.Connectors()[serviceID]
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown service "+serviceID)
		return
	}
	provider, ok := connector.(DocumentProvider)
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "service does not provide documents")
		return
	}
	if known && svc.Action == "block" {
		shared.WriteJSONError(w, http.StatusForbidden, "denied: service is blocked")
		return
	}

	text, truncated, err := provider.FetchDocumentText(r.Context(), id)
	switch {
	case err == nil:
		// ok
	case errors.Is(err, ErrUnauthorized) || strings.Contains(strings.ToLower(err.Error()), "unauthorized"):
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"not authorized — connect at /v1/auth/"+serviceID+"/login")
		return
	default:
		logger.Warn("document text failed", "service", serviceID, "id", id, "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch document text")
		return
	}

	result := DocumentText{
		Service:   serviceID,
		ID:        id,
		Text:      text,
		Truncated: truncated,
	}
	shared.WriteJSON(w, result)
	k.h.RecordAccess(r, AccessEvent{
		Agent:   agent,
		Surface: SurfaceLibrary,
		Target:  kind.ID,
		Kind:    kind.ID,
		Cache:   CacheMiss,
	})
}

// documentServiceName returns the first source service name for stable
// pre-sort.
func documentServiceName(d Document) string {
	return DocumentServiceName(d)
}

// documentFilters are agent-facing list filters for GET /v1/library/documents.
type documentFilters struct {
	Q      string // free-text on title/correspondent/document_type/tags/excerpt
	Tag    string // exact tag name (case-insensitive)
	After  string // inclusive lower bound on created_at (date or RFC3339)
	Before string // exclusive upper bound on created_at (date or RFC3339)
}

// parseDocumentFilters reads query parameters from the request.
// The issue's API shape is GET /v1/library/documents?q=&tag=&after=&before=.
func parseDocumentFilters(r *http.Request) documentFilters {
	q := r.URL.Query()
	return documentFilters{
		Q:      strings.TrimSpace(q.Get("q")),
		Tag:    strings.TrimSpace(q.Get("tag")),
		After:  strings.TrimSpace(q.Get("after")),
		Before: strings.TrimSpace(q.Get("before")),
	}
}

// empty reports whether no filter is active.
func (f documentFilters) empty() bool {
	return f.Q == "" && f.Tag == "" && f.After == "" && f.Before == ""
}

// cacheKeySuffix so filtered requests do not share unfiltered cache entries.
func (f documentFilters) cacheKeySuffix() string {
	if f.empty() {
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

// matches reports whether a document passes every active filter.
func (f documentFilters) matches(d Document) bool {
	if f.empty() {
		return true
	}
	day := documentDay(d.CreatedAt)
	if f.After != "" {
		if day == "" || !DateAtOrAfter(day, f.After) {
			return false
		}
	}
	if f.Before != "" {
		if day == "" || !DateBefore(day, f.Before) {
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

func documentDay(raw string) string {
	return TransactionDay(raw)
}

func filterDocuments(in []Document, f documentFilters) []Document {
	if f.empty() {
		return in
	}
	out := make([]Document, 0, len(in))
	for _, d := range in {
		if f.matches(d) {
			out = append(out, d)
		}
	}
	return out
}

// redactDocumentMetadata strips excerpt/text from a document while keeping
// titles, dates, tags, and sources (issue #143).
func redactDocumentMetadata(d Document) Document {
	d.Excerpt = ""
	return d
}

func redactDocumentsForLevel(items []Document, level string) []Document {
	switch policy.DocumentsLevelForResponse(level) {
	case policy.AccessMetadata:
		out := make([]Document, len(items))
		for i, d := range items {
			out[i] = redactDocumentMetadata(d)
		}
		return out
	case policy.AccessNone:
		return nil
	default:
		return items
	}
}

// documentsProviderTargets collects providers, applying per-service and
// app-level metadata/read redaction so a metadata-only surface never
// contributes excerpts (issue #143).
func documentsProviderTargets(h Host, r *http.Request, permType string, appLevel string) ([]NamedLister[Document], []string) {
	targets, denied := Collect[Document, DocumentProvider](h, r, permType,
		func(p DocumentProvider) func(context.Context) ([]Document, bool, error) {
			return p.ListUserDocuments
		})
	appMeta := policy.IsMetadataAccess(appLevel)
	for i := range targets {
		id := targets[i].ID
		inner := targets[i].List
		level := ""
		if _, ok := h.Service(id); ok {
			level = h.RequestLevel(r, id, permType)
		}
		effective := level
		if appMeta {
			effective = policy.AccessMetadata
		}
		if effective == level && !policy.IsMetadataAccess(level) {
			continue
		}
		targets[i].List = func(ctx context.Context) ([]Document, bool, error) {
			items, truncated, err := inner(ctx)
			return redactDocumentsForLevel(items, effective), truncated, err
		}
	}
	return targets, denied
}

// searchRedactDocuments re-applies per-source metadata redaction to
// index-hydrated documents (issue #178). Redactions only remove data, so
// applying every source's level in turn settles on the most restrictive
// one (mixed-source hits may over-redact; never under-redact).
func searchRedactDocuments(h Host, items []Document, r *http.Request) []Document {
	appLevel, isApp := h.AppLevel(r, "documents")
	if !isApp {
		appLevel = ""
	}
	out := make([]Document, 0, len(items))
	for _, d := range items {
		skip := false
		for _, src := range d.Sources {
			if _, ok := h.Service(src.Service); !ok {
				// Local/import sources gate their own content.
				continue
			}
			level := h.RequestLevel(r, src.Service, "documents")
			red := redactDocumentsForLevel([]Document{d}, policy.DocumentsLevelForResponse(level))
			if len(red) == 0 {
				skip = true
				break
			}
			d = red[0]
		}
		if skip {
			continue
		}
		if policy.IsMetadataAccess(appLevel) {
			d = redactDocumentMetadata(d)
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

func forEachDocumentSource(item *Document, f func(SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *CopyInfo) { src.Copy = info },
		})
	}
}
