package core

import (
	_ "embed"
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
// derived from the capture series alone so an offline bundle collection
// scores identically to a live instance build.
type DashboardScores struct {
	// Success is succeeded/(succeeded+failed) over recorded finished work,
	// percent 0-100. Nil when nothing finished.
	Success *float64 `json:"success,omitempty"`
	// Stability is the share of observed capture intervals with no new
	// failed-job events, percent 0-100. Nil when fewer than two captures.
	Stability *float64 `json:"stability,omitempty"`
	// Confidence bands how much evidence backs the marks:
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
	ReproState string    `json:"repro_state,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	ClosedAt   time.Time `json:"closed_at,omitempty"`
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
// service list, and scraped repo issues onto the static dashboard model.
// Services carry their whole capture history, failure events, scores, and
// maintenance record; records that map to no service are dropped.
func BuildMaintenanceDashboard(series []MaintenanceSignals, metas []DashboardServiceMeta, issues []MaintenanceIssueRecord, generatedAt time.Time) DashboardData {
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
		data.Services = append(data.Services, buildDashboardService(id, metaByID[id], sorted, fixes))
	}
	return data
}

// buildDashboardService assembles one service page model from the series.
func buildDashboardService(id string, meta DashboardServiceMeta, series []MaintenanceSignals, fixes []DashboardFix) DashboardService {
	svc := DashboardService{
		ID:               id,
		Name:             meta.Name,
		Connection:       meta.Connection,
		FailureInjection: meta.FailureInjection,
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
	svc.Scores = scoreDashboardService(svc.History, svc.Failures)

	for _, fix := range fixes {
		if fix.Service == id {
			svc.Fixes = append(svc.Fixes, fix)
		}
	}
	return svc
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

// scoreDashboardService derives the success / stability / confidence marks.
//
//	success    — succeeded/(succeeded+failed) from the latest cumulative
//	             rollup: how recorded finished work ends.
//	stability  — share of capture intervals with no new failed-job events:
//	             how consistently the service stays out of trouble.
//	confidence — evidence band from finished volume and capture count, so
//	             a lucky streak on two data points cannot read as high.
func scoreDashboardService(history []DashboardCapture, failures []DashboardFailure) DashboardScores {
	var s DashboardScores
	if len(history) == 0 && len(failures) == 0 {
		s.Confidence = "none"
		return s
	}
	finished := 0
	if len(history) > 0 {
		latest := history[len(history)-1]
		finished = latest.Counts.Succeeded + latest.Counts.Failed
		if finished > 0 {
			pct := 100 * float64(latest.Counts.Succeeded) / float64(finished)
			s.Success = &pct
		}
		if len(history) >= 2 {
			clean, intervals := 0, 0
			for i := 1; i < len(history); i++ {
				intervals++
				if countFailuresBetween(failures, history[i-1].CapturedAt, history[i].CapturedAt) == 0 {
					clean++
				}
			}
			pct := 100 * float64(clean) / float64(intervals)
			s.Stability = &pct
		}
		s.Confidence = confidenceBand(finished, len(history))
		return s
	}
	// Failure events without a per-service rollup (older bundles): the
	// events are evidence, the marks stay undetermined.
	s.Confidence = "low"
	return s
}

// confidenceBand grades how much evidence backs the marks: finished work
// volume plus observed captures. A single snapshot can never score high.
func confidenceBand(finished, captures int) string {
	switch {
	case finished >= 20 && captures >= 4:
		return "high"
	case finished >= 5 && captures >= 2:
		return "medium"
	case finished > 0 || captures > 0:
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
			return "fix"
		}
	},
	"pageFile": servicePageFile,
	"addInt":   func(a, b int) int { return a + b },
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
		fixed, open := 0, 0
		for _, f := range fixes {
			if f.State == "open" {
				open++
			} else {
				fixed++
			}
		}
		switch {
		case open == 0:
			return fmt.Sprintf("%d fixed", fixed)
		case fixed == 0:
			return fmt.Sprintf("%d open", open)
		default:
			return fmt.Sprintf("%d fixed · %d open", fixed, open)
		}
	},
}

// WriteMaintenanceDashboard renders data as a static site under dir:
// index.html plus one page per service under services/. Returns the index
// path. Files are written 0600 in 0700 directories, matching the bundle
// posture: the pages name services and failure modes.
func WriteMaintenanceDashboard(dir string, data DashboardData) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("empty dashboard directory")
	}
	tmpl, err := template.New("dashboard").Funcs(dashboardFuncs).Parse(maintenanceDashboardHTML)
	if err != nil {
		return "", fmt.Errorf("parse dashboard template: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o700); err != nil {
		return "", err
	}
	index := filepath.Join(dir, "index.html")
	if err := renderDashboardFile(tmpl, index, "index", data); err != nil {
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
		if err := renderDashboardFile(tmpl, filepath.Join(dir, "services", name), "service", page); err != nil {
			return "", err
		}
	}
	return index, nil
}

func renderDashboardFile(tmpl *template.Template, path, name string, v any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
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
	data := BuildMaintenanceDashboard(series, s.dashboardServiceMetas(), records, time.Now().UTC())
	data.FixHistoryError = fixErr

	outDir := filepath.Join(maintDir, "dashboard")
	index, err := WriteMaintenanceDashboard(outDir, data)
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
