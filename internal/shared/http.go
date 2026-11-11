// Package shared is stdlib-only transport/JSON/math plumbing shared by
// internal/core and internal/connectors (and importable by internal/types).
//
// Charter: no domain models, no Host, no service IDs, no provider knowledge,
// no policy. It imports none of the Aegis packages — stdlib only.
package shared

import (
	"errors"
	"fmt"
	"io"
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

// ReadBodyLimited reads an inbound request body up to max bytes. Overflow is
// reported to the caller as a 413; other read failures as a 400. ok is false
// when the response has already been written.
// MaxAdminBodyBytes covers admin JSON request bodies (permissions,
// hook-health paste, app and scope forms).
const MaxAdminBodyBytes = 4 << 20 // 4 MiB

func ReadBodyLimited(w http.ResponseWriter, r *http.Request, max int64) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		WriteBodyReadError(w, err)
		return nil, false
	}
	return data, true
}

// WriteBodyReadError maps body-read failures to 413 (too large) or 400.
func WriteBodyReadError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		WriteJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request body exceeds %d bytes", maxErr.Limit))
		return
	}
	WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
}
