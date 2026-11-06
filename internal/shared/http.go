// Package shared is stdlib-only transport/JSON/math plumbing shared by
// internal/core and internal/connectors (and importable by internal/types).
//
// Charter: no domain models, no Host, no service IDs, no provider knowledge,
// no policy. It imports none of the Aegis packages — stdlib only.
package shared

import (
	"net/http"
	"time"
)

// LibraryPageCap is the safety-valve number of pages a connector will follow
// when listing user data or auto-paginating a proxy GET. It exists to stop a
// misbehaving upstream from looping forever — not to slice a real library.
// 200 pages covers ~10k items at Spotify/YouTube page size 50, and ~20k at
// Google page size 100. Exceeding it sets truncated=true on the response.
const LibraryPageCap = 200

// HTTPClient bounds upstream calls so a hung service cannot stall a
// library aggregate for the full request-context lifetime.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// TrimRightSlash trims trailing slashes from a URL prefix.
func TrimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
