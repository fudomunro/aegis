package kinds

import "testing"

func TestMergeContacts_SharedEmailAcrossServices(t *testing.T) {
	a := NewContact("google", "Google Contacts", "g1", "Ada Lovelace",
		[]string{"ada@example.com"}, nil)
	b := NewContact("outlook", "Outlook", "o9", "Ada L.",
		[]string{"ADA@example.com"}, []string{"+1 (555) 123-4567"})
	merged := MergeContacts([]Contact{a, b})
	if len(merged) != 1 {
		t.Fatalf("got %d contacts, want 1: %+v", len(merged), merged)
	}
	c := merged[0]
	if c.Name != "Ada Lovelace" {
		t.Fatalf("name = %q", c.Name)
	}
	if len(c.Emails) != 1 || c.Emails[0] != "ada@example.com" {
		t.Fatalf("emails = %v", c.Emails)
	}
	if len(c.Phones) != 1 || c.Phones[0] != "+15551234567" {
		t.Fatalf("phones = %v", c.Phones)
	}
	if len(c.Sources) != 2 {
		t.Fatalf("sources = %+v", c.Sources)
	}
}

func TestMergeContacts_PhoneLinksDifferentEmails(t *testing.T) {
	a := NewContact("google", "Google", "g1", "Bob",
		[]string{"bob@work.example"}, []string{"+15550001111"})
	b := NewContact("outlook", "Outlook", "o1", "Robert",
		[]string{"bob@home.example"}, []string{"+1 (555) 000-1111"})
	merged := MergeContacts([]Contact{a, b})
	if len(merged) != 1 {
		t.Fatalf("phone should link contacts: %+v", merged)
	}
	if len(merged[0].Emails) != 2 {
		t.Fatalf("emails = %v", merged[0].Emails)
	}
}

func TestMergeContacts_NameFallbackSeparatesDistinctPeople(t *testing.T) {
	a := NewContact("google", "Google", "g1", "Alex Kim",
		[]string{"alex1@example.com"}, nil)
	b := NewContact("google", "Google", "g2", "Alex Kim",
		[]string{"alex2@example.com"}, nil)
	merged := MergeContacts([]Contact{a, b})
	if len(merged) != 2 {
		t.Fatalf("distinct emails must not merge by name alone: %+v", merged)
	}
}

func TestMergeContacts_UnionFindChainsOnSharedEmail(t *testing.T) {
	// A and B share email; B and C share phone → one entity via union-find.
	a := Contact{
		Name:    "Sam One",
		Emails:  []string{"sam@example.com"},
		Sources: []ContactSource{{Service: "google", ServiceName: "Google", ID: "a"}},
	}
	b := Contact{
		Name:    "Sam Two",
		Emails:  []string{"sam@example.com"},
		Phones:  []string{"+15550002222"},
		Sources: []ContactSource{{Service: "outlook", ServiceName: "Outlook", ID: "b"}},
	}
	c := Contact{
		Name:    "Sam Two",
		Phones:  []string{"+1 555 000 2222"},
		Sources: []ContactSource{{Service: "icloud", ServiceName: "iCloud", ID: "c"}},
	}
	merged := MergeContacts([]Contact{a, b, c})
	if len(merged) != 1 {
		t.Fatalf("chained keys should merge into one: %+v", merged)
	}
	if len(merged[0].Sources) != 3 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestMergeContacts_NameOnlyDoesNotJoinEmailContact(t *testing.T) {
	withEmail := Contact{
		Name:    "Alex Kim",
		Emails:  []string{"alex@example.com"},
		Sources: []ContactSource{{Service: "google", ServiceName: "Google", ID: "g"}},
	}
	nameOnly := Contact{
		Name:    "Alex Kim",
		Sources: []ContactSource{{Service: "outlook", ServiceName: "Outlook", ID: "o"}},
	}
	merged := MergeContacts([]Contact{withEmail, nameOnly})
	if len(merged) != 2 {
		t.Fatalf("name-only must not join an email-bearing contact: %+v", merged)
	}
}

func TestMergeContacts_Empty(t *testing.T) {
	if MergeContacts(nil) != nil {
		t.Fatal("nil in should yield nil out")
	}
}

func TestMergeContacts_SharedUIDLinksDriftedCards(t *testing.T) {
	// The same card seen twice with edited emails — the vCard UID folds it.
	a := Contact{
		Name:    "Ada Lovelace",
		Emails:  []string{"old@example.com"},
		Extra:   map[string]any{"uid": "card-1"},
		Sources: []ContactSource{{Service: "dav", ServiceName: "DAV", ID: "a", Extra: map[string]any{"uid": "card-1"}}},
	}
	b := Contact{
		Name:    "Ada L.",
		Emails:  []string{"new@example.com"},
		Extra:   map[string]any{"uid": "card-1"},
		Sources: []ContactSource{{Service: "icloud", ServiceName: "iCloud", ID: "b", Extra: map[string]any{"uid": "card-1"}}},
	}
	merged := MergeContacts([]Contact{a, b})
	if len(merged) != 1 {
		t.Fatalf("shared vCard UID must fold drifted cards: %+v", merged)
	}
	if len(merged[0].Emails) != 2 {
		t.Fatalf("emails = %v", merged[0].Emails)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestMergeContacts_SharedUIDLinksEmailAddedOnOneSide(t *testing.T) {
	withEmail := Contact{
		Name:    "Bob",
		Emails:  []string{"bob@example.com"},
		Extra:   map[string]any{"uid": "card-2"},
		Sources: []ContactSource{{Service: "dav", ID: "a", Extra: map[string]any{"uid": "card-2"}}},
	}
	withoutEmail := Contact{
		Name:    "Bob",
		Extra:   map[string]any{"uid": "card-2"},
		Sources: []ContactSource{{Service: "icloud", ID: "b", Extra: map[string]any{"uid": "card-2"}}},
	}
	merged := MergeContacts([]Contact{withEmail, withoutEmail})
	if len(merged) != 1 {
		t.Fatalf("shared vCard UID must fold an email-less twin: %+v", merged)
	}
}

func TestMergeContacts_UIDOnlyJoinsNameOnly(t *testing.T) {
	// The name key stays available to a UID-only card: it still folds with
	// a name-only card of the same display name.
	withUID := Contact{
		Name:    "Grace Hopper",
		Extra:   map[string]any{"uid": "card-3"},
		Sources: []ContactSource{{Service: "dav", ID: "a", Extra: map[string]any{"uid": "card-3"}}},
	}
	nameOnly := Contact{
		Name:    "Grace Hopper",
		Sources: []ContactSource{{Service: "outlook", ID: "o"}},
	}
	merged := MergeContacts([]Contact{withUID, nameOnly})
	if len(merged) != 1 {
		t.Fatalf("uid-only must still fold with a name-only twin: %+v", merged)
	}
}

func TestMergeContacts_SharedEmailLinksDistinctUIDs(t *testing.T) {
	// Two cards of one person can carry different UIDs; a shared email is
	// co-primary identity and still folds them.
	a := Contact{
		Name:    "Ada Lovelace",
		Emails:  []string{"ada@example.com"},
		Extra:   map[string]any{"uid": "card-1"},
		Sources: []ContactSource{{Service: "dav", ID: "a", Extra: map[string]any{"uid": "card-1"}}},
	}
	b := Contact{
		Name:    "Ada L.",
		Emails:  []string{"ada@example.com"},
		Extra:   map[string]any{"uid": "card-2"},
		Sources: []ContactSource{{Service: "icloud", ID: "b", Extra: map[string]any{"uid": "card-2"}}},
	}
	merged := MergeContacts([]Contact{a, b})
	if len(merged) != 1 {
		t.Fatalf("shared email must fold cards that carry distinct UIDs: %+v", merged)
	}
}

func TestMergeContacts_NameEligibleCardStaysOffEmailBearingCard(t *testing.T) {
	// The name key must not cross to an email-bearing card: a UID-only
	// card and a UID+email card with the same display name stay separate
	// even though both carry UIDs.
	a := Contact{
		Name:    "Alex Kim",
		Extra:   map[string]any{"uid": "card-1"},
		Sources: []ContactSource{{Service: "dav", ID: "a", Extra: map[string]any{"uid": "card-1"}}},
	}
	b := Contact{
		Name:    "Alex Kim",
		Emails:  []string{"alex@example.com"},
		Extra:   map[string]any{"uid": "card-2"},
		Sources: []ContactSource{{Service: "icloud", ID: "b", Extra: map[string]any{"uid": "card-2"}}},
	}
	merged := MergeContacts([]Contact{a, b})
	if len(merged) != 2 {
		t.Fatalf("a name-only card must not join an email-bearing card: %+v", merged)
	}
}
