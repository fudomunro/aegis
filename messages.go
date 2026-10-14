package main

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// messageServiceName returns the first source service name for stable pre-sort.
func messageServiceName(m types.Message) string {
	if len(m.Sources) > 0 {
		return m.Sources[0].ServiceName
	}
	return ""
}

// messageExtraString reads a string Extra field.
func messageExtraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v, ok := extra[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

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
func messagePrimaryFromEmail(m types.Message) string {
	if m.From == nil || len(m.From.Emails) == 0 {
		return ""
	}
	return types.NormalizeContactEmail(m.From.Emails[0])
}

// messageIdentityKey is the sort/merge key used by the library aggregate.
// Prefer durable RFC Message-ID when present; otherwise subject + from + sent_at.
// Messages without any identity fall back to service:id so they stay distinct.
func messageIdentityKey(m types.Message) string {
	if id := types.NormalizeMessageID(m.EntityID); id != "" {
		return "msgid:" + id
	}
	for _, src := range m.Sources {
		if id := types.NormalizeMessageID(messageExtraString(src.Extra, "message_id")); id != "" {
			return "msgid:" + id
		}
	}
	subject := types.NormalizeIdentityKey(m.Subject)
	from := messagePrimaryFromEmail(m)
	sent := messageSentNormalized(m.SentAt)
	if subject == "" || from == "" || sent == "" {
		if len(m.Sources) > 0 && m.Sources[0].Service != "" && m.Sources[0].ID != "" {
			return "src:" + m.Sources[0].Service + ":" + m.Sources[0].ID
		}
		return "anon:" + messageServiceName(m) + ":" + m.Subject + ":" + m.SentAt
	}
	return "subject:" + subject + "|from:" + from + "|sent:" + sent
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

func mergeMessageInto(dst *types.Message, src types.Message) {
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
		merged := MergeContacts([]types.Contact{*dst.From, *src.From})
		if len(merged) > 0 {
			from := merged[0]
			dst.From = &from
		}
	}
	dst.To = mergeMessageContacts(dst.To, src.To)
	dst.CC = mergeMessageContacts(dst.CC, src.CC)
	dst.Labels = unionMessageLabels(dst.Labels, src.Labels)
	dst.Sources = append(dst.Sources, src.Sources...)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

// mergeMessageContacts unions participants using contact identity matching
// (email → phone → handle → name).
func mergeMessageContacts(dst, src []types.Contact) []types.Contact {
	if len(src) == 0 {
		return dst
	}
	if len(dst) == 0 {
		out := make([]types.Contact, len(src))
		copy(out, src)
		return out
	}
	return MergeContacts(append(append([]types.Contact(nil), dst...), src...))
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
func MergeMessages(in []types.Message) []types.Message {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		m types.Message
	}
	var order []string
	byKey := map[string]*slot{}

	for _, msg := range in {
		key := messageIdentityKey(msg)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergeMessageInto(&cur.m, msg)
			continue
		}
		cp := msg
		cp.Sources = append([]types.MessageSource(nil), msg.Sources...)
		if msg.From != nil {
			from := *msg.From
			cp.From = &from
		}
		if len(msg.To) > 0 {
			cp.To = append([]types.Contact(nil), msg.To...)
		}
		if len(msg.CC) > 0 {
			cp.CC = append([]types.Contact(nil), msg.CC...)
		}
		if len(msg.Labels) > 0 {
			cp.Labels = append([]string(nil), msg.Labels...)
		}
		byKey[key] = &slot{m: cp}
		order = append(order, key)
	}

	out := make([]types.Message, 0, len(order))
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
		return messageIdentityKey(out[i]) < messageIdentityKey(out[j])
	})
	return out
}

// newMessage builds a single-source neutral message.
func newMessage(service, serviceName, id, channel, subject, snippet, sentAt string) types.Message {
	src := types.MessageSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}
	return types.Message{
		Channel: channel,
		Subject: subject,
		Snippet: snippet,
		SentAt:  sentAt,
		Sources: []types.MessageSource{src},
	}
}

// libraryArchiveMessageCap bounds archive rows feeding /v1/library/messages.
// Full history stays on /v1/mail/search; the library page is the merged
// collection overview (issue #139).
const libraryArchiveMessageCap = 200

// HandleLibraryMessages returns the user's messages from every connected
// message-capable service and the local mail archive (Gmail sync + Takeout),
// merged into one collection: GET /v1/library/messages
func (s *Server) HandleLibraryMessages(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("messages")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Message]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Message], []string) {
			targets, denied := collectProviders[types.Message, types.MessageProvider](s, kind.PermType,
				func(p types.MessageProvider) func(context.Context) ([]types.Message, bool, error) {
					return p.ListMessages
				})
			// Local archive is another source of the same collection (Gmail
			// archive rows + Google Takeout). Fail-open with live providers if
			// the archive is disabled (issue #139).
			if s.mail != nil && s.mailReadable() {
				archive := s.mail
				targets = append(targets, namedLister[types.Message]{
					id: "mail-archive",
					list: func(context.Context) ([]types.Message, bool, error) {
						return archive.ListMessagesForLibrary(libraryArchiveMessageCap)
					},
				})
			}
			return targets, denied
		},
		serviceOf: messageServiceName,
		sortKey:   messageIdentityKey,
		merge:     MergeMessages,
		encode: func(merged []types.Message, bySvc map[string]types.LibraryServiceStatus) any {
			return types.MessageList{Messages: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampMessageCanonical,
		stampCopies:   s.stampMessageCopies,
	})
}
