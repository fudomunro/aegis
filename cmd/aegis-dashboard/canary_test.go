package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCanaryOutcome(t *testing.T) {
	cases := []struct {
		total, listed int
		fetchErr      error
		want          string
	}{
		{3, 3, nil, "3 tracks listed"},
		{3, 0, nil, "shape drift flagged"},
		{0, 0, nil, "coherent empty library"},
		{3, 2, nil, "2 tracks listed"},
		{0, 0, errors.New("boom"), "upstream error surfaced"},
	}
	for _, c := range cases {
		if got := canaryOutcome(c.total, c.listed, c.fetchErr); got != c.want {
			t.Fatalf("canaryOutcome(%d,%d,%v) = %q, want %q",
				c.total, c.listed, c.fetchErr, got, c.want)
		}
	}
}

// The full exercise over the real mock connector must be green: every shape
// behaves the way the canary runner's semantics say it should.
func TestRunSyntheticCanary_AllScenariosPass(t *testing.T) {
	for _, c := range runSyntheticCanary() {
		if c.Status != "pass" {
			t.Fatalf("scenario %s = %+v, want a clean pass", c.Scenario, c)
		}
	}
}

func TestRunCanaryExercise_RegressionIsDetected(t *testing.T) {
	// A fetch that silently returns an empty page for every shape models a
	// broken parse: the shapes that must list tracks and the shape that
	// must flag drift all go wrong.
	checks := runCanaryExercise(func(string) error { return nil },
		func(context.Context) (int, int, error) { return 0, 0, nil })
	status := map[string]string{}
	for _, c := range checks {
		status[c.Scenario] = c.Status
		if c.Status == "fail" && (c.Expected == "" || c.Actual == "") {
			t.Fatalf("failing check must carry the verdict: %+v", c)
		}
	}
	if status["healthy"] != "fail" || status["drift-items"] != "fail" || status["drift-unmapped"] != "fail" {
		t.Fatalf("statuses = %v, want the silent-empty parse caught", status)
	}
	if status["empty"] != "pass" {
		t.Fatalf("a coherent empty page is health: %v", status)
	}
	for _, mode := range []string{"http-500", "http-401", "garbage"} {
		if status[mode] != "fail" {
			t.Fatalf("statuses = %v, want %s to demand an error", status, mode)
		}
	}

	// A fetch that never errors models the opposite regression: garbage
	// and error shapes are silently accepted.
	checks = runCanaryExercise(func(string) error { return nil },
		func(context.Context) (int, int, error) { return 3, 3, nil })
	status = map[string]string{}
	for _, c := range checks {
		status[c.Scenario] = c.Status
	}
	for _, mode := range []string{"http-500", "http-401", "garbage"} {
		if status[mode] != "fail" {
			t.Fatalf("statuses = %v, want %s rejected", status, mode)
		}
	}
}

func TestCanaryTickets_OnePerFailingCheck(t *testing.T) {
	now := time.Date(2026, 2, 20, 8, 0, 0, 0, time.UTC)
	checks := []CanaryCheck{
		{Scenario: "healthy", Status: "fail", Expected: "3 tracks listed", Actual: "shape drift flagged", Detail: "total 3, 0 listed"},
		{Scenario: "empty", Status: "pass", Expected: "coherent empty library", Actual: "coherent empty library"},
	}
	tickets := canaryTickets(checks, now)
	if len(tickets) != 1 {
		t.Fatalf("tickets = %+v, want one for the failing check", tickets)
	}
	tk := tickets[0]
	if tk.Service != "spotify" || tk.Kind != "tracks" || tk.JobType != "canary" {
		t.Fatalf("ticket must name the adapter under exercise: %+v", tk)
	}
	if tk.Expected != "3 tracks listed" || !strings.Contains(tk.Actual, "shape drift flagged") {
		t.Fatalf("expected/actual must define the break: %+v", tk)
	}
	if tk.ReproState != reproStateNeedsFixture || tk.Tier != scanDefaultTier {
		t.Fatalf("repro state/tier = %q/%q", tk.ReproState, tk.Tier)
	}
	if !matchSignature(renderIssueBody(tk), tk.Signature) {
		t.Fatal("signature marker must round-trip through the ticket body")
	}
	// The same failure must land on the same signature across runs.
	if again := canaryTickets(checks, now.Add(time.Hour)); again[0].Signature != tk.Signature {
		t.Fatalf("signature drifted: %q vs %q", again[0].Signature, tk.Signature)
	}
}

func TestCanaryReport_NilClientAndUpsert(t *testing.T) {
	checks := runCanaryExercise(func(string) error { return nil },
		func(context.Context) (int, int, error) { return 0, 0, nil })

	report := canaryReport(context.Background(), checks, nil)
	if report.Result != "fail" {
		t.Fatalf("result = %q, want fail", report.Result)
	}
	if len(report.Tickets) == 0 {
		t.Fatal("failing checks must be ticketable")
	}
	for _, ref := range report.Tickets {
		if ref.Action != "disabled" {
			t.Fatalf("ref = %+v, want disabled without a repo", ref)
		}
	}

	rec := &recordingRun{respond: func(args []string) (string, error) {
		switch args[1] {
		case "list":
			return "[]", nil
		case "create":
			return "https://github.com/example/repo/issues/42\n", nil
		}
		return "", errors.New("unexpected: " + strings.Join(args, " "))
	}}
	report = canaryReport(context.Background(), checks, &ghClient{repo: "example/repo", run: rec.run})
	if report.Tickets[0].Action != "opened" || report.Tickets[0].Number != 42 {
		t.Fatalf("ref = %+v, want opened issue 42", report.Tickets[0])
	}
	if joined := rec.joined(); !strings.Contains(joined[0], "--label sync-failure") ||
		!strings.Contains(joined[1], "--label sync-failure") {
		t.Fatalf("calls must ride the ticket surface: %v", joined)
	}

	rec = &recordingRun{respond: func(args []string) (string, error) {
		return "", errors.New("gh exploded")
	}}
	report = canaryReport(context.Background(), checks, &ghClient{repo: "example/repo", run: rec.run})
	if report.Tickets[0].Action != "error" || report.Tickets[0].Error == "" {
		t.Fatalf("ref = %+v, want a fail-open error record", report.Tickets[0])
	}
}
