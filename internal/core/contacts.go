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
	mark, copies := kinds.Stamps(s, "contacts", forEachContactSource)
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
		MarkCanonical: mark,
		StampCopies:   copies,
	})
}

func contactServiceName(c types.Contact) string {
	if len(c.Sources) > 0 {
		return c.Sources[0].ServiceName
	}
	return ""
}

func forEachContactSource(item *types.Contact, f func(kinds.SourceMut)) {
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
