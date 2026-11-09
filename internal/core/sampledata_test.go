package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/sampledata"
)

func TestSeedSampleCountOrDefault(t *testing.T) {
	if got := seedSampleCountOrDefault(0, false); got != sampledata.SampleMailCount {
		t.Fatalf("default small = %d", got)
	}
	if got := seedSampleCountOrDefault(0, true); got != sampledata.SampleMailStressCount {
		t.Fatalf("stress = %d", got)
	}
	if got := seedSampleCountOrDefault(12, true); got != 12 {
		t.Fatalf("explicit stress count = %d", got)
	}
	if got := seedSampleCountOrDefault(-3, false); got != sampledata.SampleMailCount {
		t.Fatalf("negative clamps to default = %d", got)
	}
}

func TestRunSeedSampleCLI(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mail-archive.db")
	if code := runSeedSampleCLI(dir, archive, 3, false); code != 0 {
		t.Fatalf("seed cli exit = %d", code)
	}
	a, err := mail.Open(archive)
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
