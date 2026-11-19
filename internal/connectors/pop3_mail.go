package connectors

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// POP3 is a service-neutral mail transport (issue #420): any server that
// speaks RFC 1939 can feed the local archive, without a provider API. It
// joins the MailIngest seam beside IMAP and the provider walks
// (docs/mail-ingest.md). Canonical like those live sources — a POP3 mailbox
// is where delivered mail lands (issue #16). The walk is read-only (no
// DELE): mail stays on the server, and the archive keeps a compressed
// search copy.
//
// POP3 carries no flags and no folders: every message is recorded as read
// (nothing marks it unread) in the single "POP3" mailbox. Messages are
// addressed by their UIDL — RFC 1939's stable unique id — so renumbering
// after a server-side delete cannot duplicate or drop rows.

const (
	// Pop3MailServiceID is the connector/service ID used in config,
	// sources[], and admin. Documented name is "POP3 Mail".
	Pop3MailServiceID   = "pop3-mail"
	Pop3MailServiceName = "POP3 Mail"

	// pop3DefaultPort is the IANA port for POP3 over TLS (RFC 8314).
	pop3DefaultPort = "995"

	// pop3ArchiveDefault / pop3ArchiveMax bound one ingest run.
	pop3ArchiveDefault = 200
	pop3ArchiveMax     = imapBatchMaxMessages

	// pop3MailboxLabel is the single mailbox every POP3 row reports — the
	// protocol has no folders.
	pop3MailboxLabel = "POP3"
)

// Pop3MailSource is the overview declaration: a live mailbox home
// (issue #16) — canonical rows, no overview parent.
var Pop3MailSource = MailSource{
	ServiceID: Pop3MailServiceID,
	Canonical: true,
}

// Pop3MailConnector feeds the local mail archive over POP3 and implements
// the MailIngest seam (issue #420).
type Pop3MailConnector struct {
	host Host
}

// NewPop3MailConnector creates a connector wired to the given Host.
func NewPop3MailConnector(host Host) *Pop3MailConnector {
	return &Pop3MailConnector{host: host}
}

// IngestMail implements MailIngest: one bounded mailbox walk into the local
// mail archive (docs/mail-ingest.md). The durable cursor is the last
// processed UIDL — a resume hint, not a safety boundary: a missing or
// reordered hint falls back to walking from the top, and HasMessage dedupes
// the overlap. Callers repeat until Result.Done.
//
// Failure contract (the seam's, docs/mail-ingest.md): a transport error
// stops the walk and leaves the cursor before the missed message so the
// next run resumes without a gap; a single unparseable message is recorded
// and walked past — one bad item must not drop the batch.
func (c *Pop3MailConnector) IngestMail(ctx context.Context, sink MailSink, opts shared.MailIngestOptions) (shared.MailIngestResult, error) {
	res := shared.MailIngestResult{Service: Pop3MailServiceID}
	maxMsgs := opts.MaxMessages
	if maxMsgs <= 0 {
		maxMsgs = pop3ArchiveDefault
	}
	if maxMsgs > pop3ArchiveMax {
		maxMsgs = pop3ArchiveMax
	}
	res.Query = "pop3"

	logger := slog.With("service", Pop3MailServiceID, "handler", "mail_archive_sync")

	cfg, username, password, err := c.transportConfig()
	if err != nil {
		res.Error = err.Error()
		_ = sink.SaveCursor(Pop3MailServiceID, shared.MailIngestCursor{Error: res.Error})
		return res, err
	}

	cursor, err := sink.LoadCursor(Pop3MailServiceID)
	if err != nil {
		cursor = shared.MailIngestCursor{}
	}
	if opts.ResetCursor {
		cursor = shared.MailIngestCursor{}
	}

	client, err := pop3Dial(ctx, cfg)
	if err != nil {
		return c.finishMailSync(sink, res, cursor, err)
	}
	defer client.pop3Quit()

	if err := client.pop3Login(username, password); err != nil {
		return c.finishMailSync(sink, res, cursor, err)
	}

	entries, err := client.pop3UIDL()
	if err != nil {
		return c.finishMailSync(sink, res, cursor, err)
	}

	// Resume after the cursor's UIDL when the server still lists it in the
	// same relative order. A deleted or missing hint restarts from the top;
	// the HasMessage probes below absorb the overlap without re-downloading.
	start := 0
	if after := strings.TrimSpace(cursor.PageToken); after != "" {
		for i, e := range entries {
			if e.UIDL == after {
				start = i + 1
				break
			}
		}
	}

	// The durable cursor never moves past a message the run did not reach —
	// including on the early-return paths below, so a retry resumes at the
	// gap rather than skipping over it.
	advance := func(uidl string) {
		cursor.PageToken = uidl
	}
	for _, entry := range entries[start:] {
		// MaxMessages bounds the hydrate step (docs/mail-ingest.md), so the
		// walk stops on fetches — a full-duplicate mailbox still does
		// bounded work per call and resumes from the cursor next time.
		if res.Listed >= maxMsgs {
			res.Truncated = true
			break
		}
		res.Listed++

		// Probe before downloading: the archive already holds this UIDL,
		// the RETR is wasted bandwidth.
		probe := kinds.Message{Sources: []kinds.MessageSource{{
			Service: Pop3MailServiceID,
			ID:      entry.UIDL,
		}}}
		has, err := sink.HasMessage(Pop3MailServiceID, probe)
		if err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		if has {
			res.Skipped++
			advance(entry.UIDL)
			continue
		}

		raw, err := client.pop3Retr(entry.Num)
		if err != nil {
			// Transport failure: stop here and keep the cursor before this
			// message, so the next run resumes the walk with no gap.
			return c.finishMailSync(sink, res, cursor, err)
		}

		neutral, parseErr := RFC822Message(raw, RFC822Identity{
			Service:     Pop3MailServiceID,
			ServiceName: Pop3MailServiceName,
			ID:          entry.UIDL,
			Mailbox:     pop3MailboxLabel,
			Direction:   "inbound",
			Read:        true,
		})
		if parseErr != nil {
			// Content failure: one bad item must not drop the batch.
			res.Failed++
			logger.Warn("pop3 message unparseable", "uidl", entry.UIDL, "error", parseErr)
			advance(entry.UIDL)
			continue
		}
		if err := sink.Store(Pop3MailServiceID, neutral, raw); err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		res.Stored++
		if neutral.EntityID != "" {
			res.LastStored = neutral.EntityID
		}
		advance(entry.UIDL)
	}

	res.Done = !res.Truncated
	return c.finishMailSync(sink, res, cursor, nil)
}

// transportConfig reads the configured endpoint and credentials for this
// source. Missing pieces are a config error the sync queue treats as
// terminal (shared.ErrMailConfig).
func (c *Pop3MailConnector) transportConfig() (transportConfig, string, string, error) {
	endpoint := ""
	var creds map[string]string
	if svc := c.host.ServiceConfig(Pop3MailServiceID); svc != nil {
		endpoint = svc.Endpoint
		creds = svc.Credentials
	}
	cfg, err := parseTransportEndpoint(endpoint, pop3DefaultPort)
	if err != nil {
		return transportConfig{}, "", "", err
	}
	username := strings.TrimSpace(creds[MailCredentialUsername])
	password := creds[MailCredentialPassword]
	if username == "" || password == "" {
		return transportConfig{}, "", "", fmt.Errorf("%w: %s needs credentials.username and credentials.password",
			shared.ErrMailConfig, Pop3MailServiceID)
	}
	return cfg, username, password, nil
}

// finishMailSync persists the cursor covering this run's rows (the sink
// commits buffered rows with it) and folds the error into the result.
func (c *Pop3MailConnector) finishMailSync(sink MailSink, res shared.MailIngestResult, cursor shared.MailIngestCursor, err error) (shared.MailIngestResult, error) {
	cursor.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	cursor.Done = res.Done
	if err != nil {
		res.Error = err.Error()
		cursor.Error = res.Error
	} else {
		cursor.Error = ""
	}
	if serr := sink.SaveCursor(Pop3MailServiceID, cursor); serr != nil {
		slog.Warn("mail archive cursor save failed", "service", Pop3MailServiceID, "error", serr)
	}
	return res, err
}
