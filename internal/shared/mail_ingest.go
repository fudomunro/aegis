package shared

import (
	"errors"
	"time"
)

// Mail ingest contract (issue #286; docs/mail-ingest.md): pure shapes for
// feeding the local mail archive from a connector-owned ingest — Gmail live
// paging or a Google Takeout export walk. Core owns the archive and the
// sync runner; connectors own the walk and the provider semantics.

// ErrMailConfig reports a mail ingest whose configured location is missing
// or was rejected. The HTTP surface maps it to a client error; the sync
// queue treats it as terminal.
var ErrMailConfig = errors.New("mail ingest not configured")

// MailIngestOptions bounds one ingest run. Ingests apply their own batch
// sizing to MaxMessages (0 means "the source default").
type MailIngestOptions struct {
	// MaxMessages is the hydrate/store cap for this call.
	MaxMessages int
	// Query is the provider list query for live sources.
	Query string
	// Path overrides the source's configured location for export sync
	// sources and is persisted by the ingest (the admin save-path contract).
	Path string
	// ResetCursor discards the durable position and starts over.
	ResetCursor bool
}

// MailIngestResult reports one bounded ingest run. Callers repeat until
// Done. JSON field names are the admin mail-sync response contract.
type MailIngestResult struct {
	Service    string `json:"service"`
	Listed     int    `json:"listed"`
	Stored     int    `json:"stored"`
	Skipped    int    `json:"skipped,omitempty"`
	Failed     int    `json:"failed"`
	Truncated  bool   `json:"truncated"`
	Done       bool   `json:"done"`
	Query      string `json:"query,omitempty"`
	LastStored string `json:"last_stored,omitempty"`
	Error      string `json:"error,omitempty"`
	// RetryAfter asks the runner to back off before the next step (provider
	// quota windows). Runner contract only — never serialized.
	RetryAfter time.Duration `json:"-"`
	// Snapshot asks core to record an optimistic snapshot in the copy
	// ledger after a clean snapshot-import run. Runner contract only —
	// never serialized.
	Snapshot *MailIngestSnapshot `json:"-"`
}

// MailIngestSnapshot is the copy-ledger stamp a snapshot import (export
// walk) reports for core to persist (issue #76).
type MailIngestSnapshot struct {
	Path    string
	Scanned int
	Stored  int
	Skipped int
	Done    bool
}

// MailIngestCursor is the durable per-service ingest position. PageToken is
// source-private (a Gmail page token, say); LastSyncAt is what library
// overviews read.
type MailIngestCursor struct {
	PageToken  string `json:"page_token,omitempty"`
	Done       bool   `json:"done,omitempty"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
	Error      string `json:"error,omitempty"`
}
