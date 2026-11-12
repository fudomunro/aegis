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
		converge: contactsConvergePlan(h, def.ID),
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

// contactsConvergePlan is the contacts kind's converge plan (issue #382):
// the etag-entity shape — the whole collection is placed per member, with
// the member's known ref+etag attached so placement takes its If-Match
// update path, and record conflicts resolved across hub and member.
func contactsConvergePlan(h Host, kind string) *ConvergePlan {
	return &ConvergePlan{
		Shape:   ConvergeEntities,
		RowName: kind,
		TargetReady: func(conn any) bool {
			return providerProvides[ContactWriteProvider](conn)
		},
		ReadSource: func(ctx context.Context, hub, _, _ string, _ int) (SourcePage, error) {
			src, err := OpFor[ContactProvider](h, hub, "converge source")
			if err != nil {
				return SourcePage{}, err
			}
			contacts, truncated, err := src.ListUserContacts(ctx)
			if err != nil {
				return SourcePage{}, err
			}
			return SourcePage{Items: batchOf(contacts), HubTotal: len(contacts), Truncated: truncated}, nil
		},
		AttachRefs: func(items Batch, member string, refs map[string]ItemRef) Batch {
			contacts := batchItems[Contact](items)
			out := make([]Contact, len(contacts))
			copy(out, contacts)
			for i := range out {
				ref, ok := refs[ItemEntityKey(kind, contactDisplayName(out[i]))]
				if !ok {
					continue
				}
				out[i].Sources = upsertContactRef(out[i].Sources, member, ref.ExternalID, ref.ETag)
			}
			return batchOf(out)
		},
		Place: func(ctx context.Context, service, _, _ string, items Batch) (PlaceResult, error) {
			dst, err := OpFor[ContactWriteProvider](h, service, "converge target")
			if err != nil {
				return PlaceResult{}, err
			}
			res, err := dst.PlaceContacts(ctx, batchItems[Contact](items))
			if err != nil {
				return PlaceResult{}, err
			}
			return PlaceResult{
				Copied:    res.Copied,
				Placed:    res.Placed,
				Skips:     res.Skips,
				Conflicts: res.Conflicts,
			}, nil
		},
		MemberExtras: func(ctx context.Context, hub, member, _, _ string) (int, func(context.Context) error, error) {
			memberList, err := contactListFor(ctx, h, member, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubList, err := contactListFor(ctx, h, hub, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubKeys := make(map[string]bool, len(hubList))
			for _, c := range hubList {
				hubKeys[PrimaryContactKey(c)] = true
			}
			var extras []Contact
			for _, c := range memberList {
				if !hubKeys[PrimaryContactKey(c)] {
					extras = append(extras, c)
				}
			}
			adopt := func(ctx context.Context) error {
				dst, err := OpFor[ContactWriteProvider](h, hub, "conflict target")
				if err != nil {
					return err
				}
				_, err = dst.PlaceContacts(ctx, extras)
				return err
			}
			return len(extras), adopt, nil
		},
		RecordResolve: func(ctx context.Context, hub, member, memberID, subject string, accept bool) ([]ItemStamp, bool, error) {
			memberList, err := contactListFor(ctx, h, member, "conflict source")
			if err != nil {
				return nil, false, err
			}
			memberCT, found := findContactByRef(memberList, member, memberID)
			if !found {
				return nil, false, nil
			}
			hubList, err := contactListFor(ctx, h, hub, "conflict source")
			if err != nil {
				return nil, false, err
			}
			hubCT := findContactByKey(hubList, PrimaryContactKey(memberCT))
			if contactDisplayName(hubCT) == "" {
				// The edit may have changed the identity key itself — the
				// watermark rows key on the display name, so fall back to it.
				hubCT = findContactByName(hubList, subject)
			}
			if accept {
				dst, err := OpFor[ContactWriteProvider](h, hub, "conflict target")
				if err != nil {
					return nil, true, err
				}
				merged := memberCT
				merged.Sources = upsertContactRef(memberCT.Sources, hub,
					contactRefID(hubCT, hub), contactRefETag(hubCT, hub))
				if _, err := dst.PlaceContacts(ctx, []Contact{merged}); err != nil {
					return nil, true, err
				}
				// The member itself is unchanged: its watermark is refreshed
				// to the live etag so the next converge updates instead of
				// conflicting again.
				return []ItemStamp{{
					EntityName: subject,
					Service:    member,
					ExternalID: memberID,
					Name:       subject,
					ETag:       contactRefETag(memberCT, member),
				}}, true, nil
			}
			dst, err := OpFor[ContactWriteProvider](h, member, "conflict target")
			if err != nil {
				return nil, true, err
			}
			merged := hubCT
			merged.Sources = upsertContactRef(hubCT.Sources, member, memberID, contactRefETag(memberCT, member))
			res, err := dst.PlaceContacts(ctx, []Contact{merged})
			if err != nil {
				return nil, true, err
			}
			return itemStampsFrom(res.Placed, member), true, nil
		},
	}
}

// contactListFor reads one service's full contact list.
func contactListFor(ctx context.Context, h Host, service, what string) ([]Contact, error) {
	src, err := OpFor[ContactProvider](h, service, what)
	if err != nil {
		return nil, err
	}
	contacts, _, err := src.ListUserContacts(ctx)
	return contacts, err
}

// contactDisplayName is the placement-side display name convention
// (mirrors the connectors' placeContactDisplay): name, else first email.
func contactDisplayName(c Contact) string {
	if c.Name != "" {
		return c.Name
	}
	if len(c.Emails) > 0 {
		return c.Emails[0]
	}
	return ""
}

// upsertContactRef patches a contact's source for service with id+etag,
// replacing any previous entry for that service.
func upsertContactRef(srcs []ContactSource, service, id, etag string) []ContactSource {
	out := make([]ContactSource, 0, len(srcs)+1)
	replaced := false
	for _, src := range srcs {
		if src.Service == service {
			if !replaced {
				src.ID = id
				src.Extra = withETag(src.Extra, etag)
				out = append(out, src)
				replaced = true
			}
			continue
		}
		out = append(out, src)
	}
	if !replaced {
		out = append(out, ContactSource{Service: service, ID: id, Extra: withETag(nil, etag)})
	}
	return out
}

// findContactByRef finds the contact a member copy row points at.
func findContactByRef(list []Contact, service, externalID string) (Contact, bool) {
	for _, c := range list {
		for _, src := range c.Sources {
			if src.Service == service && src.ID == externalID {
				return c, true
			}
		}
	}
	return Contact{}, false
}

// findContactByKey finds the contact with the given identity key.
func findContactByKey(list []Contact, key string) Contact {
	if key == "" {
		return Contact{}
	}
	for _, c := range list {
		if PrimaryContactKey(c) == key {
			return c
		}
	}
	return Contact{}
}

// findContactByName finds the contact with the given display name.
func findContactByName(list []Contact, name string) Contact {
	for _, c := range list {
		if contactDisplayName(c) == name {
			return c
		}
	}
	return Contact{}
}

// contactRefID reads a contact's service-local id for one service.
func contactRefID(c Contact, service string) string {
	for _, src := range c.Sources {
		if src.Service == service {
			return src.ID
		}
	}
	return ""
}

// contactRefETag reads a contact's etag watermark for one service.
func contactRefETag(c Contact, service string) string {
	for _, src := range c.Sources {
		if src.Service == service {
			return ExtraString(src.Extra, "etag")
		}
	}
	return ""
}
