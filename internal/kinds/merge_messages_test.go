package kinds

import "testing"

// newMessage builds a single-source neutral message.
func newMessage(service, serviceName, id, channel, subject, snippet, sentAt string) Message {
	src := MessageSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}
	return Message{
		Channel: channel,
		Subject: subject,
		Snippet: snippet,
		SentAt:  sentAt,
		Sources: []MessageSource{src},
	}
}

func TestMessageIdentityKey_PrefersMessageID(t *testing.T) {
	m := newMessage("google-gmail", "Gmail", "id1", MessageChannelEmail, "Hi", "body", "2024-06-01T09:00:00Z")
	m.EntityID = "<AbC@example.com>"
	if got := MessageIdentityKey(m); got != "msgid:abc@example.com" {
		t.Fatalf("key = %q", got)
	}
}

func TestMessageIdentityKey_SubjectFromTimeFallback(t *testing.T) {
	m := newMessage("google-gmail", "Gmail", "id1", MessageChannelEmail, "  Hello  ", "", "2024-06-01T09:00:00+00:00")
	from := NewContact("google-gmail", "Gmail", "", "Ada", []string{"Ada@Example.com"}, nil)
	m.From = &from
	if got := MessageIdentityKey(m); got != "subject:hello|from:ada@example.com|sent:2024-06-01T09:00:00Z" {
		t.Fatalf("key = %q", got)
	}
}

func TestMergeMessages_ByMessageIDAndSubjectFrom(t *testing.T) {
	a := newMessage("google-gmail", "Gmail", "a1", MessageChannelEmail, "Invoice", "s", "2024-06-01T09:00:00Z")
	a.EntityID = "shared@example.com"
	fromA := Contact{Name: "Ada", Emails: []string{"ada@example.com"}}
	a.From = &fromA
	b := newMessage("outlook-mail", "Outlook Mail", "b1", MessageChannelEmail, "Different", "s", "2024-06-01T09:00:00Z")
	b.EntityID = "shared@example.com"
	c := newMessage("google-gmail", "Gmail", "c1", MessageChannelEmail, "Planning", "", "2024-06-02T14:00:00Z")
	fromC := Contact{Name: "Grace", Emails: []string{"grace@example.com"}}
	c.From = &fromC
	d := newMessage("outlook-mail", "Outlook Mail", "d1", MessageChannelEmail, "Planning", "", "2024-06-02T14:00:00+00:00")
	fromD := Contact{Name: "Grace L", Emails: []string{"GRACE@example.com"}, Phones: []string{"+1 555 0100"}}
	d.From = &fromD

	merged := MergeMessages([]Message{a, b, c, d})
	if len(merged) != 2 {
		t.Fatalf("merged = %d messages: %+v", len(merged), merged)
	}
	// Newest first: Planning (June 2) then Invoice (June 1).
	if merged[0].Subject != "Planning" || len(merged[0].Sources) != 2 || merged[0].Match != "subject_from_time" {
		t.Fatalf("first = %+v", merged[0])
	}
	if merged[0].From == nil || len(merged[0].From.Emails) != 1 || merged[0].From.Emails[0] != "grace@example.com" {
		t.Fatalf("merged from = %+v", merged[0].From)
	}
	if merged[1].Subject != "Invoice" || len(merged[1].Sources) != 2 || merged[1].Match != "message_id" {
		t.Fatalf("second = %+v", merged[1])
	}
}

func TestMergeMessages_DistinctWhenDifferentIdentity(t *testing.T) {
	a := newMessage("google-gmail", "Gmail", "a", MessageChannelEmail, "Meeting", "", "2024-06-01T09:00:00Z")
	fromA := Contact{Emails: []string{"a@example.com"}}
	a.From = &fromA
	b := newMessage("google-gmail", "Gmail", "b", MessageChannelEmail, "Meeting", "", "2024-06-01T09:00:00Z")
	fromB := Contact{Emails: []string{"b@example.com"}}
	b.From = &fromB
	merged := MergeMessages([]Message{a, b})
	if len(merged) != 2 {
		t.Fatalf("merged = %d, want 2", len(merged))
	}
}
