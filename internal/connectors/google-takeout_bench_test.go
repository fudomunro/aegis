package connectors

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Takeout walk benchmarks (issue #75 measurement plan). They measure the
// export walk and RFC822 parse only — no archive writes — so the cost of
// reading the source is separable from the SQLite ingest cost (bench that
// end to end in internal/cli). Run with:
//
//	go test -bench 'BenchmarkTakeoutWalk' -benchtime=2s ./internal/connectors/
//
// Report both mbox and zip fixtures: zip streams are not seekable, so the
// durable cursor resumes a zip entry by draining bytes rather than seeking.

const benchMboxBody = "takeout walk benchmark body filler line\n"

// writeBenchMbox writes n RFC822 messages in one mbox stream, each with
// bodyLines filler lines (~30 bytes each).
func writeBenchMbox(path string, n, bodyLines int) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	body := strings.Repeat(benchMboxBody, bodyLines)
	var written int64
	for i := 0; i < n; i++ {
		msg := fmt.Sprintf(
			"From bench-%d@example.com Fri Jan 01 10:00:00 2024\n"+
				"From: Bench Sender <sender@example.com>\n"+
				"To: Bench Recipient <recipient@example.com>\n"+
				"Subject: takeout bench subject %d\n"+
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

// writeBenchZip writes one Takeout-shaped zip holding a single mbox entry.
func writeBenchZip(path string, n, bodyLines int) (int64, error) {
	dir := filepath.Dir(path)
	mboxPath := filepath.Join(dir, "bench.mbox")
	size, err := writeBenchMbox(mboxPath, n, bodyLines)
	if err != nil {
		return 0, err
	}
	zf, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer zf.Close()
	zw := zip.NewWriter(zf)
	w, err := zw.Create("Takeout/Mail/All mail Including Spam and Trash.mbox")
	if err != nil {
		return 0, err
	}
	mbox, err := os.Open(mboxPath)
	if err != nil {
		return 0, err
	}
	defer mbox.Close()
	if _, err := io.Copy(w, mbox); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return size, zf.Sync()
}

func benchWalk(b *testing.B, root string, messages int) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var seen int
		cursor := takeoutCursor{EntryIdx: -1}
		err := walkTakeoutFrom(root, &cursor, func(raw []byte, source string) error {
			if _, perr := parseTakeoutMessage(raw, source); perr != nil {
				return nil
			}
			seen++
			return nil
		})
		if err != nil {
			b.Fatalf("walk: %v", err)
		}
		if seen != messages {
			b.Fatalf("walked %d messages, want %d", seen, messages)
		}
	}
}

// BenchmarkTakeoutWalkMbox walks and parses an mbox export end to end.
func BenchmarkTakeoutWalkMbox(b *testing.B) {
	const messages = 5000
	path := filepath.Join(b.TempDir(), "All mail Including Spam and Trash.mbox")
	size, err := writeBenchMbox(path, messages, 40)
	if err != nil {
		b.Fatalf("write mbox: %v", err)
	}
	b.SetBytes(size)
	benchWalk(b, path, messages)
}

// BenchmarkTakeoutWalkZip walks and parses a zipped Takeout export.
func BenchmarkTakeoutWalkZip(b *testing.B) {
	const messages = 5000
	path := filepath.Join(b.TempDir(), "takeout.zip")
	size, err := writeBenchZip(path, messages, 40)
	if err != nil {
		b.Fatalf("write zip: %v", err)
	}
	b.SetBytes(size)
	benchWalk(b, path, messages)
}
