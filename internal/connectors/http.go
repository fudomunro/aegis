package connectors

import (
	"context"
	"io"
	"net/http"
	"time"
)

// LibraryPageCap is the safety-valve number of pages a connector will follow
// when listing user data or auto-paginating a proxy GET. Exceeding it sets
// truncated=true on the response.
const LibraryPageCap = 200

// HTTPClient bounds upstream calls so a hung service cannot stall a
// library aggregate for the full request-context lifetime.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// DoUpstreamRequest is the shared bearer-token upstream call used by connectors
// and library pagination.
func DoUpstreamRequest(ctx context.Context, method, url string, body io.Reader, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// TrimRightSlash trims trailing slashes from a URL prefix.
func TrimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
