package main

import "strings"

// dataTypePathRule classifies one canonical proxy path form into a data-type
// permission key. Evaluation is first-match-wins in slice order, matching the
// former switch in DataTypeForPath exactly (issue #15 type-layer seam).
//
// Prefer Exact for single paths; Prefix for resource trees. A rule set is the
// type layer's declaration of which provider paths belong to which data type —
// connectors must not invent a second classifier.
type dataTypePathRule struct {
	Exact    string
	Prefix   string
	DataType string
}

// dataTypePathRules is the ordered path → data-type table.
// Keep the most specific exact/prefix rules before broader ones when they can
// overlap (e.g. "/me/player" before any broader "/me/..." family).
var dataTypePathRules = []dataTypePathRule{
	{Exact: "/me", DataType: "user"},
	{Prefix: "/me/player", DataType: "player"},
	{Exact: "/me/tracks", DataType: "tracks"},
	{Exact: "/me/liked", DataType: "tracks"},
	{Exact: "/me/top/tracks", DataType: "tracks"},
	{Prefix: "/tracks", DataType: "tracks"},
	{Prefix: "/videos", DataType: "tracks"},
	{Exact: "/me/albums", DataType: "albums"},
	{Prefix: "/albums", DataType: "albums"},
	{Exact: "/me/artists", DataType: "artists"},
	{Exact: "/me/top/artists", DataType: "artists"},
	{Prefix: "/artists", DataType: "artists"},
	// Followed artists: Spotify /me/following and YouTube subscriptions back
	// the unified artists library, so they share the artists permission key.
	{Exact: "/me/following", DataType: "artists"},
	{Exact: "/me/subscriptions", DataType: "artists"},
	{Exact: "/me/playlists", DataType: "playlists"},
	{Prefix: "/playlists", DataType: "playlists"},
	{Exact: "/me/contacts", DataType: "contacts"},
	{Prefix: "/contacts", DataType: "contacts"},
	{Prefix: "/people", DataType: "contacts"},
	{Exact: "/me/events", DataType: "events"},
	{Exact: "/me/calendars", DataType: "events"},
	{Exact: "/calendarList", DataType: "events"},
	{Exact: "/users/me/calendarList", DataType: "events"},
	{Prefix: "/events", DataType: "events"},
	{Prefix: "/calendars", DataType: "events"},
	{Exact: "/me/messages", DataType: "messages"},
	{Exact: "/me/threads", DataType: "messages"},
	{Exact: "/me/mail", DataType: "messages"},
	{Prefix: "/messages", DataType: "messages"},
	{Prefix: "/threads", DataType: "messages"},
	{Prefix: "/me/mail", DataType: "messages"},
	{Prefix: "/gmail", DataType: "messages"},
	{Prefix: "/users/me/messages", DataType: "messages"},
	{Prefix: "/users/me/threads", DataType: "messages"},
	{Exact: "/me/photos", DataType: "photos"},
	{Prefix: "/photos", DataType: "photos"},
	{Prefix: "/mediaItems", DataType: "photos"},
	{Prefix: "/picker", DataType: "photos"},
	// Self-hosted / foreign photo stores (Immich, …). /search/metadata is the
	// Immich asset search; keep it under photos so the permission key matches
	// the unified photos library.
	{Prefix: "/assets", DataType: "photos"},
	{Exact: "/search/metadata", DataType: "photos"},
	// Financial transactions (issue #141): YNAB / Quicken / OFX paths.
	{Exact: "/me/transactions", DataType: "transactions"},
	{Prefix: "/transactions", DataType: "transactions"},
	{Prefix: "/budgets", DataType: "transactions"},
	{Prefix: "/accounts", DataType: "transactions"},
	{Prefix: "/categories", DataType: "transactions"},
	{Prefix: "/payees", DataType: "transactions"},
	{Prefix: "/bank", DataType: "transactions"},
	{Prefix: "/cc", DataType: "transactions"},
	{Prefix: "/invstmt", DataType: "transactions"},
	{Prefix: "/ofx", DataType: "transactions"},
	{Prefix: "/quicken", DataType: "transactions"},
	// Location history (issue #142): OwnTracks, Dawarich, Google Timeline.
	{Exact: "/me/places", DataType: "places"},
	{Prefix: "/places", DataType: "places"},
	{Prefix: "/visits", DataType: "places"},
	{Prefix: "/trips", DataType: "places"},
	{Prefix: "/locations", DataType: "places"},
	{Prefix: "/api/v0/locations", DataType: "places"},
	{Prefix: "/api/v0/visit", DataType: "places"},
	{Prefix: "/api/v0/places", DataType: "places"},
	{Prefix: "/api/v1/points", DataType: "places"},
	{Prefix: "/api/v1/visits", DataType: "places"},
	{Prefix: "/timeline", DataType: "places"},
	{Prefix: "/semantic", DataType: "places"},
	{Exact: "/search", DataType: "search"},
	{Prefix: "/channels", DataType: "channels"},
	{Prefix: "/me/library", DataType: "library"},
	{Prefix: "/me/recommendations", DataType: "recommendations"},
}

// dataTypeForCanonicalPath matches an already-canonicalized proxy path
// (no query string, no dot segments) against the rule table.
func dataTypeForCanonicalPath(p string) string {
	for _, rule := range dataTypePathRules {
		if rule.Exact != "" {
			if p == rule.Exact {
				return rule.DataType
			}
			continue
		}
		if rule.Prefix != "" && strings.HasPrefix(p, rule.Prefix) {
			return rule.DataType
		}
	}
	return ""
}
