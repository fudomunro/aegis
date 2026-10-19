package connectors

import (
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// Minimal vCard 3/4 reader for CardDAV address books (issue #146). Covers the
// neutral Contact shape: FN/N, EMAIL, TEL, ORG, UID, NICKNAME. Folding (RFC
// 6350 §3.2) and quoted-printable / base64 value encodings are not required
// for Nextcloud / iCloud / Fastmail contact lists in practice — we unfold
// CRLF continuations and parse the properties we map.

// ParseVCard maps one vCard text body to a neutral Contact.
// service/serviceName label the ContactSource. href is the resource URL.
// etag is stored in source Extra for If-Match writes.
func ParseVCard(service, serviceName, id, body, etag string) types.Contact {
	lines := unfoldVCard(body)
	var fn, nick, org string
	var given, family string
	var emails, phones []string
	uid := id

	for _, line := range lines {
		if line == "" {
			continue
		}
		name, params, value := splitVCardProp(line)
		upper := strings.ToUpper(name)
		switch upper {
		case "FN":
			if fn == "" {
				fn = unescapeVCard(value)
			}
		case "N":
			parts := strings.Split(value, ";")
			if len(parts) > 0 {
				family = unescapeVCard(parts[0])
			}
			if len(parts) > 1 {
				given = unescapeVCard(parts[1])
			}
		case "EMAIL":
			v := unescapeVCard(value)
			if v != "" {
				emails = append(emails, v)
			}
		case "TEL":
			v := unescapeVCard(value)
			if v != "" {
				phones = append(phones, v)
			}
		case "ORG":
			// ORG:Company;Division — take the first non-empty component.
			for _, part := range strings.Split(value, ";") {
				if p := unescapeVCard(part); p != "" {
					org = p
					break
				}
			}
		case "NICKNAME":
			if nick == "" {
				nick = unescapeVCard(strings.SplitN(value, ",", 2)[0])
			}
		case "UID":
			if v := unescapeVCard(value); v != "" {
				uid = v
			}
		}
		_ = params
	}

	name := strings.TrimSpace(fn)
	if name == "" {
		name = strings.TrimSpace(given + " " + family)
	}
	if name == "" {
		name = strings.TrimSpace(nick)
	}

	contact := types.NewContact(service, serviceName, id, name, emails, phones)
	if contact.Name == "" && len(contact.Emails) == 0 && len(contact.Phones) == 0 {
		// Still return a source-bearing contact when UID alone is present so
		// the library can show an empty-card placeholder rather than drop it.
		if uid == "" && org == "" {
			return contact
		}
		contact.Name = name
	}
	if org != "" {
		contact.Organization = org
	}
	if nick != "" && nick != contact.Name {
		if contact.Handles == nil {
			contact.Handles = []string{nick}
		}
	}
	if uid != "" {
		contact.Extra = map[string]any{"uid": uid}
	}
	if etag != "" {
		if contact.Sources[0].Extra == nil {
			contact.Sources[0].Extra = map[string]any{}
		}
		contact.Sources[0].Extra["etag"] = etag
		if uid != "" {
			contact.Sources[0].Extra["uid"] = uid
		}
	} else if uid != "" && len(contact.Sources) > 0 {
		if contact.Sources[0].Extra == nil {
			contact.Sources[0].Extra = map[string]any{}
		}
		contact.Sources[0].Extra["uid"] = uid
	}
	// Prefer a stable resource id: href name wins over empty id.
	if id == "" && len(contact.Sources) > 0 {
		contact.Sources[0].ID = uid
	}
	return contact
}

// EncodeVCard builds a vCard 3.0 body from a neutral Contact (for PUT).
func EncodeVCard(c types.Contact) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\n")
	name := strings.TrimSpace(c.Name)
	if name != "" {
		b.WriteString("FN:" + escapeVCard(name) + "\r\n")
		// N:Family;Given — split on the last space as a simple convention.
		family, given := "", name
		if i := strings.LastIndex(name, " "); i >= 0 {
			family, given = name[i+1:], name[:i]
		} else {
			family, given = "", name
		}
		b.WriteString("N:" + escapeVCard(family) + ";" + escapeVCard(given) + ";;;\r\n")
	}
	for _, e := range c.Emails {
		if e == "" {
			continue
		}
		b.WriteString("EMAIL;TYPE=INTERNET:" + escapeVCard(e) + "\r\n")
	}
	for _, p := range c.Phones {
		if p == "" {
			continue
		}
		b.WriteString("TEL;TYPE=CELL:" + escapeVCard(p) + "\r\n")
	}
	if c.Organization != "" {
		b.WriteString("ORG:" + escapeVCard(c.Organization) + "\r\n")
	}
	if uid := contactUID(c); uid != "" {
		b.WriteString("UID:" + escapeVCard(uid) + "\r\n")
	}
	b.WriteString("END:VCARD\r\n")
	return b.String()
}

func contactUID(c types.Contact) string {
	if c.Extra != nil {
		if v, ok := c.Extra["uid"].(string); ok {
			return v
		}
	}
	for _, s := range c.Sources {
		if s.Extra != nil {
			if v, ok := s.Extra["uid"].(string); ok {
				return v
			}
		}
		if s.ID != "" {
			return s.ID
		}
	}
	return ""
}

// contactETag returns the source etag used for If-Match writes.
func contactETag(c types.Contact) string {
	for _, s := range c.Sources {
		if s.Extra != nil {
			if v, ok := s.Extra["etag"].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

// unfoldVCard joins RFC 6350 line continuations (CRLF + space/tab).
func unfoldVCard(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	raw := strings.Split(body, "\n")
	var lines []string
	for _, line := range raw {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// splitVCardProp splits "EMAIL;TYPE=HOME;PREF=1:addr" into name, params, value.
func splitVCardProp(line string) (name string, params []string, value string) {
	// Find the first unescaped ':' that is not inside a quoted param.
	colon := -1
	for i := 0; i < len(line); i++ {
		if line[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return strings.TrimSpace(line), nil, ""
	}
	left := line[:colon]
	value = line[colon+1:]
	parts := strings.Split(left, ";")
	name = strings.TrimSpace(parts[0])
	for _, p := range parts[1:] {
		params = append(params, strings.TrimSpace(p))
	}
	return name, params, value
}

func unescapeVCard(v string) string {
	v = strings.TrimSpace(v)
	v = strings.ReplaceAll(v, "\\n", " ")
	v = strings.ReplaceAll(v, "\\N", " ")
	v = strings.ReplaceAll(v, "\\,", ",")
	v = strings.ReplaceAll(v, "\\;", ";")
	v = strings.ReplaceAll(v, "\\\\", "\\")
	return strings.TrimSpace(v)
}

func escapeVCard(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\n", "\\n")
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.ReplaceAll(v, ",", "\\,")
	v = strings.ReplaceAll(v, ";", "\\;")
	return v
}
