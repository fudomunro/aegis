package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingRun is the gh exec seam: it records invocations and plays back
// scripted responses.
type recordingRun struct {
	calls   [][]string
	respond func(args []string) (string, error)
}

func (r *recordingRun) run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return r.respond(args)
}

func (r *recordingRun) joined() []string {
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func sampleTicket() MaintenanceTicket {
	return MaintenanceTicket{
		Signature:  "spotify|ingest|terminal|spotify saved tracks returned #",
		Title:      "spotify ingest failed: spotify saved tracks returned 502",
		Service:    "spotify",
		JobType:    "ingest",
		ErrorClass: SyncErrorClassTerminal,
		ReproState: ReproStateNeedsFixture,
		Tier:       scanDefaultTier,
		FirstSeen:  time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC),
		LastSeen:   time.Date(2026, 2, 10, 11, 0, 0, 0, time.UTC),
		Count:      2,
	}
}

func TestIssueBodyRoundTrip(t *testing.T) {
	tk := sampleTicket()
	body := renderIssueBody(tk)
	if !matchSignature(body, tk.Signature) {
		t.Fatalf("rendered body must carry the marker: %s", body)
	}
	got := parseTicketBody(body)
	if got == nil {
		t.Fatal("parseTicketBody returned nil")
	}
	if got.Signature != tk.Signature || got.Count != tk.Count || got.Service != tk.Service {
		t.Fatalf("round-trip = %+v", got)
	}
	if parseTicketBody("plain prose, no block") != nil {
		t.Fatal("body without evidence block must parse to nil")
	}
}

func TestRefreshIssueBody_PreservesTriage(t *testing.T) {
	tk := sampleTicket()
	body := renderIssueBody(tk)

	// Triage on GitHub edits the prose and the JSON block directly
	// (simulated here by block surgery).
	edited := strings.ReplaceAll(body, "- [ ] Attach the scrubbed fixture",
		"Operator note: this started after the 2.4 rollout.\n\n- [x] Attach the scrubbed fixture")
	triaged := parseTicketBody(edited)
	triaged.Fixture = "internal/connectors/testdata/repro/spotify/track-object-null-items/fixture.json"
	triaged.ReproTest = "TestSpotifyLikedTracks_ReproFixture"
	triaged.Expected = "2 tracks"
	triaged.Actual = "0 tracks"
	triaged.Tier = "fixture"
	triaged.Title = "Spotify saved-tracks parse regression"
	edited = replaceTicketBlock(edited, triaged)

	fresh := sampleTicket()
	fresh.Count = 3
	fresh.LastSeen = time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	out := refreshIssueBody(edited, fresh)

	if !strings.Contains(out, "Operator note: this started after the 2.4 rollout.") {
		t.Fatalf("prose outside the block must survive: %s", out)
	}
	got := parseTicketBody(out)
	if got == nil {
		t.Fatal("refresh must leave a parseable evidence block")
	}
	if got.Count != 3 {
		t.Fatalf("scanner-owned count not refreshed: %+v", got)
	}
	if got.Fixture != triaged.Fixture || got.ReproTest != triaged.ReproTest ||
		got.Expected != triaged.Expected || got.Actual != triaged.Actual ||
		got.Tier != triaged.Tier || got.Title != triaged.Title {
		t.Fatalf("triage fields clobbered: %+v", got)
	}

	// Bodies without markers fall back to a fresh render.
	if out := refreshIssueBody("something else entirely", fresh); !matchSignature(out, fresh.Signature) {
		t.Fatalf("fallback render missing marker: %s", out)
	}
}

// replaceTicketBlock rewrites the evidence block in place, the way a human
// editing the issue body on GitHub would. No triage preservation — that is
// the scanner's refresh path.
func replaceTicketBlock(body string, t *MaintenanceTicket) string {
	start := strings.Index(body, evidenceStartMarker)
	end := strings.Index(body, evidenceEndMarker)
	evidence, _ := json.MarshalIndent(t, "", "  ")
	return body[:start] + evidenceStartMarker + "\n```json\n" + string(evidence) + "\n```\n" + body[end:]
}

func TestReproStateFromLabels(t *testing.T) {
	if got := reproStateFromLabels([]string{"sync-failure", readyLabel}, ReproStateNeedsFixture); got != ReproStateReady {
		t.Fatalf("ready label = %q", got)
	}
	if got := reproStateFromLabels([]string{needsFixtureLabel}, ReproStateReady); got != ReproStateNeedsFixture {
		t.Fatalf("needs-fixture label = %q", got)
	}
	if got := reproStateFromLabels(nil, ReproStateReady); got != ReproStateReady {
		t.Fatalf("fallback = %q", got)
	}
	if got := reproStateFromLabels(nil, ""); got != ReproStateNeedsFixture {
		t.Fatalf("default = %q", got)
	}
}

func TestIssueNumberFromURL(t *testing.T) {
	if n := issueNumberFromURL("https://github.com/example/repo/issues/42"); n != 42 {
		t.Fatalf("number = %d", n)
	}
}

func TestGhIssueTracker_Open(t *testing.T) {
	rec := &recordingRun{respond: func(args []string) (string, error) {
		switch args[1] {
		case "list":
			return "[]", nil
		case "create":
			return "https://github.com/example/repo/issues/7\n", nil
		}
		return "", errors.New("unexpected: " + strings.Join(args, " "))
	}}
	g := &ghIssueTracker{repo: "example/repo", run: rec.run}

	ref, action, err := g.UpsertTicket(context.Background(), sampleTicket())
	if err != nil {
		t.Fatalf("UpsertTicket: %v", err)
	}
	if action != "opened" || ref.Number != 7 {
		t.Fatalf("ref = %+v action = %q", ref, action)
	}
	joined := rec.joined()
	if len(joined) != 2 {
		t.Fatalf("calls = %v", joined)
	}
	if !strings.Contains(joined[0], "issue list -R example/repo") {
		t.Fatalf("list call = %s", joined[0])
	}
	if !strings.Contains(joined[1], "issue create -R example/repo") ||
		!strings.Contains(joined[1], "--label sync-failure") ||
		!strings.Contains(joined[1], "--label needs-fixture") {
		t.Fatalf("create call = %s", joined[1])
	}
}

func TestGhIssueTracker_UpdatePreservesTriage(t *testing.T) {
	existing := sampleTicket()
	existing.Count = 2
	existing.Fixture = "internal/connectors/testdata/repro/spotify/track-object-null-items/fixture.json"
	existing.ReproTest = "TestSpotifyLikedTracks_ReproFixture"
	existing.Expected = "2 tracks"
	existing.Actual = "0 tracks"
	existing.Tier = "fixture"
	existing.Title = "Spotify saved-tracks parse regression"
	body := renderIssueBody(existing)

	// Labels are objects in real `gh issue list --json` output, not names.
	listJSON := `[{"number":12,"state":"open","url":"https://github.com/example/repo/issues/12",` +
		`"labels":[{"name":"sync-failure"},{"name":"ready"}],"body":` + jsonString(body) + `}]`
	rec := &recordingRun{respond: func(args []string) (string, error) {
		switch args[1] {
		case "list":
			return listJSON, nil
		case "edit":
			return "", nil
		}
		return "", errors.New("unexpected: " + strings.Join(args, " "))
	}}
	g := &ghIssueTracker{repo: "example/repo", run: rec.run}

	tk := sampleTicket()
	tk.Count = 3
	tk.LastSeen = time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	ref, action, err := g.UpsertTicket(context.Background(), tk)
	if err != nil {
		t.Fatalf("UpsertTicket: %v", err)
	}
	if action != "updated" || ref.Number != 12 {
		t.Fatalf("ref = %+v action = %q", ref, action)
	}
	joined := rec.joined()
	var editCall string
	for _, c := range joined {
		if strings.Contains(c, "issue edit 12") {
			editCall = c
		}
	}
	if editCall == "" {
		t.Fatalf("no edit call in %v", joined)
	}
	// The ready label already matches the state: no label surgery expected.
	if strings.Contains(editCall, "--add-label") || strings.Contains(editCall, "--remove-label") {
		t.Fatalf("labels must not be touched when they already match: %s", editCall)
	}
	if strings.Contains(editCall, "--title") {
		t.Fatalf("update must not touch the issue title: %s", editCall)
	}
	// Extract the body arg (passed via --body) and check the merge.
	var out *MaintenanceTicket
	for _, c := range rec.calls {
		for i, a := range c {
			if a == "--body" && i+1 < len(c) {
				out = parseTicketBody(c[i+1])
			}
		}
	}
	if out == nil {
		t.Fatal("edit body must carry an evidence block")
	}
	if out.Count != 3 {
		t.Fatalf("scanner fields not refreshed: %+v", out)
	}
	if out.Fixture != existing.Fixture || out.ReproTest != existing.ReproTest ||
		out.Expected != existing.Expected || out.Actual != existing.Actual ||
		out.Tier != existing.Tier || out.Title != existing.Title {
		t.Fatalf("triage clobbered: %+v", out)
	}
	if out.ReproState != ReproStateReady {
		t.Fatalf("repro_state must follow the ready label: %q", out.ReproState)
	}
}

func TestGhIssueTracker_ReopenAndLabelSync(t *testing.T) {
	// gh emits state uppercase ("CLOSED"); the reopen check must accept
	// either casing.
	for _, state := range []string{"closed", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			existing := sampleTicket()
			body := renderIssueBody(existing)
			listJSON := `[{"number":9,"state":"` + state + `","url":"https://github.com/example/repo/issues/9",` +
				`"labels":[{"name":"sync-failure"},{"name":"ready"},{"name":"needs-fixture"}],"body":` + jsonString(body) + `}]`
			rec := &recordingRun{respond: func(args []string) (string, error) {
				switch args[1] {
				case "list":
					return listJSON, nil
				default:
					return "", nil
				}
			}}
			g := &ghIssueTracker{repo: "example/repo", run: rec.run}

			_, action, err := g.UpsertTicket(context.Background(), sampleTicket())
			if err != nil {
				t.Fatalf("UpsertTicket: %v", err)
			}
			if action != "reopened" {
				t.Fatalf("action = %q, want reopened", action)
			}
			joined := rec.joined()
			var labelCall, reopenCall string
			for _, c := range joined {
				if strings.Contains(c, "--add-label") {
					labelCall = c
				}
				if strings.Contains(c, "issue reopen") {
					reopenCall = c
				}
			}
			if reopenCall == "" {
				t.Fatalf("closed issue must reopen: %v", joined)
			}
			// Both labels present → ready wins, needs-fixture gets dropped.
			if !strings.Contains(labelCall, "--add-label ready") || !strings.Contains(labelCall, "--remove-label needs-fixture") {
				t.Fatalf("label sync = %s", labelCall)
			}
		})
	}
}

func TestGhIssueTracker_Errors(t *testing.T) {
	rec := &recordingRun{respond: func(args []string) (string, error) {
		return "", errors.New("gh: not authed")
	}}
	g := &ghIssueTracker{repo: "example/repo", run: rec.run}
	if _, _, err := g.UpsertTicket(context.Background(), sampleTicket()); err == nil {
		t.Fatal("tracker errors must surface")
	}
}

// jsonString JSON-encodes a string for embedding in a canned gh response.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
