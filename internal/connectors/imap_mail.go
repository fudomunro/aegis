package connectors

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/shared"
)

// IMAP is a service-neutral mail transport (issue #420): any server that
// speaks RFC 3501 can feed the local archive, without a provider API. It
// joins the MailIngest seam beside the Gmail live walk, the Google Takeout
// export, and the Microsoft Graph walk (docs/mail-ingest.md). Canonical
// like those live sources — the IMAP mailbox is the user's mail home when
// it is how they read mail (issue #16). Message bytes stay on the server;
// the archive keeps a compressed search copy.

const (
	// ImapMailServiceID is the connector/service ID used in config,
	// sources[], and admin. Documented name is "IMAP Mail".
	ImapMailServiceID   = "imap-mail"
	ImapMailServiceName = "IMAP Mail"

	// imapDefaultPort is the IANA port for IMAP over TLS (RFC 8314).
	imapDefaultPort = "993"

	// imapArchiveDefault / imapArchiveMax bound one ingest run. IMAP
	// FETCHes are per-message, so the default batch is the library window,
	// not a provider page.
	imapArchiveDefault = 200
	imapArchiveMax     = imapBatchMaxMessages

	// imapUIDValidityMetaKey prefixes the durable UIDVALIDITY stamp. A
	// change means the mailbox rebuilt and UIDs restarted — the cursor is
	// worthless and must reset.
	imapUIDValidityMetaKey = "imap:uidvalidity"
)

// ImapMailSource is the overview declaration: a live mailbox home
// (issue #16) — canonical rows, no overview parent.
var ImapMailSource = MailSource{
	ServiceID: ImapMailServiceID,
	Canonical: true,
}

// ImapMailConnector feeds the local mail archive over IMAP and implements
// the MailIngest seam (issue #420). Read-only: FETCH uses BODY.PEEK[] so
// walking the archive never marks messages read on the server.
type ImapMailConnector struct {
	host Host
}

// NewImapMailConnector creates a connector wired to the given Host.
func NewImapMailConnector(host Host) *ImapMailConnector {
	return &ImapMailConnector{host: host}
}

// IngestMail implements MailIngest: one bounded UID walk into the local
// mail archive (docs/mail-ingest.md). The durable cursor is the highest
// processed UID; each call searches forward from it and stores full
// payloads. Callers repeat until Result.Done.
//
// Failure contract (the seam's, docs/mail-ingest.md): a transport error
// stops the walk and leaves the cursor before the missed UID so the next
// run resumes without a gap; a single unparseable message is recorded and
// walked past — one bad item must not drop the batch.
func (c *ImapMailConnector) IngestMail(ctx context.Context, sink MailSink, opts shared.MailIngestOptions) (shared.MailIngestResult, error) {
	res := shared.MailIngestResult{Service: ImapMailServiceID}
	maxMsgs := opts.MaxMessages
	if maxMsgs <= 0 {
		maxMsgs = imapArchiveDefault
	}
	if maxMsgs > imapArchiveMax {
		maxMsgs = imapArchiveMax
	}

	logger := slog.With("service", ImapMailServiceID, "handler", "mail_archive_sync")

	cfg, username, password, err := c.transportConfig()
	if err != nil {
		res.Error = err.Error()
		_ = sink.SaveCursor(ImapMailServiceID, shared.MailIngestCursor{Error: res.Error})
		return res, err
	}
	folder := cfg.Folder
	if folder == "" {
		folder = "INBOX"
	}
	res.Query = "imap:" + folder

	cursor, err := sink.LoadCursor(ImapMailServiceID)
	if err != nil {
		cursor = shared.MailIngestCursor{}
	}
	if opts.ResetCursor {
		cursor = shared.MailIngestCursor{}
	}

	client, err := imapDial(ctx, cfg)
	if err != nil {
		return c.finishMailSync(sink, res, cursor, err)
	}
	defer client.logout()

	if err := client.imapLogin(username, password); err != nil {
		return c.finishMailSync(sink, res, cursor, err)
	}
	uidValidity, err := client.imapSelect(folder)
	if err != nil {
		return c.finishMailSync(sink, res, cursor, err)
	}

	// A UIDVALIDITY change restarts UID numbering — every prior cursor
	// position is meaningless. Reset and let the walk re-scan; HasMessage
	// dedupes the overlap (RFC 3501 §2.3.1.1).
	metaKey := imapUIDValidityMetaKey + ":" + folder
	prevValidity, _ := sink.MetaGet(metaKey)
	if prevValidity != "" && prevValidity != strconv.FormatUint(uint64(uidValidity), 10) {
		logger.Info("imap UIDVALIDITY changed, resetting cursor",
			"folder", folder, "was", prevValidity, "now", uidValidity)
		cursor = shared.MailIngestCursor{}
	}
	if err := sink.MetaSet(metaKey, strconv.FormatUint(uint64(uidValidity), 10)); err != nil {
		logger.Warn("imap UIDVALIDITY stamp failed", "error", err)
	}

	lastUID := parseImapCursorUID(cursor.PageToken)
	var uids []uint32
	if lastUID == 0 {
		uids, err = client.imapUIDSearch("ALL")
	} else {
		// UID n:* is inclusive of the current highest UID; the loop drops
		// anything already processed.
		uids, err = client.imapUIDSearch(fmt.Sprintf("UID %d:*", lastUID+1))
	}
	if err != nil {
		return c.finishMailSync(sink, res, cursor, err)
	}

	// The durable cursor never moves past a UID the run did not reach —
	// including on the early-return paths below, so a retry resumes at the
	// gap rather than skipping over it.
	advance := func(uid uint32) {
		cursor.PageToken = strconv.FormatUint(uint64(uid), 10)
	}
	for _, uid := range uids {
		if uid <= lastUID {
			continue
		}
		// MaxMessages bounds the hydrate step (docs/mail-ingest.md), so the
		// walk stops on fetches — a full-duplicate mailbox still does
		// bounded work per call and resumes from the cursor next time.
		if res.Listed >= maxMsgs {
			res.Truncated = true
			break
		}
		res.Listed++

		msg, err := client.imapUIDFetch(uid)
		if err != nil {
			// Transport failure: stop here and keep the cursor before this
			// UID, so the next run resumes the walk with no gap.
			return c.finishMailSync(sink, res, cursor, err)
		}

		neutral, parseErr := RFC822Message(msg.Raw, RFC822Identity{
			Service:     ImapMailServiceID,
			ServiceName: ImapMailServiceName,
			ID:          imapSourceID(uidValidity, msg.UID),
			Mailbox:     folder,
			Labels:      imapFlagsToLabels(msg.Flags),
			Direction:   imapFolderDirection(folder),
			Read:        imapFlagsRead(msg.Flags),
		})
		if parseErr != nil {
			// Content failure: one bad item must not drop the batch.
			res.Failed++
			logger.Warn("imap message unparseable", "uid", uid, "error", parseErr)
			advance(uid)
			continue
		}
		has, err := sink.HasMessage(ImapMailServiceID, neutral)
		if err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		if has {
			res.Skipped++
			advance(uid)
			continue
		}
		if err := sink.Store(ImapMailServiceID, neutral, msg.Raw); err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		res.Stored++
		if neutral.EntityID != "" {
			res.LastStored = neutral.EntityID
		}
		advance(uid)
	}

	res.Done = !res.Truncated
	return c.finishMailSync(sink, res, cursor, nil)
}

// transportConfig reads the configured endpoint and credentials for this
// source. Missing pieces are a config error the sync queue treats as
// terminal (shared.ErrMailConfig).
func (c *ImapMailConnector) transportConfig() (transportConfig, string, string, error) {
	endpoint := ""
	var creds map[string]string
	if svc := c.host.ServiceConfig(ImapMailServiceID); svc != nil {
		endpoint = svc.Endpoint
		creds = svc.Credentials
	}
	cfg, err := parseTransportEndpoint(endpoint, imapDefaultPort)
	if err != nil {
		return transportConfig{}, "", "", err
	}
	username := strings.TrimSpace(creds[MailCredentialUsername])
	password := creds[MailCredentialPassword]
	if username == "" || password == "" {
		return transportConfig{}, "", "", fmt.Errorf("%w: %s needs credentials.username and credentials.password",
			shared.ErrMailConfig, ImapMailServiceID)
	}
	return cfg, username, password, nil
}

// finishMailSync persists the cursor covering this run's rows (the sink
// commits buffered rows with it) and folds the error into the result.
func (c *ImapMailConnector) finishMailSync(sink MailSink, res shared.MailIngestResult, cursor shared.MailIngestCursor, err error) (shared.MailIngestResult, error) {
	cursor.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	cursor.Done = res.Done
	if err != nil {
		res.Error = err.Error()
		cursor.Error = res.Error
	} else {
		cursor.Error = ""
	}
	if serr := sink.SaveCursor(ImapMailServiceID, cursor); serr != nil {
		slog.Warn("mail archive cursor save failed", "service", ImapMailServiceID, "error", serr)
	}
	return res, err
}

// imapSourceID is the durable source id: UIDVALIDITY-scoped so a rebuilt
// mailbox (UID reset) cannot collide with the rows of the previous numbering.
func imapSourceID(uidValidity, uid uint32) string {
	return fmt.Sprintf("%d:%d", uidValidity, uid)
}

// parseImapCursorUID reads the highest processed UID from the page token.
func parseImapCursorUID(token string) uint32 {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0
	}
	n, err := strconv.ParseUint(token, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// imapFolderDirection is the folder-name heuristic Gmail applies to labels
// (GmailDirectionFromLabels): INBOX is inbound, Sent is outbound, anything
// else stays unknown rather than guessing.
func imapFolderDirection(folder string) string {
	f := strings.ToLower(strings.TrimSpace(folder))
	switch {
	case f == "inbox":
		return "inbound"
	case strings.Contains(f, "sent"):
		return "outbound"
	default:
		return "unknown"
	}
}

// imapFlagsRead reports the \Seen flag.
func imapFlagsRead(flags []string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, "\\Seen") {
			return true
		}
	}
	return false
}

// imapFlagsToLabels maps IMAP system flags (RFC 3501 §2.3.2) to neutral
// message labels. Keywords stay on the server.
func imapFlagsToLabels(flags []string) []string {
	var labels []string
	for _, f := range flags {
		switch strings.ToLower(f) {
		case "\\seen":
			labels = append(labels, "SEEN")
		case "\\answered":
			labels = append(labels, "ANSWERED")
		case "\\flagged":
			labels = append(labels, "FLAGGED")
		case "\\draft":
			labels = append(labels, "DRAFT")
		case "\\deleted":
			labels = append(labels, "DELETED")
		}
	}
	return labels
}
