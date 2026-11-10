// Command aegis-dashboard generates the sync-maintenance dashboard as a
// static site (issue #239, issue #335): one page per service connection
// showing the maintenance record and the success / stability / confidence
// marks, plus optional capture history when signal bundles are supplied.
// It is a separate tool, not part of the application code — it analyzes the
// progress of the project and writes plain files to open or publish.
//
// Data collection is deliberately file- and CLI-shaped so the application
// code owes the tool nothing beyond artifacts it already produces:
//
//   - the repo's GitHub-issue record, scraped with the gh CLI (the same
//     transport as the ticket surface; GH_TOKEN is enough in CI);
//   - the committed services manifest (which pages exist, display names,
//     and which entry is the drift-injection harness);
//   - captured signal bundles (signals-*.json) for instance history;
//   - the connector metadata catalog (maturity declarations and service
//     title aliases) — hand-maintained declarations in internal/connectors,
//     the one Go-level input.
//
// Release build (issue #239, run by pages.yml on every merge to main):
//
//	go run ./cmd/aegis-dashboard -repo owner/name -out site/maintenance \
//	  -manifest site/maintenance/services.json
//
// Instance build from captured bundles (captures stay on the admin
// surface: POST /v1/admin/maintenance/signals):
//
//	go run ./cmd/aegis-dashboard -repo owner/name -bundles /path/to/maintenance \
//	  -out /path/to/maintenance/dashboard -private
package main

import (
	"sort"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

// DashboardServiceMeta is the configured identity of one service page,
// assembled from the services manifest (or discovered in the data).
type DashboardServiceMeta struct {
	ID         string
	Name       string
	Connection string
	// FailureInjection marks the drift-injection harness (fake upstream).
	FailureInjection bool
}

// DashboardScores are one service's success / stability / confidence marks,
// derived from the maintenance record so every build — release-published or
// instance — reads the same way.
type DashboardScores struct {
	// Success is the share of recorded problems resolved (fix rate),
	// percent 0-100. Nil when the record is empty.
	Success *float64 `json:"success,omitempty"`
	// Stability is how long the service has been quiet: days since the last
	// recorded failure event over a thirty-day window, percent 0-100. Quiet
	// is not health — an old open problem does not lower it. Nil when the
	// record is empty.
	Stability *float64 `json:"stability,omitempty"`
	// Confidence bands the record depth:
	// none | low | medium | high.
	Confidence string `json:"confidence"`
}

// DashboardCapture is one capture point in a service's history. Counts are
// the cumulative queue rollup at capture time (the bundle's view of the
// table, subject to retention pruning).
type DashboardCapture struct {
	CapturedAt time.Time   `json:"captured_at"`
	Counts     SyncCounts  `json:"counts"`
	// NewFailures is failed-job events dated inside (previous capture, this].
	NewFailures int `json:"new_failures"`
	// CounterReset notes that cumulative totals dropped since the previous
	// capture — retention pruning or a fresh store, not negative activity.
	CounterReset bool `json:"counter_reset,omitempty"`
}

// DashboardFailure is one unique failed job seen across the capture series,
// carrying the scrubbed error text from the bundle.
type DashboardFailure struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind,omitempty"`
	JobType    string    `json:"job_type"`
	ErrorClass string    `json:"error_class,omitempty"`
	Error      string    `json:"error,omitempty"`
	Attempts   int       `json:"attempts"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// DashboardFix is one GitHub issue projected onto a service page.
type DashboardFix struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Service string `json:"service"`
	// Kind is "ticket" (sync-maintenance loop) or "fix" (historical fix).
	Kind string `json:"kind"`
	// State is "open", "fixed" (completed), or "closed".
	State string `json:"state"`
	// ReproState is set for tickets: needs-fixture | ready.
	ReproState string `json:"repro_state,omitempty"`
	// FirstSeen is the failure onset from the ticket evidence block, when
	// present; otherwise the issue creation time.
	FirstSeen time.Time `json:"first_seen,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ClosedAt  time.Time `json:"closed_at,omitempty"`
}

// FailureAt is when the record says the problem was observed.
func (f DashboardFix) FailureAt() time.Time {
	if !f.FirstSeen.IsZero() {
		return f.FirstSeen
	}
	return f.CreatedAt
}

// DashboardService is one service/connection page's model.
type DashboardService struct {
	ID               string              `json:"id"`
	Name             string              `json:"name,omitempty"`
	Connection       string              `json:"connection,omitempty"`
	FailureInjection bool                `json:"failure_injection,omitempty"`
	Scores           DashboardScores     `json:"scores"`
	Latest           SyncCounts          `json:"latest"`
	// LastSeen is the newest capture that carried this service.
	LastSeen time.Time           `json:"last_seen,omitempty"`
	History  []DashboardCapture  `json:"history,omitempty"`
	Failures []DashboardFailure  `json:"failures,omitempty"`
	Fixes    []DashboardFix      `json:"fixes,omitempty"`
	// Maturity is the connection's coverage declaration (issue #247): what
	// the service offers and what Aegis serves. Nil when the catalog has
	// no entry — test fixtures and the drift-injection harness.
	Maturity *connectors.ConnectionMaturity `json:"maturity,omitempty"`
	// MaturityGrade is the derived coverage mark, shown beside the
	// operational scores and deliberately not mixed into them.
	MaturityGrade *connectors.MaturityGrade `json:"maturity_grade,omitempty"`
}

// NameOrID is the display name for a service page.
func (s DashboardService) NameOrID() string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

// DashboardData is the whole dashboard model, fully materialized before
// rendering so the build is reproducible and testable without a server.
type DashboardData struct {
	GeneratedAt time.Time `json:"generated_at"`
	Hostname    string    `json:"hostname,omitempty"`
	Version     string    `json:"version,omitempty"`
	Commit      string    `json:"commit,omitempty"`
	// Captures is how many signal bundles fed the build.
	Captures int `json:"captures"`
	// WindowStart / WindowEnd bound the capture series.
	WindowStart time.Time          `json:"window_start,omitempty"`
	WindowEnd   time.Time          `json:"window_end,omitempty"`
	Services    []DashboardService `json:"services"`
	// Freshness is the latest capture's per-kind copy rollup.
	Freshness []Freshness `json:"freshness,omitempty"`
	// Fixes is every mapped maintenance record, newest first.
	Fixes []DashboardFix `json:"fixes,omitempty"`
	// FixHistoryError notes a fail-open scrape problem, if any.
	FixHistoryError string `json:"fix_history_error,omitempty"`
}

// DashboardPage wraps one service page with the shared build stamp.
type DashboardPage struct {
	GeneratedAt time.Time
	Hostname    string
	Version     string
	Commit      string
	Captures    int
	Service     DashboardService
}

// BuildMaintenanceDashboard projects one capture series, the configured
// service list, the connection maturity catalog, and scraped repo issues
// onto the static dashboard model. Services carry their whole capture
// history, failure events, scores, maturity coverage, and maintenance
// record; records that map to no service are dropped.
func BuildMaintenanceDashboard(series []SignalBundle, metas []DashboardServiceMeta, issues []MaintenanceIssueRecord, maturity map[string]connectors.ConnectionMaturity, generatedAt time.Time) DashboardData {
	sorted := append([]SignalBundle(nil), series...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CapturedAt.Before(sorted[j].CapturedAt)
	})

	data := DashboardData{
		GeneratedAt: generatedAt,
		Captures:    len(sorted),
	}
	if len(sorted) > 0 {
		data.Hostname = sorted[len(sorted)-1].Hostname
		data.Version = sorted[len(sorted)-1].Version
		data.Commit = sorted[len(sorted)-1].Commit
		data.WindowStart = sorted[0].CapturedAt
		data.WindowEnd = sorted[len(sorted)-1].CapturedAt
		data.Freshness = sorted[len(sorted)-1].Freshness
	}

	metaByID := map[string]DashboardServiceMeta{}
	ids := make([]string, 0, len(metas))
	for _, m := range metas {
		if m.ID == "" {
			continue
		}
		if _, seen := metaByID[m.ID]; !seen {
			ids = append(ids, m.ID)
		}
		metaByID[m.ID] = m
	}
	// Discover services the data knows even when config no longer lists
	// them: per-service rollups and attributed failures.
	for _, sig := range sorted {
		if sig.Sync == nil {
			continue
		}
		for id := range sig.Sync.ByService {
			if _, seen := metaByID[id]; !seen && id != "" {
				ids = append(ids, id)
				metaByID[id] = DashboardServiceMeta{ID: id}
			}
		}
		for _, f := range sig.Sync.Failures {
			if _, seen := metaByID[f.Service]; !seen && f.Service != "" {
				ids = append(ids, f.Service)
				metaByID[f.Service] = DashboardServiceMeta{ID: f.Service}
			}
		}
	}

	// Ticket services are known before mapping starts, so attribution of
	// later title-matched issues never depends on gh output order.
	for _, rec := range issues {
		if rec.TicketService == "" {
			continue
		}
		if _, seen := metaByID[rec.TicketService]; !seen {
			ids = append(ids, rec.TicketService)
			metaByID[rec.TicketService] = DashboardServiceMeta{ID: rec.TicketService}
		}
	}
	// Map the scraped issue history onto services; ticket services open
	// their own page even without config or queue evidence.
	fixes := make([]DashboardFix, 0, len(issues))
	for _, rec := range issues {
		fix, ok := mapIssueRecord(rec, ids)
		if !ok {
			continue
		}
		if _, seen := metaByID[fix.Service]; !seen && fix.Service != "" {
			ids = append(ids, fix.Service)
			metaByID[fix.Service] = DashboardServiceMeta{ID: fix.Service}
		}
		fixes = append(fixes, fix)
	}
	sort.Slice(fixes, func(i, j int) bool {
		if fixes[i].CreatedAt.Equal(fixes[j].CreatedAt) {
			return fixes[i].Number > fixes[j].Number
		}
		return fixes[i].CreatedAt.After(fixes[j].CreatedAt)
	})
	data.Fixes = fixes

	sort.Strings(ids)
	for _, id := range ids {
		data.Services = append(data.Services, buildDashboardService(id, metaByID[id], sorted, fixes, maturity[id], generatedAt))
	}
	return data
}

// buildDashboardService assembles one service page model from the series.
func buildDashboardService(id string, meta DashboardServiceMeta, series []SignalBundle, fixes []DashboardFix, maturity connectors.ConnectionMaturity, now time.Time) DashboardService {
	svc := DashboardService{
		ID:               id,
		Name:             meta.Name,
		Connection:       meta.Connection,
		FailureInjection: meta.FailureInjection,
	}
	if maturity.Service != "" {
		m := maturity
		svc.Maturity = &m
		grade := connectors.GradeConnectionMaturity(m)
		svc.MaturityGrade = &grade
	}
	for _, fix := range fixes {
		if fix.Service == id {
			svc.Fixes = append(svc.Fixes, fix)
		}
	}

	failuresByID := map[string]DashboardFailure{}
	var prevTotal int
	for i, sig := range series {
		if sig.Sync == nil {
			continue
		}
		counts, tracked := sig.Sync.ByService[id]
		if tracked {
			point := DashboardCapture{
				CapturedAt: sig.CapturedAt,
				Counts:     counts,
			}
			if i > 0 && counts.Total() < prevTotal {
				point.CounterReset = true
			}
			svc.History = append(svc.History, point)
			prevTotal = counts.Total()
			svc.Latest = counts
			svc.LastSeen = sig.CapturedAt
		}
		for _, f := range sig.Sync.Failures {
			if f.Service != id {
				continue
			}
			prev, seen := failuresByID[f.ID]
			if !seen || f.UpdatedAt.After(prev.UpdatedAt) {
				failuresByID[f.ID] = DashboardFailure{
					ID:         f.ID,
					Kind:       f.Kind,
					JobType:    f.JobType,
					ErrorClass: f.ErrorClass,
					Error:      f.Error,
					Attempts:   f.Attempts,
					UpdatedAt:  f.UpdatedAt,
				}
			}
		}
	}

	svc.Failures = make([]DashboardFailure, 0, len(failuresByID))
	for _, f := range failuresByID {
		svc.Failures = append(svc.Failures, f)
	}
	sort.Slice(svc.Failures, func(i, j int) bool {
		return svc.Failures[i].UpdatedAt.After(svc.Failures[j].UpdatedAt)
	})
	// Interval failure counts need the previous capture's timestamp.
	for i := range svc.History {
		if i == 0 {
			continue
		}
		from := svc.History[i-1].CapturedAt
		to := svc.History[i].CapturedAt
		svc.History[i].NewFailures = countFailuresBetween(svc.Failures, from, to)
	}
	svc.Scores = scoreDashboardRecord(svc.Fixes, now)
	// Last activity is the newest evidence of any kind: a capture point or
	// a record event (failure onset or resolution).
	svc.LastSeen = lastRecordActivity(svc.Fixes, svc.LastSeen)
	return svc
}

// lastRecordActivity returns the newest of baseline and the record's event
// times (failure onset, resolution).
func lastRecordActivity(fixes []DashboardFix, baseline time.Time) time.Time {
	latest := baseline
	for _, f := range fixes {
		if at := f.FailureAt(); at.After(latest) {
			latest = at
		}
		if !f.ClosedAt.IsZero() && f.ClosedAt.After(latest) {
			latest = f.ClosedAt
		}
	}
	return latest
}

// countFailuresBetween counts failure events dated inside (from, to].
func countFailuresBetween(failures []DashboardFailure, from, to time.Time) int {
	n := 0
	for _, f := range failures {
		if f.UpdatedAt.After(from) && !f.UpdatedAt.After(to) {
			n++
		}
	}
	return n
}

// scoreDashboardRecord derives the marks from the maintenance record — the
// evidence the release-published build always has. Marks measure
// maintenance responsiveness, not sync outcomes (issue #239):
//
//	success    — share of recorded problems resolved (fix rate). A problem
//	             closed as not planned counts as recorded but unresolved.
//	stability  — how long the service has been quiet: days since the last
//	             recorded failure event over a thirty-day window. Quiet is
//	             not health — an old open problem does not lower it.
//	confidence — record depth (item-count bands), so a single early blip
//	             cannot read as high confidence.
func scoreDashboardRecord(fixes []DashboardFix, now time.Time) DashboardScores {
	var s DashboardScores
	if len(fixes) == 0 {
		s.Confidence = "none"
		return s
	}
	resolved := 0
	var lastFailure time.Time
	for _, f := range fixes {
		if f.State == "fixed" {
			resolved++
		}
		if at := f.FailureAt(); at.After(lastFailure) {
			lastFailure = at
		}
	}
	pct := 100 * float64(resolved) / float64(len(fixes))
	s.Success = &pct
	if !lastFailure.IsZero() {
		quiet := now.Sub(lastFailure).Hours() / 24
		if quiet < 0 {
			quiet = 0
		}
		if quiet > 30 {
			quiet = 30
		}
		stability := 100 * quiet / 30
		s.Stability = &stability
	}
	s.Confidence = recordConfidence(len(fixes))
	return s
}

// recordConfidence grades how much evidence backs the marks: how many
// problems the record carries for the service.
func recordConfidence(items int) string {
	switch {
	case items >= 10:
		return "high"
	case items >= 3:
		return "medium"
	case items >= 1:
		return "low"
	default:
		return "none"
	}
}

// maturityByService indexes the connector metadata catalog by service id.
func maturityByService() map[string]connectors.ConnectionMaturity {
	cat := connectors.ConnectionMaturityCatalog()
	out := make(map[string]connectors.ConnectionMaturity, len(cat))
	for _, m := range cat {
		out[m.Service] = m
	}
	return out
}
