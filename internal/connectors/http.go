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
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

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
