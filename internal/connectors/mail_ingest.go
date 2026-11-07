package connectors

import (
	"context"

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

// MailSink is the core-owned write surface of the mail archive (core
// implements it over *MailArchive). Connectors never see the archive type
// or its SQL.
type MailSink interface {
	// HasMessage reports whether this source already stores the message —
	// by RFC Message-ID when present, else by source id. Cross-service
	// Message-ID matches are attachment, not duplicates (issue #139).
	HasMessage(service string, msg types.Message) (bool, error)
	// Store upserts one message with its raw provider payload. Core
	// compresses the payload and stamps service name, dataset, and FTS.
	Store(service string, msg types.Message, rawPayload []byte) error
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
