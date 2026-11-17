package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/mail"
)

// End-to-end Takeout ingest benchmark (issue #75 measurement plan). This
// is the full operator path the ticket measured: export walk + RFC822 parse
// + identity check + per-message transaction + inline FTS maintenance +
// gzip payload storage into a real SQLite mail archive. The phase-one
// ingest optimizations (batch transactions, deferred FTS, import PRAGMAs)
// are expected to move this number. Run with:
//
//	go test -bench 'BenchmarkImportTakeout' -benchtime=1x ./internal/cli/
//
// One iteration ingests the whole fixture; -benchtime=1x (or a small fixed
// count) is appropriate because an iteration is hundreds of milliseconds.
// The fixture is deliberately split across batches (batch < messages) so
// the durable walk cursor resume is on the measured path.

const benchImportBody = "import benchmark body filler line\n"

// writeImportBenchMbox writes n RFC822 messages in one mbox stream.
func writeImportBenchMbox(path string, n, bodyLines int) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	body := strings.Repeat(benchImportBody, bodyLines)
	var written int64
	for i := 0; i < n; i++ {
		msg := fmt.Sprintf(
			"From bench-%d@example.com Fri Jan 01 10:00:00 2024\n"+
				"From: Bench Sender <sender@example.com>\n"+
				"To: Bench Recipient <recipient@example.com>\n"+
				"Subject: import bench subject %d\n"+
				"Message-ID: <bench-%d@example.com>\n"+
				"Date: Fri, 01 Jan 2024 10:00:00 +0000\n"+
				"MIME-Version: 1.0\n"+
				"Content-Type: text/plain; charset=utf-8\n"+
				"\n%s\n",
			i, i, i, body)
		nr, err := f.WriteString(msg)
		written += int64(nr)
		if err != nil {
			return written, err
		}
	}
	return written, f.Sync()
}

// BenchmarkImportTakeout measures a full -import-takeout run per iteration
// against a fresh archive. Headline numbers are wall time per run (ns/op),
// source throughput (MB/s), and messages/sec (a custom metric). B/op is
// not reported: per-iteration setup (fresh archive create/open) would
// swamp it — per-message allocation cost lives in the mail package's
// BenchmarkArchiveUpsert. Run with:
//
//	go test -bench 'BenchmarkImportTakeout' -benchtime=1x ./internal/cli/
//
// One iteration ingests the whole fixture; -benchtime=1x (or a small fixed
// count) is appropriate because an iteration is hundreds of milliseconds.
// The fixture is deliberately split across batches (batch < messages) so
// the durable walk cursor resume is on the measured path.
func BenchmarkImportTakeout(b *testing.B) {
	const messages = 3000
	const batch = 1000
	mboxPath := filepath.Join(b.TempDir(), "All mail Including Spam and Trash.mbox")
	size, err := writeImportBenchMbox(mboxPath, messages, 40)
	if err != nil {
		b.Fatalf("write mbox: %v", err)
	}
	// Import logs one batch line per step; keep benchmark output readable.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(prev) })

	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dir := b.TempDir()
		archivePath := filepath.Join(dir, "mail-archive.db")
		ledgerPath := filepath.Join(dir, "copy-ledger.db")
		b.StartTimer()

		if code := ImportTakeout(archivePath, ledgerPath, mboxPath, batch); code != 0 {
			b.Fatalf("ImportTakeout exited %d", code)
		}

		b.StopTimer()
		if n := archiveMessageCount(b, archivePath); n != messages {
			b.Fatalf("archive holds %d messages, want %d", n, messages)
		}
		b.StartTimer()
	}
	b.ReportMetric(float64(messages)*float64(b.N)/b.Elapsed().Seconds(), "msgs/sec")
}

// archiveMessageCount reports stored rows via the archive status surface.
func archiveMessageCount(b *testing.B, path string) int {
	b.Helper()
	// Opened through the mail package the CLI itself uses; keep the check
	// cheap so it stays outside the measured window.
	a, err := mail.Open(path)
	if err != nil {
		b.Fatalf("reopen archive: %v", err)
	}
	defer a.Close()
	st, err := a.Status()
	if err != nil {
		b.Fatalf("archive status: %v", err)
	}
	return st.MessageCount
}
