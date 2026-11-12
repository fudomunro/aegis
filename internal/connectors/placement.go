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
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Placement helpers shared by the write-side provider operations
// (PlaylistItemWriteProvider, LikedTrackWriteProvider) and the paged list
// reads reconciliation uses. Mirroring is reconciliation over kinds
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
		return nil, 0, kinds.ErrUnauthorized
	}

	u := shared.TrimRightSlash(svc.Endpoint) + path
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
		return nil, status, kinds.ErrUnauthorized
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
func placeTrackDisplay(t kinds.Track) string {
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
func placeTrackQuery(t kinds.Track) string {
	if len(t.Artists) == 0 {
		return t.Name
	}
	return t.Name + " " + t.Artists[0].Name
}

// placeTrackLocalRef returns the service-local id/uri a track carries for
// this service, if any — the fast path that avoids catalog search.
func placeTrackLocalRef(t kinds.Track, serviceID string) (id, uri string) {
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

// placeAlbumDisplay names an album in skip reports.
func placeAlbumDisplay(a kinds.Album) string {
	names := make([]string, 0, len(a.Artists))
	for _, artist := range a.Artists {
		if artist.Name != "" {
			names = append(names, artist.Name)
		}
	}
	artists := strings.Join(names, ", ")
	if artists == "" {
		return a.Name
	}
	return a.Name + " — " + artists
}

// placeAlbumQuery is the catalog search query used when an album carries no
// service-local reference: title plus the first artist.
func placeAlbumQuery(a kinds.Album) string {
	if len(a.Artists) == 0 {
		return a.Name
	}
	return a.Name + " " + a.Artists[0].Name
}

// placeAlbumLocalRef returns the service-local id an album carries for this
// service, if any — the fast path that avoids catalog search.
func placeAlbumLocalRef(a kinds.Album, serviceID string) string {
	for _, src := range a.Sources {
		if src.Service == serviceID && src.ID != "" {
			return src.ID
		}
	}
	return ""
}

// placeArtistDisplay names an artist in skip reports.
func placeArtistDisplay(a kinds.Artist) string {
	if a.Name != "" {
		return a.Name
	}
	if len(a.Sources) > 0 {
		return a.Sources[0].Name
	}
	return ""
}

// placeArtistLocalRef returns the service-local id an artist carries for
// this service, if any — the fast path that avoids catalog search.
func placeArtistLocalRef(a kinds.Artist, serviceID string) string {
	for _, src := range a.Sources {
		if src.Service == serviceID && src.ID != "" {
			return src.ID
		}
	}
	return ""
}

// albumTitleArtistCompatible is the name-path catalog guard (issue #375's
// title + primary artist fallback): a search hit may stand in for the album
// under placement when the normalized titles agree and the primary artists
// agree whenever both name one. EntityIDs are deliberately ignored here —
// the id path proved identity by query, and the name path cannot compare a
// provider UPC against an enriched MBID.
func albumTitleArtistCompatible(hit, want kinds.Album) bool {
	if kinds.NormalizeIdentityKey(hit.Name) != kinds.NormalizeIdentityKey(want.Name) {
		return false
	}
	if len(hit.Artists) == 0 || len(want.Artists) == 0 {
		return true
	}
	return kinds.NormalizeIdentityKey(hit.Artists[0].Name) == kinds.NormalizeIdentityKey(want.Artists[0].Name)
}

// artistNameCompatible is the name-path catalog guard for artists: a search
// hit may stand in when the normalized names agree (the "- topic" unit is
// stripped by NormalizeIdentityKey).
func artistNameCompatible(hit, want kinds.Artist) bool {
	got := kinds.NormalizeIdentityKey(hit.Name)
	return got != "" && got == kinds.NormalizeIdentityKey(want.Name)
}

// placeContactDisplay names a contact in skip/conflict reports.
func placeContactDisplay(c kinds.Contact) string {
	if c.Name != "" {
		return c.Name
	}
	if len(c.Emails) > 0 {
		return c.Emails[0]
	}
	return ""
}

// placeContactRef returns the service-local resource handle and etag a
// contact carries for this service, if any — the update path.
func placeContactRef(c kinds.Contact, serviceID string) (id, etag string) {
	for _, src := range c.Sources {
		if src.Service != serviceID {
			continue
		}
		if id == "" {
			id = src.ID
		}
		if etag == "" {
			etag, _ = src.Extra["etag"].(string)
		}
	}
	return id, etag
}

// placeEventRef returns the service-local resource handle and etag an event
// carries for this service, if any — the update path.
func placeEventRef(ev kinds.CalendarEvent, serviceID string) (id, etag string) {
	for _, src := range ev.Sources {
		if src.Service != serviceID {
			continue
		}
		if id == "" {
			id = src.ID
		}
		if etag == "" {
			etag, _ = src.Extra["etag"].(string)
		}
	}
	return id, etag
}

// placeContactUID returns the cross-source contact UID carried in any
// source's extras — the stable vCard identity that follows a copy.
func placeContactUID(c kinds.Contact) string {
	for _, src := range c.Sources {
		if v, _ := src.Extra["uid"].(string); v != "" {
			return v
		}
	}
	return ""
}

// placeEventUID returns the cross-source event identity: any source's uid,
// else the catalog EntityID, else the first source's local id.
func placeEventUID(ev kinds.CalendarEvent) string {
	for _, src := range ev.Sources {
		if v, _ := src.Extra["uid"].(string); v != "" {
			return v
		}
	}
	if ev.EntityID != "" {
		return ev.EntityID
	}
	if len(ev.Sources) > 0 {
		return ev.Sources[0].ID
	}
	return ""
}

// parseRFC3339 parses a neutral timestamp field for the encoder structs.
func parseRFC3339(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
