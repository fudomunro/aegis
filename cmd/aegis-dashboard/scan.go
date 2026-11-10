package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Maintenance scanner (issue #86, phase 2; issue #344): reads the signal
// bundles a capture leaves under <data-dir>/maintenance/, clusters failures
// by normalized signature, and tickets recurrent evidence as GitHub issues
// via a ticketTracker — there is no local defect store. A local scheduled
// job triggers it outside the application (`aegis-dashboard scan`); the
// scanner only reads paths, so it can move to cloud compute later without
// touching instance IPC.
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
	// scanDefaultTier is the review tier scanner output lands in: triaged
	// for connector-logic fixes until a human reclassifies it.
	scanDefaultTier = "connector-logic"
)

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
func failureSignature(f BundleFailure) string {
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

// scanBundles runs one scan pass over the signal bundles in dir and upserts
// GitHub issues for ticketable clusters. Rescans are idempotent: counts and
// observation history are recomputed from the bundles on disk, and the
// tracker preserves triage (labels, fixture/test slots) on updates. A nil
// tracker scans only, marking ticketable clusters "disabled".
func scanBundles(ctx context.Context, dir string, tracker ticketTracker) (*MaintenanceScanReport, error) {
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

// clusterTicket builds the ticket contract for one cluster: scanner-generated
// fields recomputed from the bundles on disk. Triage fields arrive empty and
// are preserved by the tracker on updates.
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
		ReproState:   reproStateNeedsFixture,
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
