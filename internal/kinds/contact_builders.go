package kinds

import "strings"

// PrimaryContactKey is the first identity key, or "" when the contact has none.
func PrimaryContactKey(c Contact) string {
	keys := ContactIdentityKeys(c)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// newContact builds a unified contact from one service spelling.
func NewContact(service, serviceName, id, name string, emails, phones []string) Contact {
	emails = NormalizeEmailList(emails)
	phones = NormalizePhoneList(phones)
	name = strings.TrimSpace(name)
	if name == "" && len(emails) == 0 && len(phones) == 0 {
		return Contact{}
	}
	src := ContactSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Name:        name,
		Emails:      emails,
		Phones:      phones,
	}
	return Contact{
		Name:    name,
		Emails:  emails,
		Phones:  phones,
		Sources: []ContactSource{src},
	}
}

func NormalizeEmailList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		n := NormalizeContactEmail(e)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func NormalizePhoneList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		n := NormalizeContactPhone(p)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ContactUIDs returns the distinct cross-service vCard UIDs the contact
// carries — the contact-level extra first, then each source's extra. A
// source-local id is not a UID: it names one service's copy of the card,
// not the card itself.
func ContactUIDs(c Contact) []string {
	var out []string
	seen := map[string]bool{}
	add := func(val any) {
		s, _ := val.(string)
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if c.Extra != nil {
		add(c.Extra["uid"])
	}
	for _, src := range c.Sources {
		if src.Extra != nil {
			add(src.Extra["uid"])
		}
	}
	return out
}

// ContactUID is the contact's primary vCard UID — the one an outgoing copy
// should carry — or "" when it has none.
func ContactUID(c Contact) string {
	if uids := ContactUIDs(c); len(uids) > 0 {
		return uids[0]
	}
	return ""
}

// ContactItemKey is the copy-watermark key for one contact (issue #384):
// the contact's durable identity — vCard UID, else email — where it has
// one, so a rename on the canonical home does not orphan the row. A card
// with no durable id keys by its display name, what all rows used before.
func ContactItemKey(c Contact) string {
	for _, k := range ContactIdentityKeys(c) {
		if strings.HasPrefix(k, "uid:") || strings.HasPrefix(k, "email:") {
			return k
		}
	}
	return contactDisplayName(c)
}

// ContactIdentityKeys returns ordered merge keys for a contact: vCard UID
// first (the durable card identity), then email, then phone, then handle.
// Display name is a weak key — it is used only when the contact has no
// email, phone, or handle, so two different "Alex Kim" addresses do not
// merge just because the names match. A vCard UID does not suppress the
// name key: a UID-only card still folds with a name-only card. Empty keys
// are omitted. The first key is the contact's primary identity.
func ContactIdentityKeys(c Contact) []string {
	var keys []string
	seen := map[string]bool{}
	add := func(prefix, val string) {
		if val == "" {
			return
		}
		k := prefix + ":" + val
		if seen[k] {
			return
		}
		seen[k] = true
		keys = append(keys, k)
	}
	for _, uid := range ContactUIDs(c) {
		add("uid", uid)
	}
	uidKeys := len(keys)
	for _, e := range c.Emails {
		add("email", NormalizeContactEmail(e))
	}
	for _, p := range c.Phones {
		add("phone", NormalizeContactPhone(p))
	}
	for _, h := range c.Handles {
		add("handle", NormalizeContactHandle(h))
	}
	if len(keys) == uidKeys {
		if name := NormalizeIdentityKey(c.Name); name != "" {
			add("name", name)
		}
	}
	return keys
}

// RedactCalendarEventFreeBusy strips event content, leaving only the fields
// a scheduling consumer may see (issue #128). Used when the events permission
// is freebusy.
func RedactCalendarEventFreeBusy(e CalendarEvent) CalendarEvent {
	return CalendarEvent{
		StartAt: e.StartAt,
		EndAt:   e.EndAt,
		AllDay:  e.AllDay,
		Status:  e.Status,
	}
}

func RedactCalendarEventsFreeBusy(events []CalendarEvent) []CalendarEvent {
	if len(events) == 0 {
		return events
	}
	out := make([]CalendarEvent, len(events))
	for i, e := range events {
		out[i] = RedactCalendarEventFreeBusy(e)
	}
	return out
}

// NormalizeContactEmail lowercases and trims an address for identity matching.
func NormalizeContactEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormalizeContactPhone reduces a phone number to a comparable digit form.
// A leading "+" is preserved; punctuation and spaces are stripped.
func NormalizeContactPhone(phone string) string {
	s := strings.TrimSpace(phone)
	if s == "" {
		return ""
	}
	plus := strings.HasPrefix(s, "+")
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if digits == "" {
		return ""
	}
	if plus {
		return "+" + digits
	}
	return digits
}

// NormalizeContactHandle lowercases and trims a non-email handle.
func NormalizeContactHandle(handle string) string {
	s := strings.ToLower(strings.TrimSpace(handle))
	s = strings.TrimPrefix(s, "@")
	return s
}
