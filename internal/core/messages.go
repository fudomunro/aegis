package core

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

// messageServiceName returns the first source service name for stable pre-sort.
func messageServiceName(m types.Message) string {
	return types.MessageServiceName(m)
}

// libraryArchiveMessageCap bounds archive rows feeding /v1/library/messages.
// Full history stays on /v1/mail/search; the library page is the merged
// collection overview (issue #139).
const libraryArchiveMessageCap = 200

// mailArchiveSourceID is the collection source id for the local mail
// archive (Gmail sync + Google Takeout) (issue #139).
const mailArchiveSourceID = "mail-archive"

// HandleLibraryMessages returns the user's messages from every connected
// message-capable service and the local mail archive (Gmail sync + Takeout),
// merged into one collection: GET /v1/library/messages
func (s *Server) HandleLibraryMessages(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("messages")
	mark, copies := kinds.Stamps(s, "messages", forEachMessageSource)
	kinds.Aggregate(s, s.libraryEngine(), w, r, kinds.Spec[types.Message]{
		Kind: kind,
		Targets: func(r *http.Request) ([]kinds.NamedLister[types.Message], []string) {
			targets, denied := kinds.Collect[types.Message, types.MessageProvider](s, r, kind.PermType,
				func(p types.MessageProvider) func(context.Context) ([]types.Message, bool, error) {
					return p.ListMessages
				})
			// Local archive is another source of the same collection (Gmail
			// archive rows + Google Takeout). Fail-open with live providers if
			// the archive is disabled (issue #139).
			if s.mail != nil && s.mailReadableFor(r) {
				archive := s.mail
				dataset := scopes.Dataset(r)
				targets = append(targets, kinds.NamedLister[types.Message]{
					ID:    mailArchiveSourceID,
					Local: true,
					List: func(context.Context) ([]types.Message, bool, error) {
						return archive.ListMessagesForLibrary(libraryArchiveMessageCap, dataset)
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
			return types.MessageList{Messages: pageItems, LibraryListMeta: types.MetaForPage(pr, true, kinds.AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: mark,
		StampCopies:   copies,
		Status:        s.messagesStatusHooks(),
	})
}

// messagesStatusHooks declares the mail archive as a local source of this
// kind: display name, sync cursors, and the archive-ingest overview rows
// (issues #162, #312). The pipeline never branches on kind or source ids.
func (s *Server) messagesStatusHooks() kinds.StatusHooks {
	return kinds.StatusHooks{
		SourceName:         s.messagesSourceLabel,
		SourceLastSyncedAt: s.mailSourceLastSyncedAt,
		Expand:             s.expandMailArchiveSources,
	}
}

// messagesSourceLabel names the kind's own collection sources.
func (s *Server) messagesSourceLabel(id string) (string, bool) {
	if id == mailArchiveSourceID {
		return "Mail archive", true
	}
	return "", false
}

// mailSourceLastSyncedAt reports the archive sync cursor for the archive
// itself and its ingest sources; other sources have no sync time here.
func (s *Server) mailSourceLastSyncedAt(id string) (string, bool) {
	if s.mail == nil {
		return "", false
	}
	if id != mailArchiveSourceID && s.mailIngestFor(id) == nil {
		return "", false
	}
	st, err := s.mail.Status()
	if err != nil {
		return "", true
	}
	if cur, err := s.mail.LoadSyncCursor(id); err == nil && cur.LastSyncAt != "" {
		return cur.LastSyncAt, true
	}
	return st.LastSyncAt, true
}

// expandMailArchiveSources adds per-source rows for Gmail-synced and Takeout
// mail when the archive has both. Counts come from the archive inventory
// (the affirmative understanding of those sources), not the library page cap.
func (s *Server) expandMailArchiveSources(bySvc map[string]types.LibraryServiceStatus) {
	if s.mail == nil {
		return
	}
	st, err := s.mail.Status()
	if err != nil {
		return
	}
	for svc, n := range st.ByService {
		if n <= 0 {
			continue
		}
		id := svc
		name := s.SourceName(id)
		if label, ok := s.messagesSourceLabel(id); ok {
			name = label
		}
		last := ""
		if cur, err := s.mail.LoadSyncCursor(svc); err == nil {
			last = cur.LastSyncAt
		}
		if last == "" {
			last = st.LastSyncAt
		}
		role := ""
		canonical := false
		// Source grouping is declared (issue #286): nest under the declared
		// parent when present, else under the collection's aggregate row,
		// else stand alone.
		parent := ""
		if p := connectors.MailSourceFor(id).Parent; p != "" {
			if _, ok := bySvc[p]; ok {
				parent = p
			} else if _, ok := bySvc[mailArchiveSourceID]; ok {
				parent = mailArchiveSourceID
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

func forEachMessageSource(item *types.Message, f func(kinds.SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(kinds.SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}
