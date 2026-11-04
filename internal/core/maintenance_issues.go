package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GitHub issues are the sync-maintenance ticket surface (issue #86): the
// loop never grows a parallel defect tracker. One issue per failure
// signature, deduped by a body marker; the structured ticket contract lives
// in an evidence block the scanner rewrites on every scan (idempotent, no
// comment spam), while triage — labels and prose around the block — belongs
// to humans and fix agents and is never clobbered.
//
// Production transport is the gh CLI (same tool the project already uses),
// so the service needs no GitHub token of its own. The run seam keeps the
// tracker testable without exec.

const (
	// syncFailureLabel tags every maintenance ticket issue.
	syncFailureLabel = "sync-failure"
	// needsFixtureLabel / readyLabel mirror ReproState. On update the labels
	// are authoritative: flipping needs-fixture → ready is how triage
	// promotes a ticket into the phase-3 pickup queue.
	needsFixtureLabel = "needs-fixture"
	readyLabel        = "ready"

	signatureMarkerPrefix = "<!-- aegis-signature: "
	evidenceStartMarker   = "<!-- aegis-evidence:start -->"
	evidenceEndMarker     = "<!-- aegis-evidence:end -->"
)

// IssueRef identifies one GitHub issue.
type IssueRef struct {
	Number int      `json:"number"`
	URL    string   `json:"url"`
	State  string   `json:"state"` // open | closed
	Labels []string `json:"labels,omitempty"`
}

// IssueTracker is the ticket surface. UpsertTicket creates, updates, or
// reopens exactly one issue per ticket and reports what it did
// ("opened" | "updated" | "reopened"). ListIssues scrapes the repo's issue
// history for the dashboard's maintenance record (issue #239).
type IssueTracker interface {
	UpsertTicket(ctx context.Context, t MaintenanceTicket) (IssueRef, string, error)
	ListIssues(ctx context.Context) ([]MaintenanceIssueRecord, error)
}

// ghIssueTracker opens and updates tickets via the gh CLI against an
// explicit repo (owner/name). run is the exec seam for tests; it receives gh
// arguments without the binary name and returns stdout.
type ghIssueTracker struct {
	repo string
	run  func(ctx context.Context, args ...string) (string, error)
}

// NewGhIssueTracker builds a tracker for the given owner/name repo.
func NewGhIssueTracker(repo string) *ghIssueTracker {
	return &ghIssueTracker{
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

// UpsertTicket implements IssueTracker.
func (g *ghIssueTracker) UpsertTicket(ctx context.Context, t MaintenanceTicket) (IssueRef, string, error) {
	existing, err := g.findIssue(ctx, t.Signature)
	if err != nil {
		return IssueRef{}, "", err
	}
	if existing == nil {
		out, err := g.run(ctx, "issue", "create", "-R", g.repo,
			"--title", t.Title,
			"--body", renderIssueBody(t),
			"--label", syncFailureLabel,
			"--label", needsFixtureLabel)
		if err != nil {
			return IssueRef{}, "", err
		}
		url := strings.TrimSpace(out)
		return IssueRef{Number: issueNumberFromURL(url), URL: url, State: "open",
			Labels: []string{syncFailureLabel, needsFixtureLabel}}, "opened", nil
	}

	// refreshIssueBody carries triage fields from the old evidence block
	// onto t; the issue labels decide repro_state.
	labelNames := existing.labelNames()
	t.ReproState = reproStateFromLabels(labelNames, t.ReproState)

	if _, err := g.run(ctx, "issue", "edit", strconv.Itoa(existing.Number), "-R", g.repo,
		"--body", refreshIssueBody(existing.Body, t)); err != nil {
		return IssueRef{}, "", err
	}

	want, drop := needsFixtureLabel, readyLabel
	if t.ReproState == ReproStateReady {
		want, drop = readyLabel, needsFixtureLabel
	}
	if !hasLabel(labelNames, want) || hasLabel(labelNames, drop) {
		args := []string{"issue", "edit", strconv.Itoa(existing.Number), "-R", g.repo, "--add-label", want}
		if hasLabel(labelNames, drop) {
			args = append(args, "--remove-label", drop)
		}
		if _, err := g.run(ctx, args...); err != nil {
			return IssueRef{}, "", err
		}
	}

	action := "updated"
	// gh emits state uppercase ("OPEN"/"CLOSED"); a closed ticket must
	// reopen when the scanner finds fresh evidence for its signature.
	if strings.EqualFold(existing.State, "closed") {
		if _, err := g.run(ctx, "issue", "reopen", strconv.Itoa(existing.Number), "-R", g.repo); err != nil {
			return IssueRef{}, "", err
		}
		action = "reopened"
	}
	return IssueRef{Number: existing.Number, URL: existing.URL, State: "open",
		Labels: append([]string{syncFailureLabel}, want)}, action, nil
}

// findIssue returns the issue carrying this signature marker, if any.
func (g *ghIssueTracker) findIssue(ctx context.Context, signature string) (*ghListIssue, error) {
	out, err := g.run(ctx, "issue", "list", "-R", g.repo,
		"--state", "all", "--label", syncFailureLabel, "--limit", "500",
		"--json", "number,state,url,labels,body")
	if err != nil {
		return nil, err
	}
	var issues []ghListIssue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parse gh issue list: %w", err)
	}
	for i := range issues {
		if matchSignature(issues[i].Body, signature) {
			return &issues[i], nil
		}
	}
	return nil, nil
}

// signatureMarker is the dedup key embedded in every ticket body.
func signatureMarker(signature string) string {
	return signatureMarkerPrefix + signature + " -->"
}

// matchSignature reports whether a body carries this ticket's marker.
func matchSignature(body, signature string) bool {
	return strings.Contains(body, signatureMarker(signature))
}

// renderIssueBody builds a fresh ticket body: signature marker, the
// scanner-owned evidence block, and the human triage checklist.
func renderIssueBody(t MaintenanceTicket) string {
	evidence, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		evidence = []byte("{}")
	}
	var b strings.Builder
	b.WriteString(signatureMarker(t.Signature))
	b.WriteString("\n\n### Evidence\n\nScanner-owned and rewritten on every scan.\n\n")
	b.WriteString(evidenceStartMarker)
	b.WriteString("\n```json\n")
	b.Write(evidence)
	b.WriteString("\n```\n")
	b.WriteString(evidenceEndMarker)
	b.WriteString("\n\n### Triage\n\n" +
		"- [ ] Attach the scrubbed fixture (`internal/connectors/testdata/repro/<service>/<signature>/`)\n" +
		"- [ ] Add the red repro test (it must fail on main)\n" +
		"- [ ] Record expected vs actual neutral-model shape in the evidence block\n" +
		"- [ ] Add label `ready` — the fix loop picks up `ready` issues only\n")
	return b.String()
}

// refreshIssueBody replaces only the evidence block in an existing body,
// leaving marker comments and everything triage wrote alone. Triage fields
// (title, tier, fixture/test slots, earliest first-seen) in the old block are
// carried onto t before the write. Bodies without a block fall back to a
// fresh render.
func refreshIssueBody(oldBody string, t MaintenanceTicket) string {
	start := strings.Index(oldBody, evidenceStartMarker)
	end := strings.Index(oldBody, evidenceEndMarker)
	if start < 0 || end < 0 || end < start {
		return renderIssueBody(t)
	}
	if old := parseTicketBody(oldBody); old != nil {
		t.Title = old.Title
		t.Tier = old.Tier
		t.Fixture = old.Fixture
		t.ReproTest = old.ReproTest
		t.Expected = old.Expected
		t.Actual = old.Actual
		if !old.FirstSeen.IsZero() && old.FirstSeen.Before(t.FirstSeen) {
			t.FirstSeen = old.FirstSeen
		}
	}
	evidence, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		evidence = []byte("{}")
	}
	var b strings.Builder
	b.WriteString(oldBody[:start])
	b.WriteString(evidenceStartMarker)
	b.WriteString("\n```json\n")
	b.Write(evidence)
	b.WriteString("\n```\n")
	b.WriteString(oldBody[end:])
	return b.String()
}

// parseTicketBody extracts the ticket contract from an evidence block.
// Returns nil when the block is missing or unparseable.
func parseTicketBody(body string) *MaintenanceTicket {
	start := strings.Index(body, evidenceStartMarker)
	end := strings.Index(body, evidenceEndMarker)
	if start < 0 || end < 0 || end < start {
		return nil
	}
	block := body[start+len(evidenceStartMarker) : end]
	block = strings.TrimSpace(block)
	block = strings.TrimPrefix(block, "```json")
	block = strings.TrimSuffix(strings.TrimSpace(block), "```")
	var t MaintenanceTicket
	if err := json.Unmarshal([]byte(strings.TrimSpace(block)), &t); err != nil {
		return nil
	}
	return &t
}

// reproStateFromLabels lets the issue labels override repro_state on update;
// without either label the previous state stands.
func reproStateFromLabels(labels []string, fallback string) string {
	if hasLabel(labels, readyLabel) {
		return ReproStateReady
	}
	if hasLabel(labels, needsFixtureLabel) {
		return ReproStateNeedsFixture
	}
	if fallback == "" {
		return ReproStateNeedsFixture
	}
	return fallback
}

// issueNumberFromURL pulls the number out of an issue URL tail.
func issueNumberFromURL(url string) int {
	url = strings.TrimRight(url, "/")
	i := strings.LastIndex(url, "/")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(url[i+1:])
	return n
}
