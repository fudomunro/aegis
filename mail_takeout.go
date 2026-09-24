package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Google Takeout is a read-only sync location for the local mail archive —
// same role as Gmail live sync, but the source is a frozen export on disk
// (mbox file, directory of .mbox, or Takeout .zip). Guardian never writes
// back to the export; canonical home stays the live provider (issue #16).

const (
	takeoutServiceID   = "google-takeout"
	takeoutServiceName = "Google Takeout"
	// takeoutPathMetaKey stores the configured export path in mail_meta.
	takeoutPathMetaKey = "takeout:path"
	// takeoutCursorMetaKey stores a durable walk cursor so multi-batch imports
	// continue instead of re-reading the export from byte 0 (issue #75).
	takeoutCursorMetaKey = "takeout:cursor"
)

// takeoutCursor is the durable read position for a Takeout export walk.
type takeoutCursor struct {
	Root     string `json:"root"`
	FileIdx  int    `json:"file_idx"`
	EntryIdx int    `json:"entry_idx"`
	Offset   int64  `json:"offset"`
	Done     bool   `json:"done"`
}

// TakeoutSyncResult reports one bounded walk of the export.
type TakeoutSyncResult struct {
	Service   string `json:"service"`
	Path      string `json:"path"`
	Scanned   int    `json:"scanned"`
	Stored    int    `json:"stored"`
	Skipped   int    `json:"skipped"`
	Failed    int    `json:"failed"`
	Truncated bool   `json:"truncated"`
	Done      bool   `json:"done"`
	Error     string `json:"error,omitempty"`
}

// SetTakeoutPath records the read-only export location (admin/UI or CLI).
func (a *MailArchive) SetTakeoutPath(path string) error {
	if a == nil || a.db == nil {
		return fmt.Errorf("mail archive disabled")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return a.setMeta(takeoutPathMetaKey, "")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Changing the export location invalidates a mid-file cursor.
	if prev := a.TakeoutPath(); prev != abs {
		if err := a.ResetTakeoutCursor(); err != nil {
			return err
		}
	}
	return a.setMeta(takeoutPathMetaKey, abs)
}

// TakeoutPath returns the configured export path ("" when unset).
func (a *MailArchive) TakeoutPath() string {
	if a == nil || a.db == nil {
		return ""
	}
	p, _ := a.getMeta(takeoutPathMetaKey)
	return p
}

// ResetTakeoutCursor restarts the export walk at the first byte.
func (a *MailArchive) ResetTakeoutCursor() error {
	if a == nil || a.db == nil {
		return fmt.Errorf("mail archive disabled")
	}
	return a.setMeta(takeoutCursorMetaKey, "")
}

func (a *MailArchive) loadTakeoutCursor(root string) takeoutCursor {
	raw, _ := a.getMeta(takeoutCursorMetaKey)
	if strings.TrimSpace(raw) == "" {
		return takeoutCursor{EntryIdx: -1}
	}
	var cur takeoutCursor
	if err := json.Unmarshal([]byte(raw), &cur); err != nil {
		return takeoutCursor{EntryIdx: -1}
	}
	if cur.Root != root {
		return takeoutCursor{EntryIdx: -1}
	}
	return cur
}

func (a *MailArchive) saveTakeoutCursor(cur takeoutCursor) error {
	if cur.EntryIdx < 0 {
		cur.EntryIdx = -1
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return a.setMeta(takeoutCursorMetaKey, string(raw))
}

// ImportTakeout walks the read-only export and upserts messages into the
// archive. A durable byte cursor resumes mid-export so repeat calls are O(new
// messages), not a full re-scan (issue #75). maxMessages bounds one call
// (0 = 5000); repeat until Done.
func (a *MailArchive) ImportTakeout(path string, maxMessages int) (TakeoutSyncResult, error) {
	res := TakeoutSyncResult{Service: takeoutServiceID, Path: path}
	if a == nil || a.db == nil {
		res.Error = "mail archive disabled"
		return res, fmt.Errorf("mail archive disabled")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = a.TakeoutPath()
	}
	if path == "" {
		res.Error = "takeout path not configured"
		return res, fmt.Errorf("takeout path not configured")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	res.Path = path
	if maxMessages <= 0 {
		maxMessages = 5000
	}

	cursor := a.loadTakeoutCursor(abs)
	cursor.Root = abs
	if cursor.Done {
		res.Done = true
		return res, nil
	}

	var storeErr error
	handle := func(raw []byte, source string) error {
		if res.Stored >= maxMessages {
			res.Truncated = true
			return errTakeoutCap
		}
		res.Scanned++
		rec, err := recordFromTakeoutMessage(raw, source)
		if err != nil {
			// Fail-open per message: one bad RFC822 must not drop the batch.
			res.Failed++
			return nil
		}
		exists, err := a.hasMailIdentity(rec)
		if err != nil {
			storeErr = err
			return err
		}
		if exists {
			res.Skipped++
			return nil
		}
		if err := a.Upsert(rec); err != nil {
			res.Failed++
			return nil
		}
		res.Stored++
		return nil
	}

	walkErr := walkTakeoutFrom(abs, &cursor, handle)
	if walkErr == errTakeoutCap {
		if err := a.saveTakeoutCursor(cursor); err != nil {
			res.Error = err.Error()
			return res, err
		}
		return res, nil
	}
	if walkErr != nil {
		res.Error = walkErr.Error()
		// Keep progress even when the walk fails mid-file.
		_ = a.saveTakeoutCursor(cursor)
		return res, walkErr
	}
	if storeErr != nil {
		res.Error = storeErr.Error()
		_ = a.saveTakeoutCursor(cursor)
		return res, storeErr
	}
	cursor.Done = true
	if err := a.saveTakeoutCursor(cursor); err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.Done = !res.Truncated
	return res, nil
}

var errTakeoutCap = fmt.Errorf("takeout import batch cap reached")

func (a *MailArchive) hasMailIdentity(rec MailArchiveRecord) (bool, error) {
	if rec.MessageID != "" {
		var n int
		err := a.db.QueryRow(`SELECT COUNT(*) FROM mail_messages WHERE message_id=?`, rec.MessageID).Scan(&n)
		return n > 0, err
	}
	var n int
	err := a.db.QueryRow(`SELECT COUNT(*) FROM mail_messages WHERE service=? AND service_msg_id=?`,
		rec.Service, rec.ID).Scan(&n)
	return n > 0, err
}

// walkTakeoutFrom continues a cursor-aware walk of file/dir/zip.
// On errTakeoutCap, cursor points at the unprocessed message to retry.
func walkTakeoutFrom(root string, cursor *takeoutCursor, handle func(raw []byte, source string) error) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return walkTakeoutFile(root, cursor, handle)
	}
	files, err := listTakeoutFiles(root)
	if err != nil {
		return err
	}
	startFile := cursor.FileIdx
	if startFile < 0 {
		startFile = 0
	}
	for i := startFile; i < len(files); i++ {
		if i != cursor.FileIdx {
			cursor.FileIdx = i
			cursor.Offset = 0
			cursor.EntryIdx = -1
		}
		if err := walkTakeoutFile(files[i], cursor, handle); err != nil {
			return err
		}
		cursor.FileIdx = i + 1
		cursor.Offset = 0
		cursor.EntryIdx = -1
	}
	cursor.Done = true
	return nil
}

func listTakeoutFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".mbox" || ext == ".zip" || looksLikeMboxName(fi.Name()) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func walkTakeoutFile(path string, cursor *takeoutCursor, handle func(raw []byte, source string) error) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".zip" {
		return walkTakeoutZip(path, cursor, handle)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	start := cursor.Offset
	if start < 0 {
		start = 0
	}
	// Per-message callback: advance the durable offset after each handled row.
	return walkMboxFrom(f, start, filepath.Base(path), func(raw []byte, source string, msgStart, msgNext int64) error {
		if err := handle(raw, source); err != nil {
			if err == errTakeoutCap {
				// Retry this message on the next batch.
				cursor.Offset = msgStart
			}
			return err
		}
		cursor.Offset = msgNext
		return nil
	})
}

func looksLikeMboxName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".mbox") || strings.Contains(lower, "all mail")
}

func walkTakeoutZip(path string, cursor *takeoutCursor, handle func(raw []byte, source string) error) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	entryIdx := 0
	startEntry := cursor.EntryIdx
	if startEntry < 0 {
		startEntry = 0
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".mbox") && !strings.Contains(lower, "mail") {
			continue
		}
		idx := entryIdx
		entryIdx++
		if idx < startEntry {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		start := int64(0)
		if idx == cursor.EntryIdx && cursor.Offset > 0 {
			start = cursor.Offset
		}
		cursor.EntryIdx = idx
		// Zip streams are not seekable; discard up to the resume offset.
		if start > 0 {
			if _, err := io.CopyN(io.Discard, rc, start); err != nil && err != io.EOF {
				rc.Close()
				return err
			}
		}
		err = walkMboxFrom(rc, start, name, func(raw []byte, source string, msgStart, msgNext int64) error {
			if err := handle(raw, source); err != nil {
				if err == errTakeoutCap {
					cursor.Offset = msgStart
				}
				return err
			}
			cursor.Offset = msgNext
			return nil
		})
		rc.Close()
		if err != nil {
			return err
		}
		cursor.Offset = 0
		cursor.EntryIdx = idx + 1
	}
	return nil
}

// walkMboxFrom splits an mbox stream on Unix "From " separators, starting at
// byte start (must be a message boundary). The callback receives each raw
// message and the byte range [msgStart, msgNext) it occupied in the stream.
func walkMboxFrom(r io.Reader, start int64, source string, fn func(raw []byte, source string, msgStart, msgNext int64) error) error {
	if start < 0 {
		start = 0
	}
	if rs, ok := r.(io.ReadSeeker); ok && start > 0 {
		if _, err := rs.Seek(start, io.SeekStart); err != nil {
			return err
		}
	}
	br := bufio.NewReaderSize(r, 1<<20)
	pos := start
	var cur bytes.Buffer
	msgStart := start
	inMsg := false

	flush := func(next int64) error {
		if !inMsg {
			return nil
		}
		raw := append([]byte(nil), cur.Bytes()...)
		cur.Reset()
		inMsg = false
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil
		}
		return fn(raw, source, msgStart, next)
	}

	for {
		line, err := br.ReadBytes('\n')
		n := int64(len(line))
		lineStart := pos
		pos += n
		if isMboxFromLine(line) {
			if err2 := flush(lineStart); err2 != nil {
				return err2
			}
			inMsg = true
			msgStart = lineStart
			if err == io.EOF {
				break
			}
			continue
		}
		if inMsg {
			cur.Write(line)
		}
		if err == io.EOF {
			if err2 := flush(pos); err2 != nil {
				return err2
			}
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func isMboxFromLine(line []byte) bool {
	// mbox separators are "From " (with space), not the RFC 5322 "From:" header.
	return bytes.HasPrefix(line, []byte("From "))
}

// recordFromTakeoutMessage maps one RFC822 blob from Takeout into the archive.
func recordFromTakeoutMessage(raw []byte, source string) (MailArchiveRecord, error) {
	raw = bytes.TrimPrefix(raw, []byte("\n"))
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return MailArchiveRecord{}, err
	}
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
	messageID := normalizeMessageID(msg.Header.Get("Message-ID"))
	sentAt := gmailHeaderDateRFC3339(msg.Header.Get("Date"))
	if sentAt == "" {
		sentAt = time.Now().UTC().Format(time.RFC3339)
	}
	fromList := mapGmailAddressHeader(decode(msg.Header.Get("From")))
	toList := mapGmailAddressHeader(decode(msg.Header.Get("To")))
	ccList := mapGmailAddressHeader(decode(msg.Header.Get("Cc")))
	body := extractRFC822TextBody(msg)
	snippet := body
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	labels := takeoutLabels(msg, source)

	out := MailArchiveRecord{
		Service:     takeoutServiceID,
		ServiceName: takeoutServiceName,
		ID:          takeoutMessageID(messageID, raw),
		MessageID:   messageID,
		Mailbox:     takeoutMailboxFromLabels(labels),
		Labels:      labels,
		Subject:     subject,
		Snippet:     snippet,
		To:          toList,
		CC:          ccList,
		SentAt:      sentAt,
		ReceivedAt:  sentAt,
		Direction:   takeoutDirection(labels),
		Read:        !hasLabel(labels, "UNREAD"),
		BodyText:    body,
	}
	if len(fromList) > 0 {
		out.FromName = fromList[0].Name
		if len(fromList[0].Emails) > 0 {
			out.FromEmail = fromList[0].Emails[0]
		}
	}
	if gz, err := GzipPayload(raw); err == nil {
		out.PayloadGzip = gz
	}
	return out, nil
}

func takeoutMessageID(messageID string, raw []byte) string {
	if messageID != "" {
		return "msgid:" + messageID
	}
	sum := sha256.Sum256(raw)
	return "blob:" + hex.EncodeToString(sum[:8])
}

func takeoutLabels(msg *mail.Message, source string) []string {
	var labels []string
	// Gmail Takeout often preserves X-GM-LABELS.
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
	labels = append(labels, "TAKEOUT")
	return labels
}

func takeoutMailboxFromLabels(labels []string) string {
	for _, l := range labels {
		switch strings.ToUpper(l) {
		case "INBOX", "SENT", "DRAFT", "SPAM", "TRASH", "STARRED", "IMPORTANT":
			return strings.ToUpper(l)
		}
	}
	return "TAKEOUT"
}

func takeoutDirection(labels []string) string {
	if hasLabel(labels, "SENT") {
		return "outbound"
	}
	if hasLabel(labels, "INBOX") {
		return "inbound"
	}
	return "unknown"
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
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
