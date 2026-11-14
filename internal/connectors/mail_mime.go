package connectors

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
)

// RFC822 MIME attachment extraction for archived mail payloads (issue #215).
// The walk is generic mail transport, not provider knowledge — like the
// mbox walk it lives beside the Takeout ingest (docs/mail-ingest.md) and is
// exported for the archive's read path. Everything is fail-open: an
// unreadable part is skipped, never fatal.

// headerGet is the shared surface of mail.Header and multipart.Part headers.
type headerGet interface{ Get(key string) string }

// RFC822Attachments walks one raw RFC822 message and summarizes each
// attachment part: files, named inline parts, and non-text blobs. ID is the
// MIME part path ("1", "2.1", …) used to fetch the bytes later. Nested
// multiparts are included.
func RFC822Attachments(raw []byte) []kinds.AttachmentRef {
	var out []kinds.AttachmentRef
	walkRFC822PartsOf(raw, "", func(id string, h headerGet, data []byte, mediaType, filename string) bool {
		out = append(out, kinds.AttachmentRef{
			ID:       id,
			Filename: filename,
			MimeType: mediaType,
			Size:     len(data),
		})
		return true
	})
	return out
}

// RFC822AttachmentPart extracts one attachment's decoded bytes by MIME part
// path (the ID RFC822Attachments assigns).
func RFC822AttachmentPart(raw []byte, partID string) (data []byte, mimeType, filename string, ok bool) {
	if partID == "" {
		return nil, "", "", false
	}
	walkRFC822PartsOf(raw, partID, func(id string, h headerGet, part []byte, mediaType, fname string) bool {
		data, mimeType, filename, ok = part, mediaType, fname, true
		return false
	})
	return data, mimeType, filename, ok
}

// RFC822AttachmentText returns the decoded body of one attachment part.
// found reports whether the part exists; binary parts return "" (no OCR),
// matching the live Gmail text surface.
func RFC822AttachmentText(raw []byte, partID string) (text string, found bool) {
	if partID == "" {
		return "", false
	}
	walkRFC822PartsOf(raw, partID, func(id string, h headerGet, part []byte, mediaType, fname string) bool {
		found = true
		if strings.HasPrefix(mediaType, "text/") {
			text = string(part)
		}
		return false
	})
	return text, found
}

// walkRFC822PartsOf visits the attachment parts of one raw RFC822 message.
// An empty want visits every attachment; a non-empty want visits only the
// part with that id. Visit returns false to stop the walk.
func walkRFC822PartsOf(raw []byte, want string, visit func(id string, h headerGet, data []byte, mediaType, filename string) bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return
	}
	walkRFC822Entity(msg.Header, msg.Body, "", want, visit)
}

// walkRFC822Entity recursively visits the attachment parts under one MIME
// entity. prefix is the part path ("" at the message root).
func walkRFC822Entity(h headerGet, body io.Reader, prefix, want string, visit func(id string, h headerGet, data []byte, mediaType, filename string) bool) bool {
	mediaType, params, err := mime.ParseMediaType(headerContentType(h))
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return true
		}
		mr := multipart.NewReader(body, boundary)
		for i := 1; ; i++ {
			part, err := mr.NextPart()
			if err != nil {
				return true
			}
			id := strconv.Itoa(i)
			if prefix != "" {
				id = prefix + "." + id
			}
			if !walkRFC822Entity(part.Header, part, id, want, visit) {
				return false
			}
		}
	}
	// A non-multipart message root is the message body, not an attachment.
	if prefix == "" || (want != "" && want != prefix) {
		return true
	}
	if !isRFC822Attachment(h, mediaType) {
		return true
	}
	data, ok := readDecodedRFC822Part(h, body)
	if !ok {
		return true
	}
	return visit(prefix, h, data, strings.ToLower(strings.TrimSpace(mediaType)), mimePartFilename(h))
}

func headerContentType(h headerGet) string {
	ct := h.Get("Content-Type")
	if ct == "" {
		return "text/plain"
	}
	return ct
}

// isRFC822Attachment reports whether a leaf MIME part is an attachment:
// declared as such, carries a filename, or is a non-text blob.
func isRFC822Attachment(h headerGet, mediaType string) bool {
	if d, _, err := mime.ParseMediaType(h.Get("Content-Disposition")); err == nil &&
		strings.EqualFold(strings.TrimSpace(d), "attachment") {
		return true
	}
	if mimePartFilename(h) != "" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(mediaType))
	return !strings.HasPrefix(lower, "text/") && !strings.HasPrefix(lower, "multipart/")
}

// mimePartFilename reads the filename/name parameters of a part's
// Content-Disposition and Content-Type headers.
func mimePartFilename(h headerGet) string {
	for _, key := range []string{"Content-Disposition", "Content-Type"} {
		if _, params, err := mime.ParseMediaType(h.Get(key)); err == nil {
			if fn := strings.TrimSpace(params["filename"]); fn != "" {
				return fn
			}
			if fn := strings.TrimSpace(params["name"]); fn != "" {
				return fn
			}
		}
	}
	return ""
}

// readDecodedRFC822Part returns the part body with Content-Transfer-Encoding
// applied. mime/multipart already decodes quoted-printable; base64 is ours.
func readDecodedRFC822Part(h headerGet, body io.Reader) ([]byte, bool) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, false
	}
	cte := strings.ToLower(strings.TrimSpace(h.Get("Content-Transfer-Encoding")))
	if strings.HasPrefix(cte, "base64") {
		decoded, ok := decodeMIMEBase64(data)
		if !ok {
			return nil, false
		}
		return decoded, true
	}
	return data, true
}

func decodeMIMEBase64(data []byte) ([]byte, bool) {
	clean := make([]byte, 0, len(data))
	for _, b := range data {
		if b == '\r' || b == '\n' || b == ' ' || b == '\t' {
			continue
		}
		clean = append(clean, b)
	}
	if out, err := base64.StdEncoding.DecodeString(string(clean)); err == nil {
		return out, true
	}
	if out, err := base64.RawStdEncoding.DecodeString(string(clean)); err == nil {
		return out, true
	}
	return nil, false
}
