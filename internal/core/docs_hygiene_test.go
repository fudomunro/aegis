package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// User-facing docs and admin UI copy must not cite GitHub issue numbers or
// glossary-avoid synonyms (docs/glossary.md). Code comments may still cite
// issues; history belongs in maintainer docs / CHANGELOG.

var docsForbidden = []string{
	"home copy",
	"foreign canonical",
	"ingest transport",
	"type pack",
}

var issueRefRe = regexp.MustCompile(`(?i)\bissues?\s*#\d+`)

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

func markdownHeadings(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var heads []string
	inFence := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heads = append(heads, line)
		}
	}
	return heads
}

func TestReadmeHeadings_NoIssueRefsOrAvoidTerms(t *testing.T) {
	path := filepath.Join(repoRoot(t), "README.md")
	heads := markdownHeadings(t, path)
	if len(heads) < 4 {
		t.Fatalf("expected a real README, got %d headings", len(heads))
	}
	var failures []string
	for _, h := range heads {
		if issueRefRe.MatchString(h) {
			failures = append(failures, "issue ref in heading: "+h)
		}
		lower := strings.ToLower(h)
		for _, bad := range docsForbidden {
			if strings.Contains(lower, bad) {
				failures = append(failures, "avoid term in heading: "+h)
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("README headings must stay issue-free and glossary-clean:\n%s",
			strings.Join(failures, "\n"))
	}
}

// User-facing reference docs must not cite issues either (history is CHANGELOG).
func TestUserFacingDocs_NoIssueRefs(t *testing.T) {
	root := repoRoot(t)
	files := []string{
		"README.md",
		filepath.Join("docs", "api.md"),
		filepath.Join("docs", "configuration.md"),
		filepath.Join("docs", "observability.md"),
	}
	connectDir := filepath.Join(root, "docs", "connect")
	entries, err := os.ReadDir(connectDir)
	if err != nil {
		t.Fatalf("read docs/connect: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			files = append(files, filepath.Join("docs", "connect", e.Name()))
		}
	}
	var failures []string
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		// Skip fenced code samples; prose must stay clean.
		var body []string
		inFence := false
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if !inFence {
				body = append(body, line)
			}
		}
		for i, line := range body {
			if issueRefRe.MatchString(line) {
				failures = append(failures, fmt.Sprintf("%s:%d: %s", rel, i+1, truncateForTest(line, 120)))
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("user-facing docs must not cite issues (use CHANGELOG.md):\n%s",
			strings.Join(failures, "\n"))
	}
}

var (
	htmlCommentRe  = regexp.MustCompile(`(?s)<!--.*?-->`)
	blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// visibleAdminText strips HTML/JS/CSS comments so only user-visible copy remains.
func visibleAdminText(src string) string {
	src = htmlCommentRe.ReplaceAllString(src, " ")
	for {
		next := blockCommentRe.ReplaceAllString(src, " ")
		if next == src {
			break
		}
		src = next
	}
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if i := lineCommentIndex(line); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// lineCommentIndex finds a // comment start that is not part of :// in a URL.
func lineCommentIndex(line string) int {
	for i := 0; i+1 < len(line); i++ {
		if line[i] != '/' || line[i+1] != '/' {
			continue
		}
		if i > 0 && line[i-1] == ':' {
			continue
		}
		return i
	}
	return -1
}

func TestAdminHTMLVisibleText_NoIssueRefsOrAvoidTerms(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "core", "web", "admin.html")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read admin.html: %v", err)
	}
	visible := visibleAdminText(string(raw))
	if issueRefRe.MatchString(visible) {
		loc := issueRefRe.FindString(visible)
		// Show a little context for debugging.
		idx := strings.Index(visible, loc)
		start := idx - 40
		if start < 0 {
			start = 0
		}
		end := idx + len(loc) + 40
		if end > len(visible) {
			end = len(visible)
		}
		t.Fatalf("admin.html visible text must not cite issues; found %q near %q",
			loc, truncateForTest(visible[start:end], 120))
	}
	lower := strings.ToLower(visible)
	for _, bad := range docsForbidden {
		if strings.Contains(lower, bad) {
			t.Fatalf("admin.html visible text contains avoid term %q", bad)
		}
	}
}

// --- local markdown link / fragment resolution ---

var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

func githubSlug(heading string) string {
	s := strings.ToLower(heading)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), " ", "-")
}

func markdownAnchors(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]bool{}
	inFence := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			out[githubSlug(heading)] = true
		}
	}
	return out
}

func TestMarkdownLocalLinks_Resolve(t *testing.T) {
	root := repoRoot(t)
	files := []string{
		"README.md",
		"SECURITY.md",
		"AGENTS.md",
		"CHANGELOG.md",
	}
	var walkDocs func(relDir string)
	walkDocs = func(relDir string) {
		entries, err := os.ReadDir(filepath.Join(root, relDir))
		if err != nil {
			t.Fatalf("read %s: %v", relDir, err)
		}
		for _, e := range entries {
			rel := filepath.Join(relDir, e.Name())
			if e.IsDir() {
				walkDocs(rel)
				continue
			}
			if strings.HasSuffix(e.Name(), ".md") {
				files = append(files, rel)
			}
		}
	}
	walkDocs("docs")

	anchorCache := map[string]map[string]bool{}
	anchorsFor := func(rel string) map[string]bool {
		if a, ok := anchorCache[rel]; ok {
			return a
		}
		a := markdownAnchors(t, filepath.Join(root, rel))
		anchorCache[rel] = a
		return a
	}

	var failures []string
	for _, rel := range files {
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		// Drop fenced code so example URLs are not treated as links.
		var body []string
		inFence := false
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if !inFence {
				body = append(body, line)
			}
		}
		for _, m := range mdLinkRe.FindAllStringSubmatch(strings.Join(body, "\n"), -1) {
			href := m[1]
			if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "mailto:") {
				continue
			}
			target := rel
			frag := ""
			if i := strings.Index(href, "#"); i >= 0 {
				frag = href[i+1:]
				if i > 0 {
					target = href[:i]
				}
				// i == 0 → same-file fragment; keep target as rel.
			} else {
				target = href
			}
			if target != rel && !filepath.IsAbs(target) {
				// Normalize file target relative to the linking file's directory.
				target = filepath.Clean(filepath.Join(filepath.Dir(rel), target))
				if strings.HasPrefix(target, "..") {
					// Outside repo root (e.g. site/); skip.
					continue
				}
			}
			if _, err := os.Stat(filepath.Join(root, target)); err != nil {
				failures = append(failures, rel+": missing file for link "+href)
				continue
			}
			if frag == "" {
				continue
			}
			if !anchorsFor(target)[frag] {
				failures = append(failures, rel+": missing fragment "+href)
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("local markdown links must resolve:\n%s", strings.Join(failures, "\n"))
	}
}
