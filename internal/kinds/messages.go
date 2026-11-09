package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/types"
)

// MailArchiveSourceID is the collection source id for the messages kind's
// local mail archive (Gmail sync + Google Takeout) (issue #139).
const MailArchiveSourceID = "mail-archive"

// libraryArchiveMessageCap bounds archive rows feeding /v1/library/messages.
// Full history stays on /v1/mail/search; the library page is the merged
// collection overview (issue #139).
const libraryArchiveMessageCap = 200

// MailSourceStatus is one ingest source's archive inventory row (issue #162).
type MailSourceStatus struct {
	ID           string
	Count        int
	LastSyncedAt string
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
	// List returns up to cap archive rows for the dataset partition.
	List(ctx context.Context, dataset string, cap int) ([]types.Message, bool, error)
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
// local source (issue #312).
type messagesKind struct {
	providerKind
	h      Host
	e      *Engine
	mail   MailArchive
	mark   func([]types.Message, string)
	copies func([]types.Message)
}

func newMessagesKind(h Host, e *Engine, d Deps) Kind {
	k := &messagesKind{h: h, e: e, mail: d.MailArchive}
	k.mark, k.copies = Stamps(h, "messages", forEachMessageSource)
	def := *BuiltinKindByID("messages")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[types.MessageProvider],
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
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
// one collection: GET /v1/library/messages
func (k *messagesKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[types.Message]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.Message], []string) {
			targets, denied := Collect[types.Message, types.MessageProvider](k.h, r, kind.PermType,
				func(p types.MessageProvider) func(context.Context) ([]types.Message, bool, error) {
					return p.ListMessages
				})
			// Local archive is another source of the same collection (Gmail
			// archive rows + Google Takeout). Fail-open with live providers
			// if the archive is disabled (issue #139).
			if k.mail != nil && k.mail.Readable(r) {
				mail := k.mail
				dataset := scopes.Dataset(r)
				targets = append(targets, NamedLister[types.Message]{
					ID:    MailArchiveSourceID,
					Local: true,
					List: func(context.Context) ([]types.Message, bool, error) {
						return mail.List(context.Background(), dataset, libraryArchiveMessageCap)
					},
				})
			}
			return targets, denied
		},
		ServiceOf: messageServiceName,
		SortKey:   types.MessageIdentityKey,
		Merge:     types.MergeMessages,
		Encode: func(items []types.Message, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.MessageList{Messages: pageItems, LibraryListMeta: types.MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
		Status:        MessagesStatusHooks(k.h, k.mail),
	})
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
		Expand: func(bySvc map[string]types.LibraryServiceStatus) {
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
func expandMailArchiveSources(h Host, mail MailArchive, bySvc map[string]types.LibraryServiceStatus) {
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
		bySvc[id] = types.LibraryServiceStatus{
			Status:       types.LibraryStatusOK,
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
func messageServiceName(m types.Message) string {
	return types.MessageServiceName(m)
}

func forEachMessageSource(item *types.Message, f func(SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}
