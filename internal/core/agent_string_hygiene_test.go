package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// Agent-facing strings are what the LLM agent reads in pre-request injection
// or as library error bodies. They must not leak GitHub issue numbers,
// operator CLI flags, or glossary-avoid product synonyms (docs/glossary.md).
// Code comments and admin HTML may still cite issues (#180 covers admin copy).

var agentStringForbidden = []string{
	"issue #",
	// Glossary Prefer/Avoid table — product prose.
	"home copy",
	"foreign canonical",
	"ingest transport",
	"type pack",
	// "schema pack" is the allowed on-disk artifact name; prefer "user-defined type" in prose.
}

// Operator CLI flags from run.go. Agents ask the operator; they do not run flags.
var agentStringCLIFlags = []string{
	"-addr",
	"-admin-api-key",
	"-admin-logins",
	"-api-key",
	"-base-url",
	"-copy-ledger",
	"-data-dir",
	"-import-quicken",
	"-import-signal",
	"-import-takeout",
	"-import-timeline",
	"-log-file",
	"-log-format",
	"-log-level",
	"-mail-archive",
	"-quicken-store",
	"-seed-sample",
	"-token-pass",
}

type agentStringSample struct {
	name string
	text string
}

func collectAgentStrings(t *testing.T) []agentStringSample {
	t.Helper()
	base := "https://aegis.example.com"
	cfg := &Config{Services: []Service{
		{ID: "spotify", Name: "Spotify", Action: "inject", CanonicalFor: []string{"tracks", "playlists"}},
		{ID: "immich", Name: "Immich", Action: "inject", CanonicalFor: []string{"photos"}},
		{ID: "google-calendar", Name: "Google Calendar", Action: "inject", CanonicalFor: []string{"events"}},
	}}
	srv := testServerWithBase(cfg, base)

	var out []agentStringSample
	add := func(name, text string) {
		out = append(out, agentStringSample{name: name, text: text})
	}

	for _, k := range types.BuiltinLibraryKinds {
		add("LibraryKind.Instructions/"+k.ID, k.Instructions(base))
		add("LibraryKind.DenyMsg/"+k.ID, k.DenyMsg)
		add("LibraryKind.EmptyMsg/"+k.ID, k.EmptyMsg)
		add("libraryInstructionsFor/"+k.ID, srv.libraryInstructionsFor(k.ID))
	}

	schema := types.UserDefinedTypeSchema{
		ID:    "reading-list",
		Label: "reading list",
		Usage: "books the user is reading",
		Fields: map[string]types.UserDefinedField{
			"title": {Type: "string", Required: true, Description: "book title"},
		},
	}
	if err := schema.Normalize(); err != nil {
		t.Fatalf("normalize udt schema: %v", err)
	}
	add("userDefinedHint/default", srv.userDefinedHint(schema))
	add("userDefinedHint.DenyMsg", schema.DenyMsg)
	add("userDefinedHint.EmptyMsg", schema.EmptyMsg)
	add("userDefinedHint.FailMsg", schema.FailMsg)

	add("multiSourceMutationRule", multiSourceMutationRule())
	add("PhotoSourceDiscipline", types.PhotoSourceDiscipline())

	for _, notice := range srv.writePolicyNotices(cfg.Services) {
		add("writePolicyNotice", notice)
	}

	add("permissionNotice/denied", permissionNotice(types.Permissions{
		types.PermissionDefaultKey: types.AccessNone,
		"photos":                   types.AccessNone,
	}))
	add("permissionNotice/freebusy", permissionNotice(types.Permissions{
		types.PermissionDefaultKey: types.AccessRead,
		"events":                   types.AccessFreeBusy,
	}))

	out = append(out, connectorAgentSamples(t, srv)...)
	return out
}

func connectorAgentSamples(t *testing.T, host connectors.Host) []agentStringSample {
	t.Helper()
	type entry struct {
		id   string
		conn connectors.ServiceConnector
	}
	entries := []entry{
		{"spotify", connectors.NewSpotifyConnector(host)},
		{"youtube-music", connectors.NewYouTubeMusicConnector(host)},
		{"amazon-music", connectors.NewAmazonMusicConnector(host)},
		{"google-photos", connectors.NewGooglePhotosConnector(host)},
		{"google-contacts", connectors.NewGoogleContactsConnector(host)},
		{"immich", connectors.NewImmichConnector(host)},
		{"google-calendar", connectors.NewGoogleCalendarConnector(host)},
		{"google-gmail", connectors.NewGoogleGmailConnector(host)},
		{"ynab", connectors.NewYNABConnector(host)},
		{"owntracks", connectors.NewOwnTracksConnector(host)},
		{"bluesky", connectors.NewBlueskyConnector(host)},
		{"paperless", connectors.NewPaperlessConnector(host)},
		{"google-drive", connectors.NewGoogleDriveConnector(host)},
		{"carddav", connectors.NewCardDAVConnector(host)},
		{"caldav", connectors.NewCalDAVConnector(host)},
		{"sample-calendar", connectors.NewSampleCalendarConnector()},
		{"quicken", NewQuickenConnector(host, nil)},
		{"google-timeline", NewTimelineConnector(nil)},
		{"signal", NewSignalConnector(nil)},
	}

	base := "https://aegis.example.com"
	var out []agentStringSample
	for _, e := range entries {
		if e.conn == nil {
			t.Fatalf("connector %s is nil", e.id)
		}
		out = append(out, agentStringSample{
			name: "SystemInstructions/" + e.id,
			text: e.conn.SystemInstructions(base),
		})
		caps := e.conn.Capabilities()
		for _, dt := range caps.DataTypes {
			out = append(out, agentStringSample{
				name: fmt.Sprintf("Capabilities/%s/%s", e.id, dt.Name),
				text: dt.Description,
			})
			for oi, op := range dt.Operations {
				out = append(out, agentStringSample{
					name: fmt.Sprintf("Capabilities/%s/%s/op%d", e.id, dt.Name, oi),
					text: op.Description + " " + op.Response.Description,
				})
				for pi, p := range op.Parameters {
					out = append(out, agentStringSample{
						name: fmt.Sprintf("Capabilities/%s/%s/op%d/param%d", e.id, dt.Name, oi, pi),
						text: p.Description,
					})
				}
			}
		}
	}
	return out
}

func agentStringViolations(text string) []string {
	var hits []string
	lower := strings.ToLower(text)
	for _, bad := range agentStringForbidden {
		if strings.Contains(lower, strings.ToLower(bad)) {
			hits = append(hits, bad)
		}
	}
	for _, flag := range agentStringCLIFlags {
		// Word-boundary-ish: flag name as its own token (start, space, or paren).
		if strings.Contains(lower, flag) {
			hits = append(hits, flag)
		}
	}
	return hits
}

func TestAgentFacingStrings_NoIssueRefsCLIOrAvoidTerms(t *testing.T) {
	samples := collectAgentStrings(t)
	if len(samples) < 50 {
		t.Fatalf("expected a broad sample of agent strings, got %d", len(samples))
	}
	var failures []string
	for _, s := range samples {
		if strings.TrimSpace(s.text) == "" {
			continue
		}
		for _, bad := range agentStringViolations(s.text) {
			failures = append(failures, fmt.Sprintf("%s: contains %q in %q", s.name, bad, truncateForTest(s.text, 160)))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("agent-facing strings must not cite issues, CLI flags, or glossary-avoid terms:\n%s",
			strings.Join(failures, "\n"))
	}
}

func truncateForTest(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
