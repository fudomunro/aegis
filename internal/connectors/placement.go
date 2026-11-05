package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// Placement helpers shared by the write-side provider operations
// (PlaylistItemWriteProvider, LikedTrackWriteProvider) and the paged list
// reads reconciliation uses. Mirroring is reconciliation over data types
// (issue #254): core plans, connectors operate.

// apiJSON performs a raw JSON call to a service's configured endpoint using
// the stored OAuth token. Unlike the proxy path it runs no response
// transforms, so callers can use native API shapes. It errors (rather than
// falling back to a default base URL) when the service has no configured
// endpoint, so a misconfigured instance fails loudly instead of calling the
// public provider.
func apiJSON(ctx context.Context, host Host, serviceID, method, path string, query url.Values, body any) (map[string]any, int, error) {
	svc := host.ServiceConfig(serviceID)
	if svc == nil {
		return nil, 0, fmt.Errorf("service %q not configured", serviceID)
	}
	if svc.Endpoint == "" {
		return nil, 0, fmt.Errorf("service %q has no upstream endpoint", serviceID)
	}
	token := host.GetValidToken(serviceID, slog.Default())
	if token == "" {
		return nil, 0, types.ErrUnauthorized
	}

	u := TrimRightSlash(svc.Endpoint) + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		bodyReader = bytes.NewReader(buf)
	}

	data, status, err := DoUpstreamRequest(ctx, method, u, bodyReader, token)
	if err != nil {
		return nil, status, err
	}
	if status == http.StatusUnauthorized {
		return nil, status, types.ErrUnauthorized
	}
	if status >= 400 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 240 {
			msg = msg[:240] + "…"
		}
		return nil, status, fmt.Errorf("upstream %s %s returned %d: %s", method, path, status, msg)
	}
	if len(data) == 0 {
		return map[string]any{}, status, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, status, fmt.Errorf("decode upstream response: %w", err)
	}
	return parsed, status, nil
}

// placeTrackDisplay names a track in skip reports.
func placeTrackDisplay(t types.Track) string {
	names := make([]string, 0, len(t.Artists))
	for _, a := range t.Artists {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	artists := strings.Join(names, ", ")
	if artists == "" {
		return t.Name
	}
	return t.Name + " — " + artists
}

// placeTrackQuery is the catalog search query used when a track carries no
// service-local reference: title plus the first artist.
func placeTrackQuery(t types.Track) string {
	if len(t.Artists) == 0 {
		return t.Name
	}
	return t.Name + " " + t.Artists[0].Name
}

// placeTrackLocalRef returns the service-local id/uri a track carries for
// this service, if any — the fast path that avoids catalog search.
func placeTrackLocalRef(t types.Track, serviceID string) (id, uri string) {
	for _, src := range t.Sources {
		if src.Service != serviceID {
			continue
		}
		if id == "" {
			id = src.ID
		}
		if uri == "" {
			if u, _ := src.Extra["uri"].(string); u != "" {
				uri = u
			}
		}
	}
	return id, uri
}
