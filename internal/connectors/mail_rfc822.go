package connectors

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
)

// RFC 822 → neutral Message mapping shared by the raw-mail ingest sources
// (Google Takeout, IMAP, POP3; issue #420). Header/body/attachment parsing
// is transport-neutral; each source supplies its identity and flags.

// RFC822Identity is the source-specific stamp one ingest applies to a mapped
// message: who is reporting this copy (service, source id, mailbox) and how
// it should be flagged (labels, direction, read).
type RFC822Identity struct {
	Service     string
	ServiceName string
	// ID is the source-local message id (a Takeout blob id, an IMAP UID, a
	// POP3 UIDL).
	ID        string
	Mailbox   string
	Labels    []string
	Direction string
	Read      bool
}

// RFC822Message maps one raw RFC 822 message to a neutral Message, stamping
// the caller's source identity. Attachment metadata rides the source Extra
// (issue #215).
func RFC822Message(raw []byte, id RFC822Identity) (kinds.Message, error) {
	raw = bytes.TrimPrefix(raw, []byte("\n"))
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return kinds.Message{}, err
	}
	return mapRFC822Message(msg, raw, id), nil
}

// mapRFC822Message maps an already-parsed message. Callers that need the
// parse for their own header semantics (Takeout's X-GM-LABELS) use this so
// the message is parsed once.
func mapRFC822Message(msg *mail.Message, raw []byte, id RFC822Identity) kinds.Message {
	decode := func(v string) string {
		if v == "" {
			return ""
		}
		if out, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
			return strings.TrimSpace(out)
		}
		return strings.TrimSpace(v)
	}
	subject := decode(msg.Header.Get("Subject"))
	messageID := kinds.NormalizeMessageID(msg.Header.Get("Message-ID"))
	sentAt := GmailHeaderDateRFC3339(msg.Header.Get("Date"))
	if sentAt == "" {
		sentAt = time.Now().UTC().Format(time.RFC3339)
	}
	fromList := mapAddressHeader(decode(msg.Header.Get("From")), id.Service, id.ServiceName)
	toList := mapAddressHeader(decode(msg.Header.Get("To")), id.Service, id.ServiceName)
	ccList := mapAddressHeader(decode(msg.Header.Get("Cc")), id.Service, id.ServiceName)
	body := extractRFC822TextBody(msg)
	snippet := body
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}

	out := kinds.Message{
		Channel:    kinds.MessageChannelEmail,
		Subject:    subject,
		Snippet:    snippet,
		Body:       body,
		To:         toList,
		CC:         ccList,
		SentAt:     sentAt,
		ReceivedAt: sentAt,
		Direction:  id.Direction,
		Read:       id.Read,
		EntityID:   messageID,
		Labels:     id.Labels,
		Sources: []kinds.MessageSource{{
			Service:     id.Service,
			ServiceName: id.ServiceName,
			ID:          id.ID,
			Mailbox:     id.Mailbox,
		}},
	}
	if len(fromList) > 0 {
		out.From = &fromList[0]
	}
	if atts := RFC822Attachments(raw); len(atts) > 0 {
		out.Sources[0].Extra = map[string]any{"attachments": atts}
	}
	return out
}

// mapAddressHeader parses a single address or address list into Contacts
// stamped with the reporting service.
func mapAddressHeader(raw, service, serviceName string) []kinds.Contact {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		// Fail-open: keep a crude name/email split rather than dropping the party.
		if strings.Contains(raw, "@") {
			c := kinds.NewContact(service, serviceName, "", "", []string{raw}, nil)
			return []kinds.Contact{c}
		}
		c := kinds.NewContact(service, serviceName, "", raw, nil, nil)
		return []kinds.Contact{c}
	}
	out := make([]kinds.Contact, 0, len(addrs))
	for _, a := range addrs {
		if a == nil {
			continue
		}
		var emails []string
		if a.Address != "" {
			emails = []string{a.Address}
		}
		out = append(out, kinds.NewContact(service, serviceName, "", a.Name, emails, nil))
	}
	return out
}

// extractRFC822TextBody prefers text/plain, then text/*, from a parsed message.
func extractRFC822TextBody(msg *mail.Message) string {
	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return readMailAllText(msg.Body)
		}
		mr := multipart.NewReader(msg.Body, boundary)
		var plain, anyText string
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			pct := part.Header.Get("Content-Type")
			if pct == "" {
				pct = "text/plain"
			}
			pmt, _, _ := mime.ParseMediaType(pct)
			body, err := io.ReadAll(part)
			if err != nil {
				continue
			}
			text := string(body)
			if strings.HasPrefix(strings.ToLower(pmt), "text/plain") && plain == "" {
				plain = text
			}
			if strings.HasPrefix(strings.ToLower(pmt), "text/") && anyText == "" {
				anyText = text
			}
		}
		if plain != "" {
			return plain
		}
		return anyText
	}
	return readMailAllText(msg.Body)
}

func readMailAllText(r io.Reader) string {
	data, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	return string(data)
}
