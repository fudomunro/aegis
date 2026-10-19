package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestGenerateSampleMail_Deterministic(t *testing.T) {
	a := GenerateSampleMail(8)
	b := GenerateSampleMail(8)
	if len(a.Records) != len(b.Records) {
		t.Fatalf("record count %d != %d", len(a.Records), len(b.Records))
	}
	if a.OldestID != b.OldestID || a.NewestID != b.NewestID {
		t.Fatalf("pins differ: %+v vs %+v", []string{a.OldestID, a.NewestID}, []string{b.OldestID, b.NewestID})
	}
	for i := range a.Records {
		if a.Records[i].ID != b.Records[i].ID || a.Records[i].Subject != b.Records[i].Subject {
			t.Fatalf("record %d drifted: %+v vs %+v", i, a.Records[i], b.Records[i])
		}
	}
}

func TestGenerateSampleMail_IncludesEdgeCases(t *testing.T) {
	set := GenerateSampleMail(3)
	byID := map[string]MailArchiveRecord{}
	for _, r := range set.Records {
		byID[r.ID] = r
	}
	for _, id := range []string{
		"edge-unicode-rtl",
		"edge-html-only",
		"edge-huge-body",
		"edge-missing-sent",
		"edge-missing-entity",
		"edge-spam",
		"edge-trash",
	} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing edge case %s", id)
		}
	}
	if byID["edge-missing-sent"].SentAt != "" {
		t.Fatal("edge-missing-sent should have empty sent_at")
	}
	if byID["edge-missing-entity"].MessageID != "" {
		t.Fatal("edge-missing-entity should have empty message_id")
	}
	if len(byID["edge-huge-body"].BodyText) < 10000 {
		t.Fatalf("huge body too small: %d", len(byID["edge-huge-body"].BodyText))
	}
	if !strings.Contains(byID["edge-unicode-rtl"].Subject, "שלום") {
		t.Fatalf("unicode subject = %q", byID["edge-unicode-rtl"].Subject)
	}
	if !hasString(byID["edge-spam"].Labels, "SPAM") || !hasString(byID["edge-trash"].Labels, "TRASH") {
		t.Fatal("spam/trash labels missing")
	}
}

func TestGenerateSampleMail_SmallSetUnder1MB(t *testing.T) {
	set := GenerateSampleMail(SampleMailCount)
	// Rough size: metadata + bodies, before gzip payload overhead.
	total := 0
	for _, r := range set.Records {
		total += len(r.Subject) + len(r.BodyText) + len(r.Snippet)
	}
	if total > 1<<20 {
		t.Fatalf("small set text size %d exceeds 1MB budget", total)
	}
}

func TestGenerateSampleLibrary_MergeFixtures(t *testing.T) {
	lib := GenerateSampleLibrary()
	if len(lib.Tracks) < 4 || len(lib.Contacts) < 4 || len(lib.Messages) < 4 {
		t.Fatalf("library set too small: tracks=%d contacts=%d messages=%d",
			len(lib.Tracks), len(lib.Contacts), len(lib.Messages))
	}
	if len(lib.Playlists) == 0 || len(lib.Albums) == 0 || len(lib.Artists) == 0 ||
		len(lib.Photos) == 0 || len(lib.Events) == 0 || len(lib.Documents) == 0 {
		t.Fatal("library set must cover every neutral kind")
	}

	// ISRC pair shares EntityID across services.
	isrcCount := 0
	for _, tr := range lib.Tracks {
		if tr.EntityID == "USSYN17000001" {
			isrcCount++
		}
	}
	if isrcCount != 2 {
		t.Fatalf("want 2 ISRC pair tracks, got %d", isrcCount)
	}

	// Contact chain: email + phone + handle on Ada must union-find to one entity.
	merged := MergeContacts(lib.Contacts)
	var ada *types.Contact
	for i := range merged {
		if strings.Contains(strings.ToLower(merged[i].Name), "ada") {
			ada = &merged[i]
			break
		}
	}
	if ada == nil {
		t.Fatal("Ada contact missing after merge")
	}
	if len(ada.Sources) < 2 {
		t.Fatalf("Ada should merge across services: %+v", ada)
	}

	// Message-ID pair merges; subject+from+time pair merges without Message-ID.
	mergedMsgs := MergeMessages(lib.Messages)
	var ship, invoice *types.Message
	for i := range mergedMsgs {
		switch mergedMsgs[i].Subject {
		case "Ship the release":
			ship = &mergedMsgs[i]
		case "Invoice reminder":
			invoice = &mergedMsgs[i]
		}
	}
	if ship == nil || invoice == nil {
		t.Fatalf("expected merged subjects, got %+v", mergedMsgs)
	}
	if ship.Match != "message_id" || len(ship.Sources) != 2 {
		t.Fatalf("ship merge = match %q sources %d", ship.Match, len(ship.Sources))
	}
	if invoice.Match != "subject_from_time" || len(invoice.Sources) != 2 {
		t.Fatalf("invoice merge = match %q sources %d", invoice.Match, len(invoice.Sources))
	}

	// Document checksum pair merges; same-title different-date must not.
	mergedDocs := MergeDocuments(lib.Documents)
	leaseSources := 0
	warrantyTitles := 0
	for _, d := range mergedDocs {
		if d.EntityID == "sha256:lease2024shared" {
			leaseSources = len(d.Sources)
		}
		if d.Title == "Laptop warranty" {
			warrantyTitles++
		}
	}
	if leaseSources != 2 {
		t.Fatalf("lease checksum pair should merge to 2 sources, got %d", leaseSources)
	}
	if warrantyTitles != 2 {
		t.Fatalf("warranty near-miss should stay distinct, got %d", warrantyTitles)
	}
}

func TestWriteSampleMbox_ImportRoundTrip(t *testing.T) {
	set := GenerateSampleMail(4)
	path := filepath.Join(t.TempDir(), "All mail Including Spam and Trash.mbox")
	if err := WriteSampleMbox(path, set.Records); err != nil {
		t.Fatalf("write mbox: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Message-ID:") || !strings.Contains(string(raw), "From:") {
		t.Fatalf("mbox looks empty:\n%s", raw)
	}

	a := openTestMailArchive(t)
	if err := a.SetTakeoutPath(path); err != nil {
		t.Fatalf("set takeout path: %v", err)
	}
	res, err := a.ImportTakeout(path, 1000)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Stored == 0 {
		t.Fatalf("import stored nothing: %+v", res)
	}
	// Generated rows use google-gmail IDs; the importer labels them google-takeout.
	st, err := a.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.MessageCount != res.Stored {
		t.Fatalf("status count %d != stored %d", st.MessageCount, res.Stored)
	}
}

func TestSeedSampleArchiveAndFiles(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mail-archive.db")
	mboxPath, libraryPath, set, err := WriteSampleFiles(filepath.Join(dir, "sample"), 5)
	if err != nil {
		t.Fatalf("write sample files: %v", err)
	}
	if _, err := os.Stat(mboxPath); err != nil {
		t.Fatalf("mbox missing: %v", err)
	}
	if _, err := os.Stat(libraryPath); err != nil {
		t.Fatalf("library json missing: %v", err)
	}
	raw, err := os.ReadFile(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	var lib SampleLibrarySet
	if err := json.Unmarshal(raw, &lib); err != nil {
		t.Fatalf("library json decode: %v", err)
	}
	if len(lib.Tracks) == 0 || len(lib.Messages) == 0 {
		t.Fatal("library json empty")
	}

	seeded, err := SeedSampleArchive(archive, 5)
	if err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if len(seeded.Records) != len(set.Records) {
		t.Fatalf("seeded %d records, files wrote %d", len(seeded.Records), len(set.Records))
	}

	a, err := OpenMailArchive(archive)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer a.Close()
	st, err := a.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.MessageCount != len(seeded.Records) {
		t.Fatalf("archive count %d want %d", st.MessageCount, len(seeded.Records))
	}
}

func TestSeedSampleArchive_RequiresPath(t *testing.T) {
	if _, err := SeedSampleArchive("", 1); err == nil {
		t.Fatal("expected error for empty archive path")
	}
	if _, _, _, err := WriteSampleFiles("", 1); err == nil {
		t.Fatal("expected error for empty sample dir")
	}
	if err := SeedMailArchive(nil, nil); err == nil {
		t.Fatal("expected error for nil archive")
	}
	if err := WriteSampleLibraryJSON(filepath.Join(t.TempDir(), "nested", "lib.json"), GenerateSampleLibrary()); err != nil {
		t.Fatalf("nested library json: %v", err)
	}
}

func TestSeedSampleCountOrDefault(t *testing.T) {
	if got := seedSampleCountOrDefault(0, false); got != SampleMailCount {
		t.Fatalf("default small = %d", got)
	}
	if got := seedSampleCountOrDefault(0, true); got != SampleMailStressCount {
		t.Fatalf("stress = %d", got)
	}
	if got := seedSampleCountOrDefault(12, true); got != 12 {
		t.Fatalf("explicit stress count = %d", got)
	}
	if got := seedSampleCountOrDefault(-3, false); got != SampleMailCount {
		t.Fatalf("negative clamps to default = %d", got)
	}
}

func TestRunSeedSampleCLI(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mail-archive.db")
	if code := runSeedSampleCLI(dir, archive, 3, false); code != 0 {
		t.Fatalf("seed cli exit = %d", code)
	}
	a, err := OpenMailArchive(archive)
	if err != nil {
		t.Fatalf("open seeded archive: %v", err)
	}
	defer a.Close()
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.MessageCount == 0 {
		t.Fatal("cli left archive empty")
	}
	if _, err := os.Stat(filepath.Join(dir, "sample", "sample-library.json")); err != nil {
		t.Fatalf("library json not written: %v", err)
	}
}
