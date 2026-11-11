package kinds

import (
	"context"
	"net/http"
)

type contactsKind struct {
	providerKind
	h      Host
	e      *Engine
	mark   func([]Contact, string)
	copies func([]Contact)
}

func newContactsKind(h Host, e *Engine, d Deps) Kind {
	k := &contactsKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "contacts", forEachContactSource)
	def := *BuiltinKindByID("contacts")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[ContactProvider],
		writeCapable: providerProvides[ContactWriteProvider],
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
	Aggregate(k.h, k.e, w, r, Spec[Contact]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Contact], []string) {
			return Collect[Contact, ContactProvider](k.h, r, kind.PermType,
				func(p ContactProvider) func(context.Context) ([]Contact, bool, error) {
					return p.ListUserContacts
				})
		},
		ServiceOf: contactServiceName,
		SortKey:   PrimaryContactKey,
		Merge:     MergeContacts,
		Encode: func(items []Contact, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return ContactList{Contacts: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func contactServiceName(c Contact) string {
	if len(c.Sources) > 0 {
		return c.Sources[0].ServiceName
	}
	return ""
}

func forEachContactSource(item *Contact, f func(SourceMut)) {
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
