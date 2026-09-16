package main

import (
	"net/http"
	"time"
)

// libraryPageCap is the safety-valve number of pages a connector will follow
// when listing user data or auto-paginating a proxy GET. It exists to stop a
// misbehaving upstream from looping forever — not to slice a real library.
// 200 pages covers ~10k items at Spotify/YouTube page size 50, and ~20k at
// Google page size 100. Exceeding it sets truncated=true on the response.
const libraryPageCap = 200

// guardianHTTPClient bounds upstream calls so a hung service cannot stall a
// library aggregate for the full request-context lifetime.
var guardianHTTPClient = &http.Client{Timeout: 30 * time.Second}
