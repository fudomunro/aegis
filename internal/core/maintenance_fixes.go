package core

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// GitHub issue history for the sync-maintenance dashboard (issue #239). The
// same gh-CLI transport as the ticket surface scrapes the repo's issue
// record so each service page can show historical fixes alongside the signal
// history — the maintenance loop's work made visible. Fail-open: a scrape
// error degrades the dashboard with a note, never a failed build.
//
// Two kinds of record material exist:
//   - Tickets (label sync-failure): the loop's own issues, shown in full
//     lifecycle (open with repro state, or fixed when closed completed).
//   - Historical fixes: completed issues that read as fixes and name a
//     configured service. Feature asks, docs, and unmapped issues are
//     dropped — the page is a maintenance record, not an issue mirror.

// MaintenanceIssueRecord is one repo issue scraped for the dashboard's
// maintenance record. Raw bodies are parsed and dropped at scrape time so
// triage prose never reaches the rendered pages.
type MaintenanceIssueRecord struct {
	Number      int      `json:"number"`
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	State       string   `json:"state"`        // open | closed (lowercased)
	StateReason string   `json:"state_reason"` // completed | not_planned | "" (lowercased)
	Labels      []string `json:"labels,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ClosedAt    time.Time `json:"closed_at,omitempty"`
	// IsTicket marks sync-maintenance loop tickets.
	IsTicket bool `json:"is_ticket,omitempty"`
	// TicketService is the service the ticket contract attributes the issue
	// to (evidence block, falling back to the signature marker).
	TicketService string `json:"ticket_service,omitempty"`
	// ReproState mirrors the ticket's label state for display.
	ReproState string `json:"repro_state,omitempty"`
}

// ListIssues scrapes the repo's issue history via the gh CLI. Implements the
// read half of IssueTracker so the dashboard build can enrich service pages
// without a second transport.
func (g *ghIssueTracker) ListIssues(ctx context.Context) ([]MaintenanceIssueRecord, error) {
	out, err := g.run(ctx, "issue", "list", "-R", g.repo,
		"--state", "all", "--limit", "500",
		"--json", "number,title,url,state,stateReason,labels,createdAt,closedAt,body")
	if err != nil {
		return nil, err
	}
	var issues []ghListIssue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, err
	}
	recs := make([]MaintenanceIssueRecord, 0, len(issues))
	for i := range issues {
		recs = append(recs, issueRecord(&issues[i]))
	}
	return recs, nil
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

// featureAskPrefixes are title openings that read as product asks, not
// fixes. Matched case-insensitively at the start of the title.
var featureAskPrefixes = []string{
	"add ", "docs:", "explore ", "investigate ", "evaluate ",
	"define ", "design ", "map out ", "provide ", "create ",
}

// isFeatureAskTitle reports whether a title reads as a feature request or
// investigation rather than a fix. The heuristic is deliberately narrow: it
// only ever suppresses, never attributes.
func isFeatureAskTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	for _, p := range featureAskPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// serviceTitleAliases map unambiguous title phrases onto canonical service
// ids. A match is kept only when it resolves to a service in the dashboard.
var serviceTitleAliases = map[string]string{
	"spotify":          "spotify",
	"youtube music":    "youtube",
	"youtube":          "youtube",
	"amazon music":     "amazonmusic",
	"amazonmusic":      "amazonmusic",
	"gmail":            "google-gmail",
	"google photos":    "google-photos",
	"google contacts":  "google-contacts",
	"google calendar":  "google-calendar",
	"immich":           "immich",
	"bluesky":          "bluesky",
	"ynab":             "ynab",
	"owntracks":        "owntracks",
	"quicken":          "quicken",
	"signal messenger": "signal",
}

// matchServiceInTitle attributes an issue title to a known service: longest
// configured service id as a substring first, then curated alias phrases.
// Returns "" when the title names no known service.
func matchServiceInTitle(title string, ids []string) string {
	lower := strings.ToLower(title)
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			ordered = append(ordered, id)
		}
	}
	sortStringsByLenDesc(ordered)
	for _, id := range ordered {
		if strings.Contains(lower, strings.ToLower(id)) {
			return id
		}
	}
	aliases := make([]string, 0, len(serviceTitleAliases))
	for a := range serviceTitleAliases {
		aliases = append(aliases, a)
	}
	sortStringsByLenDesc(aliases)
	for _, a := range aliases {
		if strings.Contains(lower, a) {
			if id := resolveServiceID(serviceTitleAliases[a], ids); id != "" {
				return id
			}
		}
	}
	return ""
}

// resolveServiceID picks the dashboard service a canonical alias names:
// exact id first, then an id containing the canonical name (custom ids).
func resolveServiceID(canonical string, ids []string) string {
	for _, id := range ids {
		if strings.EqualFold(id, canonical) {
			return id
		}
	}
	for _, id := range ids {
		if strings.Contains(strings.ToLower(id), strings.ToLower(canonical)) {
			return id
		}
	}
	return ""
}

// mapIssueRecord projects a scraped issue onto the dashboard's maintenance
// record. ok is false when the issue is not record material for any service.
func mapIssueRecord(rec MaintenanceIssueRecord, ids []string) (DashboardFix, bool) {
	if rec.IsTicket {
		svc := rec.TicketService
		if svc == "" {
			svc = matchServiceInTitle(rec.Title, ids)
		}
		if svc == "" {
			return DashboardFix{}, false
		}
		state := "open"
		switch {
		case rec.State == "closed" && rec.StateReason != "not_planned":
			state = "fixed"
		case rec.State == "closed":
			state = "closed"
		}
		return DashboardFix{
			Number:     rec.Number,
			URL:        rec.URL,
			Title:      rec.Title,
			Service:    svc,
			Kind:       "ticket",
			State:      state,
			ReproState: rec.ReproState,
			CreatedAt:  rec.CreatedAt,
			ClosedAt:   rec.ClosedAt,
		}, true
	}
	if rec.State != "closed" || rec.StateReason != "completed" {
		return DashboardFix{}, false
	}
	if hasAnyLabel(rec.Labels, "enhancement", "documentation") {
		return DashboardFix{}, false
	}
	if isFeatureAskTitle(rec.Title) {
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
		Kind:      "fix",
		State:     "fixed",
		CreatedAt: rec.CreatedAt,
		ClosedAt:  rec.ClosedAt,
	}, true
}

// hasAnyLabel reports whether labels contain any of the given names.
func hasAnyLabel(labels []string, names ...string) bool {
	for _, n := range names {
		if hasLabel(labels, n) {
			return true
		}
	}
	return false
}

// sortStringsByLenDesc orders longest first so the longest match wins.
func sortStringsByLenDesc(s []string) {
	sort.Slice(s, func(i, j int) bool { return len(s[i]) > len(s[j]) })
}
