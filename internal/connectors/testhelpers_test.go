package connectors

// Test-only helpers, moved out of production sources so they stop shipping
// in the binary. They were unreachable from any non-test call site.

import (
	"crypto/sha256"
	"encoding/hex"
)

// SharePhotoSHA256 is the claim hash helper: lowercase hex of the bytes.
func SharePhotoSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// GmailAttachmentPath parses messages/{mid}/attachments/{aid}.
func GmailAttachmentPath(remainingPath string) (messageID, attachmentID string, ok bool) {
	messageID, attachmentID, ok = matchGmailAttachmentRoute(remainingPath)
	if !ok || attachmentID == "" {
		return "", "", false
	}
	if !validGmailBareID(messageID) || !validGmailAttachmentID(attachmentID) {
		return "", "", false
	}
	return messageID, attachmentID, true
}

// GmailAttachmentListPath parses messages/{mid}/attachments (list form).
// The message id is returned even when it is not a Gmail id so the caller can
// 404 with a sources hint instead of generic pass-through.
func GmailAttachmentListPath(remainingPath string) (messageID string, ok bool) {
	messageID, attachmentID, ok := matchGmailAttachmentRoute(remainingPath)
	if !ok || attachmentID != "" {
		return "", false
	}
	return messageID, true
}
