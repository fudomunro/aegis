package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The published manifest carries bare ids like "google" and "signal";
// attribution against it must not let them swallow unrelated titles.
func TestMatchServiceInTitle_RealManifest(t *testing.T) {
	metas, err := LoadDashboardServiceMetas(filepath.Join(repoRoot(t), "site", "maintenance", "services.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if len(metas) < 5 {
		t.Fatalf("manifest too small: %+v", metas)
	}
	ids := make([]string, 0, len(metas))
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	cases := []struct{ title, want string }{
		{"Google Photos picker returns 403", "google-photos"},
		{"Google Calendar sync drops recurring events", "google-calendar"},
		{"maintenance signal bundle loses failures", ""},
		{"YouTube Music import misses Spotify playlist items", "youtube-music"},
		{"google-gmail attachments endpoint returns 502", "google-gmail"},
		{"Signal messenger attachments fail to import", "signal"},
	}
	for _, tc := range cases {
		if got := matchServiceInTitle(tc.title, ids); got != tc.want {
			t.Fatalf("matchServiceInTitle(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestIssueScraper_ListIssues(t *testing.T) {
	evidenceBody := "<!-- aegis-signature: spotify|canary|terminal|shape -->\n\n" +
		"### Evidence\n\n<!-- aegis-evidence:start -->\n" +
		"```json\n{\"signature\":\"spotify|canary|terminal|shape\",\"service\":\"spotify\",\"repro_state\":\"ready\",\"first_seen\":\"2026-11-03T13:00:00Z\"}\n```\n" +
		"<!-- aegis-evidence:end -->\n"
	issuePayload := "[" +
		`{"number":240,"title":"spotify canary failed: shape drift",` +
		`"url":"https://github.com/example/repo/issues/240","state":"CLOSED","stateReason":"COMPLETED",` +
		`"labels":[{"name":"sync-failure"},{"name":"ready"}],` +
		`"createdAt":"2026-11-03T13:53:34Z","closedAt":"2026-11-03T15:39:18Z",` +
		"\"body\":" + jsonString(evidenceBody) + "}," +
		`{"number":207,"title":"google-gmail attachments endpoint returns 502",` +
		`"url":"https://github.com/example/repo/issues/207","state":"CLOSED","stateReason":"COMPLETED",` +
		`"labels":[],"createdAt":"2026-10-10T10:00:00Z","closedAt":"2026-10-11T10:00:00Z",` +
		`"body":"prose only"}]`
	prPayload := "[" +
		`{"title":"fix: discover Gmail attachments (#207)","headRefName":"fix/gmail-attachment-discovery",` +
		`"closingIssuesReferences":[{"number":207}]},` +
		`{"title":"feat: some feature","headRefName":"feat/some-feature",` +
		`"closingIssuesReferences":[{"number":999}]}]`
	rec := &recordingRun{respond: func(args []string) (string, error) {
		switch {
		case args[0] == "issue" && args[1] == "list":
			return issuePayload, nil
		case args[0] == "pr" && args[1] == "list":
			return prPayload, nil
		}
		return "", errors.New("unexpected: " + strings.Join(args, " "))
	}}
	g := &issueScraper{repo: "example/repo", run: rec.run}

	got, err := g.ListIssues(context.Background())
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("records = %+v, want 2", got)
	}
	ticket := got[0]
	if !ticket.IsTicket || ticket.TicketService != "spotify" || ticket.ReproState != reproStateReady {
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
	// Only the fix-convention PR marks its issue as fix-closed.
	if !got[1].ClosedByFix {
		t.Fatalf("issue 207 must carry closed_by_fix: %+v", got[1])
	}
	joined := strings.Join(rec.joined(), " | ")
	if !strings.Contains(joined, "--json number,title,url,state,stateReason") ||
		!strings.Contains(joined, "--state merged") {
		t.Fatalf("calls = %s", joined)
	}
}

func TestIsFixPR(t *testing.T) {
	if !isFixPR("fix: discover Gmail attachments (#207)", "fix/gmail-attachment-discovery") {
		t.Fatal("fix title must match")
	}
	if !isFixPR("something else", "fix/branch-name") {
		t.Fatal("fix branch must match")
	}
	if isFixPR("feat: add a connector", "feat/add-connector") {
		t.Fatal("feat PR must not match")
	}
}

func TestIssueScraper_ListIssuesErrors(t *testing.T) {
	rec := &recordingRun{respond: func([]string) (string, error) {
		return "", errors.New("gh: auth required")
	}}
	g := &issueScraper{repo: "example/repo", run: rec.run}
	if _, err := g.ListIssues(context.Background()); err == nil {
		t.Fatal("want transport error")
	}

	rec = &recordingRun{respond: func([]string) (string, error) {
		return "not json", nil
	}}
	g = &issueScraper{repo: "example/repo", run: rec.run}
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
	if rec.ReproState != reproStateNeedsFixture {
		t.Fatalf("repro state = %q", rec.ReproState)
	}
	if rec.State != "open" {
		t.Fatalf("state = %q", rec.State)
	}
}

// Non-ticket items pass only the explicit allowlist: a bug label or a
// fix-convention PR closing them. Title shapes never qualify.
func TestMapIssueRecord_Allowlist(t *testing.T) {
	ids := []string{"spotify", "google-gmail"}
	keep := []MaintenanceIssueRecord{
		{Number: 1, Title: "spotify paging returns 502", State: "closed", StateReason: "completed",
			Labels: []string{"bug"}, CreatedAt: dashTime(1)},
		{Number: 2, Title: "gmail fetch drops attachments", State: "open", ClosedByFix: true, CreatedAt: dashTime(2)},
	}
	for _, rec := range keep {
		if _, ok := mapIssueRecord(rec, ids); !ok {
			t.Fatalf("allowlisted record dropped: %+v", rec)
		}
	}
	drop := []MaintenanceIssueRecord{
		// Design asks and test tasks never count, closed or not.
		{Number: 3, Title: "Treat Gmail and Takeout as two sources", State: "closed", StateReason: "completed", CreatedAt: dashTime(3)},
		{Number: 4, Title: "Live testing: connect Spotify accounts", State: "closed", StateReason: "completed", CreatedAt: dashTime(4)},
		{Number: 5, Title: "Add support for Spotify widgets", State: "closed", StateReason: "completed",
			Labels: []string{"enhancement"}, CreatedAt: dashTime(5)},
		{Number: 6, Title: "gmail gap still open", State: "open", CreatedAt: dashTime(6)},
	}
	for _, rec := range drop {
		if _, ok := mapIssueRecord(rec, ids); ok {
			t.Fatalf("non-allowlisted record kept: %+v", rec)
		}
	}
	// Only a completed close is fixed; not planned stays unresolved.
	fix, ok := mapIssueRecord(MaintenanceIssueRecord{
		Number: 7, Title: "gmail outage postmortem", State: "closed", StateReason: "not_planned",
		Labels: []string{"bug"}, CreatedAt: dashTime(7),
	}, ids)
	if !ok || fix.State != "closed" {
		t.Fatalf("not-planned closure = %+v ok=%v, want state closed", fix, ok)
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
