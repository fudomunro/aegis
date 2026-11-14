// Package cli holds the operator subcommands the aegis binary exposes
// through flags (seed, import, rekey): one-shot data tooling that runs
// against explicit paths and exits. Wiring and flags stay in the hub.
package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/sampledata"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
)

// runSeedSampleCLI writes synthetic sample fixtures under the data directory
// and loads the mail set into the configured archive, then exits.
// Data is fictional; safe to regenerate. See README "Sample data".
func SeedSample(dataDir, archivePath string, count int, stress bool) int {
	count = seedSampleCountOrDefault(count, stress)
	sampleDir := filepath.Join(dataDir, "sample")
	mboxPath, libraryPath, _, err := sampledata.WriteSampleFiles(sampleDir, count)
	if err != nil {
		slog.Error("failed to write sample files", "dir", sampleDir, "error", err)
		return 1
	}
	set, err := sampledata.SeedSampleArchive(archivePath, count)
	if err != nil {
		slog.Error("failed to seed mail archive", "path", archivePath, "error", err)
		return 1
	}
	slog.Info("sample data ready (all synthetic)",
		"mail_messages", len(set.Records),
		"oldest_id", set.OldestID,
		"newest_id", set.NewestID,
		"mail_archive", archivePath,
		"mbox", mboxPath,
		"library_json", libraryPath,
	)
	return 0
}

func seedSampleCountOrDefault(count int, stress bool) int {
	if stress {
		if count > 0 && count != sampledata.SampleMailCount {
			return count
		}
		return sampledata.SampleMailStressCount
	}
	if count <= 0 {
		return sampledata.SampleMailCount
	}
	return count
}

// runSeedDemoDatasetsCLI writes named demo packs (shire, gondor, lincoln)
// under <data-dir>/sample/datasets/ and loads each partition's mail into the
// archive. Content rules vary by pack; see docs/demo-datasets.md (issue #217).
func SeedDemoDatasets(dataDir, archivePath string) int {
	outDir := filepath.Join(dataDir, "sample", "datasets")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		slog.Error("failed to create demo dataset dir", "dir", outDir, "error", err)
		return 1
	}
	archive, err := mail.Open(archivePath)
	if err != nil {
		slog.Error("failed to open mail archive", "path", archivePath, "error", err)
		return 1
	}
	if archive != nil {
		defer archive.Close()
	}
	for _, dataset := range sampledata.DemoDatasetIDs() {
		lib, ok := sampledata.GenerateDemoDataset(dataset)
		if !ok {
			continue
		}
		libPath := filepath.Join(outDir, dataset+"-library.json")
		if err := sampledata.WriteSampleLibraryJSON(libPath, lib); err != nil {
			slog.Error("failed to write demo library", "path", libPath, "error", err)
			return 1
		}
		mail := sampledata.GenerateDemoMail(dataset)
		if archive != nil {
			for _, rec := range mail.Records {
				if err := archive.Upsert(rec); err != nil {
					slog.Error("failed to seed demo mail", "dataset", dataset, "error", err)
					return 1
				}
			}
		}
		slog.Info("demo dataset ready",
			"dataset", dataset,
			"mail_messages", len(mail.Records),
			"library_json", libPath,
		)
	}
	slog.Info("demo dataset packs written", "dir", outDir, "mail_archive", archivePath)
	return 0
}

// runImportTakeoutCLI syncs a read-only Takeout export into the mail archive
// and exits. Used for large local exports that should not go through HTTP.
func ImportTakeout(archivePath, copyLedgerPath, takeoutPath string, batch int) int {
	archivePath = strings.TrimSpace(archivePath)
	if archivePath == "" {
		slog.Error("mail-archive path is required for -import-takeout")
		return 1
	}
	a, err := mail.Open(archivePath)
	if err != nil {
		slog.Error("failed to open mail archive", "path", archivePath, "error", err)
		return 1
	}
	if a == nil {
		slog.Error("mail archive disabled — set -mail-archive")
		return 1
	}
	defer a.Close()
	copyLedger, err := copies.Open(copyLedgerPath)
	if err != nil {
		slog.Error("failed to open copy ledger", "path", copyLedgerPath, "error", err)
		return 1
	}
	if copyLedger != nil {
		defer copyLedger.Close()
	}
	if batch <= 0 {
		batch = 20000
	}
	ingest := connectors.NewTakeoutIngest()
	sink := mail.Sink{A: a}
	var stored, skipped, scanned, failed int
	done := false
	for {
		// Path persists the export location (the CLI's -import-takeout contract).
		res, err := ingest.IngestMail(context.Background(), sink,
			shared.MailIngestOptions{Path: takeoutPath, MaxMessages: batch})
		stored += res.Stored
		skipped += res.Skipped
		scanned += res.Listed
		failed += res.Failed
		slog.Info("takeout import batch",
			"path", takeoutPath,
			"scanned", res.Listed,
			"stored", res.Stored,
			"skipped", res.Skipped,
			"failed", res.Failed,
			"done", res.Done,
			"error", res.Error,
		)
		if err != nil && res.Error != "" && res.Stored == 0 && res.Listed == 0 {
			return 1
		}
		if res.Done {
			done = true
			break
		}
		if !res.Truncated {
			break
		}
	}
	copyLedger.RecordTakeoutSnapshot(takeoutPath, scanned, stored, skipped, done, connectors.TakeoutServiceID)
	slog.Info("takeout import finished",
		"path", takeoutPath,
		"scanned", scanned,
		"stored", stored,
		"skipped", skipped,
		"failed", failed,
	)
	return 0
}

// runImportCLI runs a one-shot import against any local import store, then
// exits (issue #266). Flag parsing stays here; the store owns the run.
func importStore(name, storePath, importPath string, open func(string) (connectors.ImportStore, error), run func(connectors.ImportStore, string) (connectors.ImportOutcome, error)) int {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		slog.Error(name + " store path is required for -import-" + name)
		return 1
	}
	store, err := open(storePath)
	if err != nil {
		slog.Error("failed to open "+name+" store", "path", storePath, "error", err)
		return 1
	}
	if store == nil || !store.Enabled() {
		slog.Error(name + " store disabled — set -" + name + "-store")
		return 1
	}
	defer store.Close()
	res, err := run(store, importPath)
	if err != nil {
		slog.Error(name+" import failed", "path", importPath, "error", err)
		return 1
	}
	slog.Info(name+" import finished",
		append([]any{"path", importPath}, res.LogAttrs()...)...,
	)
	if res.Failed() {
		return 1
	}
	return 0
}

// runImportQuickenCLI imports a QIF/QFX file or directory into the local
// Quicken store, then exits (issue #141).
func ImportQuicken(storePath, importPath string) int {
	return importStore("quicken", storePath, importPath,
		func(p string) (connectors.ImportStore, error) {
			return connectors.OpenQuickenStore(p)
		},
		func(st connectors.ImportStore, p string) (connectors.ImportOutcome, error) {
			return connectors.ImportQuickenPath(st.(*connectors.QuickenStore), p)
		})
}

// runImportTimelineCLI imports a Google Timeline JSON file/directory/zip into
// the local store, then exits (issue #142).
func ImportTimeline(storePath, importPath string) int {
	return importStore("timeline", storePath, importPath,
		func(p string) (connectors.ImportStore, error) {
			return connectors.OpenTimelineStore(p)
		},
		func(st connectors.ImportStore, p string) (connectors.ImportOutcome, error) {
			return connectors.ImportTimelinePath(st.(*connectors.TimelineStore), p)
		})
}

// runImportSignalCLI imports a Signal export file or directory into the local
// store, then exits (issue #168).
func ImportSignal(storePath, importPath string) int {
	return importStore("signal", storePath, importPath,
		func(p string) (connectors.ImportStore, error) {
			return connectors.OpenSignalStore(p)
		},
		func(st connectors.ImportStore, p string) (connectors.ImportOutcome, error) {
			return connectors.ImportSignalPath(st.(*connectors.SignalStore), p)
		})
}

// ImportLinkedIn imports a LinkedIn data export (.zip or CSV) into the local
// store, then exits (issue #218).
func ImportLinkedIn(storePath, importPath string) int {
	return importStore("linkedin", storePath, importPath,
		func(p string) (connectors.ImportStore, error) {
			return connectors.OpenLinkedInStore(p)
		},
		func(st connectors.ImportStore, p string) (connectors.ImportOutcome, error) {
			return connectors.ImportLinkedInPath(st.(*connectors.LinkedInStore), p)
		})
}

// runTokenRekeyCLI re-encrypts the token store under a new passphrase with
// current KDF parameters, then exits (issue #101). Passing the same passphrase
// still rotates the master salt and raises PBKDF2 cost on legacy files.
func TokenRekey(path, currentPass, newPass string) int {
	path = strings.TrimSpace(path)
	if path == "" {
		slog.Error("token file path is required for -token-new-pass")
		return 1
	}
	if currentPass == "" {
		slog.Error("token-pass (current) is required for -token-new-pass")
		return 1
	}
	if strings.TrimSpace(newPass) == "" {
		slog.Error("token-new-pass must not be empty")
		return 1
	}
	ts, err := tokens.NewTokenStore(path, currentPass)
	if err != nil {
		slog.Error("failed to load token store for rekey", "path", path, "error", err)
		return 1
	}
	if err := ts.ChangePassphrase(newPass); err != nil {
		slog.Error("failed to rekey token store", "path", path, "error", err)
		return 1
	}
	slog.Info("token store rekeyed",
		"path", path,
		"services", len(ts.Tokens),
		"secrets", len(ts.Secrets),
		"iterations", ts.KDFIterations(),
	)
	return 0
}
