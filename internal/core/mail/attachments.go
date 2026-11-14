package mail

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
)

// Attachment documents over archived payloads (issue #215): email
// attachments of read-only export sources become documents the library
// lists and fetches. Canonical sources (the live mailbox) serve their own
// attachment surface; the archive covers the export-only gap.

// attachmentBackfillBatch bounds one listing's lazy examination of legacy
// rows — payloads stored before attachment summaries existed.
const attachmentBackfillBatch = 200

// AttachmentQuery is the archive-side row selection for the documents
// kind (issue #215). The kind's own list filters re-filter the merged
// result; these bounds only shape the row window.
type AttachmentQuery struct {
	Dataset string
	Q       string
	After   string
	Before  string
	// Cap bounds scanned rows (newest first). 0 uses the store window.
	Cap int
}

// AttachmentDocuments lists one email-attachment document per attachment of
// archived export-source rows. baseURL anchors each copy's blob_url at the
// library byte route. truncated reports a filled row window.
func (a *Archive) AttachmentDocuments(baseURL string, q AttachmentQuery) ([]kinds.Document, bool, error) {
	if a == nil || a.db == nil {
		return nil, false, fmt.Errorf("mail archive disabled")
	}
	a.backfillAttachmentSummaries()

	limit := q.Cap
	if limit <= 0 {
		limit = mailSearchMergeCap
	}
	where := []string{`attachments_json IS NOT NULL`, `attachments_json <> '[]'`}
	args := []any{}
	if ds := strings.TrimSpace(q.Dataset); ds != "" {
		where = append(where, "dataset = ?")
		args = append(args, ds)
	}
	// Canonical sources provide their own attachment surface (issue #215);
	// undeclared sources are non-canonical and belong to this listing.
	if canonical := canonicalMailServices(); len(canonical) > 0 {
		marks := make([]string, 0, len(canonical))
		for _, svc := range canonical {
			marks = append(marks, "?")
			args = append(args, svc)
		}
		where = append(where, "service NOT IN ("+strings.Join(marks, ",")+")")
	}
	if q.After != "" {
		where = append(where, "sent_at >= ?")
		args = append(args, normalizeMailTimeBound(q.After, true))
	}
	if q.Before != "" {
		where = append(where, "sent_at < ?")
		args = append(args, normalizeMailTimeBound(q.Before, false))
	}
	if term := strings.TrimSpace(q.Q); term != "" {
		like := "%" + strings.ToLower(term) + "%"
		where = append(where,
			"(LOWER(subject) LIKE ? OR LOWER(from_name) LIKE ? OR LOWER(from_email) LIKE ? "+
				"OR LOWER(snippet) LIKE ? OR LOWER(attachments_json) LIKE ?)")
		args = append(args, like, like, like, like, like)
	}
	args = append(args, limit)

	rows, err := a.db.Query(`
SELECT service, service_msg_id, thread_id, message_id, mailbox, labels_json,
       subject, snippet, from_name, from_email, to_json, cc_json,
       sent_at, received_at, direction, is_read, body_text, dataset,
       attachments_json
FROM mail_messages WHERE `+strings.Join(where, " AND ")+`
ORDER BY CASE WHEN sent_at = '' THEN 1 ELSE 0 END, sent_at DESC, id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := []kinds.Document{}
	truncated := false
	count := 0
	for rows.Next() {
		rec, err := scanMailRows(rows, false)
		if err != nil {
			return nil, false, err
		}
		count++
		if count >= limit {
			truncated = true
		}
		msg := rec.Message()
		for _, att := range rec.Attachments {
			docID := ArchiveAttachmentDocID(rec.ID, att.ID)
			out = append(out, kinds.EmailAttachmentDocument(msg, att, kinds.DocumentSource{
				Service:     rec.Service,
				ServiceName: rec.ServiceName,
				ID:          docID,
				Canonical:   connectors.MailSourceFor(rec.Service).Canonical,
				BlobURL:     kinds.DocumentBytesURL(baseURL, rec.Service, docID),
			}))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}

// AttachmentText returns the decoded text of one archived attachment (empty
// for binary parts). ok=false when the named copy is missing or outside
// the dataset partition.
func (a *Archive) AttachmentText(dataset, service, id string) (string, bool) {
	rowID, partID, ok := SplitArchiveAttachmentDocID(id)
	if !ok {
		return "", false
	}
	payload, ok := a.attachmentPayload(dataset, service, rowID)
	if !ok {
		return "", false
	}
	text, found := connectors.RFC822AttachmentText(payload, partID)
	return text, found
}

// AttachmentBytes returns one archived attachment's decoded bytes with its
// media type and filename. ok=false when the named copy is missing or
// outside the dataset partition.
func (a *Archive) AttachmentBytes(dataset, service, id string) ([]byte, string, string, bool) {
	rowID, partID, ok := SplitArchiveAttachmentDocID(id)
	if !ok {
		return nil, "", "", false
	}
	payload, ok := a.attachmentPayload(dataset, service, rowID)
	if !ok {
		return nil, "", "", false
	}
	data, mimeType, filename, ok := connectors.RFC822AttachmentPart(payload, partID)
	return data, mimeType, filename, ok
}

// attachmentPayload fetches one row's raw payload under the dataset
// partition (docs/data-scopes.md).
func (a *Archive) attachmentPayload(dataset, service, rowID string) ([]byte, bool) {
	if a == nil || a.db == nil {
		return nil, false
	}
	rec, err := a.Get(service, rowID)
	if err != nil {
		return nil, false
	}
	// Data-scope isolation: a partitioned principal cannot fetch rows from
	// another dataset even with a direct id.
	if dataset != "" && rec.Dataset != dataset {
		return nil, false
	}
	payload, err := rec.Payload()
	if err != nil {
		return nil, false
	}
	return payload, true
}

// backfillAttachmentSummaries examines legacy rows — payloads stored before
// attachment summaries existed — in one bounded batch per call. Rows are
// stamped '[]' ("examined, none") so they are never retried; the pass is
// fail-open and self-heals across listings.
func (a *Archive) backfillAttachmentSummaries() {
	rows, err := a.db.Query(`
SELECT service, service_msg_id, payload_gzip FROM mail_messages
WHERE attachments_json IS NULL AND payload_gzip IS NOT NULL
LIMIT ?`, attachmentBackfillBatch)
	if err != nil {
		return
	}
	type pending struct {
		service, id, summary string
	}
	var batch []pending
	for rows.Next() {
		var service, id string
		var gz []byte
		if err := rows.Scan(&service, &id, &gz); err != nil {
			rows.Close()
			return
		}
		rec := Record{PayloadGzip: gz}
		payload, err := rec.Payload()
		if err != nil {
			// Undecodable payload: stamp examined-none so it is not retried.
			batch = append(batch, pending{service, id, "[]"})
			continue
		}
		atts := connectors.RFC822Attachments(payload)
		if atts == nil {
			atts = []kinds.AttachmentRef{}
		}
		summary, err := json.Marshal(atts)
		if err != nil {
			continue
		}
		batch = append(batch, pending{service, id, string(summary)})
	}
	rows.Close()
	for _, p := range batch {
		_, _ = a.db.Exec(`UPDATE mail_messages SET attachments_json=? WHERE service=? AND service_msg_id=?`,
			p.summary, p.service, p.id)
	}
}

// canonicalMailServices lists ingest sources that claim mailbox home — the
// sources whose attachments this listing does not cover.
func canonicalMailServices() []string {
	var out []string
	for _, src := range connectors.MailSources() {
		if src.Canonical {
			out = append(out, src.ServiceID)
		}
	}
	return out
}

// ArchiveAttachmentDocID is the archive copy's service-local document id:
// the row's message id plus the MIME part path.
func ArchiveAttachmentDocID(rowID, partID string) string {
	return rowID + "~" + partID
}

// SplitArchiveAttachmentDocID splits an archive document id on the last
// "~" (MIME part paths never contain one).
func SplitArchiveAttachmentDocID(id string) (rowID, partID string, ok bool) {
	i := strings.LastIndex(id, "~")
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}
