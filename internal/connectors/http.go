package connectors

import (
	"context"
	"io"
	"net/http"

	"github.com/fudomunro/aegis/internal/shared"
)

// DoUpstreamRequest is the shared bearer-token upstream call used by connectors
// and library pagination.
func DoUpstreamRequest(ctx context.Context, method, url string, body io.Reader, token string) ([]byte, int, error) {
	return DoUpstreamRequestHeaders(ctx, method, url, body, token, nil)
}

// DoUpstreamRequestHeaders is DoUpstreamRequest with extra upstream headers
// beside the bearer token — services whose credential is more than one
// header (Apple Music's developer token + Music User Token).
func DoUpstreamRequestHeaders(ctx context.Context, method, url string, body io.Reader, token string, extra http.Header) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, vals := range extra {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	resp, err := shared.HTTPClient.Do(req)
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
