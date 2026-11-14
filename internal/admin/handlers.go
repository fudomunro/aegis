package admin

import (
	"context"
	"net/http"
	"sort"

	"github.com/fudomunro/aegis/internal/admin/apps"
	"github.com/fudomunro/aegis/internal/admin/scopes"
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/hookhealth"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/core/ownership"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/core/udk"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/syncjobs"
)

// KindCatalog is the kind-registry query surface the admin views need. The
// agent data plane (internal/library) satisfies it; the hub wires the
// instance, so the two surfaces never import each other.
type KindCatalog interface {
	AllLibraryKinds() []kinds.LibraryKind
	RegistryKind(id string) (kinds.Kind, bool)
	UDKSchemaForKind(id string) (kinds.UserDefinedKindSchema, bool)
	LibraryReadableFor(permType string) bool
}

// MaintenanceCapture is the maintenance signal-bundle surface (capture and
// persist). The sync domain's Runners satisfies it; the hub wires the
// instance so the surfaces stay uncoupled.
type MaintenanceCapture interface {
	CollectMaintenanceSignals() syncjobs.MaintenanceSignals
	WriteMaintenanceSignals(sig syncjobs.MaintenanceSignals) (string, error)
}

// ConflictResolve applies an accept/reject decision to one detected
// non-hub edit (issue #365 phase 3). The sync domain's Runners satisfies
// it; the hub wires the instance so the surfaces stay uncoupled.
type ConflictResolve interface {
	ResolveConflict(ctx context.Context, id string, accept bool) (syncjobs.SyncConflict, error)
}

// SearchStatus is the search-index status view. The concrete engine
// (library/search) satisfies it; the hub wires the instance.
type SearchStatus interface {
	Enabled() bool
	Path() string
	CountsByKind() (map[string]int, error)
}

// Handlers is the management surface. The hub constructs it once and mounts
// its routes behind the admin perimeter.
type Handlers struct {
	// Host is the hub seam (config views and writes, CSRF, invalidation).
	Host Host
	// KindCatalog answers registry queries for admin listings.
	KindCatalog KindCatalog
	// Search reports search-index status.
	Search SearchStatus
	// Maintenance captures and persists the signal bundles.
	Maintenance MaintenanceCapture
	// Apps is the third-party app registry (mint/verify/consent).
	Apps *apps.Store
	// Grants mints and consumes one-time app consent codes.
	Grants *apps.GrantStore
	// Scopes is the data-scope store (invite tokens, partitions).
	Scopes *scopes.Store
	// UDKs is the user-defined-kind registry (schema packs + overlay).
	UDKs *udk.Registry
	// Ownership holds per-kind user confirmation state.
	Ownership *ownership.Store
	// Mail is the optional local mail archive.
	Mail *mail.Archive
	// MailIngests are the archive ingest sources keyed by service id
	// (issue #286).
	MailIngests map[string]connectors.MailIngest
	// Copies is the durable copy ledger.
	Copies *copies.Ledger
	// SyncJobs is the durable sync job queue (nil when disabled).
	SyncJobs *syncjobs.JobStore
	// SyncRunners maps (kind, job_type) to a runner.
	SyncRunners *syncjobs.RunnerRegistry
	// SyncWorker drains SyncJobs (nil when disabled).
	SyncWorker *syncjobs.SyncWorker
	// SyncSchedules holds recurring sync job templates (nil when disabled).
	SyncSchedules *syncjobs.ScheduleStore
	// SyncConflicts holds the detected non-hub edits awaiting an
	// accept/reject decision (issue #365 phase 3). Nil when disabled.
	SyncConflicts *syncjobs.ConflictStore
	// Conflicts applies accept/reject decisions (wired to the sync
	// domain's Runners).
	Conflicts ConflictResolve
	// Metrics is the in-memory access ring buffer for the admin map.
	Metrics *metrics.AccessMetrics
	// Prom exports the Prometheus text view of the same series.
	Prom *metrics.PromMetrics
	// Connectors is the configured connector registry.
	Connectors map[string]connectors.ServiceConnector
	// LocalStores are the optional local import stores keyed by stem
	// (issue #393). Entries may be typed-nil behind the interface when
	// disabled — Admin() copy stays describable.
	LocalStores map[string]connectors.ImportStore
	// Cache and MBCache back the admin cache panel (shared response cache
	// and the MusicBrainz lookup cache).
	Cache   *shared.Cache
	MBCache *shared.Cache
	// HookHealth records Aperture pre-request outcomes for admin
	// diagnostics.
	HookHealth *hookhealth.HookHealth
	// BaseURL is the instance base URL for onboarding copy.
	Tokens *tokens.TokenStore
	// BaseURL is the instance base URL for onboarding copy.
	BaseURL string
	// DataDir is the resolved local state directory (issue #74).
	DataDir string
}

// listHandlerFor resolves a kind's list-route handler (its Def().Path route)
// from the routes the kind declares.
func listHandlerFor(k kinds.Kind) http.HandlerFunc {
	for _, route := range k.Routes() {
		if route.Pattern == k.Def().Path {
			return route.Handler
		}
	}
	return nil
}

// cacheStats sums the response cache and the MBID cache for the admin panel.
func (h *Handlers) cacheStats() (live, total, bytes int) {
	if h.Cache != nil {
		l, t, b := h.Cache.Stats()
		live, total, bytes = live+l, total+t, bytes+b
	}
	if h.MBCache != nil {
		l, t, b := h.MBCache.Stats()
		live, total, bytes = live+l, total+t, bytes+b
	}
	return live, total, bytes
}

// cacheSnapshot lists both caches' entries (MBID keys keep their "mbid:"
// prefix so the two are distinguishable).
func (h *Handlers) cacheSnapshot() []shared.CacheItem {
	items := []shared.CacheItem{}
	if h.Cache != nil {
		items = append(items, h.Cache.Snapshot()...)
	}
	if h.MBCache != nil {
		items = append(items, h.MBCache.Snapshot()...)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}
