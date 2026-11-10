package main

import (
	_ "embed"
	"fmt"
	"hash/fnv"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

//go:embed maintenance_dashboard.html
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
	"transportBadge": func(t connectors.Transport) string {
		switch t {
		case connectors.TransportAPI:
			return "good"
		case connectors.TransportProtocol:
			return "info"
		case connectors.TransportReverseEngineered:
			return "warn"
		default:
			return "plain"
		}
	},
	"transportLabel": func(t connectors.Transport) string {
		switch t {
		case connectors.TransportAPI:
			return "API"
		case connectors.TransportProtocol:
			return "protocol"
		case connectors.TransportReverseEngineered:
			return "reverse-engineered"
		case connectors.TransportArchive:
			return "export/archive"
		case "":
			return "—"
		default:
			return string(t)
		}
	},
	"maturityCoverage": func(g *connectors.MaturityGrade) string {
		if g == nil {
			return "—"
		}
		return fmt.Sprintf("%d/%d", g.Supported, g.Offered)
	},
	// maturityPct is the figure the level bands on: the lower of type
	// coverage and field coverage. Shown beside the level so a one-type
	// connection with thin fields does not read as a puzzle.
	"maturityPct": func(g *connectors.MaturityGrade) string {
		if g == nil {
			return "—"
		}
		pct := g.CoveragePct
		if g.FieldPct != nil && *g.FieldPct < pct {
			pct = *g.FieldPct
		}
		return fmt.Sprintf("%.0f%%", pct)
	},
	"maturityFieldCount": func(f connectors.KindMaturity) string {
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
	"sumDegraded": func(rows []Freshness) int {
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
// (the Pages upload fails otherwise), while an instance build under the data
// dir carries scrubbed failure text and stays private like the bundles.
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
