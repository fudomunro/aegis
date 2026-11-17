package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

// Synthetic canary (issue #243): the loop's scheduled exercise, outside the
// application like the rest of the maintenance tooling. It drives the
// connector functionality against the mock connector — the drift-injection
// harness's saved-tracks shapes — and files a sync-failure ticket when the
// behavior regresses. No live Aegis, no live provider, no credentials: the
// exercise is self-contained, so a laptop cron or a scheduled GitHub
// Actions workflow can run it three times a day.
//
// The scenarios pin the canary runner's semantics (internal/syncjobs):
// fetch errors fail, a page that claims a library while listing nothing is
// shape drift, a genuinely empty library is health. The healthy and
// learned-rename shapes also pin the mock fixture's track count, so a
// silent partial loss fails the same way a total loss does.

// The exercise drives the connector the mock models: the Spotify
// saved-tracks page. Liked-tracks is the canaried surface.
const (
	canaryService = "spotify"
	canaryKind    = "tracks"
	canaryPageMax = 50
)

// canaryScenarios are the mock shapes the exercise replays, with the
// outcome each must show. The track counts match the mock's fixture.
var canaryScenarios = []struct{ mode, want string }{
	{connectors.FakeModeHealthy, "3 tracks listed"},
	{connectors.FakeModeEmpty, "coherent empty library"},
	{connectors.FakeModeDriftItems, "3 tracks listed"},
	{connectors.FakeModeDriftUnmapped, "shape drift flagged"},
	{connectors.FakeModeHTTP500, "upstream error surfaced"},
	{connectors.FakeModeHTTP401, "upstream error surfaced"},
	{connectors.FakeModeGarbage, "upstream error surfaced"},
}

// CanaryCheck is one mock shape's verdict.
type CanaryCheck struct {
	Scenario string `json:"scenario"`
	Status   string `json:"status"` // pass | fail
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Detail   string `json:"detail,omitempty"`
}

// CanaryTicketRef says what the ticket surface did for one failure
// signature ("disabled" when no repo was given, like the scanner).
type CanaryTicketRef struct {
	Signature string `json:"signature"`
	Action    string `json:"action"` // opened | updated | reopened | disabled | error
	Number    int    `json:"number,omitempty"`
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
}

// CanaryReport is one exercise run.
type CanaryReport struct {
	Result  string            `json:"result"` // pass | fail
	Checks  []CanaryCheck     `json:"checks"`
	Tickets []CanaryTicketRef `json:"tickets,omitempty"`
}

// canaryOutcome names one fetch the way the canary judgment does.
func canaryOutcome(total, listed int, fetchErr error) string {
	switch {
	case fetchErr != nil:
		return "upstream error surfaced"
	case total > 0 && listed == 0:
		return "shape drift flagged"
	case listed > 0:
		return strconv.Itoa(listed) + " tracks listed"
	default:
		return "coherent empty library"
	}
}

// runCanaryExercise replays every mock shape through fetch and reports each
// verdict against what that shape must show. setMode switches the mock;
// fetch reads one saved-tracks page and reports its total, its listed
// count, and any error.
func runCanaryExercise(setMode func(string) error, fetch func(context.Context) (int, int, error)) []CanaryCheck {
	checks := make([]CanaryCheck, 0, len(canaryScenarios))
	for _, sc := range canaryScenarios {
		check := CanaryCheck{Scenario: sc.mode, Expected: sc.want}
		if err := setMode(sc.mode); err != nil {
			check.Status = "fail"
			check.Actual = "scenario setup failed"
			check.Detail = err.Error()
			checks = append(checks, check)
			continue
		}
		total, listed, fetchErr := fetch(context.Background())
		check.Actual = canaryOutcome(total, listed, fetchErr)
		switch {
		case fetchErr != nil:
			check.Detail = fetchErr.Error()
		case total > 0 && listed == 0:
			check.Detail = fmt.Sprintf("total %d, %d listed", total, listed)
		}
		if check.Actual == check.Expected {
			check.Status = "pass"
		} else {
			check.Status = "fail"
		}
		checks = append(checks, check)
	}
	return checks
}

// canaryHost is the minimal connectors.Host the exercise needs: the mock's
// endpoint and a placeholder token. No instance, no OAuth, no state.
type canaryHost struct{ endpoint string }

func (h *canaryHost) GetValidToken(string, *slog.Logger) string { return "synthetic-canary" }
func (h *canaryHost) UpstreamBaseFor(serviceID, fallback string) string {
	return h.endpoint
}
func (h *canaryHost) ServiceConfig(serviceID string) *connectors.ServiceInfo {
	return &connectors.ServiceInfo{ID: serviceID, Action: "inject", Endpoint: h.endpoint}
}
func (h *canaryHost) BaseURL() string                            { return "https://example.com" }
func (h *canaryHost) CacheGet(string) (string, bool)             { return "", false }
func (h *canaryHost) CacheSet(string, string, time.Duration)     {}
func (h *canaryHost) ObserveUpstream(string, time.Duration, int) {}
func (h *canaryHost) LibraryReadable(string) bool                { return true }
func (h *canaryHost) CanonicalOwner(string) string               { return "" }
func (h *canaryHost) SetupSecret(string) string                  { return "" }
func (h *canaryHost) SetupSettings(string) map[string]string     { return nil }

// runSyntheticCanary wires the mock connector and replays the scenarios.
func runSyntheticCanary() []CanaryCheck {
	fu := connectors.NewFakeUpstream()
	up := httptest.NewServer(fu)
	defer up.Close()
	conn := connectors.NewSpotifyConnector(&canaryHost{endpoint: up.URL})
	return runCanaryExercise(fu.SetMode, func(ctx context.Context) (int, int, error) {
		page, err := conn.ListUserLikedTracksPage(ctx, "", canaryPageMax)
		return page.Total, len(page.Tracks), err
	})
}

// canaryTickets turns failing checks into maintenance tickets: one per
// failure signature — alike failures normalize to one ticket, the scanner's
// rule. The service names the adapter under exercise so the fix cycle
// knows where to look.
func canaryTickets(checks []CanaryCheck, now time.Time) []MaintenanceTicket {
	var out []MaintenanceTicket
	for _, c := range checks {
		if c.Status != "fail" {
			continue
		}
		failText := c.Scenario + ": expected " + c.Expected + ", got " + c.Actual
		if c.Detail != "" {
			failText += " (" + c.Detail + ")"
		}
		actual := c.Actual
		if c.Detail != "" {
			actual += " (" + c.Detail + ")"
		}
		excerpt := failText
		if r := []rune(excerpt); len(r) > scanTitleErrorCap {
			excerpt = string(r[:scanTitleErrorCap]) + "…"
		}
		out = append(out, MaintenanceTicket{
			Signature:  canaryService + "|canary|terminal|" + normalizeErrorShape(failText),
			Title:      fmt.Sprintf("%s canary failed: %s", canaryService, excerpt),
			Service:    canaryService,
			Kind:       canaryKind,
			JobType:    "canary",
			ErrorClass: "terminal",
			ReproState: reproStateNeedsFixture,
			Expected:   c.Expected,
			Actual:     actual,
			Tier:       scanDefaultTier,
			FirstSeen:  now,
			LastSeen:   now,
			Count:      1,
			Observations: []MaintenanceObservation{{
				Bundle:     "synthetic-canary",
				CapturedAt: now,
				Error:      failText,
			}},
		})
	}
	return out
}

// canaryReport summarizes one exercise and files a ticket per failing
// signature. A nil client reports ticketable failures as "disabled" — the
// dry-run shape the scanner shares.
func canaryReport(ctx context.Context, checks []CanaryCheck, g *ghClient) *CanaryReport {
	report := &CanaryReport{Result: "pass", Checks: checks}
	for _, c := range checks {
		if c.Status == "fail" {
			report.Result = "fail"
		}
	}
	for _, t := range canaryTickets(checks, time.Now().UTC()) {
		ref := CanaryTicketRef{Signature: t.Signature}
		switch {
		case g == nil:
			ref.Action = "disabled"
		default:
			issue, action, err := g.UpsertTicket(ctx, t)
			if err != nil {
				ref.Action = "error"
				ref.Error = err.Error()
			} else {
				ref.Action = action
				ref.Number = issue.Number
				ref.URL = issue.URL
			}
		}
		report.Tickets = append(report.Tickets, ref)
	}
	return report
}

// runCanary exercises the connector against the mock connector and prints
// the report as JSON. With a repo, each failing signature is filed (or
// refreshed) as a sync-failure ticket; without one the run reports
// "disabled" and touches nothing.
func runCanary(args []string) {
	fs := flag.NewFlagSet("canary", flag.ExitOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "GitHub repo (owner/name) to file tickets in; empty exercises without ticketing")
	fs.Parse(args)

	var g *ghClient
	if *repo != "" {
		g = newGHClient(*repo)
	}
	report := canaryReport(context.Background(), runSyntheticCanary(), g)
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail("encode report: " + err.Error())
	}
	fmt.Println(string(out))
}
