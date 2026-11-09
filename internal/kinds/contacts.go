package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

type contactsKind struct {
	providerKind
	h      Host
	e      *Engine
	mark   func([]types.Contact, string)
	copies func([]types.Contact)
}

func newContactsKind(h Host, e *Engine, d Deps) Kind {
	k := &contactsKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "contacts", forEachContactSource)
	def := *BuiltinKindByID("contacts")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[types.ContactProvider],
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

func (k *contactsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[types.Contact]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.Contact], []string) {
			return Collect[types.Contact, types.ContactProvider](k.h, r, kind.PermType,
				func(p types.ContactProvider) func(context.Context) ([]types.Contact, bool, error) {
					return p.ListUserContacts
				})
		},
		ServiceOf: contactServiceName,
		SortKey:   types.PrimaryContactKey,
		Merge:     types.MergeContacts,
		Encode: func(items []types.Contact, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.ContactList{Contacts: pageItems, LibraryListMeta: types.MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func contactServiceName(c types.Contact) string {
	if len(c.Sources) > 0 {
		return c.Sources[0].ServiceName
	}
	return ""
}

func forEachContactSource(item *types.Contact, f func(SourceMut)) {
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
