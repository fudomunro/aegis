package connectors

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
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

	"github.com/fudomunro/aegis/internal/types"
)

// Google Takeout is a read-only ingest source for the local mail archive —
// same role as Gmail live sync, but the source is a frozen export on disk
// (mbox file, directory of .mbox, or Takeout .zip). Aegis never writes
// back to the export; canonical home stays the live provider (issue #16).
// The pair lives beside google-gmail.go so it never splits across a
// package wall (issue #266); design: docs/mail-ingest.md.

const (
	TakeoutServiceID   = "google-takeout"
	TakeoutServiceName = "Google Takeout"
	// TakeoutPathMetaKey stores the configured export path in the sink's
	// durable meta (mail_meta). Exact key is a compatibility contract.
	TakeoutPathMetaKey = "takeout:path"
	// TakeoutCursorMetaKey stores a durable walk cursor so multi-batch
	// imports continue instead of re-reading the export from byte 0
	// (issue #75).
	TakeoutCursorMetaKey = "takeout:cursor"
)

// takeoutTag labels every imported message so a Takeout row is always
// distinguishable from a live-synced one.
const takeoutTag = "TAKEOUT"

// errTakeoutCap ends a walk at the batch cap; the cursor points at the
// unprocessed message to retry.
var errTakeoutCap = fmt.Errorf("takeout import batch cap reached")

// takeoutDefaultBatch / takeoutMaxBatch bound one export walk.
const (
	takeoutDefaultBatch = 5000
	takeoutMaxBatch     = 20000
)

// TakeoutIngest implements MailIngest over a read-only export on disk. No
// Host and no tokens — the export is already local.
type TakeoutIngest struct{}

// NewTakeoutIngest returns the Takeout export ingest.
func NewTakeoutIngest() *TakeoutIngest { return &TakeoutIngest{} }

var _ MailIngest = (*TakeoutIngest)(nil)

// IngestMail walks one bounded batch of the export and stores what is new.
// Options.Path is the admin save-path override (persisted); Options.Query
// is a per-run location (not persisted); otherwise the stored path is used.
// Options.MaxMessages == 0 with an explicit Path is save-path-only (the
// admin UI contract). Repeat until Result.Done.
func (TakeoutIngest) IngestMail(_ context.Context, sink MailSink, opts types.MailIngestOptions) (types.MailIngestResult, error) {
	res := types.MailIngestResult{Service: TakeoutServiceID}
	if opts.ResetCursor {
		_ = sink.MetaSet(TakeoutCursorMetaKey, "")
	}
	// Explicit Path overrides (and persists) the export location; Query is
	// a per-run location carried by queue payloads (not persisted).
	path := strings.TrimSpace(opts.Path)
	persist := path != ""
	if path == "" {
		path = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(opts.Query), "takeout:"))
	}
	if persist {
		if err := setTakeoutPath(sink, path); err != nil {
			res.Error = "failed to set takeout path: " + err.Error()
			return res, fmt.Errorf("%w: %s", types.ErrMailConfig, res.Error)
		}
	}
	if path == "" {
		p, _ := sink.MetaGet(TakeoutPathMetaKey)
		path = strings.TrimSpace(p)
	}
	if path == "" {
		res.Error = "takeout path not configured — set path in this request or via the admin UI"
		return res, fmt.Errorf("%w: takeout path not configured", types.ErrMailConfig)
	}
	res.Query = "takeout:" + path
	// max_messages=0 with an explicit path is save-path-only (admin UI).
	if opts.MaxMessages == 0 && persist {
		res.Done = true
		return res, nil
	}

	max := opts.MaxMessages
	if max <= 0 {
		max = takeoutDefaultBatch
	}
	if max > takeoutMaxBatch {
		max = takeoutMaxBatch
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	cursor := loadTakeoutCursor(sink, abs)
	cursor.Root = abs
	if cursor.Done {
		res.Done = true
		res.Snapshot = &types.MailIngestSnapshot{Path: path, Done: true}
		return res, nil
	}

	var storeErr error
	handle := func(raw []byte, source string) error {
		if res.Stored >= max {
			res.Truncated = true
			return errTakeoutCap
		}
		res.Listed++
		msg, perr := parseTakeoutMessage(raw, source)
		if perr != nil {
			// Fail-open per message: one bad RFC822 must not drop the batch.
			res.Failed++
			return nil
		}
		exists, err := sink.HasMessage(TakeoutServiceID, msg)
		if err != nil {
			storeErr = err
			return err
		}
		if exists {
			res.Skipped++
			return nil
		}
		if err := sink.Store(TakeoutServiceID, msg, raw); err != nil {
			res.Failed++
			return nil
		}
		res.Stored++
		return nil
	}

	walkErr := walkTakeoutFrom(abs, &cursor, handle)
	if walkErr == errTakeoutCap {
		if err := saveTakeoutCursor(sink, cursor); err != nil {
			res.Error = err.Error()
			return res, err
		}
		res.Snapshot = snapshotOf(path, res)
		return res, nil
	}
	if walkErr != nil {
		res.Error = walkErr.Error()
		// Keep progress even when the walk fails mid-file.
		_ = saveTakeoutCursor(sink, cursor)
		return res, walkErr
	}
	if storeErr != nil {
		res.Error = storeErr.Error()
		_ = saveTakeoutCursor(sink, cursor)
		return res, storeErr
	}
	cursor.Done = true
	if err := saveTakeoutCursor(sink, cursor); err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.Done = !res.Truncated
	res.Snapshot = snapshotOf(path, res)
	return res, nil
}

func snapshotOf(path string, res types.MailIngestResult) *types.MailIngestSnapshot {
	return &types.MailIngestSnapshot{
		Path:    path,
		Scanned: res.Listed,
		Stored:  res.Stored,
		Skipped: res.Skipped,
		Done:    res.Done,
	}
}

// setTakeoutPath records the read-only export location. Changing the
// location invalidates a mid-file walk cursor (the admin save-path contract).
func setTakeoutPath(sink MailSink, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if prev, _ := sink.MetaGet(TakeoutPathMetaKey); prev != abs {
		if err := sink.MetaSet(TakeoutCursorMetaKey, ""); err != nil {
			return err
		}
	}
	return sink.MetaSet(TakeoutPathMetaKey, abs)
}

// takeoutCursor is the durable read position for a Takeout export walk.
type takeoutCursor struct {
	Root     string `json:"root"`
	FileIdx  int    `json:"file_idx"`
	EntryIdx int    `json:"entry_idx"`
	Offset   int64  `json:"offset"`
	Done     bool   `json:"done"`
}

func loadTakeoutCursor(sink MailSink, root string) takeoutCursor {
	raw, _ := sink.MetaGet(TakeoutCursorMetaKey)
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

func saveTakeoutCursor(sink MailSink, cur takeoutCursor) error {
	if cur.EntryIdx < 0 {
		cur.EntryIdx = -1
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return sink.MetaSet(TakeoutCursorMetaKey, string(raw))
}

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

// parseTakeoutMessage maps one RFC822 blob from a Takeout export into a
// neutral Message. Missing fields stay empty rather than failing ingest.
func parseTakeoutMessage(raw []byte, source string) (types.Message, error) {
	raw = bytes.TrimPrefix(raw, []byte("\n"))
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return types.Message{}, err
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
	messageID := types.NormalizeMessageID(msg.Header.Get("Message-ID"))
	sentAt := GmailHeaderDateRFC3339(msg.Header.Get("Date"))
	if sentAt == "" {
		sentAt = time.Now().UTC().Format(time.RFC3339)
	}
	fromList := MapGmailAddressHeader(decode(msg.Header.Get("From")))
	toList := MapGmailAddressHeader(decode(msg.Header.Get("To")))
	ccList := MapGmailAddressHeader(decode(msg.Header.Get("Cc")))
	body := extractRFC822TextBody(msg)
	snippet := body
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	labels := TakeoutLabels(msg, source)

	out := types.Message{
		Channel:    types.MessageChannelEmail,
		Subject:    subject,
		Snippet:    snippet,
		Body:       body,
		To:         toList,
		CC:         ccList,
		SentAt:     sentAt,
		ReceivedAt: sentAt,
		Direction:  TakeoutDirection(labels),
		Read:       TakeoutRead(labels),
		EntityID:   messageID,
		Labels:     labels,
		Sources: []types.MessageSource{{
			Service:     TakeoutServiceID,
			ServiceName: TakeoutServiceName,
			ID:          TakeoutMessageID(messageID, raw),
			Mailbox:     TakeoutMailboxFromLabels(labels),
		}},
	}
	if len(fromList) > 0 {
		out.From = &fromList[0]
	}
	return out, nil
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
