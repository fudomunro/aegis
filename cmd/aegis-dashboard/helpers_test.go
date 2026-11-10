package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingRun is the gh exec seam: it records invocations and plays back
// canned responses.
type recordingRun struct {
	calls   [][]string
	respond func(args []string) (string, error)
}

func (r *recordingRun) run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return r.respond(args)
}

func (r *recordingRun) joined() []string {
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// jsonString JSON-encodes a string for embedding in a canned gh response.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// repoRoot walks up from the package directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("go.mod not found from test working directory")
	return ""
}
