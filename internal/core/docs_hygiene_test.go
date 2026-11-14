package core

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
)

// User-facing docs and admin UI copy must not cite GitHub issue numbers or
// glossary-avoid synonyms (docs/glossary.md). Code comments may still cite
// issues; history belongs in maintainer docs / CHANGELOG. The Go literal scan
// below extends the same rule to error strings, flag help, log warnings and
// other string literals that reach operators or the admin UI.

var docsForbidden = []string{
	"home copy",
	"foreign canonical",
	"ingest transport",
	"type pack",
	// Issue #305: "data type" is retired in favor of "library kind".
	"data type",
}

// issueRefRe matches prose issue citations and the tracker-URL form
// ([](…/issues/4), [#43](…/issues/43)) — the same citation in link shape.
var issueRefRe = regexp.MustCompile(`(?i)(\bissues?\s*#\d+|github\.com/fudomunro/aegis/issues/\d+)`)

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

// TestGoStringLiterals_NoIssueRefs scans every string literal in non-test Go
// sources. Error strings, flag help, slog warnings, UI labels, onboarding
// hints and generated sample copy all reach operators or the admin UI, so
// they follow the same rule as docs: no GitHub issue refs outside comments.
// (Run.go flag help and service-connection save errors are the regressions
// this catches; keep those out of literals.)
func TestGoStringLiterals_NoIssueRefs(t *testing.T) {
	root := repoRoot(t)
	goLiteralIssueRefRe := regexp.MustCompile(`(?i)(\bissues?\s*#|github\.com/fudomunro/aegis/issues/\d+)`)
	fset := token.NewFileSet()
	var failures []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			failures = append(failures, rel+": parse error: "+perr.Error())
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if loc := goLiteralIssueRefRe.FindString(s); loc != "" {
				pos := fset.Position(lit.Pos())
				failures = append(failures,
					fmt.Sprintf("%s:%d: string literal cites %q: %s", rel, pos.Line, loc, truncateForTest(s, 120)))
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
	if len(failures) > 0 {
		t.Fatalf("Go string literals must not cite issues (comments and CHANGELOG.md only):\n%s",
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
	// admin.html and the maintenance dashboard template are the visible
	// operator UI: copy rules apply to all of them.
	for _, rel := range []string{
		filepath.Join("internal", "admin", "web", "admin.html"),
		filepath.Join("cmd", "aegis-dashboard", "maintenance_dashboard.html"),
	} {
		path := filepath.Join(repoRoot(t), rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
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
			t.Fatalf("%s visible text must not cite issues; found %q near %q",
				rel, loc, truncateForTest(visible[start:end], 120))
		}
		lower := strings.ToLower(visible)
		for _, bad := range docsForbidden {
			if strings.Contains(lower, bad) {
				t.Fatalf("%s visible text contains avoid term %q", rel, bad)
			}
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

// TestImmichAPIKeyContract_SingleOwner pins the issue #265 seam rule: the
// Immich single-key checkbox set has exactly one owner — the connector. The
// admin UI renders it from the declared spec and docs point at that surface;
// neither may restate the permission names, or the three copies drift again.
func TestImmichAPIKeyContract_SingleOwner(t *testing.T) {
	root := repoRoot(t)
	perms := connectors.ImmichAPIKeyPermissions()
	if len(perms) < 10 {
		t.Fatalf("permission set looks truncated: %v", perms)
	}

	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}

	admin := read("internal/admin/web/admin.html")
	for _, rel := range []string{
		"docs/immich-live-testing.md",
		"docs/connect/immich.md",
		"docs/api.md",
		"docs/configuration.md",
	} {
		body := read(rel)
		for _, p := range perms {
			if strings.Contains(body, p) {
				t.Errorf("%s restates Immich API-key permission %q — the connector owns the set (issue #265)", rel, p)
			}
		}
	}
	for _, p := range perms {
		if strings.Contains(admin, p) {
			t.Errorf("web/admin.html restates Immich API-key permission %q — render it from the onboarding spec instead (issue #265)", p)
		}
	}

	// The setup routes the contract used to be duplicated against are gone:
	// connection setup speaks /v1/admin/setup/{id}, and the VIP wizard does
	// too since Phase 3 (issue #265).
	for _, gone := range []string{
		"/v1/admin/immich/setup",
		"/v1/admin/immich/config",
		"/v1/admin/immich/test",
		"/v1/admin/immich/check",
		"/v1/admin/immich/tailnet",
	} {
		if strings.Contains(admin, gone) {
			t.Errorf("web/admin.html still calls %q — setup speaks /v1/admin/setup/immich (issue #265)", gone)
		}
	}
}

// TestImmichVIPContract_SingleOwner pins the same rule for the VIP-Service
// wizard (issue #124): its walkthrough copy and fix rules live beside the
// connector. Core holds no svc:immich knowledge and neither does the UI.
func TestImmichVIPContract_SingleOwner(t *testing.T) {
	root := repoRoot(t)
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	contract := []string{
		"svc:immich",
		"tag:immich-host",
		"tagOwners",
		"tskey-api-",
		"tailscale serve --service",
		"Create the VIP Service",
		"Declare a host tag",
		"never Funnel",
	}
	admin := read("internal/admin/web/admin.html")
	for _, rel := range []string{
		"internal/core/host.go",
		"internal/admin/onboarding.go",
		"internal/core/run.go",
		"internal/core/service_connections.go",
	} {
		body := read(rel)
		for _, c := range contract {
			if strings.Contains(body, c) {
				t.Errorf("%s holds VIP wizard contract %q — the connector owns it (issue #265)", rel, c)
			}
		}
	}
	for _, c := range contract {
		if strings.Contains(admin, c) {
			t.Errorf("web/admin.html restates VIP wizard contract %q — render it from the onboarding spec (issue #265)", c)
		}
	}
}

// TestAdminHTML_RestatesNoOnboardingCopy walks every string the connectors
// declare in their onboarding specs and rejects each one in web/admin.html.
// That is the issue #265 end state: the panel and the stepped walkthrough
// render provider copy from the spec, so the file that renders it cannot
// also contain it.
func TestAdminHTML_RestatesNoOnboardingCopy(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "internal/admin/web/admin.html"))
	if err != nil {
		t.Fatal(err)
	}
	admin := string(b)

	specs := []shared.OnboardingSpec{
		connectors.NewImmichConnector(&Server{}).Onboarding(),
		connectors.NewYNABConnector(&Server{}).Onboarding(),
	}
	seen := map[string]bool{}
	// Panel vocabulary other surfaces legitimately share. These are not
	// restatements of an onboarding spec: each is owned by a different
	// admin surface (the Aperture panel's copy button; the generic busy
	// state several forms use) and merely spells the same words.
	sharedVocabulary := map[string]string{
		"Copy JSON": "Aperture panel copy button",
		"Saving…":   "generic busy state shared by admin forms",
	}
	check := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) < 8 || seen[s] {
			return
		}
		seen[s] = true
		if why, shared := sharedVocabulary[s]; shared {
			t.Logf("skipping shared panel vocabulary %q (%s)", s, why)
			return
		}
		if strings.Contains(admin, s) {
			t.Errorf("web/admin.html restates onboarding copy %q — render it from the spec instead (issue #265)", s)
		}
	}
	for _, spec := range specs {
		check(spec.Title)
		check(spec.WalkthroughLabel)
		check(spec.WalkthroughTitle)
		check(spec.DetailsLabel)
		check(spec.Connection.Description)
		for _, f := range spec.Fields {
			check(f.Label)
			check(f.Placeholder)
			check(f.WalkthroughPlaceholder)
			check(f.Help)
		}
		for _, st := range spec.Steps {
			check(st.Label)
		}
		for _, c := range spec.Checks {
			check(c.Label)
		}
		for _, a := range spec.Actions {
			check(a.Label)
			check(a.WalkthroughLabel)
			check(a.BusyLabel)
		}
		for _, g := range spec.Guides {
			check(g.Title)
			check(g.Short)
			check(g.Body)
			check(g.Label)
			check(g.Button)
			check(g.LiveLabel)
			for _, r := range g.Requirements {
				check(r)
			}
			for _, st := range g.Steps {
				check(st.Title)
				check(st.Detail)
				check(st.Command)
			}
			for _, bl := range g.Blocks {
				check(bl.Body)
				check(bl.Command)
				check(bl.Label)
				check(bl.Button)
				for _, it := range bl.Items {
					check(it)
				}
			}
		}
	}
	if len(seen) < 40 {
		t.Fatalf("only %d distinct copy strings walked — the spec walk looks broken", len(seen))
	}
}

// TestAdminHTML_RestatesNoImportPanelCopy walks every string the local
// import stores declare for their admin panels and rejects each one in
// web/admin.html (issue #393). Panels render from the connectors registry
// via /v1/admin/import-stores, so the render file cannot also contain the
// copy — the issue #265 "declare, don't render" rule for the import family.
func TestAdminHTML_RestatesNoImportPanelCopy(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "internal/admin/web/admin.html"))
	if err != nil {
		t.Fatal(err)
	}
	admin := string(b)

	seen := map[string]bool{}
	check := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) < 8 || seen[s] {
			return
		}
		seen[s] = true
		if strings.Contains(admin, s) {
			t.Errorf("web/admin.html restates import panel copy %q — render it from the spec instead (issue #393)", s)
		}
	}
	for _, spec := range connectors.LocalImportStoreSpecs() {
		st, _ := spec.Open("")
		adminCopy := st.Admin()
		check(adminCopy.Panel.Heading)
		check(adminCopy.Panel.Title)
		check(adminCopy.Panel.Intro)
		check(adminCopy.Panel.Placeholder)
		for _, step := range adminCopy.Panel.Steps {
			check(step)
		}
		check(adminCopy.PathHint)
		check(adminCopy.SamplePath)
	}
	if len(seen) < 20 {
		t.Fatalf("only %d distinct copy strings walked — the spec walk looks broken", len(seen))
	}
}
