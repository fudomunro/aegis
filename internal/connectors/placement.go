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

// serviceHeader reads one configured static credential header (case-
// insensitive name match, trimmed) off a service's connection. "" when the
// service or the header is unset.
func serviceHeader(host Host, serviceID, name string) string {
	svc := host.ServiceConfig(serviceID)
	if svc == nil {
		return ""
	}
	for k, v := range svc.Headers {
		if strings.EqualFold(k, name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// apiJSON performs a raw JSON call to a service's configured endpoint using
// the stored OAuth token. Unlike the proxy path it runs no response
// transforms, so callers can use native API shapes. It errors (rather than
// falling back to a default base URL) when the service has no configured
// endpoint, so a misconfigured instance fails loudly instead of calling the
// public provider.
func apiJSON(ctx context.Context, host Host, serviceID, method, path string, query url.Values, body any) (map[string]any, int, error) {
	return apiJSONAuth(ctx, host, serviceID, method, path, query, body,
		func(logger *slog.Logger) (string, http.Header) {
			return host.GetValidToken(serviceID, logger), nil
		})
}

// apiJSONAuth is apiJSON for connectors whose upstream credential is more
// than one bearer token: auth resolves the bearer value and any extra
// headers (Apple Music's developer token + Music User Token).
func apiJSONAuth(ctx context.Context, host Host, serviceID, method, path string, query url.Values, body any,
	auth func(logger *slog.Logger) (string, http.Header)) (map[string]any, int, error) {
	svc := host.ServiceConfig(serviceID)
	if svc == nil {
		return nil, 0, fmt.Errorf("service %q not configured", serviceID)
	}
	if svc.Endpoint == "" {
		return nil, 0, fmt.Errorf("service %q has no upstream endpoint", serviceID)
	}
	token, extra := auth(slog.Default())
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

	data, status, err := DoUpstreamRequestHeaders(ctx, method, u, bodyReader, token, extra)
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

// splitContactName splits a display name into given/family for provider write
// bodies: everything before the last space is the given name(s), so "Ada
// Lovelace" and "Mary Ann Smith" land the family name last. A one-word name
// is all given.
func splitContactName(name string) (given, family string) {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, " "); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
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

// placeEventCalendar returns the service-local calendar id an event carries
// for this service ("": use the service default calendar). A cross-service
// copy must never be placed under the source service's calendar id.
func placeEventCalendar(ev kinds.CalendarEvent, serviceID string) string {
	for _, src := range ev.Sources {
		if src.Service == serviceID {
			return src.CalendarID
		}
	}
	return ""
}

// placeEventUID returns the cross-source event identity: any source's uid
// or iCal uid, else the catalog EntityID, else the first source's local id.
func placeEventUID(ev kinds.CalendarEvent) string {
	for _, src := range ev.Sources {
		for _, key := range []string{"uid", "ical_uid"} {
			if v, _ := src.Extra[key].(string); v != "" {
				return v
			}
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

// --- match ladder ---
//
// The match ladder is the resolve order a write-side connector walks to map
// a neutral entity onto one provider id (issue #366): the service-local
// reference rung first (reference shapes are provider-specific and stay
// beside each connector), then the catalog-search rungs driven here — the
// durable entity id (ISRC/UPC) when the provider exposes an exact lookup,
// then a guarded name search. A search error skips the entity; an exhausted
// ladder skips with "no match on <service>". Core plans, connectors operate
// (issue #254).

// LadderRef is one resolved placement: the provider id to write plus the
// record the copy ledger stamps from (the two differ when a provider's ref
// rung carries a library id and a catalog id).
type LadderRef struct {
	ID     string
	Record kinds.PlaceRecord
}

// SearchLadder is the catalog-search half of the match ladder for one
// entity kind. A provider declares its API calls and guards; the rung
// order, skip policy, and record shape live here.
type SearchLadder[T any] struct {
	// Service names the provider in skip copy ("no match on Spotify").
	Service string
	// Display names an entity in skip reports and place records.
	Display func(T) string
	// IdentityKey is the entity's copy-watermark key (issue #384).
	IdentityKey func(T) string
	// DurableID returns the entity's durable id (ISRC/UPC) and the
	// MatchedBy token proving it, or ("", "") when it carries none.
	DurableID func(T) (string, string)
	// ByDurableID is the exact catalog lookup: ok=false means no hit and
	// the ladder falls through to the name rung. nil means the provider
	// exposes no exact lookup (YouTube Music) and the name rung is the
	// only search.
	ByDurableID func(ctx context.Context, entity T, durableID string) (id string, ok bool, err error)
	// ByName is the guarded name search. "" means no acceptable hit — the
	// guard (duration, title, artist) rejected whatever the search found.
	ByName func(ctx context.Context, entity T) (string, error)
}

// Ref wraps a service-local reference rung hit into the shared record shape
// ("ref"). The provider may adjust ID when its write id differs from the
// reference it stamps.
func (l SearchLadder[T]) Ref(entity T, id string) LadderRef {
	return LadderRef{ID: id, Record: kinds.PlaceRecord{
		Name:       l.Display(entity),
		ExternalID: id,
		MatchedBy:  "ref",
		EntityName: l.IdentityKey(entity),
	}}
}

// Resolve walks the search rungs for one entity, appending a PlaceSkip
// when it cannot be placed.
func (l SearchLadder[T]) Resolve(ctx context.Context, entity T, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
	display, identity := l.Display(entity), l.IdentityKey(entity)
	if l.ByDurableID != nil {
		if durable, token := l.DurableID(entity); durable != "" {
			id, hit, err := l.ByDurableID(ctx, entity, durable)
			if err != nil {
				*skips = append(*skips, kinds.PlaceSkip{Name: display, Reason: err.Error()})
				return LadderRef{}, false
			}
			if hit {
				return LadderRef{ID: id, Record: kinds.PlaceRecord{
					Name: display, ExternalID: id, MatchedBy: token, EntityName: identity,
				}}, true
			}
		}
	}
	id, err := l.ByName(ctx, entity)
	if err != nil {
		*skips = append(*skips, kinds.PlaceSkip{Name: display, Reason: err.Error()})
		return LadderRef{}, false
	}
	if id == "" {
		*skips = append(*skips, kinds.PlaceSkip{Name: display, Reason: "no match on " + l.Service})
		return LadderRef{}, false
	}
	return LadderRef{ID: id, Record: kinds.PlaceRecord{
		Name: display, ExternalID: id, MatchedBy: kinds.TrackMatchName, EntityName: identity,
	}}, true
}

// SaveOnLadder is the create-only save shape (SaveLikedTracks, SaveAlbums,
// FollowArtists): resolve each entity through its ladder, dedupe by
// provider id, hand the batch to write, and report what landed. write takes
// ids in order and returns how many landed, so a half-done batch reports
// only its prefix.
func SaveOnLadder[T any](ctx context.Context, entities []T,
	resolve func(context.Context, T, *[]kinds.PlaceSkip) (LadderRef, bool),
	write func(ctx context.Context, ids []string) (written int, err error),
) (copied int, placed []kinds.PlaceRecord, skips []kinds.PlaceSkip, err error) {
	var refs []LadderRef
	seen := map[string]bool{}
	for _, entity := range entities {
		ref, ok := resolve(ctx, entity, &skips)
		if !ok || ref.ID == "" || seen[ref.ID] {
			continue
		}
		seen[ref.ID] = true
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return 0, nil, skips, nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	written, err := write(ctx, ids)
	for _, ref := range refs[:written] {
		placed = append(placed, ref.Record)
	}
	return written, placed, skips, err
}
