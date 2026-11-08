package core

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/audit"
)

// TestAuditLog_WriteListAndRedaction exercises the AccessEvent → audit.Event
// projection plus List filtering. The projection is a hub concern, so this
// integration test stays in core while the store tests live in core/audit.
func TestAuditLog_WriteListAndRedaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	a, err := audit.Open(path, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.Enabled() || a.Path() != path {
		t.Fatalf("enabled=%v path=%q", a.Enabled(), a.Path())
	}
	if a.Retention() != 30*24*time.Hour {
		t.Fatalf("retention = %v", a.Retention())
	}

	a.Write(AccessEventToAudit(AccessEvent{
		Agent:    "alice@example.com",
		Surface:  MetricSurfaceLibrary,
		Target:   "tracks",
		Kind:     "tracks",
		Cache:    MetricCacheMiss,
		Decision: audit.DecisionAllow,
		Method:   http.MethodGet,
		Status:   200,
	}))
	a.Write(AccessEventToAudit(AccessEvent{
		Agent:    "bob@example.com",
		Surface:  MetricSurfaceService,
		Target:   "spotify",
		Kind:     "player",
		Decision: audit.DecisionDeny,
		Reason:   "permission",
		Method:   http.MethodPost,
		Status:   403,
	}))
	// Empty agent/decision defaults.
	a.Write(audit.Event{Surface: "hook", Target: "hook"})

	if n := a.Count(); n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}

	all, err := a.List(audit.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("list = %d rows", len(all))
	}
	// Newest first.
	if all[0].Surface != "hook" {
		t.Fatalf("newest = %+v", all[0])
	}
	if all[0].Agent != "api-key" || all[0].Decision != audit.DecisionAllow {
		t.Fatalf("defaults = %+v", all[0])
	}

	denied, err := a.List(audit.Filter{Decision: audit.DecisionDeny})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied) != 1 || denied[0].Agent != "bob@example.com" || denied[0].Reason != "permission" {
		t.Fatalf("denied = %+v", denied)
	}

	byAgent, err := a.List(audit.Filter{Agent: "alice@example.com", Target: "tracks"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 1 {
		t.Fatalf("byAgent = %+v", byAgent)
	}

	// Redaction: rows never carry token material.
	for _, row := range all {
		blob := row.Agent + row.Target + row.Reason + row.Kind + row.Cache + row.Method
		for _, bad := range []string{"Bearer ", "eyJ", "refresh_token", "access_token"} {
			if strings.Contains(blob, bad) {
				t.Fatalf("audit row looks like it holds secret material: %+v", row)
			}
		}
	}

	// Limit clamps; unknown filters return empty.
	if _, err := a.List(audit.Filter{Limit: 0}); err != nil {
		t.Fatal(err)
	}
	if rows, err := a.List(audit.Filter{Agent: "nobody"}); err != nil || len(rows) != 0 {
		t.Fatalf("unknown agent rows=%v err=%v", rows, err)
	}
}
