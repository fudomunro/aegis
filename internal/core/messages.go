package core

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/core/scopes"
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

// HandleLibraryMessages returns the user's messages from every connected
// message-capable service and the local mail archive (Gmail sync + Takeout),
// merged into one collection: GET /v1/library/messages
func (s *Server) HandleLibraryMessages(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("messages")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Message]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Message], []string) {
			targets, denied := collectProviders[types.Message, types.MessageProvider](s, r, kind.PermType,
				func(p types.MessageProvider) func(context.Context) ([]types.Message, bool, error) {
					return p.ListMessages
				})
			// Local archive is another source of the same collection (Gmail
			// archive rows + Google Takeout). Fail-open with live providers if
			// the archive is disabled (issue #139).
			if s.mail != nil && s.mailReadableFor(r) {
				archive := s.mail
				dataset := scopes.Dataset(r)
				targets = append(targets, namedLister[types.Message]{
					id: "mail-archive",
					list: func(context.Context) ([]types.Message, bool, error) {
						return archive.ListMessagesForLibrary(libraryArchiveMessageCap, dataset)
					},
				})
			}
			return targets, denied
		},
		serviceOf: messageServiceName,
		sortKey:   types.MessageIdentityKey,
		merge:     types.MergeMessages,
		encode: func(items []types.Message, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.MessageList{Messages: pageItems, LibraryListMeta: types.MetaForPage(pr, true, anyServiceTruncated(bySvc), bySvc)}
		},
		markCanonical: s.stampMessageCanonical,
		stampCopies:   s.stampMessageCopies,
	})
}
