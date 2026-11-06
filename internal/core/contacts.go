package core

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

// HandleLibraryContacts returns the user's contacts from every connected
// service: GET /v1/library/contacts
func (s *Server) HandleLibraryContacts(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("contacts")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Contact]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Contact], []string) {
			return collectProviders[types.Contact, types.ContactProvider](s, r, kind.PermType,
				func(p types.ContactProvider) func(context.Context) ([]types.Contact, bool, error) {
					return p.ListUserContacts
				})
		},
		serviceOf: contactServiceName,
		sortKey:   types.PrimaryContactKey,
		merge:     types.MergeContacts,
		encode: func(items []types.Contact, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.ContactList{Contacts: pageItems, LibraryListMeta: types.MetaForPage(pr, true, anyServiceTruncated(bySvc), bySvc)}
		},
		markCanonical: s.stampContactCanonical,
		stampCopies:   s.stampContactCopies,
	})
}

func contactServiceName(c types.Contact) string {
	if len(c.Sources) > 0 {
		return c.Sources[0].ServiceName
	}
	return ""
}
