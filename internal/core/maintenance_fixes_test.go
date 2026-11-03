package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGhIssueTracker_ListIssues(t *testing.T) {
	evidenceBody := "<!-- aegis-signature: spotify|canary|terminal|shape -->\n\n" +
		"### Evidence\n\n<!-- aegis-evidence:start -->\n" +
		"```json\n{\"signature\":\"spotify|canary|terminal|shape\",\"service\":\"spotify\",\"repro_state\":\"ready\"}\n```\n" +
		"<!-- aegis-evidence:end -->\n"
	payload := "[" +
		`{"number":240,"title":"spotify canary failed: shape drift",` +
		`"url":"https://github.com/example/repo/issues/240","state":"CLOSED","stateReason":"COMPLETED",` +
		`"labels":[{"name":"sync-failure"},{"name":"ready"}],` +
		`"createdAt":"2026-11-03T13:53:34Z","closedAt":"2026-11-03T15:39:18Z",` +
		"\"body\":" + jsonString(evidenceBody) + "}," +
		`{"number":207,"title":"google-gmail attachments endpoint returns 502",` +
		`"url":"https://github.com/example/repo/issues/207","state":"CLOSED","stateReason":"COMPLETED",` +
		`"labels":[],"createdAt":"2026-10-10T10:00:00Z","closedAt":"2026-10-11T10:00:00Z",` +
		`"body":"prose only"}]`
	rec := &recordingRun{respond: func(args []string) (string, error) {
		if args[1] != "list" {
			return "", errors.New("unexpected: " + strings.Join(args, " "))
		}
		return payload, nil
	}}
	g := &ghIssueTracker{repo: "example/repo", run: rec.run}

	got, err := g.ListIssues(context.Background())
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("records = %+v, want 2", got)
	}
	ticket := got[0]
	if !ticket.IsTicket || ticket.TicketService != "spotify" || ticket.ReproState != ReproStateReady {
		t.Fatalf("ticket record = %+v", ticket)
	}
	if ticket.State != "closed" || ticket.StateReason != "completed" {
		t.Fatalf("state not normalized: %+v", ticket)
	}
	wantCreated := time.Date(2026, 11, 3, 13, 53, 34, 0, time.UTC)
	if !ticket.CreatedAt.Equal(wantCreated) || ticket.ClosedAt.IsZero() {
		t.Fatalf("timestamps = %v / %v", ticket.CreatedAt, ticket.ClosedAt)
	}
	if got[1].IsTicket || got[1].TicketService != "" {
		t.Fatalf("plain issue misclassified: %+v", got[1])
	}
	if !strings.Contains(rec.joined()[0], "--json number,title,url,state,stateReason") {
		t.Fatalf("list call = %s", rec.joined()[0])
	}
}

func TestGhIssueTracker_ListIssuesErrors(t *testing.T) {
	rec := &recordingRun{respond: func([]string) (string, error) {
		return "", errors.New("gh: auth required")
	}}
	g := &ghIssueTracker{repo: "example/repo", run: rec.run}
	if _, err := g.ListIssues(context.Background()); err == nil {
		t.Fatal("want transport error")
	}

	rec = &recordingRun{respond: func([]string) (string, error) {
		return "not json", nil
	}}
	g = &ghIssueTracker{repo: "example/repo", run: rec.run}
	if _, err := g.ListIssues(context.Background()); err == nil {
		t.Fatal("want parse error")
	}
}

func TestIssueRecord_SignatureMarkerFallback(t *testing.T) {
	rec := issueRecord(&ghListIssue{
		Number: 5,
		State:  "OPEN",
		Title:  "something failed",
		Body:   signatureMarker("youtube|canary|terminal|shape") + "\n\nprose",
		Labels: []ghLabel{{Name: syncFailureLabel}, {Name: needsFixtureLabel}},
	})
	if !rec.IsTicket || rec.TicketService != "youtube" {
		t.Fatalf("marker fallback = %+v", rec)
	}
	if rec.ReproState != ReproStateNeedsFixture {
		t.Fatalf("repro state = %q", rec.ReproState)
	}
	if rec.State != "open" {
		t.Fatalf("state = %q", rec.State)
	}
}

func TestIsFeatureAskTitle(t *testing.T) {
	for _, title := range []string{
		"Add support for Signal messenger sync",
		"docs: clarify the setup guide",
		"Evaluate some kind of indexing system",
	} {
		if !isFeatureAskTitle(title) {
			t.Fatalf("want feature ask: %q", title)
		}
	}
	for _, title := range []string{
		"google-gmail attachments endpoint returns 502",
		"fix: reconnect the mirror writer",
	} {
		if isFeatureAskTitle(title) {
			t.Fatalf("want fix-shaped title: %q", title)
		}
	}
}

func TestMapIssueRecord_TicketWithoutServiceDrops(t *testing.T) {
	_, ok := mapIssueRecord(MaintenanceIssueRecord{
		Number: 1, Title: "mystery failure", State: "open", IsTicket: true,
	}, nil)
	if ok {
		t.Fatal("ticket with no attributable service must drop")
	}
}
