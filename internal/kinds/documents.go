package kinds

import (
	"context"
	"errors"
	"net/http"
	"strings"

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

// Grant keys are routes: the library-relative path (issue #348).
const (
	documentsRouteKey         = "documents"
	documentsMetadataRouteKey = "documents/metadata"
)

func newDocumentsKind(h Host, e *Engine, d Deps) Kind {
	k := &documentsKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "documents", forEachDocumentSource)
	def := *BuiltinKindByID("documents")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[DocumentProvider],
		routes: []Route{
			{Pattern: def.Path, AppReadable: def.AppReadable, Handler: k.serve, GrantKey: documentsRouteKey},
			{Pattern: "/v1/library/documents/metadata", AppReadable: true, Handler: k.serveMetadata, GrantKey: documentsMetadataRouteKey},
			// Full text is a read on the documents route: the metadata
			// route never sees it (issue #143).
			{Pattern: "/v1/library/documents/{service}/{id}/text", Handler: k.serveText, GrantKey: documentsRouteKey},
		},
	}
	return k
}

// serve returns the user's documents with list excerpts:
// GET /v1/library/documents. Query filters: q, tag, after, before (issue
// #143).
func (k *documentsKind) serve(w http.ResponseWriter, r *http.Request) {
	k.serveShape(w, r, documentsRouteKey, false)
}

// serveMetadata returns titles, dates, tags, and sources only — no excerpt
// or text: GET /v1/library/documents/metadata (issue #348).
func (k *documentsKind) serveMetadata(w http.ResponseWriter, r *http.Request) {
	k.serveShape(w, r, documentsMetadataRouteKey, true)
}

func (k *documentsKind) serveShape(w http.ResponseWriter, r *http.Request, grantKey string, metadataOnly bool) {
	kind := k.def
	filters := parseDocumentFilters(r)
	if kind.Search != nil {
		// Free-text q is owned by the search index (issue #178). tag/after/
		// before stay handler-side filters over whatever produced the items.
		filters.Q = ""
	}
	Aggregate(k.h, k.e, w, r, Spec[Document]{
		Kind:     kind,
		GrantKey: grantKey,
		Targets: func(r *http.Request) ([]NamedLister[Document], []string) {
			return documentsProviderTargets(k.h, r, grantKey)
		},
		ServiceOf: documentServiceName,
		SortKey:   DocumentIdentityKey,
		Merge:     MergeDocuments,
		Encode: func(items []Document, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			filtered := filterDocuments(items, filters)
			if metadataOnly {
				filtered = redactDocumentsMetadata(filtered)
			}
			pageItems, pr := SlicePage(filtered, page)
			return DocumentList{
				Documents:       pageItems,
				LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc),
			}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
		CacheKey:      "library:" + grantKey + filters.cacheKeySuffix(),
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

	// Full text is a read on the documents route (issue #348): a
	// documents/metadata-only key is denied here.
	grants, _ := k.h.Grants(r)
	level := grants.Level(documentsRouteKey)
	if !grants.AllowsKindMethod(documentsRouteKey, r.Method) {
		k.h.RecordAccess(r, AccessEvent{
			Agent:    agent,
			Surface:  SurfaceLibrary,
			Target:   kind.ID,
			Kind:     kind.ID,
			Decision: DecisionDeny,
			Reason:   "data_grant",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		shared.WriteJSONError(w, http.StatusForbidden,
			"denied: data grant for documents is "+level)
		return
	}
	svc, known := k.h.Service(serviceID)
	if known {
		// Data-scope isolation: foreign partitions look missing (docs/data-scopes.md).
		if !shared.ServiceVisible(svc.Dataset, shared.DataScopeFrom(r)) {
			shared.WriteJSONError(w, http.StatusNotFound, "unknown service "+serviceID)
			return
		}
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

// redactDocumentsMetadata strips excerpt/text from documents while keeping
// titles, dates, tags, and sources (issue #143).
func redactDocumentsMetadata(items []Document) []Document {
	out := make([]Document, len(items))
	for i, d := range items {
		out[i] = redactDocumentMetadata(d)
	}
	return out
}

// documentsProviderTargets collects providers; the route's encode owns the
// disclosure shape (issue #348).
func documentsProviderTargets(h Host, r *http.Request, grantKey string) ([]NamedLister[Document], []string) {
	return Collect[Document, DocumentProvider](h, r, grantKey,
		func(p DocumentProvider) func(context.Context) ([]Document, bool, error) {
			return p.ListUserDocuments
		})
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
