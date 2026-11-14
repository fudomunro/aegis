package kinds

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

// Converge-plan tests (issue #382): each plan is driven directly against
// stub connectors over the Host seam — the composition the sync engine
// iterates must check itself before another kind lands on it.

// convergeStubHost is the minimal kinds.Host the plan constructors touch.
type convergeStubHost struct {
	conns map[string]any
}

func newConvergeStubHost(conns map[string]any) *convergeStubHost {
	return &convergeStubHost{conns: conns}
}

func (h *convergeStubHost) Connectors() map[string]any { return h.conns }
func (h *convergeStubHost) Service(string) (ServiceView, bool) {
	return ServiceView{}, false
}
func (h *convergeStubHost) Services() []ServiceView { return nil }
func (h *convergeStubHost) Grants(*http.Request) (shared.GrantMap, Principal) {
	return shared.GrantMap{}, PrincipalOwner
}
func (h *convergeStubHost) OwnerGrants() shared.GrantMap            { return shared.GrantMap{} }
func (h *convergeStubHost) RecordAccess(*http.Request, AccessEvent) {}
func (h *convergeStubHost) Logger(*http.Request, string) *slog.Logger {
	return slog.Default()
}
func (h *convergeStubHost) Agent(*http.Request) string                { return "test" }
func (h *convergeStubHost) SourceName(string) string                  { return "test" }
func (h *convergeStubHost) CopyInfo(string, string, string) *CopyInfo { return nil }

// entityStub is a per-service stub carrying one contact and one event
// collection and recording placements (create-or-update, never deletes).
type entityStub struct {
	contacts []Contact
	events   []CalendarEvent
	// placed captures placement inputs across calls (the write asserts).
	placed []any
}

func (s *entityStub) ListUserContacts(context.Context) ([]Contact, bool, error) {
	return append([]Contact(nil), s.contacts...), false, nil
}

func (s *entityStub) PlaceContacts(_ context.Context, in []Contact) (PlaceEntitiesResult, error) {
	s.placed = append(s.placed, in)
	res := PlaceEntitiesResult{}
	for _, c := range in {
		found := false
		for j := range s.contacts {
			if s.contacts[j].Name == c.Name {
				s.contacts[j] = c
				found = true
				break
			}
		}
		if !found {
			s.contacts = append(s.contacts, c)
		}
		res.Copied++
		res.Placed = append(res.Placed, PlaceRecord{
			Name:       c.Name,
			ExternalID: "id-" + c.Name,
			ETag:       "etag-" + c.Name,
		})
	}
	return res, nil
}

func (s *entityStub) ListCalendarEvents(context.Context) ([]CalendarEvent, bool, error) {
	return append([]CalendarEvent(nil), s.events...), false, nil
}

func (s *entityStub) PlaceEvents(_ context.Context, in []CalendarEvent) (PlaceEntitiesResult, error) {
	s.placed = append(s.placed, in)
	res := PlaceEntitiesResult{}
	for _, e := range in {
		found := false
		for j := range s.events {
			if s.events[j].Title == e.Title {
				s.events[j] = e
				found = true
				break
			}
		}
		if !found {
			s.events = append(s.events, e)
		}
		res.Copied++
		res.Placed = append(res.Placed, PlaceRecord{
			Name:       e.Title,
			ExternalID: "id-" + e.Title,
			ETag:       "etag-" + e.Title,
		})
	}
	return res, nil
}

func TestBatchTruncate(t *testing.T) {
	b := batchOf([]int{1, 2, 3, 4, 5})
	if b.Len() != 5 {
		t.Fatalf("Len = %d, want 5", b.Len())
	}
	cut := b.Truncate(2)
	if cut.Len() != 2 {
		t.Fatalf("Truncate(2).Len = %d, want 2", cut.Len())
	}
	if got := batchItems[int](cut); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("Truncate(2) items = %v, want [1 2]", got)
	}
	if same := b.Truncate(9); same.Len() != 5 {
		t.Fatalf("Truncate past the end = %d, want 5", same.Len())
	}
}

// eventsPlanWiring builds the events plan over a hub/member stub pair.
func eventsPlanWiring(t *testing.T, hub, member *entityStub) *ConvergePlan {
	t.Helper()
	return eventsConvergePlan(newConvergeStubHost(map[string]any{
		"hubcal":    hub,
		"membercal": member,
	}), "events")
}

// editedEvent is a member-side edit of one canonical event (same ical
// identity, different content).
func editedEvent() CalendarEvent {
	return CalendarEvent{
		Title:       "Standup (edited)",
		Description: "moved rooms",
		StartAt:     "2024-06-01T10:00:00Z",
		Sources: []CalendarEventSource{{
			Service: "membercal",
			ID:      "m1",
			Extra:   map[string]any{"ical_uid": "uid-1", "etag": "live-etag"},
		}},
	}
}

func canonicalEvent() CalendarEvent {
	return CalendarEvent{
		Title:   "Standup",
		StartAt: "2024-06-01T10:00:00Z",
		Sources: []CalendarEventSource{{
			Service: "hubcal",
			ID:      "e1",
			Extra:   map[string]any{"ical_uid": "uid-1", "etag": "hub-etag"},
		}},
	}
}

func TestEventsPlan_RecordResolveAccept(t *testing.T) {
	hub := &entityStub{events: []CalendarEvent{canonicalEvent()}}
	member := &entityStub{events: []CalendarEvent{editedEvent()}}
	plan := eventsPlanWiring(t, hub, member)

	stamps, found, err := plan.RecordResolve(context.Background(),
		"hubcal", "membercal", "m1", "Standup", true)
	if err != nil || !found {
		t.Fatalf("RecordResolve = (%v, %v, %v), want found", stamps, found, err)
	}
	if len(hub.placed) != 1 {
		t.Fatalf("hub placements = %d, want 1", len(hub.placed))
	}
	got := hub.placed[0].([]CalendarEvent)
	if len(got) != 1 || got[0].Title != "Standup (edited)" {
		t.Fatalf("hub received %v, want the member version", got)
	}
	// The placement carries the hub's existing ref so it updates in place.
	var hubRef *CalendarEventSource
	for i := range got[0].Sources {
		if got[0].Sources[i].Service == "hubcal" {
			hubRef = &got[0].Sources[i]
		}
	}
	if hubRef == nil || hubRef.ID != "e1" || ExtraString(hubRef.Extra, "etag") != "hub-etag" {
		t.Fatalf("hub ref = %+v, want id e1 with hub-etag", hubRef)
	}
	if len(stamps) != 1 || stamps[0].Service != "membercal" || stamps[0].ExternalID != "m1" ||
		stamps[0].EntityName != "ical:uid-1" || stamps[0].ETag != "live-etag" {
		t.Fatalf("stamps = %+v, want the member's live etag under the event's identity key", stamps)
	}
}

func TestEventsPlan_RecordResolveReject(t *testing.T) {
	hub := &entityStub{events: []CalendarEvent{canonicalEvent()}}
	member := &entityStub{events: []CalendarEvent{editedEvent()}}
	plan := eventsPlanWiring(t, hub, member)

	stamps, found, err := plan.RecordResolve(context.Background(),
		"hubcal", "membercal", "m1", "Standup", false)
	if err != nil || !found {
		t.Fatalf("RecordResolve = (%v, %v, %v), want found", stamps, found, err)
	}
	if len(member.placed) != 1 {
		t.Fatalf("member placements = %d, want 1", len(member.placed))
	}
	got := member.placed[0].([]CalendarEvent)
	if len(got) != 1 || got[0].Title != "Standup" {
		t.Fatalf("member received %v, want the canonical version", got)
	}
	// The restore is If-Match against the member's live ref.
	var memberRef *CalendarEventSource
	for i := range got[0].Sources {
		if got[0].Sources[i].Service == "membercal" {
			memberRef = &got[0].Sources[i]
		}
	}
	if memberRef == nil || memberRef.ID != "m1" || ExtraString(memberRef.Extra, "etag") != "live-etag" {
		t.Fatalf("member ref = %+v, want id m1 with live-etag", memberRef)
	}
	if len(stamps) != 1 || stamps[0].ETag != "etag-Standup" {
		t.Fatalf("stamps = %+v, want the placement watermarks", stamps)
	}
	if len(hub.placed) != 0 {
		t.Fatalf("hub was written on reject: %v", hub.placed)
	}
}

func TestEventsPlan_RecordResolveVanishedMember(t *testing.T) {
	hub := &entityStub{events: []CalendarEvent{canonicalEvent()}}
	member := &entityStub{}
	plan := eventsPlanWiring(t, hub, member)

	_, found, err := plan.RecordResolve(context.Background(),
		"hubcal", "membercal", "gone", "Standup", true)
	if err != nil {
		t.Fatalf("RecordResolve err = %v", err)
	}
	if found {
		t.Fatal("found = true, want false for a vanished member record")
	}
	if len(hub.placed) != 0 || len(member.placed) != 0 {
		t.Fatal("a vanished member record must not write anywhere")
	}
}

func TestEventsPlan_RecordResolveFallsBackToSubject(t *testing.T) {
	// The member's edit moved the meeting: the identity key no longer
	// matches, so the hub counterpart is found by the conflict subject —
	// the display name the copy row is keyed on.
	hub := &entityStub{events: []CalendarEvent{canonicalEvent()}}
	moved := editedEvent()
	moved.StartAt = "2024-06-01T15:00:00Z"
	moved.Sources[0].Extra = map[string]any{"etag": "live-etag"}
	member := &entityStub{events: []CalendarEvent{moved}}
	plan := eventsPlanWiring(t, hub, member)

	_, found, err := plan.RecordResolve(context.Background(),
		"hubcal", "membercal", "m1", "Standup", true)
	if err != nil || !found {
		t.Fatalf("RecordResolve = (_, %v, %v), want found", found, err)
	}
	if len(hub.placed) != 1 {
		t.Fatalf("hub placements = %d, want 1", len(hub.placed))
	}
	got := hub.placed[0].([]CalendarEvent)
	var hubRef *CalendarEventSource
	for i := range got[0].Sources {
		if got[0].Sources[i].Service == "hubcal" {
			hubRef = &got[0].Sources[i]
		}
	}
	if hubRef == nil || hubRef.ID != "e1" {
		t.Fatalf("hub ref = %+v, want the subject-matched e1", hubRef)
	}
}

func TestEventsPlan_MemberExtrasAdoptsOntoHub(t *testing.T) {
	extra := CalendarEvent{
		Title:   "Retro",
		StartAt: "2024-06-02T10:00:00Z",
		Sources: []CalendarEventSource{{Service: "membercal", ID: "m2"}},
	}
	hub := &entityStub{events: []CalendarEvent{canonicalEvent()}}
	member := &entityStub{events: []CalendarEvent{canonicalEvent(), extra}}
	plan := eventsPlanWiring(t, hub, member)

	count, adopt, err := plan.MemberExtras(context.Background(),
		"hubcal", "membercal", "e-list", "m-list")
	if err != nil {
		t.Fatalf("MemberExtras err = %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1 member-only event", count)
	}
	if err := adopt(context.Background()); err != nil {
		t.Fatalf("adopt err = %v", err)
	}
	if len(hub.placed) != 1 {
		t.Fatalf("hub placements = %d, want 1", len(hub.placed))
	}
	got := hub.placed[0].([]CalendarEvent)
	if len(got) != 1 || got[0].Title != "Retro" {
		t.Fatalf("adopted %v, want just Retro", got)
	}
}

func TestEventsPlan_AttachRefsAddsMemberWatermark(t *testing.T) {
	plan := eventsPlanWiring(t, &entityStub{}, &entityStub{})
	items := batchOf([]CalendarEvent{canonicalEvent()})
	refs := map[string]ItemRef{
		ItemEntityKey("events", "Standup"): {ExternalID: "m1", ETag: "live-etag"},
	}
	out := batchItems[CalendarEvent](plan.AttachRefs(items, "membercal", refs))
	if len(out) != 1 {
		t.Fatalf("out = %v, want one event", out)
	}
	var ref *CalendarEventSource
	for i := range out[0].Sources {
		if out[0].Sources[i].Service == "membercal" {
			ref = &out[0].Sources[i]
		}
	}
	if ref == nil || ref.ID != "m1" || ExtraString(ref.Extra, "etag") != "live-etag" {
		t.Fatalf("member ref = %+v, want id m1 with live-etag", ref)
	}
	// The canonical ref is untouched.
	if out[0].Sources[0].ID != "e1" {
		t.Fatalf("canonical ref = %+v, want e1 kept", out[0].Sources[0])
	}
}

func TestContactsPlan_RecordResolveAcceptFallsBackToName(t *testing.T) {
	// The edit changed the contact's identity (a new primary email), so
	// the hub counterpart is found by the conflict subject's display name.
	hub := &entityStub{contacts: []Contact{{
		Name:    "Ada",
		Emails:  []string{"ada@example.com"},
		Sources: []ContactSource{{Service: "hubc", ID: "c1", Extra: map[string]any{"etag": "hub-etag"}}},
	}}}
	member := &entityStub{contacts: []Contact{{
		Name:    "Ada Lovelace",
		Emails:  []string{"ada@other.example"},
		Sources: []ContactSource{{Service: "memberc", ID: "m1", Extra: map[string]any{"etag": "live-etag"}}},
	}}}
	plan := contactsConvergePlan(newConvergeStubHost(map[string]any{
		"hubc":    hub,
		"memberc": member,
	}), "contacts")

	stamps, found, err := plan.RecordResolve(context.Background(),
		"hubc", "memberc", "m1", "Ada", true)
	if err != nil || !found {
		t.Fatalf("RecordResolve = (_, %v, %v), want found", found, err)
	}
	if len(hub.placed) != 1 {
		t.Fatalf("hub placements = %d, want 1", len(hub.placed))
	}
	got := hub.placed[0].([]Contact)
	if len(got) != 1 || got[0].Name != "Ada Lovelace" {
		t.Fatalf("hub received %v, want the member version", got)
	}
	var hubRef *ContactSource
	for i := range got[0].Sources {
		if got[0].Sources[i].Service == "hubc" {
			hubRef = &got[0].Sources[i]
		}
	}
	if hubRef == nil || hubRef.ID != "c1" || ExtraString(hubRef.Extra, "etag") != "hub-etag" {
		t.Fatalf("hub ref = %+v, want the subject-matched c1", hubRef)
	}
	if len(stamps) != 1 || stamps[0].ETag != "live-etag" || stamps[0].EntityName != "email:ada@other.example" {
		t.Fatalf("stamps = %+v, want the member's live etag under the new identity key", stamps)
	}
}

// --- order propagation helpers (issue #378) ---

func TestOrderWatermark_DetectsPureReorder(t *testing.T) {
	base := OrderWatermark([]string{"a", "b", "c"})
	same := OrderWatermark([]string{"a", "b", "c"})
	reordered := OrderWatermark([]string{"b", "a", "c"})
	// Same items, same order: not a move.
	if orderMoved(base, same) {
		t.Fatal("an identical sequence must not read as a move")
	}
	// Same items, different order: a member-side reorder.
	if !orderMoved(base, reordered) {
		t.Fatal("a pure reorder must read as a move")
	}
	// Membership drift is the items channel's, never an order conflict.
	added := OrderWatermark([]string{"a", "b", "c", "d"})
	if orderMoved(base, added) {
		t.Fatal("a membership change must not read as an order move")
	}
	removed := OrderWatermark([]string{"a", "b"})
	if orderMoved(base, removed) {
		t.Fatal("a shortfall must not read as an order move")
	}
	// No baseline is not a move.
	if orderMoved("", reordered) {
		t.Fatal("an empty baseline must not read as a move")
	}
}

func TestOrderRefIDs_FillsSlotsAndKeepsExtras(t *testing.T) {
	hub := []Track{stubOrderTrack("c", "C"), stubOrderTrack("a", "A"), stubOrderTrack("b", "B")}
	// The member holds the canonical items in its own slots with a
	// member-only item between them.
	member := []Track{
		stubOrderTrack("a", "A"), stubOrderTrack("x", "Solo"),
		stubOrderTrack("c", "C"), stubOrderTrack("b", "B"),
	}
	// The canonical order C,A,B lands in the slots the canonical items
	// already occupy; Solo keeps its place (issue #378: a reorder never
	// moves anything else).
	got := orderRefIDs(hub, member, "icloud")
	want := []string{"c", "x", "a", "b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("orderRefIDs = %v, want %v", got, want)
	}
	// What stands on the member vs what a write leaves behind.
	stands := matchedRefIDs(hub, member, "icloud")
	if strings.Join(stands, ",") != "a,c,b" {
		t.Fatalf("matchedRefIDs = %v, want the member's order a,c,b", stands)
	}
	written := canonicalRefIDs(hub, member, "icloud")
	if strings.Join(written, ",") != "c,a,b" {
		t.Fatalf("canonicalRefIDs = %v, want the canonical order c,a,b", written)
	}
}

// stubOrderTrack builds a track with a service-local ref id.
func stubOrderTrack(id, name string) Track {
	return Track{Name: name, Sources: []TrackSource{{Service: "icloud", ID: id}}}
}

// --- copy-watermark keys (issue #384) ---

func TestContactItemKey_PrefersDurableIdentity(t *testing.T) {
	// A vCard UID is the durable card identity: a rename keeps the key.
	withUID := Contact{Name: "Ada", Extra: map[string]any{"uid": "card-1"}}
	if got := ContactItemKey(withUID); got != "uid:card-1" {
		t.Fatalf("ContactItemKey(uid) = %q, want uid:card-1", got)
	}
	// Email is next — rename-stable where the card carries no UID.
	withEmail := Contact{Name: "Ada", Emails: []string{"ada@example.com"}}
	if got := ContactItemKey(withEmail); got != "email:ada@example.com" {
		t.Fatalf("ContactItemKey(email) = %q, want the email key", got)
	}
	// A card with no durable id keys by its display name — what every
	// row was keyed by before.
	if got := ContactItemKey(Contact{Name: "Ada"}); got != "Ada" {
		t.Fatalf("ContactItemKey(name) = %q, want Ada", got)
	}
}

func TestEventItemKey_PrefersICalUID(t *testing.T) {
	ev := CalendarEvent{Title: "Standup", Sources: []CalendarEventSource{{
		Service: "hubcal", ID: "e1", Extra: map[string]any{"ical_uid": "uid-1"},
	}}}
	if got := EventItemKey(ev); got != "ical:uid-1" {
		t.Fatalf("EventItemKey(uid) = %q, want ical:uid-1", got)
	}
	// A retitle keeps the key — the point of the convention.
	ev.Title = "Daily sync (retitled)"
	if got := EventItemKey(ev); got != "ical:uid-1" {
		t.Fatalf("EventItemKey(retitled) = %q, want the same key", got)
	}
	if got := EventItemKey(CalendarEvent{Title: "Standup"}); got != "Standup" {
		t.Fatalf("EventItemKey(title) = %q, want Standup", got)
	}
}

func TestEventsPlan_AttachRefsSurvivesARetitle(t *testing.T) {
	// The watermark row keys on the event's iCal UID (issue #384): a
	// retitle on the canonical home still finds it, so placement takes the
	// If-Match update path instead of placing a fresh copy.
	plan := eventsPlanWiring(t, &entityStub{}, &entityStub{})
	retitled := canonicalEvent()
	retitled.Title = "Daily sync (retitled)"
	items := batchOf([]CalendarEvent{retitled})
	refs := map[string]ItemRef{
		ItemEntityKey("events", EventItemKey(retitled)): {ExternalID: "m1", ETag: "live-etag"},
	}
	out := batchItems[CalendarEvent](plan.AttachRefs(items, "membercal", refs))
	if len(out) != 1 {
		t.Fatalf("out = %v, want one event", out)
	}
	var ref *CalendarEventSource
	for i := range out[0].Sources {
		if out[0].Sources[i].Service == "membercal" {
			ref = &out[0].Sources[i]
		}
	}
	if ref == nil || ref.ID != "m1" || ExtraString(ref.Extra, "etag") != "live-etag" {
		t.Fatalf("member ref = %+v, want id m1 with live-etag", ref)
	}
}

func TestContactsPlan_AttachRefsFallsBackToLegacyNameRows(t *testing.T) {
	// Rows written before the identity-key convention key on the display
	// name: the lookup falls back so landing it does not re-place every
	// tracked copy (issue #384).
	plan := contactsConvergePlan(newConvergeStubHost(map[string]any{
		"memberc": &entityStub{},
	}), "contacts")
	ct := Contact{Name: "Ada", Emails: []string{"ada@example.com"}}
	items := batchOf([]Contact{ct})
	refs := map[string]ItemRef{
		ItemEntityKey("contacts", "Ada"): {ExternalID: "m1", ETag: "live-etag"},
	}
	out := batchItems[Contact](plan.AttachRefs(items, "memberc", refs))
	if len(out) != 1 {
		t.Fatalf("out = %v, want one contact", out)
	}
	var ref *ContactSource
	for i := range out[0].Sources {
		if out[0].Sources[i].Service == "memberc" {
			ref = &out[0].Sources[i]
		}
	}
	if ref == nil || ref.ID != "m1" || ExtraString(ref.Extra, "etag") != "live-etag" {
		t.Fatalf("member ref = %+v, want the legacy name row found", ref)
	}
}

func TestClassifyEntityRefs_Matrix(t *testing.T) {
	rep := ClassifyEntityRefs([][2]string{
		{"id-ada", "e1"},  // unchanged → matched
		{"id-bob", "e1"},  // live etag drifted → conflicting
		{"id-zoe", "e1"},  // vanished → conflicting
		{"", ""},          // no ref → created
	}, [][2]string{
		{"id-ada", "e1"},
		{"id-bob", "e2"},
	})
	if rep.Matched != 1 || rep.Created != 1 || rep.Conflicting != 2 {
		t.Fatalf("report = %+v, want matched=1 created=1 conflicting=2", rep)
	}
	// Missing etags on either side fall back to matched (nothing to judge).
	rep = ClassifyEntityRefs([][2]string{{"id-ada", ""}}, [][2]string{{"id-ada", ""}})
	if rep.Matched != 1 {
		t.Fatalf("report = %+v", rep)
	}
}
