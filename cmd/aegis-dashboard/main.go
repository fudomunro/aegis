// Command aegis-dashboard is the sync-maintenance tool (issue #239, issue
// #335, issue #344): it analyzes the progress of the project and runs the
// loop's outside-the-application phases. It is not part of the application
// code — data collection is file- and CLI-shaped, so the service owes the
// tool nothing beyond artifacts it already produces (signal captures, the
// ticket contract, the connector metadata catalog).
//
// Two subcommands; bare flags mean build, so existing invocations keep
// working:
//
//	build  — generate the static dashboard from the repo's GitHub-issue
//	         maintenance record (the pages.yml entry point):
//
//	go run ./cmd/aegis-dashboard build -repo owner/name -out site/maintenance \
//	  -manifest site/maintenance/services.json
//
//	scan   — run the maintenance scanner over captured signal bundles
//	         (POST /v1/admin/maintenance/signals used to do this in-app),
//	         ticketing recurrent failures as GitHub issues:
//
//	go run ./cmd/aegis-dashboard scan -repo owner/name \
//	  -bundles "$DATA/maintenance"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "scan" {
		runScan(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "build" {
		args = args[1:]
	} else if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		fail("unknown command " + args[0] + " (want build or scan)")
	}
	runBuild(args)
}

func runBuild(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "GitHub repo (owner/name) to scrape for the maintenance record")
	out := fs.String("out", "site/maintenance", "output directory for the static dashboard")
	manifest := fs.String("manifest", "", "optional services manifest (JSON) naming the pages to show")
	bundles := fs.String("bundles", "", "optional directory of captured signal bundles (signals-*.json) to build capture history from")
	version := fs.String("version", "dev", "build version stamp for record-only builds (captures carry their own stamps)")
	commit := fs.String("commit", "none", "build commit stamp for record-only builds (captures carry their own stamps)")
	private := fs.Bool("private", false, "write the private posture (0600/0700) for instance builds under a data directory")
	allowScrapeFailure := fs.Bool("allow-scrape-failure", false, "build without the maintenance record on a scrape error (notes the gap instead of failing)")
	fs.Parse(args)

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
		// An explicit -bundles with nothing behind it is a wrong path; a
		// record-only build must drop the flag instead.
		if len(series) == 0 {
			fail("no signal bundles in " + *bundles)
		}
	}

	// A scrape failure fails the build by default: the release publishes
	// evidence, and an empty record must not overwrite a good previous
	// dashboard. -allow-scrape-failure degrades to a noted gap instead.
	records, scrapeErr := newGHClient(*repo).ListIssues(context.Background())
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

// runScan runs the maintenance scanner over captured bundles and prints the
// scan report as JSON. Ticketing needs a repo (the gh CLI); without one the
// pass scans only, marking ticketable clusters "disabled".
func runScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "GitHub repo (owner/name) to open tickets in; empty scans without ticketing")
	bundles := fs.String("bundles", "", "directory of captured signal bundles (signals-*.json) to scan")
	fs.Parse(args)

	if *bundles == "" {
		fail("the -bundles flag is required")
	}
	var tracker ticketTracker
	if *repo != "" {
		tracker = newGHClient(*repo)
	}
	report, err := scanBundles(context.Background(), *bundles, tracker)
	if err != nil {
		fail("scan: " + err.Error())
	}
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail("encode report: " + err.Error())
	}
	fmt.Println(string(out))
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "aegis-dashboard: "+msg)
	os.Exit(1)
}
