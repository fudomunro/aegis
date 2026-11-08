package core

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

// HandleLibraryContacts returns the user's contacts from every connected
// service: GET /v1/library/contacts
func (s *Server) HandleLibraryContacts(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("contacts")
	kinds.Aggregate(s, s.libraryEngine(), w, r, kinds.Spec[types.Contact]{
		Kind: kind,
		Targets: func(r *http.Request) ([]kinds.NamedLister[types.Contact], []string) {
			return kinds.Collect[types.Contact, types.ContactProvider](s, r, kind.PermType,
				func(p types.ContactProvider) func(context.Context) ([]types.Contact, bool, error) {
					return p.ListUserContacts
				})
		},
		ServiceOf: contactServiceName,
		SortKey:   types.PrimaryContactKey,
		Merge:     types.MergeContacts,
		Encode: func(items []types.Contact, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.ContactList{Contacts: pageItems, LibraryListMeta: types.MetaForPage(pr, true, kinds.AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: s.stampContactCanonical,
		StampCopies:   s.stampContactCopies,
	})
}

func contactServiceName(c types.Contact) string {
	if len(c.Sources) > 0 {
		return c.Sources[0].ServiceName
	}
	return ""
}
