package core

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// Sync-maintenance dashboard (issue #239): an entirely build-time generated
// static site over the signal-bundle history — one page per service
// connection showing history and the success / stability / confidence marks,
// plus the scraped GitHub-issue maintenance record (historical fixes). The
// drift-injection harness (fake upstream) appears like any other service,
// annotated as deliberately unstable so its failures read as test activity.
//
// The generator is pure over its inputs, like the scanner reading paths:
// bundle files in, HTML files out, no instance IPC. Generation is an
// explicit build step (POST /v1/admin/maintenance/dashboard), not a live
// view; the output is plain files to open or publish.

// DashboardServiceMeta is the configured identity of one service page,
// assembled from instance config at build time.
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
	CapturedAt time.Time  `json:"captured_at"`
	Counts     SyncCounts `json:"counts"`
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
	ID               string             `json:"id"`
	Name             string             `json:"name,omitempty"`
	Connection       string             `json:"connection,omitempty"`
	FailureInjection bool               `json:"failure_injection,omitempty"`
	Scores           DashboardScores    `json:"scores"`
	Latest           SyncCounts         `json:"latest"`
	// LastSeen is the newest capture that carried this service.
	LastSeen  time.Time          `json:"last_seen,omitempty"`
	History   []DashboardCapture `json:"history,omitempty"`
	Failures  []DashboardFailure `json:"failures,omitempty"`
	Fixes     []DashboardFix     `json:"fixes,omitempty"`
	// Maturity is the connection's coverage declaration (issue #247): what
	// the service offers and what Aegis serves. Nil when the catalog has
	// no entry — test fixtures and the drift-injection harness.
	Maturity *types.ConnectionMaturity `json:"maturity,omitempty"`
	// MaturityGrade is the derived coverage mark, shown beside the
	// operational scores and deliberately not mixed into them.
	MaturityGrade *types.MaturityGrade `json:"maturity_grade,omitempty"`
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
	WindowStart time.Time `json:"window_start,omitempty"`
	WindowEnd   time.Time `json:"window_end,omitempty"`
	Services    []DashboardService `json:"services"`
	// Freshness is the latest capture's per-kind copy rollup.
	Freshness []CopyFreshness `json:"freshness,omitempty"`
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

// MaintenanceDashboardResult is the POST /v1/admin/maintenance/dashboard
// response: where the static site landed and what it contains.
type MaintenanceDashboardResult struct {
	Path     string   `json:"path"`
	Index    string   `json:"index"`
	Services []string `json:"services"`
	Captures int      `json:"captures"`
	Fixes    int      `json:"fixes"`
	// FixHistoryError is the scrape error, if the issue history degraded.
	FixHistoryError string `json:"fix_history_error,omitempty"`
}

// BuildMaintenanceDashboard projects one capture series, the configured
// service list, the connection maturity catalog, and scraped repo issues
// onto the static dashboard model. Services carry their whole capture
// history, failure events, scores, maturity coverage, and maintenance
// record; records that map to no service are dropped.
func BuildMaintenanceDashboard(series []MaintenanceSignals, metas []DashboardServiceMeta, issues []MaintenanceIssueRecord, maturity map[string]types.ConnectionMaturity, generatedAt time.Time) DashboardData {
	sorted := append([]MaintenanceSignals(nil), series...)
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
func buildDashboardService(id string, meta DashboardServiceMeta, series []MaintenanceSignals, fixes []DashboardFix, maturity types.ConnectionMaturity, now time.Time) DashboardService {
	svc := DashboardService{
		ID:               id,
		Name:             meta.Name,
		Connection:       meta.Connection,
		FailureInjection: meta.FailureInjection,
	}
	if maturity.Service != "" {
		m := maturity
		svc.Maturity = &m
		grade := types.GradeConnectionMaturity(m)
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

//go:embed web/maintenance_dashboard.html
var maintenanceDashboardHTML string

// dashboardFuncs are the formatting and classification helpers the templates
// render through, kept out of the markup so the HTML stays pure structure.
var dashboardFuncs = template.FuncMap{
	"fmtTime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.UTC().Format("2006-01-02 15:04 UTC")
	},
	"fmtPct": func(p *float64) string {
		if p == nil {
			return "—"
		}
		return fmt.Sprintf("%.0f%%", *p)
	},
	"scoreBadge": func(p *float64) string {
		switch {
		case p == nil:
			return "plain"
		case *p >= 90:
			return "good"
		case *p >= 70:
			return "warn"
		default:
			return "bad"
		}
	},
	"confBadge": func(c string) string {
		switch c {
		case "high":
			return "good"
		case "medium":
			return "info"
		case "low":
			return "warn"
		default:
			return "plain"
		}
	},
	"confLabel": func(c string) string {
		switch c {
		case "":
			return "none"
		default:
			return c
		}
	},
	"maturityBadge": func(level string) string {
		switch level {
		case "complete":
			return "good"
		case "substantial":
			return "info"
		case "partial":
			return "warn"
		default:
			return "bad"
		}
	},
	"maturityLabel": func(level string) string {
		if level == "" {
			return "—"
		}
		return level
	},
	"transportBadge": func(t types.Transport) string {
		switch t {
		case types.TransportAPI:
			return "good"
		case types.TransportProtocol:
			return "info"
		case types.TransportReverseEngineered:
			return "warn"
		default:
			return "plain"
		}
	},
	"transportLabel": func(t types.Transport) string {
		switch t {
		case types.TransportAPI:
			return "API"
		case types.TransportProtocol:
			return "protocol"
		case types.TransportReverseEngineered:
			return "reverse-engineered"
		case types.TransportArchive:
			return "export/archive"
		case "":
			return "—"
		default:
			return string(t)
		}
	},
	"maturityCoverage": func(g *types.MaturityGrade) string {
		if g == nil {
			return "—"
		}
		return fmt.Sprintf("%d/%d", g.Supported, g.Offered)
	},
	// maturityPct is the figure the level bands on: the lower of type
	// coverage and field coverage. Shown beside the level so a one-type
	// connection with thin fields does not read as a puzzle.
	"maturityPct": func(g *types.MaturityGrade) string {
		if g == nil {
			return "—"
		}
		pct := g.CoveragePct
		if g.FieldPct != nil && *g.FieldPct < pct {
			pct = *g.FieldPct
		}
		return fmt.Sprintf("%.0f%%", pct)
	},
	"maturityFieldCount": func(f types.DataMaturity) string {
		if len(f.Fields) == 0 {
			return "—"
		}
		pop := 0
		for _, c := range f.Fields {
			if c.Supported {
				pop++
			}
		}
		return fmt.Sprintf("%d/%d", pop, len(f.Fields))
	},
	"stateBadge": func(state string) string {
		switch state {
		case "fixed":
			return "good"
		case "open":
			return "warn"
		default:
			return "plain"
		}
	},
	"stateLabel": func(state string) string {
		switch state {
		case "fixed":
			return "fixed"
		case "open":
			return "open"
		default:
			return "closed"
		}
	},
	"kindLabel": func(kind string) string {
		switch kind {
		case "ticket":
			return "sync ticket"
		default:
			return "reported"
		}
	},
	"pageFile": servicePageFile,
	"sumDegraded": func(rows []CopyFreshness) int {
		n := 0
		for _, r := range rows {
			n += r.Stale + r.Missing
		}
		return n
	},
	"firstN": func(fixes []DashboardFix, n int) []DashboardFix {
		if len(fixes) <= n {
			return fixes
		}
		return fixes[:n]
	},
	"recordSummary": func(fixes []DashboardFix) string {
		if len(fixes) == 0 {
			return "—"
		}
		fixed, open, closed := 0, 0, 0
		for _, f := range fixes {
			switch f.State {
			case "fixed":
				fixed++
			case "open":
				open++
			default:
				closed++
			}
		}
		parts := make([]string, 0, 3)
		if fixed > 0 {
			parts = append(parts, fmt.Sprintf("%d fixed", fixed))
		}
		if open > 0 {
			parts = append(parts, fmt.Sprintf("%d open", open))
		}
		if closed > 0 {
			parts = append(parts, fmt.Sprintf("%d closed", closed))
		}
		return strings.Join(parts, " · ")
	},
}

// Dashboard write postures. The published site must be readable by others
// (the Pages upload fails otherwise), while the instance build under the
// data dir carries scrubbed failure text and stays private like the bundles.
const (
	DashboardPublishedFileMode = 0o644
	DashboardPublishedDirMode  = 0o755
	DashboardPrivateFileMode   = 0o600
	DashboardPrivateDirMode    = 0o700
)

// WriteMaintenanceDashboard renders data as a static site under dir:
// index.html plus one page per service under services/. Returns the index
// path. Files are written fileMode in dirMode directories — use the
// DashboardPrivate* posture for instance builds and DashboardPublished*
// for the release-published site.
func WriteMaintenanceDashboard(dir string, data DashboardData, fileMode, dirMode os.FileMode) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("empty dashboard directory")
	}
	tmpl, err := template.New("dashboard").Funcs(dashboardFuncs).Parse(maintenanceDashboardHTML)
	if err != nil {
		return "", fmt.Errorf("parse dashboard template: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services"), dirMode); err != nil {
		return "", err
	}
	// MkdirAll and OpenFile leave existing modes untouched and creation
	// modes are umask-masked; the posture must hold either way (the Pages
	// upload fails on non-world-readable output).
	for _, d := range []string{dir, filepath.Join(dir, "services")} {
		if err := os.Chmod(d, dirMode); err != nil {
			return "", err
		}
	}
	index := filepath.Join(dir, "index.html")
	if err := renderDashboardFile(tmpl, index, "index", data, fileMode); err != nil {
		return "", err
	}
	for _, svc := range data.Services {
		page := DashboardPage{
			GeneratedAt: data.GeneratedAt,
			Hostname:    data.Hostname,
			Version:     data.Version,
			Commit:      data.Commit,
			Captures:    data.Captures,
			Service:     svc,
		}
		name := servicePageFile(svc.ID) + ".html"
		if err := renderDashboardFile(tmpl, filepath.Join(dir, "services", name), "service", page, fileMode); err != nil {
			return "", err
		}
	}
	return index, nil
}

func renderDashboardFile(tmpl *template.Template, path, name string, v any, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	// Force the mode: OpenFile is umask-masked on create and keeps the old
	// mode when truncating an existing file.
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := tmpl.ExecuteTemplate(f, name, v); err != nil {
		f.Close()
		return fmt.Errorf("render %s: %w", name, err)
	}
	return f.Close()
}

// servicePageFile maps a service id to a safe page filename. Ids that
// needed rewriting get a short hash suffix so two ids cannot collide on one
// page.
func servicePageFile(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" {
		s = "service"
	}
	if s != id {
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		s = fmt.Sprintf("%s-%08x", s, h.Sum32())
	}
	return s
}

// dashboardServiceManifestEntry is one row of the services manifest the
// release-published build carries: CI has no instance config to project
// page identity from.
type dashboardServiceManifestEntry struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	Connection       string `json:"connection,omitempty"`
	FailureInjection bool   `json:"failure_injection,omitempty"`
}

// LoadDashboardServiceMetas reads a services manifest (JSON array of
// {id, name, connection, failure_injection}) for the published build: the
// pages to show and which of them back onto the drift-injection harness.
func LoadDashboardServiceMetas(path string) ([]DashboardServiceMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []dashboardServiceManifestEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse services manifest: %w", err)
	}
	metas := make([]DashboardServiceMeta, 0, len(entries))
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		metas = append(metas, DashboardServiceMeta(e))
	}
	return metas, nil
}

// dashboardServiceMetas projects configured services onto page identity,
// flagging the drift-injection harness (fake upstream) so its page can say
// why it looks unstable.
func (s *Server) dashboardServiceMetas() []DashboardServiceMeta {
	if s.config == nil {
		return nil
	}
	snapshots := s.config.ServiceSnapshots()
	metas := make([]DashboardServiceMeta, 0, len(snapshots))
	for _, svc := range snapshots {
		metas = append(metas, DashboardServiceMeta{
			ID:               svc.ID,
			Name:             svc.Name,
			Connection:       s.config.connectionIDForService(svc.ID),
			FailureInjection: strings.Contains(svc.Endpoint, fakeUpstreamMount),
		})
	}
	return metas
}

// HandleAdminMaintenanceDashboard builds the static sync-maintenance
// dashboard (POST): capture history, per-service pages, and the scraped
// maintenance record, written under <data-dir>/maintenance/dashboard/.
// Admin-scoped like the rest of /v1/admin — pages name services and failure
// modes and must not leak to agent credentials (issue #181). The issue
// scrape is fail-open: without it the build still lands, minus history.
func (s *Server) HandleAdminMaintenanceDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.dataDir == "" {
		writeJSONError(w, http.StatusInternalServerError, "data directory not configured")
		return
	}
	maintDir := filepath.Join(s.dataDir, dataDirMaintenanceSubdir)
	bundles, err := loadSignalBundles(maintDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load bundles failed: "+err.Error())
		return
	}
	series := make([]MaintenanceSignals, 0, len(bundles)+1)
	for _, b := range bundles {
		series = append(series, b.MaintenanceSignals)
	}
	// The live snapshot lands as the final capture so the build always
	// reflects now, not only the last scheduled capture.
	series = append(series, s.CollectMaintenanceSignals())

	var records []MaintenanceIssueRecord
	fixErr := ""
	if s.maintenanceIssues != nil {
		records, err = s.maintenanceIssues.ListIssues(r.Context())
		if err != nil {
			fixErr = err.Error()
		}
	}
	data := BuildMaintenanceDashboard(series, s.dashboardServiceMetas(), records, MaturityByService(), time.Now().UTC())
	data.FixHistoryError = fixErr

	outDir := filepath.Join(maintDir, "dashboard")
	index, err := WriteMaintenanceDashboard(outDir, data, DashboardPrivateFileMode, DashboardPrivateDirMode)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "dashboard build failed: "+err.Error())
		return
	}
	services := make([]string, 0, len(data.Services))
	for _, svc := range data.Services {
		services = append(services, svc.ID)
	}
	writeJSON(w, MaintenanceDashboardResult{
		Path:            outDir,
		Index:           index,
		Services:        services,
		Captures:        data.Captures,
		Fixes:           len(data.Fixes),
		FixHistoryError: fixErr,
	})
}
