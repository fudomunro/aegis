package shared

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandUserPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := ExpandUserPath("~/x.json"); got != filepath.Join(home, "x.json") {
		t.Fatalf("got %q", got)
	}
	if got := ExpandUserPath("~"); got != home {
		t.Fatalf("got %q", got)
	}
	if got := ExpandUserPath("/abs/path"); got != "/abs/path" {
		t.Fatalf("got %q", got)
	}
	if got := ExpandUserPath("  "); got != "" {
		t.Fatalf("got %q", got)
	}
}
