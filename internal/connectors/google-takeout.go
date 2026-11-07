package connectors

import (
	"crypto/sha256"
	"encoding/hex"
	"net/mail"
	"path/filepath"
	"strings"
)

// Google Takeout label and id semantics (issue #286): provider knowledge for
// the read-only export that pairs with Gmail live sync (docs/mail-ingest.md).
// The export walk itself still lives in core; this file owns what the walk
// must not decide — label mapping, mailbox/direction/read, and the archive
// id scheme. Kept beside google-gmail.go so the pair never splits across a
// package wall (issue #266).

const (
	TakeoutServiceID   = "google-takeout"
	TakeoutServiceName = "Google Takeout"
)

// takeoutTag labels every imported message so a Takeout row is always
// distinguishable from a live-synced one.
const takeoutTag = "TAKEOUT"

// TakeoutMessageID is the archive id scheme for a Takeout row: the RFC 5322
// Message-ID when present, else a prefix of the content hash. Prefixed so ids
// cannot collide with live Gmail ids.
func TakeoutMessageID(messageID string, raw []byte) string {
	if messageID != "" {
		return "msgid:" + messageID
	}
	sum := sha256.Sum256(raw)
	return "blob:" + hex.EncodeToString(sum[:8])
}

// TakeoutLabels maps one export message to its label set: Gmail Takeout often
// preserves X-GM-LABELS; without them the source file's base name stands in.
// Every row also carries the TAKEOUT tag.
func TakeoutLabels(msg *mail.Message, source string) []string {
	var labels []string
	if raw := msg.Header.Get("X-GM-LABELS"); raw != "" {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ' ' || r == ','
		}) {
			part = strings.Trim(part, `"`)
			if part != "" {
				labels = append(labels, strings.ToUpper(part))
			}
		}
	}
	if len(labels) == 0 {
		base := strings.ToUpper(strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)))
		base = strings.ReplaceAll(base, " ", "_")
		if base != "" {
			labels = append(labels, base)
		}
	}
	labels = append(labels, takeoutTag)
	return labels
}

// TakeoutMailboxFromLabels picks the mailbox from the Gmail system labels;
// rows without one land in the TAKEOUT mailbox.
func TakeoutMailboxFromLabels(labels []string) string {
	for _, l := range labels {
		switch strings.ToUpper(l) {
		case "INBOX", "SENT", "DRAFT", "SPAM", "TRASH", "STARRED", "IMPORTANT":
			return strings.ToUpper(l)
		}
	}
	return takeoutTag
}

// TakeoutDirection maps labels to the neutral Message direction.
func TakeoutDirection(labels []string) string {
	if hasTakeoutLabel(labels, "SENT") {
		return "outbound"
	}
	if hasTakeoutLabel(labels, "INBOX") {
		return "inbound"
	}
	return "unknown"
}

// TakeoutRead reports whether the message is not marked unread.
func TakeoutRead(labels []string) bool { return !hasTakeoutLabel(labels, "UNREAD") }

func hasTakeoutLabel(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}
