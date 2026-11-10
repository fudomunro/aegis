package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

// GitHub issue history for the sync-maintenance dashboard (issue #239). The
// same gh-CLI transport as the ticket surface scrapes the repo's issue
// record so each service page can show historical fixes alongside the signal
// history — the maintenance loop's work made visible. A scrape failure fails
// the build by default (an empty record must not overwrite a good previous
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

// ListIssues scrapes the repo's issue history via the gh CLI. A second call
// reads merged PRs so non-ticket record items can prove they were fixed
// (closed by a fix-convention PR).
func (g *ghClient) ListIssues(ctx context.Context) ([]MaintenanceIssueRecord, error) {
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
func (g *ghClient) listFixClosedIssues(ctx context.Context) (map[int]bool, error) {
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
	if ticket := parseTicketBody(i.Body); ticket != nil {
		rec.IsTicket = true
		rec.TicketService = ticket.Service
		if rec.TicketService == "" {
			rec.TicketService = signatureService(ticket.Signature)
		}
		rec.FirstSeen = ticket.FirstSeen
		if rec.ReproState == "" {
			rec.ReproState = reproStateFromLabels(rec.Labels, ticket.ReproState)
		}
	}
	if rec.TicketService == "" {
		rec.TicketService = signatureService(signatureFromBody(i.Body))
	}
	return rec
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
