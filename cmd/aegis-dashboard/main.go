// Command aegis-dashboard generates the sync-maintenance dashboard as a
// static site from the repo's GitHub-issue maintenance record (issue #239).
// It is a separate tool that analyzes the progress of the project (issue
// #335), not part of the application code: it collects data from the gh
// CLI, the committed services manifest, captured signal bundle files, and
// the connector metadata catalog. The instance keeps only signal captures
// (POST /v1/admin/maintenance/signals) on the admin surface.
//
// Release build (the pages.yml entry point):
//
//	go run ./cmd/aegis-dashboard -repo owner/name -out site/maintenance \
//	  -manifest site/maintenance/services.json
//
// Instance build from captured bundles:
//
//	go run ./cmd/aegis-dashboard -repo owner/name \
//	  -bundles "$DATA/maintenance" -out "$DATA/maintenance/dashboard" -private
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	repo := flag.String("repo", os.Getenv("GITHUB_REPOSITORY"), "GitHub repo (owner/name) to scrape for the maintenance record")
	out := flag.String("out", "site/maintenance", "output directory for the static dashboard")
	manifest := flag.String("manifest", "", "optional services manifest (JSON) naming the pages to show")
	bundles := flag.String("bundles", "", "optional directory of captured signal bundles (signals-*.json) to build capture history from")
	version := flag.String("version", "dev", "build version stamp for record-only builds (captures carry their own stamps)")
	commit := flag.String("commit", "none", "build commit stamp for record-only builds (captures carry their own stamps)")
	private := flag.Bool("private", false, "write the private posture (0600/0700) for instance builds under a data directory")
	allowScrapeFailure := flag.Bool("allow-scrape-failure", false, "build without the maintenance record on a scrape error (notes the gap instead of failing)")
	flag.Parse()

	if *repo == "" {
		fail("the -repo flag (or GITHUB_REPOSITORY) is required")
	}
	var metas []DashboardServiceMeta
	if *manifest != "" {
		var err error
		if metas, err = LoadDashboardServiceMetas(*manifest); err != nil {
			fail(err.Error())
		}
	}
	var series []SignalBundle
	if *bundles != "" {
		var err error
		if series, err = loadSignalBundles(*bundles); err != nil {
			fail("load signal bundles: " + err.Error())
		}
	}

	// A scrape failure fails the build by default: the release publishes
	// evidence, and an empty record must not overwrite a good previous
	// dashboard. -allow-scrape-failure degrades to a noted gap instead.
	records, scrapeErr := newIssueScraper(*repo).ListIssues(context.Background())
	if scrapeErr != nil {
		if !*allowScrapeFailure {
			fail("scrape maintenance record: " + scrapeErr.Error())
		}
		fmt.Fprintln(os.Stderr, "aegis-dashboard: maintenance record unavailable: "+scrapeErr.Error())
	}

	data := BuildMaintenanceDashboard(series, metas, records, maturityByService(), time.Now().UTC())
	// Captures carry their own build stamps; the flags stamp record-only
	// builds and fill any capture that lacks one.
	if data.Version == "" {
		data.Version = *version
	}
	if data.Commit == "" {
		data.Commit = *commit
	}
	if scrapeErr != nil {
		data.FixHistoryError = scrapeErr.Error()
	}

	fileMode, dirMode := os.FileMode(DashboardPublishedFileMode), os.FileMode(DashboardPublishedDirMode)
	if *private {
		fileMode, dirMode = DashboardPrivateFileMode, DashboardPrivateDirMode
	}
	index, err := WriteMaintenanceDashboard(*out, data, fileMode, dirMode)
	if err != nil {
		fail("build dashboard: " + err.Error())
	}
	fmt.Println(index)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "aegis-dashboard: "+msg)
	os.Exit(1)
}
