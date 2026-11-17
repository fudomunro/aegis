package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ghIssueJSON builds one canned `gh issue list` row.
func ghIssueJSON(t *testing.T, number int, url, title, body string, created time.Time) string {
	t.Helper()
	b, err := json.Marshal(ghListIssue{
		Number:    number,
		URL:       url,
		Title:     title,
		Body:      body,
		CreatedAt: created,
		Labels:    []ghLabel{{Name: syncFailureLabel}, {Name: readyLabel}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// readyTicket is a sample ticket with the repro bundle attached.
func readyTicket() MaintenanceTicket {
	tk := sampleTicket()
	tk.Fixture = "internal/connectors/testdata/repro/spotify/track-object-null-items/fixture.json"
	tk.ReproTest = "TestSpotifyLikedTracks_ReproFixture"
	tk.Expected = "3 tracks"
	tk.Actual = "0 tracks"
	return tk
}

func TestIntakeTickets_PicksReadyWithBundle(t *testing.T) {
	older := readyTicket()
	newer := sampleTicket() // labeled ready but the bundle is not attached

	// The listing returns the newer issue first; the queue must still run
	// oldest ticket first.
	list := "[" +
		ghIssueJSON(t, 12, "https://github.com/example/repo/issues/12", "newer", renderIssueBody(newer), time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC)) + "," +
		ghIssueJSON(t, 7, "https://github.com/example/repo/issues/7", "older", renderIssueBody(older), time.Date(2026, 2, 11, 10, 0, 0, 0, time.UTC)) +
		"]"
	rec := &recordingRun{respond: func(args []string) (string, error) {
		if args[1] != "list" {
			return "", errors.New("unexpected: " + strings.Join(args, " "))
		}
		return list, nil
	}}
	g := &ghClient{repo: "example/repo", run: rec.run}

	report, err := intakeTickets(context.Background(), g)
	if err != nil {
		t.Fatalf("intakeTickets: %v", err)
	}
	joined := rec.joined()
	if len(joined) != 1 ||
		!strings.Contains(joined[0], "issue list -R example/repo") ||
		!strings.Contains(joined[0], "--state open") ||
		!strings.Contains(joined[0], "--label sync-failure") ||
		!strings.Contains(joined[0], "--label ready") {
		t.Fatalf("pickup query = %v, want the label-filtered ready queue", joined)
	}
	if len(report.Picked) != 1 {
		t.Fatalf("picked = %+v, want the one ticket with a repro bundle", report.Picked)
	}
	got := report.Picked[0]
	if got.Number != 7 || got.Fixture != older.Fixture || got.ReproTest != older.ReproTest {
		t.Fatalf("work order = %+v, want issue 7 with the attached bundle", got)
	}
	if got.Expected != "3 tracks" || got.Actual != "0 tracks" || got.Tier != scanDefaultTier {
		t.Fatalf("work order must carry the fix contract: %+v", got)
	}
	if len(report.Incomplete) != 1 || report.Incomplete[0].Number != 12 {
		t.Fatalf("incomplete = %+v, want the bundle-less issue 12", report.Incomplete)
	}
	if report.Incomplete[0].Reason != "no scrubbed fixture attached" {
		t.Fatalf("reason = %q", report.Incomplete[0].Reason)
	}
}

func TestIntakeTickets_IncompleteReasons(t *testing.T) {
	fixtureOnly := sampleTicket()
	fixtureOnly.Fixture = "internal/connectors/testdata/repro/spotify/track-object-null-items/fixture.json"
	fixtureOnly.ReproTest = ""
	noBundle := sampleTicket()
	noBundle.Fixture = ""
	noBundle.ReproTest = ""
	list := "[" +
		ghIssueJSON(t, 3, "https://github.com/example/repo/issues/3", "no evidence", "just prose", time.Date(2026, 2, 11, 10, 0, 0, 0, time.UTC)) + "," +
		ghIssueJSON(t, 4, "https://github.com/example/repo/issues/4", "no test", renderIssueBody(fixtureOnly), time.Date(2026, 2, 11, 11, 0, 0, 0, time.UTC)) + "," +
		ghIssueJSON(t, 5, "https://github.com/example/repo/issues/5", "no fixture", renderIssueBody(noBundle), time.Date(2026, 2, 11, 12, 0, 0, 0, time.UTC)) +
		"]"
	rec := &recordingRun{respond: func(args []string) (string, error) { return list, nil }}
	g := &ghClient{repo: "example/repo", run: rec.run}

	report, err := intakeTickets(context.Background(), g)
	if err != nil {
		t.Fatalf("intakeTickets: %v", err)
	}
	if len(report.Picked) != 0 {
		t.Fatalf("picked = %+v, want none without a complete bundle", report.Picked)
	}
	if len(report.Incomplete) != 3 {
		t.Fatalf("incomplete = %+v, want every entry reported", report.Incomplete)
	}
	want := []string{"no evidence block", "no repro test attached", "no scrubbed fixture attached"}
	for i, w := range want {
		if report.Incomplete[i].Reason != w {
			t.Fatalf("incomplete[%d].Reason = %q, want %q", i, report.Incomplete[i].Reason, w)
		}
	}
}

func TestIntakeTickets_EmptyAndErrors(t *testing.T) {
	rec := &recordingRun{respond: func(args []string) (string, error) { return "[]", nil }}
	report, err := intakeTickets(context.Background(), &ghClient{repo: "example/repo", run: rec.run})
	if err != nil {
		t.Fatalf("empty queue: %v", err)
	}
	if len(report.Picked) != 0 || len(report.Incomplete) != 0 {
		t.Fatalf("empty queue must report nothing: %+v", report)
	}
	if report.Picked == nil {
		t.Fatal("picked must marshal as [] rather than null")
	}

	rec = &recordingRun{respond: func(args []string) (string, error) {
		return "", errors.New("gh exploded")
	}}
	if _, err := intakeTickets(context.Background(), &ghClient{repo: "example/repo", run: rec.run}); err == nil {
		t.Fatal("a listing failure must surface")
	}
	rec = &recordingRun{respond: func(args []string) (string, error) { return "not json", nil }}
	if _, err := intakeTickets(context.Background(), &ghClient{repo: "example/repo", run: rec.run}); err == nil {
		t.Fatal("an unparseable listing must surface")
	}
}
