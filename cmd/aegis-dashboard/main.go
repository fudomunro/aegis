// Command aegis-dashboard generates the sync-maintenance dashboard as a
// static site from the repo's GitHub-issue maintenance record. It is the
// release-pipeline entry point (issue #239): every release builds and
// publishes an updated dashboard next to the marketing site. The instance
// build over signal bundles stays on the admin surface.
//
// The record is scraped with the gh CLI (the same transport as the ticket
// surface), so CI needs no GitHub token of its own — GH_TOKEN is enough:
//
//	go run ./cmd/aegis-dashboard -repo owner/name -out site/maintenance \
//	  -manifest site/maintenance/services.json
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fudomunro/aegis/internal/core"
)

func main() {
	repo := flag.String("repo", os.Getenv("GITHUB_REPOSITORY"), "GitHub repo (owner/name) to scrape for the maintenance record")
	out := flag.String("out", "site/maintenance", "output directory for the static dashboard")
	manifest := flag.String("manifest", "", "optional services manifest (JSON) naming the pages to show")
	version := flag.String("version", core.Version, "build version stamp shown on the pages")
	commit := flag.String("commit", core.Commit, "build commit stamp shown on the pages")
	flag.Parse()

	if *repo == "" {
		fail("the -repo flag (or GITHUB_REPOSITORY) is required")
	}
	var metas []core.DashboardServiceMeta
	if *manifest != "" {
		var err error
		if metas, err = core.LoadDashboardServiceMetas(*manifest); err != nil {
			fail(err.Error())
		}
	}
	// A scrape failure fails the build: the release publishes evidence, and
	// an empty record must not overwrite a good previous dashboard.
	records, err := core.NewGhIssueTracker(*repo).ListIssues(context.Background())
	if err != nil {
		fail("scrape maintenance record: " + err.Error())
	}

	data := core.BuildMaintenanceDashboard(nil, metas, records, core.MaturityByService(), time.Now().UTC())
	data.Version = *version
	data.Commit = *commit
	index, err := core.WriteMaintenanceDashboard(*out, data,
		core.DashboardPublishedFileMode, core.DashboardPublishedDirMode)
	if err != nil {
		fail("build dashboard: " + err.Error())
	}
	fmt.Println(index)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "aegis-dashboard: "+msg)
	os.Exit(1)
}
