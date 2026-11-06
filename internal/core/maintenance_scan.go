package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/shared"
)

// Maintenance scanner (issue #86, phase 2): reads the signal bundles a
// capture leaves under <data-dir>/maintenance/, clusters failures by
// normalized signature, and tickets recurrent evidence as GitHub issues via
// an IssueTracker — there is no local defect store. A local scheduled job
// triggers it (POST /v1/admin/maintenance/scan); the scanner itself only
// reads paths, so it can move to cloud compute later without touching
// instance IPC.
//
// Two reproducibility gates, one per signal nature:
//   - Failure clusters are event-like: ticketed only when the same signature
//     recurs across scanRecurrenceMin distinct bundles. One-off flake stays a
//     metric.
//   - Freshness rollups are state-like: the latest bundle showing
//     stale/missing copies for a kind is the evidence, no recurrence needed.
//
// Ticket updates never clobber human/agent triage: Title, Tier, Fixture,
// ReproTest, ReproState, Expected, and Actual survive a rescan.

const (
	// scanRecurrenceMin is how many distinct bundles a failure signature must
	// appear in before it is ticketed.
	scanRecurrenceMin = 2
	// scanObservationCap bounds per-ticket observation history.
	scanObservationCap = 10
	// scanErrorShapeCap truncates normalized error shapes.
	scanErrorShapeCap = 120
	// scanTitleErrorCap bounds the error excerpt in a ticket title.
	scanTitleErrorCap = 80
)

// Repro-state values on a ticket (docs/sync-maintenance.md).
const (
	ReproStateNeedsFixture = "needs-fixture"
	ReproStateReady        = "ready"
)

// Review tier default: scanner output is triaged for connector-logic fixes
// until a human reclassifies it.
const scanDefaultTier = "connector-logic"

// MaintenanceTicket is the structured ticket contract (issue body evidence
// block), not prose — see docs/sync-maintenance.md.
type MaintenanceTicket struct {
	Signature  string `json:"signature"`
	Title      string `json:"title"`
	Service    string `json:"service,omitempty"`
	Kind       string `json:"kind,omitempty"`
	JobType    string `json:"job_type,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
	// ReproState gates phase-3 pickup: needs-fixture until the repro bundle
	// (fixture + red test) is attached, then ready.
	ReproState string    `json:"repro_state"`
	Fixture    string    `json:"fixture,omitempty"`
	ReproTest  string    `json:"repro_test,omitempty"`
	Expected   string    `json:"expected,omitempty"`
	Actual     string    `json:"actual,omitempty"`
	Tier       string    `json:"tier"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	Count      int       `json:"count"`
	// Observations is the most recent bundle evidence, newest last.
	Observations []MaintenanceObservation `json:"observations,omitempty"`
}

// MaintenanceObservation is one bundle's evidence for a ticket. Error is the
// already-scrubbed excerpt from the bundle.
type MaintenanceObservation struct {
	Bundle     string    `json:"bundle"`
	CapturedAt time.Time `json:"captured_at"`
	Version    string    `json:"version,omitempty"`
	Commit     string    `json:"commit,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// MaintenanceScanReport summarizes one scan pass.
type MaintenanceScanReport struct {
	ScannedBundles int                        `json:"scanned_bundles"`
	Clusters       int                        `json:"clusters"`
	Tickets        []MaintenanceScanTicketRef `json:"tickets"`
}

// MaintenanceScanTicketRef says what the scan did for one signature.
type MaintenanceScanTicketRef struct {
	Signature string `json:"signature"`
	Action    string `json:"action"` // opened | updated | reopened | skipped | disabled | error
	Number    int    `json:"number,omitempty"`
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
}

type scanCluster struct {
	signature  string
	service    string
	kind       string
	jobType    string
	errorClass string
	obs        []MaintenanceObservation
}

// scanURLRe et al. collapse an error line to its stable shape: two failures
// from the same bug (different ids, URLs, status codes) share a signature.
var (
	scanQuotedRe = regexp.MustCompile(`"[^"]*"`)
	scanURLRe    = regexp.MustCompile(`https?://\S+`)
	scanHexRe    = regexp.MustCompile(`\b[0-9a-f]{8,}\b`)
	scanDigitsRe = regexp.MustCompile(`[0-9]+`)
)

// normalizeErrorShape reduces error text to the stable cluster shape.
func normalizeErrorShape(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = scanQuotedRe.ReplaceAllString(s, "str")
	s = scanURLRe.ReplaceAllString(s, "url")
	s = scanHexRe.ReplaceAllString(s, "id")
	s = scanDigitsRe.ReplaceAllString(s, "#")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > scanErrorShapeCap {
		s = string(r[:scanErrorShapeCap])
	}
	return s
}

// failureSignature is the stable cluster key for a failed sync job.
func failureSignature(f MaintenanceFailure) string {
	svc := f.Service
	if svc == "" {
		svc = "-"
	}
	jt := f.JobType
	if jt == "" {
		jt = f.Kind
	}
	if jt == "" {
		jt = "-"
	}
	class := f.ErrorClass
	if class == "" {
		class = "-"
	}
	return svc + "|" + jt + "|" + class + "|" + normalizeErrorShape(f.Error)
}

// freshnessSignature is the stable cluster key for one kind's copy
// freshness degradation. Shape is fixed: counts are state, not identity.
func freshnessSignature(kind string) string {
	return kind + "|freshness|degraded|stale-or-missing"
}

// ScanMaintenanceDir runs one scan pass over the signal bundles in dir and
// upserts GitHub issues for ticketable clusters. Rescans are idempotent:
// counts and observation history are recomputed from the bundles on disk,
// and the tracker preserves triage (labels, fixture/test slots) on updates.
// A nil tracker scans only, marking ticketable clusters "disabled".
func ScanMaintenanceDir(ctx context.Context, dir string, tracker IssueTracker) (*MaintenanceScanReport, error) {
	report := &MaintenanceScanReport{Tickets: []MaintenanceScanTicketRef{}}
	bundles, err := loadSignalBundles(dir)
	if err != nil {
		return nil, err
	}
	report.ScannedBundles = len(bundles)
	clusters := map[string]*scanCluster{}

	for _, b := range bundles {
		if b.Sync == nil {
			continue
		}
		for _, f := range b.Sync.Failures {
			sig := failureSignature(f)
			c := clusters[sig]
			if c == nil {
				c = &scanCluster{
					signature:  sig,
					service:    f.Service,
					kind:       f.Kind,
					jobType:    f.JobType,
					errorClass: f.ErrorClass,
				}
				clusters[sig] = c
			}
			c.obs = append(c.obs, MaintenanceObservation{
				Bundle:     b.sourceName,
				CapturedAt: b.CapturedAt,
				Version:    b.Version,
				Commit:     b.Commit,
				Error:      f.Error,
			})
		}
	}

	// Freshness findings come from the latest bundle only: the rollup is a
	// current state, not an event stream.
	if len(bundles) > 0 {
		latest := bundles[len(bundles)-1]
		for _, fr := range latest.Freshness {
			if fr.Stale+fr.Missing == 0 {
				continue
			}
			sig := freshnessSignature(fr.Kind)
			c := clusters[sig]
			if c == nil {
				c = &scanCluster{signature: sig, kind: fr.Kind}
				clusters[sig] = c
			}
			c.obs = append(c.obs, MaintenanceObservation{
				Bundle:     latest.sourceName,
				CapturedAt: latest.CapturedAt,
				Version:    latest.Version,
				Commit:     latest.Commit,
				Error:      fmt.Sprintf("stale=%d missing=%d", fr.Stale, fr.Missing),
			})
		}
	}

	report.Clusters = len(clusters)
	sigs := make([]string, 0, len(clusters))
	for sig := range clusters {
		sigs = append(sigs, sig)
	}
	sort.Strings(sigs)
	for _, sig := range sigs {
		c := clusters[sig]
		isFreshness := strings.HasSuffix(sig, "|freshness|degraded|stale-or-missing")
		if !isFreshness && len(c.obs) < scanRecurrenceMin {
			report.Tickets = append(report.Tickets, MaintenanceScanTicketRef{
				Signature: sig,
				Action:    "skipped",
			})
			continue
		}
		ref := MaintenanceScanTicketRef{Signature: sig}
		if tracker == nil {
			ref.Action = "disabled"
			report.Tickets = append(report.Tickets, ref)
			continue
		}
		issue, action, err := tracker.UpsertTicket(ctx, clusterTicket(c))
		if err != nil {
			// Fail-open: one tracker error must not sink the scan pass.
			ref.Action = "error"
			ref.Error = err.Error()
		} else {
			ref.Action = action
			ref.Number = issue.Number
			ref.URL = issue.URL
		}
		report.Tickets = append(report.Tickets, ref)
	}
	return report, nil
}

// loadSignalBundles reads signals-*.json under dir, oldest first.
func loadSignalBundles(dir string) ([]*signalBundle, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "signals-*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]*signalBundle, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var sig MaintenanceSignals
		if err := json.Unmarshal(raw, &sig); err != nil {
			return nil, fmt.Errorf("parse signal bundle %s: %w", p, err)
		}
		out = append(out, &signalBundle{MaintenanceSignals: sig, sourceName: filepath.Base(p)})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CapturedAt.Before(out[j].CapturedAt)
	})
	return out, nil
}

// signalBundle is a bundle plus the file it came from.
type signalBundle struct {
	MaintenanceSignals
	sourceName string
}

// clusterTicket builds the ticket contract for one cluster: scanner-generated
// fields recomputed from the bundles on disk. Triage fields arrive empty and
// are preserved by the IssueTracker on updates.
func clusterTicket(c *scanCluster) MaintenanceTicket {
	obs := c.obs
	if len(obs) > scanObservationCap {
		obs = obs[len(obs)-scanObservationCap:]
	}
	return MaintenanceTicket{
		Signature:    c.signature,
		Title:        clusterTitle(c),
		Service:      c.service,
		Kind:         c.kind,
		JobType:      c.jobType,
		ErrorClass:   c.errorClass,
		ReproState:   ReproStateNeedsFixture,
		Tier:         scanDefaultTier,
		FirstSeen:    obs[0].CapturedAt,
		LastSeen:     obs[len(obs)-1].CapturedAt,
		Count:        len(c.obs),
		Observations: obs,
	}
}

func clusterTitle(c *scanCluster) string {
	if strings.HasSuffix(c.signature, "|freshness|degraded|stale-or-missing") {
		return fmt.Sprintf("freshness degraded for %s copies", c.kind)
	}
	label := c.service
	if label == "" {
		label = c.kind
	}
	if label == "" {
		label = "sync"
	}
	excerpt := ""
	if len(c.obs) > 0 {
		excerpt = c.obs[len(c.obs)-1].Error
	}
	if r := []rune(excerpt); len(r) > scanTitleErrorCap {
		excerpt = string(r[:scanTitleErrorCap]) + "…"
	}
	if excerpt == "" {
		return fmt.Sprintf("%s %s failed (%s)", label, c.jobType, c.errorClass)
	}
	return fmt.Sprintf("%s %s failed: %s", label, c.jobType, excerpt)
}

// HandleAdminMaintenanceScan runs one scan pass over the signal bundles on
// disk and upserts GitHub-issue tickets (POST). Admin-scoped like the rest of
// /v1/admin — tickets name services and failure modes (issue #181).
func (s *Server) HandleAdminMaintenanceScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.dataDir == "" {
		shared.WriteJSONError(w, http.StatusInternalServerError, "data directory not configured")
		return
	}
	report, err := ScanMaintenanceDir(r.Context(), filepath.Join(s.dataDir, dataDirMaintenanceSubdir), s.maintenanceIssues)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
		return
	}
	shared.WriteJSON(w, report)
}
