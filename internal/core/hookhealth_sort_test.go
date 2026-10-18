package core

import (
	"testing"
)

func TestSortHookIssues_StableBySeverityThenCode(t *testing.T) {
	issues := []HookIssue{
		{Severity: "info", Code: "b"},
		{Severity: "error", Code: "z"},
		{Severity: "warn", Code: "a"},
		{Severity: "error", Code: "a"},
		{Severity: "warn", Code: "b"},
	}
	sortHookIssues(issues)
	want := []string{"error:a", "error:z", "warn:a", "warn:b", "info:b"}
	for i, w := range want {
		got := issues[i].Severity + ":" + issues[i].Code
		if got != w {
			t.Fatalf("[%d] = %s, want %s (all=%+v)", i, got, w, issues)
		}
	}
}
