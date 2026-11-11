package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/shared"
)

// MailArchiveSourceID is the collection source id for the messages kind's
// local mail archive (Gmail sync + Google Takeout) (issue #139).
const MailArchiveSourceID = "mail-archive"

// libraryArchiveMessageCap bounds archive rows feeding the collection
// overview on GET /v1/library/messages. Filtered queries walk the full
// archive instead (issue #311).
const libraryArchiveMessageCap = 200

// MailSourceStatus is one ingest source's archive inventory row (issue #162).
type MailSourceStatus struct {
	ID           string
	Count        int
	LastSyncedAt string
}

// MailQuery selects archive rows for the messages kind (issue #311): the
// capped collection overview and full-history filtered retrieval share one
// shape.
type MailQuery struct {
	// Dataset restricts to one data-scope partition (docs/data-scopes.md).
	// Empty is the owner/default partition.
	Dataset string
	// Filters select full history (q/from/to/after/before/label). Empty
	// lists by recency.
	Filters MessageFilters
	// WithBody fills Message.Body on returned rows.
	WithBody bool
	// Cap bounds the overview window (newest first). 0 returns every match
	// up to the store's merge window, with truncated when it filled.
	Cap int
}

// MailArchive is the messages kind's declared local collection source
// (issue #139): the compressed mail archive and its ingest sources. Core
// adapts it beside Host (issue #312) — a constructor dep, not a Host
// method, because only this kind owns it.
type MailArchive interface {
	// Available reports whether the archive can feed the kind at all
	// (HasProviders without a request).
	Available() bool
	// Readable reports whether this request may read the archive.
	Readable(r *http.Request) bool
	// List returns matching archive rows for the dataset partition,
	// merged across ingest sources into one identity space.
	List(ctx context.Context, q MailQuery) ([]Message, bool, error)
	// Get returns one archived message with body and sibling sources
	// merged into a single identity entry. ok=false when the row is
	// missing or outside the dataset partition.
	Get(dataset, service, id string) (Message, bool)
	// Inventory reports ingest-source rows for the overview (issue #162):
	// per-source counts and last-sync timestamps; 0-count sources omitted.
	Inventory() []MailSourceStatus
	// SourceLastSynced answers the overview timestamp for one of the
	// kind's own sources (the archive itself or an ingest source).
	SourceLastSynced(id string) (string, bool)
	// Parent returns the declared parent source id for ingest grouping
	// (issue #286).
	Parent(id string) string
}

// messagesKind owns the messages aggregate (issue #139): live
// message-capable connectors plus the local mail archive as a declared
// local source (issue #312), on one retrieval surface (issue #311).
type messagesKind struct {
	providerKind
	h      Host
	e      *Engine
	mail   MailArchive
	mark   func([]Message, string)
	copies func([]Message)
}

func newMessagesKind(h Host, e *Engine, d Deps) Kind {
	k := &messagesKind{h: h, e: e, mail: d.MailArchive}
	k.mark, k.copies = Stamps(h, "messages", forEachMessageSource)
	def := *BuiltinKindByID("messages")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[MessageProvider],
		routes: []Route{
			{Pattern: def.Path, AppReadable: def.AppReadable, Handler: k.serve},
			// One message with body: the kind-owned fetch subroute (issue
			// #311). Not app-readable — bodies are content, not list
			// metadata.
			{Pattern: "/v1/library/messages/{service}/{id}", Handler: k.serveOne},
		},
	}
	return k
}

// HasProviders adds the local mail archive to the connector scan (issue #139).
func (k *messagesKind) HasProviders(h Host) bool {
	if k.providerKind.HasProviders(h) {
		return true
	}
	return k.mail != nil && k.mail.Available()
}

// serve returns the user's messages from every connected message-capable
// service and the local mail archive (Gmail sync + Takeout), merged into
// one collection: GET /v1/library/messages. Query filters (q, from, to,
// after, before, label, sort, include_body) walk the full history instead
// of the overview window (issue #311).
func (k *messagesKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	filters := ParseMessageFilters(r)
	cap := libraryArchiveMessageCap
	if filters.QueryMode() {
		cap = 0
	}
	Aggregate(k.h, k.e, w, r, Spec[Message]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Message], []string) {
			targets, denied := Collect[Message, MessageProvider](k.h, r, kind.PermType,
				func(p MessageProvider) func(context.Context) ([]Message, bool, error) {
					return p.ListMessages
				})
			// Local archive is another source of the same collection (Gmail
			// archive rows + Google Takeout). Fail-open with live providers
			// if the archive is disabled (issue #139).
			if k.mail != nil && k.mail.Readable(r) {
				mail := k.mail
				dataset := shared.Dataset(r)
				targets = append(targets, NamedLister[Message]{
					ID:    MailArchiveSourceID,
					Local: true,
					List: func(context.Context) ([]Message, bool, error) {
						return mail.List(context.Background(), MailQuery{
							Dataset:  dataset,
							Filters:  filters,
							WithBody: filters.NeedBody(),
							Cap:      cap,
						})
					},
				})
			}
			return targets, denied
		},
		ServiceOf: messageServiceName,
		SortKey:   MessageIdentityKey,
		Merge:     MergeMessages,
		// Filter before the aggregate is cached so full-history bodies
		// never sit in the cache and page walks share one fan-out.
		Finalize: func(items []Message) []Message {
			out := filterMessages(items, filters)
			if !filters.IncludeBody {
				stripMessageBodies(out)
			}
			return out
		},
		Encode: func(items []Message, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			sortMessages(items, filters.Ascending())
			pageItems, pr := SlicePage(items, page)
			return MessageList{Messages: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
		CacheKey:      "library:messages" + filters.CacheKeySuffix(),
		Status:        MessagesStatusHooks(k.h, k.mail),
		// Free-text is an archive FTS + merged live rows query (issue
		// #311), not a subject/snippet index lookup.
		QAsFilter: true,
	})
}

// serveOne returns one message with body from the local mail archive,
// sibling sources merged into the same identity entry the collection
// endpoint serves: GET /v1/library/messages/{service}/{id}
func (k *messagesKind) serveOne(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	kind := k.def
	agent := k.h.Agent(r)

	serviceID := r.PathValue("service")
	id := r.PathValue("id")
	if serviceID == "" || id == "" {
		shared.WriteJSONError(w, http.StatusNotFound, "expected /v1/library/messages/{service}/{id}")
		return
	}

	// One authorization lookup over the request key's rule map, mirroring
	// the collection endpoint.
	grants, _ := k.h.Grants(r)
	if !grants.AllowsKindMethod(kind.PermType, r.Method) {
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
		shared.WriteJSONError(w, http.StatusForbidden, kind.DenyMsg)
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
	if svc.Action == "block" {
		shared.WriteJSONError(w, http.StatusForbidden, "denied: service is blocked")
		return
	}

	// Archived copy first: body text and multi-source identity live there
	// (issue #311). Then the live provider, so every row the collection
	// endpoint shows is fetchable by its source id regardless of source.
	if k.mail != nil && k.mail.Readable(r) {
		if msg, ok := k.mail.Get(shared.Dataset(r), serviceID, id); ok {
			shared.WriteJSON(w, msg)
			k.h.RecordAccess(r, AccessEvent{
				Agent:   agent,
				Surface: SurfaceLibrary,
				Target:  kind.ID,
				Kind:    kind.ID,
				Cache:   CacheMiss,
			})
			return
		}
	}
	if p, ok := k.h.Connectors()[serviceID].(MessageProvider); ok {
		items, _, err := p.ListMessages(r.Context())
		if err == nil {
			for i := range items {
				if messageHasSource(items[i], serviceID, id) {
					shared.WriteJSON(w, items[i])
					k.h.RecordAccess(r, AccessEvent{
						Agent:   agent,
						Surface: SurfaceLibrary,
						Target:  kind.ID,
						Kind:    kind.ID,
						Cache:   CacheMiss,
					})
					return
				}
			}
		}
	}
	shared.WriteJSONError(w, http.StatusNotFound, "message not found")
}

// messageHasSource reports whether a message carries a service-local copy.
func messageHasSource(m Message, service, id string) bool {
	for _, src := range m.Sources {
		if src.Service == service && src.ID == id {
			return true
		}
	}
	return false
}

// MessagesStatusHooks declares the mail archive as a local source of the
// messages kind: display name, sync cursors, and the archive-ingest
// overview rows (issues #162, #312). The pipeline never branches on kind
// or source ids.
func MessagesStatusHooks(h Host, mail MailArchive) StatusHooks {
	return StatusHooks{
		SourceName: messagesSourceLabel,
		SourceLastSyncedAt: func(id string) (string, bool) {
			if mail == nil {
				return "", false
			}
			return mail.SourceLastSynced(id)
		},
		Expand: func(bySvc map[string]LibraryServiceStatus) {
			if mail == nil {
				return
			}
			expandMailArchiveSources(h, mail, bySvc)
		},
	}
}

// messagesSourceLabel names the kind's own collection sources.
func messagesSourceLabel(id string) (string, bool) {
	if id == MailArchiveSourceID {
		return "Mail archive", true
	}
	return "", false
}

// expandMailArchiveSources adds per-source rows for Gmail-synced and
// Takeout mail when the archive knows them. Counts come from the archive
// inventory (the affirmative understanding of those sources), not the
// library page cap.
func expandMailArchiveSources(h Host, mail MailArchive, bySvc map[string]LibraryServiceStatus) {
	for _, inv := range mail.Inventory() {
		if inv.Count <= 0 {
			continue
		}
		id := inv.ID
		name := h.SourceName(id)
		if label, ok := messagesSourceLabel(id); ok {
			name = label
		}
		last := inv.LastSyncedAt
		role := ""
		canonical := false
		// Source grouping is declared (issue #286): nest under the declared
		// parent when present, else under the collection's aggregate row,
		// else stand alone.
		parent := ""
		if p := mail.Parent(id); p != "" {
			if _, ok := bySvc[p]; ok {
				parent = p
			} else if _, ok := bySvc[MailArchiveSourceID]; ok {
				parent = MailArchiveSourceID
			}
		}
		existing, exists := bySvc[id]
		if exists && existing.Count >= inv.Count {
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
		bySvc[id] = LibraryServiceStatus{
			Status:       LibraryStatusOK,
			Count:        inv.Count,
			Name:         name,
			LastSyncedAt: last,
			Canonical:    canonical,
			Role:         role,
			Parent:       parent,
		}
	}
}

// messageServiceName returns the first source service name for stable
// pre-sort.
func messageServiceName(m Message) string {
	return MessageServiceName(m)
}

func forEachMessageSource(item *Message, f func(SourceMut)) {
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
