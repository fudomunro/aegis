package syncjobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/kinds"
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
	// Cur and Exists are the live tokens behind the status, so a
	// divergence can be classified into a sync conflict (issue #365
	// phase 3) without a second fetch.
	Cur    copies.Version
	Exists bool
}

// FreshnessEngine walks copy rows and stamps check results. Shared by the
// per-kind freshness runners.
type FreshnessEngine struct {
	run      *Runners
	checkers *CheckerRegistry
	logger   *slog.Logger
}

// NewFreshnessEngine builds an engine over the server's copy ledger.
func NewFreshnessEngine(run *Runners, checkers *CheckerRegistry) *FreshnessEngine {
	return &FreshnessEngine{
		run:      run,
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
	return freshnessOutcome{
		Status:  compareCopyVersion(rec.SyncMode, rec.Version, cur, exists),
		Checked: true,
		Cur:     cur,
		Exists:  exists,
	}
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
	if e == nil || e.run == nil || e.run.Copies == nil || !e.run.Copies.Enabled() {
		out.Done = true
		return out, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows := e.run.Copies.ListForCheck(kind, afterID, limit)
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
			if err := e.run.Copies.MarkChecked(rec.Kind, rec.EntityKey, rec.Service, rec.ExternalID, res.Status); err != nil {
				e.logger.Warn("copy freshness stamp failed",
					"kind", rec.Kind, "service", rec.Service, "error", err)
			}
			e.recordDivergence(rec, res.Cur, res.Exists)
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

// recordDivergence files a sync conflict when a member copy's live tokens
// diverge from its watermark (issue #365 phase 3). Only sync-group members
// are in scope: a plain mirror target has no canonical home to accept into
// or reject against, and the canonical home's own rows record its edits —
// which propagate rather than conflict.
func (e *FreshnessEngine) recordDivergence(rec copies.Record, cur copies.Version, exists bool) {
	field := classifyCopyDivergence(rec.Version, cur, exists)
	if field == "" || e.run == nil || e.run.Host == nil {
		return
	}
	hub, members := e.run.Host.SyncGroup(rec.Kind)
	if hub == "" || hub == rec.Service {
		return
	}
	inGroup := false
	for _, m := range members {
		if m == rec.Service {
			inGroup = true
			break
		}
	}
	if !inGroup {
		return
	}
	e.run.recordConflict(SyncConflict{
		Kind:        rec.Kind,
		Service:     rec.Service,
		EntityKey:   rec.EntityKey,
		ExternalID:  rec.ExternalID,
		Subject:     rec.Version.Name,
		Field:       field,
		MemberValue: divergenceMemberValue(field, cur, exists),
		LastValue:   divergenceLastValue(field, rec.Version),
		Source:      ConflictSourceFreshness,
	})
}

// divergenceMemberValue formats what the member copy now holds.
func divergenceMemberValue(field string, got copies.Version, exists bool) string {
	if !exists {
		return "missing on this copy"
	}
	switch field {
	case ConflictFieldTitle:
		return got.Name
	case ConflictFieldItems:
		if got.ItemCount > 0 {
			return fmt.Sprintf("%d items", got.ItemCount)
		}
		return "items changed on this copy"
	default:
		return "changed on this copy"
	}
}

// divergenceLastValue formats the watermark the copy diverged from — the
// state convergence last produced there.
func divergenceLastValue(field string, want copies.Version) string {
	switch field {
	case ConflictFieldTitle:
		return want.Name
	case ConflictFieldItems:
		if want.ItemCount > 0 {
			return fmt.Sprintf("%d items", want.ItemCount)
		}
		return "items unchanged"
	default:
		return "not overwritten"
	}
}

// --- checkers ---

// musicMirrorChecker validates mirrored playlists and liked-tracks lists
// against the target service's live metadata (kinds.PlaylistMetaProvider)
// — the same "snapshot_id / item_count" tokens the copy fidelity table
// promises. Endpoint comes from the connector's configured service (never
// hardcoded), per the connector rules.
type musicMirrorChecker struct {
	run     *Runners
	service string
}

// Services implements CopyChecker.
func (c *musicMirrorChecker) Services() []string { return []string{c.service} }

// Check implements CopyChecker.
func (c *musicMirrorChecker) Check(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
	p, ok := c.run.Connectors[c.service].(kinds.PlaylistMetaProvider)
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
	run     *Runners
	service string
}

// Services implements CopyChecker.
func (c *snapshotChecker) Services() []string { return []string{c.service} }

// Check implements CopyChecker.
func (c *snapshotChecker) Check(_ context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
	p, ok := c.run.MailIngests[c.service].(kinds.SnapshotProber)
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

// entityItem is one live member record the entity list carries: the
// service-local id and the etag token placement's If-Match matches against.
type entityItem struct {
	id   string
	etag string
}

// entityList is one service's live entity collection for a kind.
type entityList struct {
	byID      map[string]entityItem
	count     int
	truncated bool
	fetchedAt time.Time
}

// entityListTTL bounds how long one live list serves a freshness walk.
const entityListTTL = 30 * time.Second

// entityChecker validates contact and event copies against the member's
// live collection (issue #377). The list is the surface the DAV REPORT
// already returns — one call carrying every item with its etag — so one
// fetch answers the whole walk: an item row probes its etag watermark
// (a member-side edit diverges it), and the collection row probes the item
// count, which is where a member-side create surfaces — the created
// contact has no watermark row of its own to diverge.
//
// No etag is invented: a live item without one leaves the watermark
// comparison empty (compareCopyVersion only compares tokens present on
// both sides), and a truncated list reports nothing at all — a capped
// collection can neither prove absence nor a count.
type entityChecker struct {
	run     *Runners
	service string
	// maxAge bounds how long one list serves the walk; non-positive
	// fetches on every check (tests), registration sets entityListTTL.
	maxAge time.Duration

	mu    sync.Mutex
	lists map[string]entityList
}

// Services implements CopyChecker.
func (c *entityChecker) Services() []string { return []string{c.service} }

// Check implements CopyChecker.
func (c *entityChecker) Check(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
	kind := strings.TrimSpace(rec.Kind)
	if kind != "contacts" && kind != "events" {
		return copies.Version{}, false, false, nil
	}
	list, ok, err := c.listFor(ctx, kind)
	if err != nil {
		return copies.Version{}, false, false, err
	}
	if !ok || list.truncated {
		return copies.Version{}, false, false, nil
	}
	if strings.TrimSpace(rec.ExternalID) == "" {
		// The collection row: the live count is the member-side create
		// probe. A count above the recorded watermark classifies as an
		// items divergence (a shortfall does not — convergence restores).
		return copies.Version{ItemCount: list.count}, true, true, nil
	}
	item, found := list.byID[rec.ExternalID]
	if !found {
		return copies.Version{}, false, true, nil
	}
	// Only the etag is comparable here: it covers renames and record
	// edits alike, and entities have no per-field rename resolution, so
	// a name change must classify as a record divergence, not a title one.
	return copies.Version{ExternalID: rec.ExternalID, ETag: item.etag}, true, true, nil
}

// listFor returns the member's live items for one kind, fetching at most
// once per maxAge window so one list serves the whole walk. ok is false
// when the service cannot list that kind.
func (c *entityChecker) listFor(ctx context.Context, kind string) (entityList, bool, error) {
	c.mu.Lock()
	if l, ok := c.lists[kind]; ok && c.maxAge > 0 && time.Since(l.fetchedAt) < c.maxAge {
		c.mu.Unlock()
		return l, true, nil
	}
	c.mu.Unlock()

	var items []entityItem
	var truncated bool
	switch kind {
	case "contacts":
		p, ok := c.run.Connectors[c.service].(kinds.ContactProvider)
		if !ok {
			return entityList{}, false, nil
		}
		contacts, trunc, err := p.ListUserContacts(ctx)
		if err != nil {
			return entityList{}, false, err
		}
		truncated = trunc
		for _, ct := range contacts {
			for _, src := range ct.Sources {
				if src.Service == c.service && src.ID != "" {
					items = append(items, entityItem{id: src.ID, etag: kinds.ExtraString(src.Extra, "etag")})
					break
				}
			}
		}
	case "events":
		p, ok := c.run.Connectors[c.service].(kinds.CalendarEventProvider)
		if !ok {
			return entityList{}, false, nil
		}
		events, trunc, err := p.ListCalendarEvents(ctx)
		if err != nil {
			return entityList{}, false, err
		}
		truncated = trunc
		for _, ev := range events {
			for _, src := range ev.Sources {
				if src.Service == c.service && src.ID != "" {
					items = append(items, entityItem{id: src.ID, etag: kinds.ExtraString(src.Extra, "etag")})
					break
				}
			}
		}
	default:
		return entityList{}, false, nil
	}

	l := entityList{
		byID:      make(map[string]entityItem, len(items)),
		count:     len(items),
		truncated: truncated,
		fetchedAt: time.Now(),
	}
	for _, it := range items {
		l.byID[it.id] = it
	}
	c.mu.Lock()
	if c.lists == nil {
		c.lists = make(map[string]entityList, 2)
	}
	c.lists[kind] = l
	c.mu.Unlock()
	return l, true, nil
}

// newCheckerRegistry wires the checkers this build supports. Music services
// register from the connector map: any connector exposing
// kinds.PlaylistMetaProvider gets the shared mirror checker, and any
// connector exposing an entity provider (contacts, events) gets the entity
// checker. Local-snapshot ingest sources (issue #286) register from the
// ingest registry the same way. A service takes one checker — no service
// exposes both a playlist/mail surface and an entity one today; services
// with no checker at all (Bluesky shares) are deliberately absent: their
// rows stay `unknown` until a checker lands, which is the honest answer.
func newCheckerRegistry(run *Runners) *CheckerRegistry {
	r := NewCheckerRegistry()
	for id, conn := range run.Connectors {
		if _, ok := conn.(kinds.PlaylistMetaProvider); ok {
			r.Register(&musicMirrorChecker{run: run, service: id})
			continue
		}
		if _, ok := conn.(kinds.ContactProvider); ok {
			r.Register(&entityChecker{run: run, service: id, maxAge: entityListTTL})
			continue
		}
		if _, ok := conn.(kinds.CalendarEventProvider); ok {
			r.Register(&entityChecker{run: run, service: id, maxAge: entityListTTL})
		}
	}
	for id, ing := range run.MailIngests {
		if _, ok := ing.(kinds.SnapshotProber); ok {
			r.Register(&snapshotChecker{run: run, service: id})
		}
	}
	return r
}
