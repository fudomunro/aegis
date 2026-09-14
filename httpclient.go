package main

import (
	"net/http"
	"time"
)

// libraryPageCap is the maximum number of pages a connector will follow when
// listing user data. Exceeding it sets LibraryServiceStatus.Truncated.
const libraryPageCap = 10

// guardianHTTPClient bounds upstream calls so a hung service cannot stall a
// library aggregate for the full request-context lifetime.
var guardianHTTPClient = &http.Client{Timeout: 30 * time.Second}
