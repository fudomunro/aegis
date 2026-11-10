package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

// GitHub issue history for the sync-maintenance dashboard (issue #239,
// issue #335). The tool scrapes the repo's issue record with the gh CLI —
// the same transport as the ticket surface — so each service page can show
// historical fixes alongside the signal history. A scrape failure fails the
// build by default (an empty record must not overwrite a good previous
// dashboard); -allow-scrape-failure degrades to a noted gap instead.
//
// Two kinds of record material exist:
//   - Tickets (label sync-failure): the loop's own issues, shown in full
//     lifecycle (open with repro state, or fixed when closed completed).
//   - Reported issues, behind an explicit allowlist — a `bug` label or
//     closure by a fix-convention PR — that names a configured service:
//     open problems stay visible and count against the fix rate, completed
//     ones read as historical fixes. Anything else is dropped — the page is
//     a maintenance record, not an issue mirror.
//
// The tool reads the ticket contract (evidence block and signature marker,
// documented in docs/sync-maintenance.md) as external format: the
// application code writes it, this package only consumes it.

// Ticket contract vocabulary the record scrape reads. Mirrors the
// scanner-side writers in internal/core — the format is the contract.
const (
	syncFailureLabel      = "sync-failure"
	needsFixtureLabel     = "needs-fixture"
	readyLabel            = "ready"
	reproStateNeedsFixture = "needs-fixture"
	reproStateReady        = "ready"

	signatureMarkerPrefix = "<!-- aegis-signature: "
	evidenceStartMarker   = "<!-- aegis-evidence:start -->"
	evidenceEndMarker     = "<!-- aegis-evidence:end -->"
)

// MaintenanceIssueRecord is one repo issue scraped for the dashboard's
// maintenance record. Raw bodies are parsed and dropped at scrape time so
// triage prose never reaches the rendered pages.
type MaintenanceIssueRecord struct {
	Number      int       `json:"number"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	State       string    `json:"state"`        // open | closed (lowercased)
	StateReason string    `json:"state_reason"` // completed | not_planned | "" (lowercased)
	Labels      []string  `json:"labels,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ClosedAt    time.Time `json:"closed_at,omitempty"`
	// IsTicket marks sync-maintenance loop tickets.
	IsTicket bool `json:"is_ticket,omitempty"`
	// ClosedByFix marks issues a merged fix-convention PR closes (fix:
	// title or fix/ branch) — the allowlist evidence for non-ticket items.
	ClosedByFix bool `json:"closed_by_fix,omitempty"`
	// TicketService is the service the ticket contract attributes the issue
	// to (evidence block, falling back to the signature marker).
	TicketService string `json:"ticket_service,omitempty"`
	// ReproState mirrors the ticket's label state for display.
	ReproState string `json:"repro_state,omitempty"`
	// FirstSeen is the failure onset from the ticket evidence block.
	FirstSeen time.Time `json:"first_seen,omitempty"`
}

// scrapeIssueLimit caps one gh fetch. Hitting the cap means the record is
// truncated — the oldest history would quietly drop out — so the scrape
// fails instead of publishing a partial view.
const scrapeIssueLimit = 2000

// issueScraper reads the repo's issue history via the gh CLI. run is the
// exec seam for tests; it receives gh arguments without the binary name and
// returns stdout.
type issueScraper struct {
	repo string
	run  func(ctx context.Context, args ...string) (string, error)
}

// newIssueScraper builds a scraper for the given owner/name repo.
func newIssueScraper(repo string) *issueScraper {
	return &issueScraper{
		repo: repo,
		run: func(ctx context.Context, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, "gh", args...).CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args[:2], " "), err, strings.TrimSpace(string(out)))
			}
			return string(out), nil
		},
	}
}

// ghLabel is one label object on an issue. gh emits labels as objects
// ({"name": …}), not bare name strings.
type ghLabel struct {
	Name string `json:"name"`
}

// ghListIssue is the subset of `gh issue list --json` we rely on.
type ghListIssue struct {
	Number      int       `json:"number"`
	State       string    `json:"state"`
	StateReason string    `json:"stateReason"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Labels      []ghLabel `json:"labels"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"createdAt"`
	ClosedAt    time.Time `json:"closedAt"`
}

// labelNames extracts the label names for matching and repro-state lookup.
func (i *ghListIssue) labelNames() []string {
	out := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		out = append(out, l.Name)
	}
	return out
}

// ListIssues scrapes the repo's issue history via the gh CLI. A second call
// reads merged PRs so non-ticket record items can prove they were fixed
// (closed by a fix-convention PR).
func (g *issueScraper) ListIssues(ctx context.Context) ([]MaintenanceIssueRecord, error) {
	out, err := g.run(ctx, "issue", "list", "-R", g.repo,
		"--state", "all", "--limit", strconv.Itoa(scrapeIssueLimit),
		"--json", "number,title,url,state,stateReason,labels,createdAt,closedAt,body")
	if err != nil {
		return nil, err
	}
	var issues []ghListIssue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, err
	}
	if len(issues) >= scrapeIssueLimit {
		return nil, fmt.Errorf("issue history truncated at %d issues; raise the scrape limit", scrapeIssueLimit)
	}
	fixClosed, err := g.listFixClosedIssues(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]MaintenanceIssueRecord, 0, len(issues))
	for i := range issues {
		rec := issueRecord(&issues[i])
		rec.ClosedByFix = fixClosed[rec.Number]
		recs = append(recs, rec)
	}
	return recs, nil
}

// ghListPR is the subset of `gh pr list --json` the allowlist needs.
type ghListPR struct {
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	Closing     []struct {
		Number int `json:"number"`
	} `json:"closingIssuesReferences"`
}

// listFixClosedIssues returns the issue numbers a merged fix-convention PR
// closes (fix: title or fix/ branch).
func (g *issueScraper) listFixClosedIssues(ctx context.Context) (map[int]bool, error) {
	out, err := g.run(ctx, "pr", "list", "-R", g.repo,
		"--state", "merged", "--limit", strconv.Itoa(scrapeIssueLimit),
		"--json", "title,headRefName,closingIssuesReferences")
	if err != nil {
		return nil, err
	}
	var prs []ghListPR
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}
	if len(prs) >= scrapeIssueLimit {
		return nil, fmt.Errorf("merged PR history truncated at %d; raise the scrape limit", scrapeIssueLimit)
	}
	fixClosed := map[int]bool{}
	for _, pr := range prs {
		if !isFixPR(pr.Title, pr.HeadRefName) {
			continue
		}
		for _, c := range pr.Closing {
			fixClosed[c.Number] = true
		}
	}
	return fixClosed, nil
}

// isFixPR reports whether a merged PR follows the repo's fix convention.
func isFixPR(title, headRefName string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(title)), "fix:") ||
		strings.HasPrefix(strings.ToLower(headRefName), "fix/")
}

// issueRecord flattens one scraped issue: parse the ticket contract (then
// drop the body) and normalize state casing.
func issueRecord(i *ghListIssue) MaintenanceIssueRecord {
	rec := MaintenanceIssueRecord{
		Number:      i.Number,
		URL:         i.URL,
		Title:       i.Title,
		State:       strings.ToLower(i.State),
		StateReason: strings.ToLower(i.StateReason),
		Labels:      i.labelNames(),
		CreatedAt:   i.CreatedAt,
		ClosedAt:    i.ClosedAt,
	}
	if hasLabel(rec.Labels, syncFailureLabel) {
		rec.IsTicket = true
		rec.ReproState = reproStateFromLabels(rec.Labels, "")
	}
	if ev := parseTicketEvidence(i.Body); ev != nil {
		rec.IsTicket = true
		rec.TicketService = ev.Service
		if rec.TicketService == "" {
			rec.TicketService = signatureService(ev.Signature)
		}
		rec.FirstSeen = ev.FirstSeen
		if rec.ReproState == "" {
			rec.ReproState = reproStateFromLabels(rec.Labels, ev.ReproState)
		}
	}
	if rec.TicketService == "" {
		rec.TicketService = signatureService(signatureFromBody(i.Body))
	}
	return rec
}

// ticketEvidence is the read-only view of the ticket contract's evidence
// block — the fields the record needs. The scanner owns the full contract;
// this is a consumer-side projection.
type ticketEvidence struct {
	Signature  string    `json:"signature"`
	Service    string    `json:"service"`
	ReproState string    `json:"repro_state"`
	FirstSeen  time.Time `json:"first_seen"`
}

// parseTicketEvidence extracts the ticket contract from an evidence block.
// Returns nil when the block is missing or unparseable.
func parseTicketEvidence(body string) *ticketEvidence {
	start := strings.Index(body, evidenceStartMarker)
	end := strings.Index(body, evidenceEndMarker)
	if start < 0 || end < 0 || end < start {
		return nil
	}
	block := body[start+len(evidenceStartMarker) : end]
	block = strings.TrimSpace(block)
	block = strings.TrimPrefix(block, "```json")
	block = strings.TrimSuffix(strings.TrimSpace(block), "```")
	var ev ticketEvidence
	if err := json.Unmarshal([]byte(strings.TrimSpace(block)), &ev); err != nil {
		return nil
	}
	return &ev
}

// signatureMarker is the dedup key embedded in every ticket body.
func signatureMarker(signature string) string {
	return signatureMarkerPrefix + signature + " -->"
}

// signatureService returns the service prefix of a failure signature
// ("spotify|canary|terminal|…" → "spotify").
func signatureService(sig string) string {
	svc, _, ok := strings.Cut(sig, "|")
	if !ok {
		return ""
	}
	return strings.TrimSpace(svc)
}

// signatureFromBody pulls the dedup marker value out of a ticket body.
func signatureFromBody(body string) string {
	i := strings.Index(body, signatureMarkerPrefix)
	if i < 0 {
		return ""
	}
	rest := body[i+len(signatureMarkerPrefix):]
	j := strings.Index(rest, " -->")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// reproStateFromLabels lets the issue labels decide repro_state; without
// either label the fallback stands.
func reproStateFromLabels(labels []string, fallback string) string {
	if hasLabel(labels, readyLabel) {
		return reproStateReady
	}
	if hasLabel(labels, needsFixtureLabel) {
		return reproStateNeedsFixture
	}
	if fallback == "" {
		return reproStateNeedsFixture
	}
	return fallback
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}

// matchServiceInTitle attributes an issue title to a known service. Every
// id and alias-phrase candidate is ranked by match length — longest wins —
// so "Google Photos" cannot fall to the bare "google" id and "YouTube
// Music" beats a stray "spotify". Returns "" when the title names no known
// service. Alias phrases and common-word ids are connector-declared
// (issue #254); only the ranking lives here.
func matchServiceInTitle(title string, ids []string) string {
	lower := strings.ToLower(title)
	commonWordIDs := connectors.CommonWordIDs()
	best, bestLen := "", 0
	consider := func(id string, n int) {
		if id == "" {
			return
		}
		if n > bestLen || (n == bestLen && id < best) {
			best, bestLen = id, n
		}
	}
	for _, id := range ids {
		if id == "" || commonWordIDs[strings.ToLower(id)] {
			continue
		}
		if strings.Contains(lower, strings.ToLower(id)) {
			consider(id, len(id))
		}
	}
	for a, canonical := range connectors.ServiceTitleAliases() {
		if strings.Contains(lower, a) {
			consider(resolveServiceID(canonical, ids), len(a))
		}
	}
	return best
}

// resolveServiceID picks the dashboard service a canonical alias names.
// Exact id only: a substring guess would misattribute (the contacts id
// "google" must not swallow every "google-*" page).
func resolveServiceID(canonical string, ids []string) string {
	for _, id := range ids {
		if strings.EqualFold(id, canonical) {
			return id
		}
	}
	return ""
}

// mapIssueRecord projects a scraped issue onto the dashboard's maintenance
// record. ok is false when the issue is not record material for any service.
// Non-ticket items pass an explicit allowlist — a `bug` label or closure by
// a fix-convention PR — so published marks never count a design ask or a
// test task as a problem; allowlisted problems stay visible while open and
// count against the fix rate.
func mapIssueRecord(rec MaintenanceIssueRecord, ids []string) (DashboardFix, bool) {
	if rec.IsTicket {
		svc := rec.TicketService
		if svc == "" {
			svc = matchServiceInTitle(rec.Title, ids)
		}
		if svc == "" {
			return DashboardFix{}, false
		}
		return DashboardFix{
			Number:     rec.Number,
			URL:        rec.URL,
			Title:      rec.Title,
			Service:    svc,
			Kind:       "ticket",
			State:      recordState(rec),
			ReproState: rec.ReproState,
			FirstSeen:  rec.FirstSeen,
			CreatedAt:  rec.CreatedAt,
			ClosedAt:   rec.ClosedAt,
		}, true
	}
	if !hasLabel(rec.Labels, "bug") && !rec.ClosedByFix {
		return DashboardFix{}, false
	}
	svc := matchServiceInTitle(rec.Title, ids)
	if svc == "" {
		return DashboardFix{}, false
	}
	return DashboardFix{
		Number:    rec.Number,
		URL:       rec.URL,
		Title:     rec.Title,
		Service:   svc,
		Kind:      "issue",
		State:     recordState(rec),
		FirstSeen: rec.FirstSeen,
		CreatedAt: rec.CreatedAt,
		ClosedAt:  rec.ClosedAt,
	}, true
}

// recordState maps a scraped issue state onto the record vocabulary. Only
// a completed close counts as fixed — not planned and duplicate closures
// stay recorded but unresolved.
func recordState(rec MaintenanceIssueRecord) string {
	switch {
	case rec.State == "closed" && rec.StateReason == "completed":
		return "fixed"
	case rec.State == "closed":
		return "closed"
	default:
		return "open"
	}
}
