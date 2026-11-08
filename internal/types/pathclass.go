package types

import (
	"errors"
	"strings"
)

// kindPathRule classifies one canonical proxy path form into a kind
// permission key. Evaluation is first-match-wins in slice order, matching the
// former switch in KindForPath exactly (issue #15 type-layer seam).
//
// Prefer Exact for single paths; Prefix for resource trees. A rule set is the
// type layer's declaration of which provider paths belong to which kind —
// connectors must not invent a second classifier.
type kindPathRule struct {
	Exact  string
	Prefix string
	Kind   string
}

// kindPathRules is the ordered path → kind table.
// Keep the most specific exact/prefix rules before broader ones when they can
// overlap (e.g. "/me/player" before any broader "/me/..." family).
var kindPathRules = []kindPathRule{
	{Exact: "/me", Kind: "user"},
	{Prefix: "/me/player", Kind: "player"},
	{Exact: "/me/tracks", Kind: "tracks"},
	{Exact: "/me/liked", Kind: "tracks"},
	{Exact: "/me/top/tracks", Kind: "tracks"},
	{Prefix: "/tracks", Kind: "tracks"},
	{Prefix: "/videos", Kind: "tracks"},
	{Exact: "/me/albums", Kind: "albums"},
	{Prefix: "/albums", Kind: "albums"},
	{Exact: "/me/artists", Kind: "artists"},
	{Exact: "/me/top/artists", Kind: "artists"},
	{Prefix: "/artists", Kind: "artists"},
	// Followed artists: Spotify /me/following and YouTube subscriptions back
	// the unified artists library, so they share the artists permission key.
	{Exact: "/me/following", Kind: "artists"},
	{Exact: "/me/subscriptions", Kind: "artists"},
	{Exact: "/me/playlists", Kind: "playlists"},
	{Prefix: "/playlists", Kind: "playlists"},
	{Exact: "/me/contacts", Kind: "contacts"},
	{Prefix: "/contacts", Kind: "contacts"},
	{Prefix: "/people", Kind: "contacts"},
	{Exact: "/me/events", Kind: "events"},
	{Exact: "/me/calendars", Kind: "events"},
	{Exact: "/calendarList", Kind: "events"},
	{Exact: "/users/me/calendarList", Kind: "events"},
	{Prefix: "/events", Kind: "events"},
	{Prefix: "/calendars", Kind: "events"},
	{Exact: "/me/messages", Kind: "messages"},
	{Exact: "/me/threads", Kind: "messages"},
	{Exact: "/me/mail", Kind: "messages"},
	{Prefix: "/messages", Kind: "messages"},
	{Prefix: "/threads", Kind: "messages"},
	{Prefix: "/me/mail", Kind: "messages"},
	{Prefix: "/gmail", Kind: "messages"},
	{Prefix: "/users/me/messages", Kind: "messages"},
	{Prefix: "/users/me/threads", Kind: "messages"},
	{Exact: "/me/photos", Kind: "photos"},
	{Prefix: "/photos", Kind: "photos"},
	{Prefix: "/mediaItems", Kind: "photos"},
	{Prefix: "/picker", Kind: "photos"},
	// Self-hosted / foreign photo stores (Immich, …). /search/metadata is the
	// Immich asset search; keep it under photos so the permission key matches
	// the unified photos library.
	{Prefix: "/assets", Kind: "photos"},
	{Exact: "/search/metadata", Kind: "photos"},
	// Financial transactions (issue #141): YNAB / Quicken / OFX paths.
	{Exact: "/me/transactions", Kind: "transactions"},
	{Prefix: "/transactions", Kind: "transactions"},
	{Prefix: "/budgets", Kind: "transactions"},
	{Prefix: "/accounts", Kind: "transactions"},
	{Prefix: "/categories", Kind: "transactions"},
	{Prefix: "/payees", Kind: "transactions"},
	{Prefix: "/bank", Kind: "transactions"},
	{Prefix: "/cc", Kind: "transactions"},
	{Prefix: "/invstmt", Kind: "transactions"},
	{Prefix: "/ofx", Kind: "transactions"},
	{Prefix: "/quicken", Kind: "transactions"},
	// Location history (issue #142): OwnTracks, Dawarich, Google Timeline.
	{Exact: "/me/places", Kind: "places"},
	{Prefix: "/places", Kind: "places"},
	{Prefix: "/visits", Kind: "places"},
	{Prefix: "/trips", Kind: "places"},
	{Prefix: "/locations", Kind: "places"},
	{Prefix: "/api/v0/locations", Kind: "places"},
	{Prefix: "/api/v0/visit", Kind: "places"},
	{Prefix: "/api/v0/places", Kind: "places"},
	{Prefix: "/api/v1/points", Kind: "places"},
	{Prefix: "/api/v1/visits", Kind: "places"},
	{Prefix: "/timeline", Kind: "places"},
	{Prefix: "/semantic", Kind: "places"},
	// Documents (issue #143): Paperless-ngx and similar document stores.
	// Endpoint usually includes /api, so remaining paths start at /documents.
	{Exact: "/me/documents", Kind: "documents"},
	{Prefix: "/documents", Kind: "documents"},
	{Prefix: "/api/documents", Kind: "documents"},
	{Prefix: "/correspondents", Kind: "documents"},
	{Prefix: "/document_types", Kind: "documents"},
	{Prefix: "/api/correspondents", Kind: "documents"},
	{Prefix: "/api/document_types", Kind: "documents"},
	{Prefix: "/api/tags", Kind: "documents"},
	// Google Drive / Google Docs (issue #143): Drive files.list + files.export.
	{Prefix: "/files", Kind: "documents"},
	{Prefix: "/drive/files", Kind: "documents"},
	{Prefix: "/drive/v3/files", Kind: "documents"},
	{Prefix: "/drive/v2/files", Kind: "documents"},
	{Exact: "/search", Kind: "search"},
	{Prefix: "/channels", Kind: "channels"},
	{Prefix: "/me/library", Kind: "library"},
	{Prefix: "/me/recommendations", Kind: "recommendations"},
}

// KindForCanonicalPath matches an already-canonicalized proxy path
// (no query string, no dot segments) against the rule table.
func KindForCanonicalPath(p string) string {
	for _, rule := range kindPathRules {
		if rule.Exact != "" {
			if p == rule.Exact {
				return rule.Kind
			}
			continue
		}
		if rule.Prefix != "" && strings.HasPrefix(p, rule.Prefix) {
			return rule.Kind
		}
	}
	return ""
}

// CanonicalProxyPath normalizes a service-proxy remaining path so permission
// classification and upstream routing see the same string (issue #94).
// Duplicate slashes are collapsed. "." / ".." segments and other non-canonical
// forms are rejected. Empty means the service root.
func CanonicalProxyPath(remaining string) (string, error) {
	if remaining == "" {
		return "", nil
	}
	if !strings.HasPrefix(remaining, "/") {
		remaining = "/" + remaining
	}
	// URL.Path is already percent-decoded (%2f → /). Reject leftover characters
	// that can still smuggle alternate path forms through to upstream.
	if strings.ContainsAny(remaining, "\\\x00") {
		return "", errNonCanonicalPath
	}
	segs := strings.Split(remaining, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch seg {
		case "":
			// Collapse leading/duplicate/trailing slashes.
			continue
		case ".", "..":
			return "", errNonCanonicalPath
		default:
			out = append(out, seg)
		}
	}
	if len(out) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(out, "/"), nil
}

func KindForPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	// Drop query string if present.
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	clean, err := CanonicalProxyPath(p)
	if err != nil {
		return ""
	}
	if clean == "" || clean == "/" {
		return ""
	}
	return KindForCanonicalPath(clean)
}

var errNonCanonicalPath = errors.New("non-canonical path")
