package core

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// documentServiceName returns the first source service name for stable pre-sort.
func documentServiceName(d types.Document) string {
	return types.DocumentServiceName(d)
}

// documentExtraString reads a string Extra field.
func documentExtraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v, ok := extra[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// documentIdentityKey is the sort/merge key used by the library aggregate.
// Prefer durable content checksum, then archive serial, then title + created.
// Documents without any identity fall back to service:id so they stay distinct.
func documentIdentityKey(d types.Document) string {
	if id := strings.TrimSpace(d.EntityID); id != "" {
		return "entity:" + strings.ToLower(id)
	}
	for _, src := range d.Sources {
		if cs := strings.TrimSpace(documentExtraString(src.Extra, "checksum")); cs != "" {
			return "checksum:" + strings.ToLower(cs)
		}
		if asn := strings.TrimSpace(documentExtraString(src.Extra, "archive_serial")); asn != "" {
			return "asn:" + asn
		}
	}
	title := types.NormalizeIdentityKey(d.Title)
	created := strings.TrimSpace(d.CreatedAt)
	if title == "" || created == "" {
		if len(d.Sources) > 0 && d.Sources[0].Service != "" && d.Sources[0].ID != "" {
			return "src:" + d.Sources[0].Service + ":" + d.Sources[0].ID
		}
		return "anon:" + documentServiceName(d) + ":" + d.Title + ":" + d.CreatedAt
	}
	return "title:" + title + "|created:" + created
}

// documentMatchKind records how identity was established for multi-source merges.
func documentMatchKind(key string) string {
	switch {
	case strings.HasPrefix(key, "entity:"):
		// EntityID may be checksum or archive serial; both are durable IDs.
		return "entity_id"
	case strings.HasPrefix(key, "checksum:"):
		return "checksum"
	case strings.HasPrefix(key, "asn:"):
		return "archive_serial"
	case strings.HasPrefix(key, "title:"):
		return "title_created"
	default:
		return ""
	}
}

func mergeDocumentInto(dst *types.Document, src types.Document) {
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.MimeType == "" {
		dst.MimeType = src.MimeType
	}
	if dst.CreatedAt == "" {
		dst.CreatedAt = src.CreatedAt
	}
	if dst.ModifiedAt == "" {
		dst.ModifiedAt = src.ModifiedAt
	}
	if dst.Correspondent == "" {
		dst.Correspondent = src.Correspondent
	}
	if dst.DocumentType == "" {
		dst.DocumentType = src.DocumentType
	}
	if dst.Excerpt == "" {
		dst.Excerpt = src.Excerpt
	}
	if dst.EntityID == "" {
		dst.EntityID = src.EntityID
	}
	dst.Tags = unionDocumentStrings(dst.Tags, src.Tags)
	dst.People = mergeMessageContacts(dst.People, src.People)
	dst.Sources = append(dst.Sources, src.Sources...)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

func unionDocumentStrings(dst, src []string) []string {
	if len(src) == 0 {
		return dst
	}
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{dst, src} {
		for _, l := range list {
			l = strings.TrimSpace(l)
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// MergeDocuments groups documents that share a durable identity (checksum,
// archive serial, or entity id) or the same title + created into one
// user-facing entry with multiple Sources. Documents without an identity
// stay distinct via service:id.
func MergeDocuments(in []types.Document) []types.Document {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		d types.Document
	}
	var order []string
	byKey := map[string]*slot{}

	for _, doc := range in {
		key := documentIdentityKey(doc)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergeDocumentInto(&cur.d, doc)
			continue
		}
		cp := doc
		cp.Sources = append([]types.DocumentSource(nil), doc.Sources...)
		if len(doc.Tags) > 0 {
			cp.Tags = append([]string(nil), doc.Tags...)
		}
		if len(doc.People) > 0 {
			cp.People = append([]types.Contact(nil), doc.People...)
		}
		byKey[key] = &slot{d: cp}
		order = append(order, key)
	}

	out := make([]types.Document, 0, len(order))
	for _, key := range order {
		item := byKey[key]
		if len(item.d.Sources) > 1 {
			if mk := documentMatchKind(key); mk != "" {
				item.d.Match = mk
			}
		}
		sort.SliceStable(item.d.Sources, func(i, j int) bool {
			if item.d.Sources[i].Canonical != item.d.Sources[j].Canonical {
				return item.d.Sources[i].Canonical
			}
			if item.d.Sources[i].ServiceName != item.d.Sources[j].ServiceName {
				return item.d.Sources[i].ServiceName < item.d.Sources[j].ServiceName
			}
			return item.d.Sources[i].Service < item.d.Sources[j].Service
		})
		out = append(out, item.d)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := out[i].CreatedAt, out[j].CreatedAt
		if ci != cj {
			return ci > cj // newest first
		}
		ti, tj := out[i].Title, out[j].Title
		if ti != tj {
			return ti < tj
		}
		return documentIdentityKey(out[i]) < documentIdentityKey(out[j])
	})
	return out
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
	return transactionDay(raw)
}

// matches reports whether a document passes every active filter.
func (f DocumentFilters) matches(d types.Document) bool {
	if f.Empty() {
		return true
	}
	day := documentDay(d.CreatedAt)
	if f.After != "" {
		if day == "" || !dateAtOrAfter(day, f.After) {
			return false
		}
	}
	if f.Before != "" {
		if day == "" || !dateBefore(day, f.Before) {
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
			level = s.effectivePermissions(svc).For(permType)
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
		sortKey:   documentIdentityKey,
		merge:     MergeDocuments,
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
		markCanonical: s.stampDocumentCanonical,
		stampCopies:   s.stampDocumentCopies,
		cacheKey:      "library:documents" + filters.cacheKeySuffix() + "|meta:" + boolKey(metaOnly),
	})
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
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	kind := *LibraryKindByID("documents")
	logger := withPeerLog(slog.With("handler", "library_documents_text"), r)
	agent := agentForRequest(r)

	serviceID := r.PathValue("service")
	id := r.PathValue("id")
	if serviceID == "" || id == "" {
		writeJSONError(w, http.StatusNotFound, "expected /v1/library/documents/{service}/{id}/text")
		return
	}

	app := AppIdentityFrom(r)
	if app != nil && !appAllowsLibrary(app, kind.PermType, r.Method) {
		writeJSONError(w, http.StatusForbidden,
			"denied: app data grant for documents is "+app.Scopes.For(kind.PermType))
		return
	}

	level := types.DefaultAccessType
	svc := s.config.GetService(serviceID)
	if svc != nil {
		// Data-scope isolation: foreign partitions look missing (docs/data-scopes.md).
		if !serviceVisibleToScope(svc, DataScopeFrom(r)) {
			writeJSONError(w, http.StatusNotFound, "unknown service "+serviceID)
			return
		}
		level = s.effectivePermissions(svc).For(kind.PermType)
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
		writeJSONError(w, http.StatusForbidden,
			"denied: document text requires documents=read (metadata is list-only)")
		return
	}

	connector, ok := s.connectors[serviceID]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown service "+serviceID)
		return
	}
	provider, ok := connector.(types.DocumentProvider)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "service does not provide documents")
		return
	}
	if svc != nil && svc.Action == "block" {
		writeJSONError(w, http.StatusForbidden, "denied: service is blocked")
		return
	}

	text, truncated, err := provider.FetchDocumentText(r.Context(), id)
	switch {
	case err == nil:
		// ok
	case errors.Is(err, types.ErrUnauthorized) || strings.Contains(strings.ToLower(err.Error()), "unauthorized"):
		writeJSONError(w, http.StatusUnauthorized,
			"not authorized — connect at /v1/auth/"+serviceID+"/login")
		return
	default:
		logger.Warn("document text failed", "service", serviceID, "id", id, "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch document text")
		return
	}

	result := types.DocumentText{
		Service:   serviceID,
		ID:        id,
		Text:      text,
		Truncated: truncated,
	}
	writeJSON(w, result)
	s.recordAccessFrom(r, AccessEvent{
		Agent:    agent,
		Surface:  MetricSurfaceLibrary,
		Target:   kind.ID,
		DataType: kind.ID,
		Cache:    MetricCacheMiss,
	})
}
