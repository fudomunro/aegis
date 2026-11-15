package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
)

// Ticket intake (issue #243, phase 3): the pickup side of the self-repair
// loop. The scanner opens tickets at needs-fixture; triage attaches the
// repro bundle and flips the label to ready. Intake lists those ready
// tickets and emits them as fix-cycle work orders — oldest first — so a
// scheduled driver can start the cycle (red test verified, fix, PR) without
// a human initiating each one. It runs outside the application beside the
// scanner (issue #344) and is read-only: it never touches labels or prose.
//
// The pickup gate is the repro bundle (docs/sync-maintenance.md): a ready
// ticket without its fixture or repro test is reported as incomplete, not
// silently dropped.

// MaintenanceIntakeReport is one pickup pass.
type MaintenanceIntakeReport struct {
	// Picked are the work orders, oldest ticket first.
	Picked []MaintenanceIntakeTicket `json:"picked"`
	// Incomplete lists ready-labeled tickets the pickup will not start
	// because their repro bundle is not attached.
	Incomplete []MaintenanceIntakeIncomplete `json:"incomplete,omitempty"`
}

// MaintenanceIntakeTicket is one fix-cycle work order: the ticket contract
// plus the issue refs a fix PR cites.
type MaintenanceIntakeTicket struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	MaintenanceTicket
}

// MaintenanceIntakeIncomplete is a ready-labeled ticket intake cannot pick
// up yet. Reason tells triage which half of the repro bundle is missing.
type MaintenanceIntakeIncomplete struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title,omitempty"`
	Reason string `json:"reason"`
}

// readyTickets lists the intake queue: open sync-failure tickets the `ready`
// label says carry a complete repro bundle.
func (g *ghClient) readyTickets(ctx context.Context) ([]ghListIssue, error) {
	out, err := g.run(ctx, "issue", "list", "-R", g.repo,
		"--state", "open", "--label", syncFailureLabel, "--label", readyLabel,
		"--limit", "500",
		"--json", "number,url,title,labels,body,createdAt")
	if err != nil {
		return nil, err
	}
	var issues []ghListIssue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parse gh issue list: %w", err)
	}
	return issues, nil
}

// intakeTickets runs one pickup pass and returns the work orders. Ordering
// is the queue's: the oldest ticket is the next one to fix.
func intakeTickets(ctx context.Context, g *ghClient) (*MaintenanceIntakeReport, error) {
	issues, err := g.readyTickets(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return issues[i].CreatedAt.Before(issues[j].CreatedAt)
	})
	report := &MaintenanceIntakeReport{Picked: []MaintenanceIntakeTicket{}}
	for _, issue := range issues {
		ticket := parseTicketBody(issue.Body)
		switch {
		case ticket == nil:
			report.Incomplete = append(report.Incomplete, intakeIncomplete(issue, "no evidence block"))
		case ticket.Fixture == "":
			report.Incomplete = append(report.Incomplete, intakeIncomplete(issue, "no scrubbed fixture attached"))
		case ticket.ReproTest == "":
			report.Incomplete = append(report.Incomplete, intakeIncomplete(issue, "no repro test attached"))
		default:
			report.Picked = append(report.Picked, MaintenanceIntakeTicket{
				Number:            issue.Number,
				URL:               issue.URL,
				MaintenanceTicket: *ticket,
			})
		}
	}
	return report, nil
}

func intakeIncomplete(issue ghListIssue, reason string) MaintenanceIntakeIncomplete {
	return MaintenanceIntakeIncomplete{
		Number: issue.Number,
		URL:    issue.URL,
		Title:  issue.Title,
		Reason: reason,
	}
}

// runIntake runs one pickup pass and prints the queue as JSON. A scheduled
// driver consumes the report and starts the fix cycle (docs/sync-maintenance.md
// phase 3).
func runIntake(args []string) {
	fs := flag.NewFlagSet("intake", flag.ExitOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "GitHub repo (owner/name) to read tickets from")
	fs.Parse(args)

	if *repo == "" {
		fail("the -repo flag (or GITHUB_REPOSITORY) is required")
	}
	report, err := intakeTickets(context.Background(), newGHClient(*repo))
	if err != nil {
		fail("intake: " + err.Error())
	}
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail("encode report: " + err.Error())
	}
	fmt.Println(string(out))
}
