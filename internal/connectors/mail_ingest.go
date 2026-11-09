package connectors

import (
	"context"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

// Mail ingest seam (issue #286; docs/mail-ingest.md): one contract, two
// implementations — the Gmail connector's live paging and the Google
// Takeout export walk — writing through a core-owned sink. Optional
// capability beside ServiceConnector and Onboarder: looked up by service
// id, never assumed. The Gmail+Takeout pair lives beside each other here so
// it never splits across a package wall (issue #266).

// MailIngest feeds the local mail archive one bounded batch at a time.
// Callers repeat until Result.Done.
type MailIngest interface {
	IngestMail(ctx context.Context, sink MailSink, opts types.MailIngestOptions) (types.MailIngestResult, error)
}

// MailSource is how one ingest source groups beside the archive: its
// overview parent and canonical-ness (issue #286; docs/mail-ingest.md).
// Display names are not declared here — the serviceNames registry is the
// one name owner (issue #254).
type MailSource struct {
	ServiceID string
	// Parent nests this source under another in overviews when that source
	// is present (google-takeout under google-gmail). When the declared
	// parent is absent, core nests under the collection's aggregate row.
	Parent string
	// Canonical marks rows as the mailbox home (issue #16). Canonical is
	// claimed, never assumed: undeclared sources are non-canonical.
	Canonical bool
	// Start is the call-to-action for an ingest-only source with no
	// connection entry of its own (issue #165). Sources that are also
	// connections declare Start on their maturity row instead; a Start
	// here is only for sources the maturity catalog does not carry.
	Start *types.OwnershipAction
}

// mailSources lists the declarations beside each ingest.
var mailSources = []MailSource{GmailMailSource, TakeoutMailSource}

// MailSourceFor returns the declaration for an ingest source — the zero
// value (no parent, non-canonical) when the source is undeclared.
func MailSourceFor(service string) MailSource {
	for _, s := range mailSources {
		if s.ServiceID == service {
			return s
		}
	}
	return MailSource{}
}

// MailSink is the core-owned write surface of the mail archive (core
// implements it over *MailArchive). Connectors never see the archive type
// or its SQL.
type MailSink interface {
	// HasMessage reports whether this source already stores the message —
	// by RFC Message-ID when present, else by source id. Cross-service
	// Message-ID matches are attachment, not duplicates (issue #139).
	HasMessage(service string, msg kinds.Message) (bool, error)
	// Store upserts one message with its raw provider payload. Core
	// compresses the payload and stamps service name, dataset, and FTS.
	Store(service string, msg kinds.Message, rawPayload []byte) error
	// LoadCursor/SaveCursor round-trip the durable ingest position under
	// the shared cursor keys; the cursor's LastSyncAt is what overviews
	// read.
	LoadCursor(service string) (types.MailIngestCursor, error)
	SaveCursor(service string, cur types.MailIngestCursor) error
	// MetaGet/MetaSet round-trip ingest-private durable state (export
	// location, walk cursor) under keys the ingest owns.
	MetaGet(key string) (string, error)
	MetaSet(key, value string) error
}
