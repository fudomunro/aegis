package mail

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

// Benchmark fixtures for the mail archive (issue #75 measurement plan).
// Run with:
//
//	go test -bench 'BenchmarkArchive' -benchtime=2s ./internal/core/mail/
//
// Body and payload sizes track the stress sample set (docs/development.md):
// ~2KB of body text and a ~4KB gzip provider payload per message.

const benchBodyFiller = "archive benchmark body filler sentence "

func benchBody(n int) string {
	return strings.Repeat(benchBodyFiller, n)
}

func benchGzipPayload(b *testing.B, size int) []byte {
	b.Helper()
	raw := []byte(strings.Repeat("x", size))
	gz, err := GzipPayload(raw)
	if err != nil {
		b.Fatalf("gzip payload: %v", err)
	}
	return gz
}

func benchArchive(b *testing.B) *Archive {
	b.Helper()
	a, err := Open(b.TempDir() + "/mail-archive.db")
	if err != nil {
		b.Fatalf("open archive: %v", err)
	}
	if a == nil {
		b.Fatal("open archive: got nil archive")
	}
	b.Cleanup(func() { _ = a.Close() })
	return a
}

func benchRecord(i int, body string, payload []byte) Record {
	return Record{
		Service:     "takeout",
		ServiceName: "Google Takeout",
		ID:          fmt.Sprintf("msgid:bench-%d@example.com", i),
		MessageID:   fmt.Sprintf("bench-%d@example.com", i),
		Mailbox:     "INBOX",
		Labels:      []string{"TAKEOUT", "INBOX"},
		Subject:     fmt.Sprintf("archive bench subject %d", i),
		Snippet:     body[:64],
		FromName:    "Bench Sender",
		FromEmail:   "sender@example.com",
		To: []kinds.Contact{
			kinds.NewContact("takeout", "Google Takeout", "", "Bench Recipient",
				[]string{"recipient@example.com"}, nil),
		},
		SentAt:      fmt.Sprintf("2024-01-%02dT10:00:00Z", (i%28)+1),
		ReceivedAt:  fmt.Sprintf("2024-01-%02dT10:00:01Z", (i%28)+1),
		Direction:   "inbound",
		Read:        true,
		BodyText:    body,
		PayloadGzip: payload,
	}
}

// BenchmarkArchiveUpsert measures fresh inserts (the Takeout ingest write
// path: identity-stamped row + inline FTS maintenance + gzip payload).
// Record construction stays outside the timed region.
func BenchmarkArchiveUpsert(b *testing.B) {
	a := benchArchive(b)
	body := benchBody(50)
	payload := benchGzipPayload(b, 4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		rec := benchRecord(i, body, payload)
		b.StartTimer()
		if err := a.Upsert(rec); err != nil {
			b.Fatalf("upsert %d: %v", i, err)
		}
	}
}

// BenchmarkArchiveUpsertUpdate re-upserts rows that already exist — the
// ON CONFLICT path plus the FTS delete/insert pair per message.
func BenchmarkArchiveUpsertUpdate(b *testing.B) {
	a := benchArchive(b)
	body := benchBody(50)
	payload := benchGzipPayload(b, 4096)
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		if err := a.Upsert(benchRecord(i, body, payload)); err != nil {
			b.Fatalf("seed upsert %d: %v", i, err)
		}
	}
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		rec := benchRecord(i, body, payload)
		b.StartTimer()
		if err := a.Upsert(rec); err != nil {
			b.Fatalf("update upsert %d: %v", i, err)
		}
	}
}

// BenchmarkArchiveSearch measures the read path over a fixed corpus: FTS
// match plus page hydration, the shape /v1/mail/search exercises.
func BenchmarkArchiveSearch(b *testing.B) {
	a := benchArchive(b)
	body := benchBody(50)
	payload := benchGzipPayload(b, 4096)
	const corpus = 2000
	for i := 0; i < corpus; i++ {
		if err := a.Upsert(benchRecord(i, body, payload)); err != nil {
			b.Fatalf("seed upsert %d: %v", i, err)
		}
	}
	queries := []SearchQuery{
		{Q: "subject", Limit: 50},
		{Q: "benchmark body filler", Limit: 50},
		{From: "sender@example.com", Limit: 50},
		{Q: "subject", Limit: 50, WithBody: true},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := queries[i%len(queries)]
		if _, err := a.Search(q); err != nil {
			b.Fatalf("search %+v: %v", q, err)
		}
	}
}
