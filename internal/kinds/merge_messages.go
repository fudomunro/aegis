package kinds

import (
	"sort"
	"strings"
	"time"
)

// messageSentNormalized parses RFC3339 (or RFC3339Nano) and re-emits it at
// second precision in UTC so the same send time matches across services.
func messageSentNormalized(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format(time.RFC3339)
}

// messagePrimaryFromEmail returns the first normalized sender address.
func messagePrimaryFromEmail(m Message) string {
	if m.From == nil || len(m.From.Emails) == 0 {
		return ""
	}
	return NormalizeContactEmail(m.From.Emails[0])
}

// MessageIdentityKey is the sort/merge key used by the library aggregate.
// Prefer durable RFC Message-ID when present; otherwise subject + from + sent_at.
// Messages without any identity fall back to service:id so they stay distinct.
func MessageIdentityKey(m Message) string {
	if id := NormalizeMessageID(m.EntityID); id != "" {
		return "msgid:" + id
	}
	for _, src := range m.Sources {
		if id := NormalizeMessageID(ExtraString(src.Extra, "message_id")); id != "" {
			return "msgid:" + id
		}
	}
	subject := NormalizeIdentityKey(m.Subject)
	from := messagePrimaryFromEmail(m)
	sent := messageSentNormalized(m.SentAt)
	if subject == "" || from == "" || sent == "" {
		if len(m.Sources) > 0 && m.Sources[0].Service != "" && m.Sources[0].ID != "" {
			return "src:" + m.Sources[0].Service + ":" + m.Sources[0].ID
		}
		return "anon:" + MessageServiceName(m) + ":" + m.Subject + ":" + m.SentAt
	}
	return "subject:" + subject + "|from:" + from + "|sent:" + sent
}

// MessageServiceName returns the display name of the first source, or "".
func MessageServiceName(m Message) string {
	if len(m.Sources) > 0 {
		return m.Sources[0].ServiceName
	}
	return ""
}

// messageMatchKind records how identity was established for multi-source merges.
func messageMatchKind(key string) string {
	switch {
	case strings.HasPrefix(key, "msgid:"):
		return "message_id"
	case strings.HasPrefix(key, "subject:"):
		return "subject_from_time"
	default:
		return ""
	}
}

func mergeMessageInto(dst *Message, src Message) {
	if dst.Channel == "" {
		dst.Channel = src.Channel
	}
	if dst.Subject == "" {
		dst.Subject = src.Subject
	}
	if dst.Snippet == "" {
		dst.Snippet = src.Snippet
	}
	if dst.Body == "" {
		dst.Body = src.Body
	}
	if dst.SentAt == "" {
		dst.SentAt = src.SentAt
	}
	if dst.ReceivedAt == "" {
		dst.ReceivedAt = src.ReceivedAt
	}
	if dst.Direction == "" {
		dst.Direction = src.Direction
	}
	if !dst.Read {
		dst.Read = src.Read
	}
	if dst.ThreadID == "" {
		dst.ThreadID = src.ThreadID
	}
	if dst.EntityID == "" {
		dst.EntityID = src.EntityID
	}
	if dst.From == nil && src.From != nil {
		from := *src.From
		dst.From = &from
	} else if dst.From != nil && src.From != nil {
		merged := MergeContacts([]Contact{*dst.From, *src.From})
		if len(merged) > 0 {
			from := merged[0]
			dst.From = &from
		}
	}
	dst.To = MergeMessageContacts(dst.To, src.To)
	dst.CC = MergeMessageContacts(dst.CC, src.CC)
	dst.Labels = unionMessageLabels(dst.Labels, src.Labels)
	dst.Sources = append(dst.Sources, src.Sources...)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

// MergeMessageContacts unions participants using contact identity matching
// (email → phone → handle → name).
func MergeMessageContacts(dst, src []Contact) []Contact {
	if len(src) == 0 {
		return dst
	}
	if len(dst) == 0 {
		out := make([]Contact, len(src))
		copy(out, src)
		return out
	}
	return MergeContacts(append(append([]Contact(nil), dst...), src...))
}

func unionMessageLabels(dst, src []string) []string {
	if len(src) == 0 {
		return dst
	}
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{dst, src} {
		for _, l := range list {
			l = strings.TrimSpace(l)
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// MergeMessages groups messages that share a durable identity (RFC Message-ID)
// or the same subject + from + sent_at into one user-facing entry with
// multiple Sources. Messages without an identity stay distinct via service:id.
func MergeMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		m Message
	}
	var order []string
	byKey := map[string]*slot{}

	for _, msg := range in {
		key := MessageIdentityKey(msg)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergeMessageInto(&cur.m, msg)
			continue
		}
		cp := msg
		cp.Sources = append([]MessageSource(nil), msg.Sources...)
		if msg.From != nil {
			from := *msg.From
			cp.From = &from
		}
		if len(msg.To) > 0 {
			cp.To = append([]Contact(nil), msg.To...)
		}
		if len(msg.CC) > 0 {
			cp.CC = append([]Contact(nil), msg.CC...)
		}
		if len(msg.Labels) > 0 {
			cp.Labels = append([]string(nil), msg.Labels...)
		}
		byKey[key] = &slot{m: cp}
		order = append(order, key)
	}

	out := make([]Message, 0, len(order))
	for _, key := range order {
		item := byKey[key]
		if len(item.m.Sources) > 1 {
			if mk := messageMatchKind(key); mk != "" {
				item.m.Match = mk
			}
		}
		sort.SliceStable(item.m.Sources, func(i, j int) bool {
			if item.m.Sources[i].Canonical != item.m.Sources[j].Canonical {
				return item.m.Sources[i].Canonical
			}
			if item.m.Sources[i].ServiceName != item.m.Sources[j].ServiceName {
				return item.m.Sources[i].ServiceName < item.m.Sources[j].ServiceName
			}
			return item.m.Sources[i].Service < item.m.Sources[j].Service
		})
		out = append(out, item.m)
	}

	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].SentAt, out[j].SentAt
		if si != sj {
			return si > sj // newest first
		}
		si2, sj2 := out[i].Subject, out[j].Subject
		if si2 != sj2 {
			return si2 < sj2
		}
		return MessageIdentityKey(out[i]) < MessageIdentityKey(out[j])
	})
	return out
}
