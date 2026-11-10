package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ghClient is the GitHub issue transport (issue #86, issue #344): ticket
// upserts for the scanner and the issue-record scrape for the dashboard
// share one gh-CLI client, so the tool needs no GitHub token of its own —
// GH_TOKEN in CI, the operator's gh auth locally. run is the exec seam for
// tests; it receives gh arguments without the binary name and returns
// stdout.
type ghClient struct {
	repo string
	run  func(ctx context.Context, args ...string) (string, error)
}

// newGHClient builds a client for the given owner/name repo.
func newGHClient(repo string) *ghClient {
	return &ghClient{
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
