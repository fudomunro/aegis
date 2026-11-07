package core

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
	"github.com/fudomunro/aegis/internal/core/copies"
)

// Copy freshness checks (issue #227 phase 2, closing the observational
// validate half of #76). A check answers "is this backup still current?"
// without re-syncing anything: it reads the copy's live version descriptor,
// compares it to the token recorded when the copy was placed, and stamps
// status + last_checked_at on the ledger row. It never writes to a provider.
//
// Fail-open by construction. A row we cannot check right now (no checker for
// that service, no token, transient upstream error) is left completely
// untouched — the docs' contract is that an unchecked copy stays `unknown`,
// and a missing checker must never claim a check happened.

// CopyChecker observes the live version descriptor of one copy location.
// Implementations are read-only and cheap: one metadata call, no paging.
type CopyChecker interface {
	// Services lists the copy-location service ids this checker covers.
	Services() []string
	// CheckFetches the current version descriptor for one copy. ok is false
	// when the check could not be made (auth, transient error, unsupported
	// shape) and the ledger row must be left alone.
	Check(ctx context.Context, rec copies.Record) (cur copies.Version, exists bool, ok bool, err error)
}

// CheckerRegistry maps a copy-location service id to its checker.
type CheckerRegistry struct {
	byService map[string]CopyChecker
}

// NewCheckerRegistry builds an empty registry.
func NewCheckerRegistry() *CheckerRegistry {
	return &CheckerRegistry{byService: make(map[string]CopyChecker)}
}

// Register adds a checker for each service it covers.
func (r *CheckerRegistry) Register(c CopyChecker) {
	if r == nil || c == nil {
		return
	}
	for _, svc := range c.Services() {
		r.byService[strings.TrimSpace(svc)] = c
	}
}

// For returns the checker covering a copy-location service.
func (r *CheckerRegistry) For(service string) (CopyChecker, bool) {
	if r == nil {
		return nil, false
	}
	c, ok := r.byService[strings.TrimSpace(service)]
	return c, ok
}

// Services lists the service ids with a checker (sorted by map order in the
// caller; this is for diagnostics only).
func (r *CheckerRegistry) Services() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byService))
	for k := range r.byService {
		out = append(out, k)
	}
	return out
}

// compareCopyVersion decides a copy's status from its recorded token and the
// live one. Only tokens present on BOTH sides are compared, so a provider
// that omits snapshot_id never produces a false "stale". Name is deliberately
// not a staleness signal: a renamed playlist is not a drifted backup.
//
// Optimistic copies (mail-like snapshots) can only report presence — their
// contract is "material newer than the watermark is expected", so a check
// confirms the copy still exists and leaves drift to a re-import.
func compareCopyVersion(mode string, want, got copies.Version, exists bool) string {
	if !exists {
		return copies.CopyStatusMissing
	}
	if strings.EqualFold(mode, copies.CopySyncModeOptimistic) {
		return copies.CopyStatusInSync
	}
	if want.SnapshotID != "" && got.SnapshotID != "" && want.SnapshotID != got.SnapshotID {
		return copies.CopyStatusStale
	}
	if want.ETag != "" && got.ETag != "" && want.ETag != got.ETag {
		return copies.CopyStatusStale
	}
	if want.ContentHash != "" && got.ContentHash != "" && want.ContentHash != got.ContentHash {
		return copies.CopyStatusStale
	}
	if want.ItemCount > 0 && got.ItemCount > 0 && want.ItemCount != got.ItemCount {
		return copies.CopyStatusStale
	}
	return copies.CopyStatusInSync
}

// freshnessOutcome is what one row's check produced.
type freshnessOutcome struct {
	Status  string
	Checked bool // false: leave the ledger row untouched
	Skip    string
}

// FreshnessEngine walks copy rows and stamps check results. Shared by the
// per-kind freshness runners.
type FreshnessEngine struct {
	srv      *Server
	checkers *CheckerRegistry
	logger   *slog.Logger
}

// NewFreshnessEngine builds an engine over the server's copy ledger.
func NewFreshnessEngine(srv *Server, checkers *CheckerRegistry) *FreshnessEngine {
	return &FreshnessEngine{
		srv:      srv,
		checkers: checkers,
		logger:   slog.Default().With("component", "sync_freshness"),
	}
}

// checkOne evaluates a single copy row. ok is false when the row must be left
// as-is (no checker, no auth, transient failure).
func (e *FreshnessEngine) checkOne(ctx context.Context, rec copies.Record) freshnessOutcome {
	checker, ok := e.checkers.For(rec.Service)
	if !ok {
		return freshnessOutcome{Skip: "no_checker"}
	}
	cur, exists, ok, err := checker.Check(ctx, rec)
	if err != nil {
		// Auth gaps and provider errors are not evidence about the copy.
		return freshnessOutcome{Skip: "error:" + err.Error()}
	}
	if !ok {
		return freshnessOutcome{Skip: "unchecked"}
	}
	return freshnessOutcome{Status: compareCopyVersion(rec.SyncMode, rec.Version, cur, exists), Checked: true}
}

// FreshnessBatch is one bounded walk of copy rows.
type FreshnessBatch struct {
	Kind      string `json:"kind"`
	Checked   int    `json:"checked"`
	Stale     int    `json:"stale"`
	Missing   int    `json:"missing"`
	InSync    int    `json:"in_sync"`
	Skipped   int    `json:"skipped"`
	AfterID   int64  `json:"after_id"`
	Done      bool   `json:"done"`
	NextAfter int64  `json:"next_after"`
}

// RunBatch checks up to limit rows for a kind after afterID, stamping each.
// Chained by the runner via NextAfter/AfterID so a large ledger cannot stall
// one job step.
func (e *FreshnessEngine) RunBatch(ctx context.Context, kind string, afterID int64, limit int) (FreshnessBatch, error) {
	out := FreshnessBatch{Kind: kind, AfterID: afterID, NextAfter: afterID}
	if e == nil || e.srv == nil || e.srv.copyLedger == nil || !e.srv.copyLedger.Enabled() {
		out.Done = true
		return out, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows := e.srv.copyLedger.ListForCheck(kind, afterID, limit)
	for _, row := range rows {
		rec := row.Rec
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res := e.checkOne(ctx, rec)
		if !res.Checked {
			out.Skipped++
			if res.Skip != "" {
				e.logger.Debug("copy freshness check skipped",
					"kind", rec.Kind, "service", rec.Service,
					"external_id", rec.ExternalID, "reason", res.Skip)
			}
		} else {
			if err := e.srv.copyLedger.MarkChecked(rec.Kind, rec.EntityKey, rec.Service, rec.ExternalID, res.Status); err != nil {
				e.logger.Warn("copy freshness stamp failed",
					"kind", rec.Kind, "service", rec.Service, "error", err)
			}
			out.Checked++
			switch res.Status {
			case copies.CopyStatusStale:
				out.Stale++
			case copies.CopyStatusMissing:
				out.Missing++
			case copies.CopyStatusInSync:
				out.InSync++
			}
		}
		if row.ID > out.NextAfter {
			out.NextAfter = row.ID
		}
	}
	out.Done = len(rows) < limit
	return out, nil
}

// --- checkers ---

// musicMirrorChecker validates mirrored playlists and liked-tracks lists
// against the target service's live metadata (types.PlaylistMetaProvider)
// — the same "snapshot_id / item_count" tokens the copy fidelity table
// promises. Endpoint comes from the connector's configured service (never
// hardcoded), per the connector rules.
type musicMirrorChecker struct {
	srv     *Server
	service string
}

// Services implements CopyChecker.
func (c *musicMirrorChecker) Services() []string { return []string{c.service} }

// Check implements CopyChecker.
func (c *musicMirrorChecker) Check(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
	p, ok := c.srv.connectors[c.service].(types.PlaylistMetaProvider)
	if !ok {
		return copies.Version{}, false, false, nil
	}
	// Liked-tracks mirrors have no target id; the only comparable token is
	// how many items the target's saved list holds now.
	if strings.TrimSpace(rec.ExternalID) == "" {
		n, err := p.SavedTrackCount(ctx)
		if err != nil {
			return copies.Version{}, false, false, err
		}
		return copies.Version{ItemCount: n}, true, true, nil
	}
	meta, found, err := p.GetPlaylistMeta(ctx, rec.ExternalID)
	if err != nil {
		return copies.Version{}, false, false, err
	}
	if !found {
		return copies.Version{}, false, true, nil
	}
	return copies.Version{
		ExternalID: rec.ExternalID,
		SnapshotID: meta.VersionToken,
		ItemCount:  meta.ItemCount,
		Name:       meta.Name,
	}, true, true, nil
}

// snapshotChecker validates an optimistic local-snapshot copy (issue #286,
// the PlaylistMetaProvider pattern): the source itself decides what
// "still there" means for its recorded export location. A vanished export
// is `missing` — the ingested data is still in the archive, but the
// recorded copy location is gone.
type snapshotChecker struct {
	srv     *Server
	service string
}

// Services implements CopyChecker.
func (c *snapshotChecker) Services() []string { return []string{c.service} }

// Check implements CopyChecker.
func (c *snapshotChecker) Check(_ context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
	p, ok := c.srv.mailIngestFor(c.service).(types.SnapshotProber)
	if !ok {
		return copies.Version{}, false, false, nil
	}
	path := strings.TrimSpace(rec.ExternalID)
	if path == "" {
		return copies.Version{}, false, false, nil
	}
	exists, name, err := p.SnapshotProbe(path)
	if err != nil {
		return copies.Version{}, false, false, err
	}
	if !exists {
		return copies.Version{}, false, true, nil
	}
	return copies.Version{ExternalID: path, Name: name}, true, true, nil
}

// newCheckerRegistry wires the checkers this build supports. Music services
// register from the connector map: any connector exposing
// types.PlaylistMetaProvider gets the shared mirror checker. Local-snapshot
// ingest sources (issue #286) register from the ingest registry the same
// way. Services with no checker (Bluesky shares, DAV item etags) are
// deliberately absent: their rows stay `unknown` until a checker lands,
// which is the honest answer.
func newCheckerRegistry(srv *Server) *CheckerRegistry {
	r := NewCheckerRegistry()
	for id, conn := range srv.connectors {
		if _, ok := conn.(types.PlaylistMetaProvider); ok {
			r.Register(&musicMirrorChecker{srv: srv, service: id})
		}
	}
	for id, ing := range srv.mailIngests {
		if _, ok := ing.(types.SnapshotProber); ok {
			r.Register(&snapshotChecker{srv: srv, service: id})
		}
	}
	return r
}
