package syncjobs

import (
	"sync"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

// Host is the narrow hub seam the background domain needs (issue #358).
type Host interface {
	// OwnerGrants is the owner-tier rule map — background runs execute as
	// the owner.
	OwnerGrants() shared.GrantMap
	// ServiceBlocked reports whether a configured service is missing or
	// blocked. Ingest-only sync locations are not services and report false.
	ServiceBlocked(serviceID string) bool
}

// Runners owns the background-consistency execution: the mail ingest walks,
// mirror repair, canary drift detection, freshness checks, and maintenance
// capture. The hub constructs it and registers its runners on the queue's
// RunnerRegistry (issue #358).
type Runners struct {
	// Host is the hub seam (owner grants, service block checks).
	Host Host
	// Mirror is the agent data plane's mirror engine: adapter lookups,
	// write placement, and copy recording.
	Mirror *library.Handlers
	// Mail is the optional local mail archive.
	Mail *mail.Archive
	// MailIngests are the archive ingest sources keyed by service id.
	MailIngests map[string]connectors.MailIngest
	// Connectors is the configured connector registry.
	Connectors map[string]connectors.ServiceConnector
	// Copies is the durable copy ledger.
	Copies *copies.Ledger
	// Jobs is the durable sync job queue (nil when disabled).
	Jobs *JobStore
	// DataDir is the resolved local state directory (issue #74).
	DataDir string
	// Version and Commit stamp maintenance signal bundles.
	Version string
	Commit  string

	// mailSyncMu serializes ingest walks so two page cursors cannot
	// interleave into one store — the admin sync route and the queue share
	// this lock through DoMailIngest.
	mailSyncMu sync.Mutex
}

// Register wires every background runner onto the queue registry.
func (r *Runners) Register(reg *RunnerRegistry) {
	if r == nil || reg == nil {
		return
	}
	reg.Register(&mailIngestRunner{run: r})
	registerMirrorRunners(reg, r)
	registerCanaryRunners(reg, r)
	registerFreshnessRunners(reg, r)
}
