package main

import (
	"context"
	"io"
	"net/http"
	"time"
)

// libraryPageCap is the safety-valve number of pages a connector will follow
// when listing user data or auto-paginating a proxy GET. It exists to stop a
// misbehaving upstream from looping forever — not to slice a real library.
// 200 pages covers ~10k items at Spotify/YouTube page size 50, and ~20k at
// Google page size 100. Exceeding it sets truncated=true on the response.
const libraryPageCap = 200

// aegisHTTPClient bounds upstream calls so a hung service cannot stall a
// library aggregate for the full request-context lifetime.
var aegisHTTPClient = &http.Client{Timeout: 30 * time.Second}

// doUpstreamRequest is the shared bearer-token upstream call used by connectors
// and library pagination.
func doUpstreamRequest(ctx context.Context, method, url string, body io.Reader, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := aegisHTTPClient.Do(req)
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

// UpstreamBaseFor returns the configured endpoint for a service, or fallback.
func (s *Server) UpstreamBaseFor(serviceID, fallback string) string {
	if svc := s.config.GetService(serviceID); svc != nil && svc.Endpoint != "" {
		return trimRightSlash(svc.Endpoint)
	}
	return fallback
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
