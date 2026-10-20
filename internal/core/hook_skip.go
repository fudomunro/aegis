package core

import (
	"fmt"
	"regexp"
	"strings"
)

// HookConfig controls when the pre-request hook skips matching entirely
// (issue #205). Operators who develop software from the same environment that
// runs consumer agents can exclude those conversations without disabling the
// hook for everyone.
type HookConfig struct {
	// SkipPatterns are case-insensitive regular expressions. When any matches
	// the user message, Aegis skips service and library matching and allows
	// the request unchanged.
	SkipPatterns []string `json:"skip_patterns,omitempty"`
}

// hookSkip holds compiled skip state derived from HookConfig at load/reload.
type hookSkip struct {
	patterns []*regexp.Regexp
}

func compileHookSkip(h HookConfig) (hookSkip, error) {
	out := hookSkip{}
	for i, p := range h.SkipPatterns {
		if strings.TrimSpace(p) == "" {
			return hookSkip{}, fmt.Errorf("hook.skip_patterns[%d]: empty pattern", i)
		}
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return hookSkip{}, fmt.Errorf("hook.skip_patterns[%d]: %w", i, err)
		}
		out.patterns = append(out.patterns, re)
	}
	return out, nil
}

// matches reports whether any skip pattern hits the user message.
func (h hookSkip) matches(msg string) bool {
	for _, re := range h.patterns {
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}

// sourceFilePattern matches a path-like token ending in a source or build
// extension. Conservative on purpose: consumer questions rarely name
// `something.go` / `path/file.ts` as a file.
var sourceFilePattern = regexp.MustCompile(
	`(?i)(^|[\s(/\\'"` + "`" + `])[\w./\\-]+\.(go|ts|tsx|js|jsx|mjs|cjs|py|rs|java|rb|php|c|cpp|h|hpp|cc|swift|kt|kts|cs|sh|bash|zsh|sql|yml|yaml|toml|html|css|scss|vue|svelte)\b`,
)

// aegisDevMarkers are identifiers that only appear when someone is working on
// the Aegis codebase (or a close fork). Issue #205: matching language such as
// "Spotify" shows up in these messages as code, not as consumer intent.
var aegisDevMarkers = []string{
	"agents.md",
	"internal/core",
	"internal/connectors",
	"internal/types",
	"aegis.example.json",
	"librarykind",
	"serviceconnector",
	"handleprerequest",
	"findmatchingservices",
	"handlelibraryaggregate",
	"go.mod",
	"go test",
	"go vet",
	"staticcheck",
}

// looksLikeDevelopment reports whether the user message is clearly about
// software development rather than personal-data access (issue #205).
//
// Heuristics stay conservative: code fences, source file paths, and Aegis
// repo identifiers. Ordinary product questions ("play jazz on Spotify") do
// not hit these. Operators can widen the net with hook.skip_patterns.
func looksLikeDevelopment(msg string) bool {
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "```") {
		return true
	}
	lower := strings.ToLower(msg)
	for _, marker := range aegisDevMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return sourceFilePattern.MatchString(msg)
}

// shouldSkipMatching reports whether pre-request service/library matching
// must be skipped for this user message, and why (for hook diagnostics).
// Reasons: "development", "skip_pattern". Empty reason means do not skip.
func (c *Config) shouldSkipMatching(userMessage string) (reason string) {
	if strings.TrimSpace(userMessage) == "" {
		return ""
	}
	if looksLikeDevelopment(userMessage) {
		return "development"
	}
	if c.hookSkip.matches(userMessage) {
		return "skip_pattern"
	}
	return ""
}
