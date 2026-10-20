package core

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Public launch hygiene: tracked source must not reintroduce private
// infrastructure identity or personal paths. Generic credentials are
// covered by gitleaks in CI; this denylist is project-specific.

// Patterns are split so this file does not match its own denylist.
var privateIdentifierDenylist = []string{
	"heron" + "-census",
	"noredink" + ".com",
	"/Users/" + "alec",
	"Alecs" + "MacBook",
	"Alecs-" + "MacBook",
}

var publicHygieneSkipDirs = map[string]bool{
	".git":        true,
	".mimocode":   true,
	".idea":       true,
	".vscode":     true,
	"node_modules": true,
}

var publicHygieneSkipFiles = map[string]bool{
	"aegis":            true, // local binary
	"coverage.out":     true,
	"aegis-backup-before-authorship-rewrite.bundle": true,
}

func isPublicHygieneText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	sample := data
	if len(sample) > 8096 {
		sample = sample[:8096]
	}
	for _, b := range sample {
		if b == 0 {
			return false
		}
	}
	return true
}

// TestTrackedSource_NoPrivateIdentifiers walks the repo worktree (same set a
// public clone would publish) and fails on private host/path/email patterns.
func TestTrackedSource_NoPrivateIdentifiers(t *testing.T) {
	root := repoRoot(t)
	self := filepath.Join(root, "internal", "core", "public_hygiene_test.go")

	var failures []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		// Worktrees use a .git *file* (gitdir: /Users/...) — skip it too.
		if name == ".git" || strings.HasPrefix(name, ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if publicHygieneSkipDirs[name] {
				return filepath.SkipDir
			}
			// Dot-directories are local tooling/state, not publishable source.
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if publicHygieneSkipFiles[name] {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		// Keep coverage of docs/tests/source; skip this file's literals.
		if path == self {
			return nil
		}
		if strings.HasSuffix(rel, ".bundle") || strings.HasSuffix(rel, ".db") ||
			strings.HasSuffix(rel, ".db-shm") || strings.HasSuffix(rel, ".db-wal") ||
			strings.HasSuffix(rel, ".out") || strings.HasSuffix(rel, ".png") ||
			strings.HasSuffix(rel, ".jpg") || strings.HasSuffix(rel, ".jpeg") ||
			strings.HasSuffix(rel, ".webp") || strings.HasSuffix(rel, ".gif") ||
			strings.HasSuffix(rel, ".ico") || strings.HasSuffix(rel, ".woff") ||
			strings.HasSuffix(rel, ".woff2") || strings.HasSuffix(rel, ".ttf") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !isPublicHygieneText(data) {
			return nil
		}
		text := string(data)
		for _, bad := range privateIdentifierDenylist {
			if strings.Contains(text, bad) {
				line := 1
				for i := 0; i < len(text); i++ {
					if text[i] == '\n' {
						line++
					}
				}
				// Report first hit with line number.
				if idx := strings.Index(text, bad); idx >= 0 {
					line = 1 + strings.Count(text[:idx], "\n")
				}
				failures = append(failures, fmt.Sprintf("%s:%d: contains %q", rel, line, bad))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if len(failures) > 0 {
		t.Fatalf("private identifiers must stay out of the tree (use placeholders like your-tailnet.ts.net / example.com):\n%s",
			strings.Join(failures, "\n"))
	}
}

// TestPrivateIdentifierDenylist_CoversKnownLeakClasses keeps the denylist
// honest if someone "simplifies" it away.
func TestPrivateIdentifierDenylist_CoversKnownLeakClasses(t *testing.T) {
	need := map[string]string{
		"tailnet MagicDNS": "heron" + "-census",
		"work email":       "noredink" + ".com",
		"local home path":  "/Users/" + "alec",
		"machine hostname": "Alecs" + "MacBook",
	}
	for label, want := range need {
		found := false
		for _, p := range privateIdentifierDenylist {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("denylist must keep covering %s (%q)", label, want)
		}
	}
}
