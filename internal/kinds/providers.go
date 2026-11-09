package kinds

import (
	"context"
	"errors"
)

// ErrUnauthorized is returned by library providers when a service has no valid token.
var ErrUnauthorized = errors.New("unauthorized")

// PlaylistProvider is implemented by connectors that can return the user's
// playlists in Aegis's neutral format. truncated is true when the page cap
// was hit with more items still upstream.
type PlaylistProvider interface {
	ListUserPlaylists(ctx context.Context) (playlists []Playlist, truncated bool, err error)
}

// LikedTrackProvider is implemented by connectors that can return the user's
// liked/saved tracks in Aegis's neutral format.
type LikedTrackProvider interface {
	ListUserLikedTracks(ctx context.Context) (tracks []Track, truncated bool, err error)
}

// AlbumProvider is implemented by connectors that can return the user's
// saved albums in Aegis's neutral format.
type AlbumProvider interface {
	ListUserSavedAlbums(ctx context.Context) (albums []Album, truncated bool, err error)
}

// ArtistProvider is implemented by connectors that can return the user's
// followed or top artists in Aegis's neutral format.
type ArtistProvider interface {
	ListUserFollowedArtists(ctx context.Context) (artists []Artist, truncated bool, err error)
}

// ContactProvider is implemented by connectors that can return the user's
// contacts in Aegis's neutral format. The aggregate library endpoint uses
// this so agents can ask for "my contacts" once and receive every service.
// Each returned Contact should carry one ContactSource for that service.
// https://github.com/fudomunro/aegis/issues/12
type ContactProvider interface {
	// ListUserContacts fetches the user's contacts from the upstream service
	// and maps them into neutral Contact values (service identity in Sources).
	// Return ErrUnauthorized when no valid token is available.
	// truncated is true when the page cap was hit with more items still upstream.
	ListUserContacts(ctx context.Context) (contacts []Contact, truncated bool, err error)
}

// MessageProvider is implemented by connectors that can return the user's
// messages in Aegis's neutral format. The aggregate library endpoint uses
// this so agents can ask for "my email" once and receive every connected
// service. Each returned Message should carry one MessageSource for that
// service. Aegis never stores message bodies as the home copy — the
// authoritative content stays on the source service (issue #16).
// https://github.com/fudomunro/aegis/issues/60
type MessageProvider interface {
	// ListMessages fetches messages from the upstream service and maps them
	// into neutral Message values (service identity in Sources). Return
	// ErrUnauthorized when no valid token is available. truncated is true
	// when the page/message cap was hit with more items still upstream.
	ListMessages(ctx context.Context) (messages []Message, truncated bool, err error)
}

// PhotoProvider is implemented by connectors that can return the user's photos
// in Aegis's neutral format. The aggregate library endpoint uses this so
// agents can ask for "my photos" once and receive every service. Each returned
// Photo should carry one PhotoSource for that service. Connectors must not
// download or cache photo bytes — only metadata and service-provided URLs.
// https://github.com/fudomunro/aegis/issues/19
type PhotoProvider interface {
	// ListUserPhotos fetches the user's photo metadata from the upstream
	// service and maps them into neutral Photo values (service identity in
	// Sources; bytes stay on the service). Return ErrUnauthorized when no
	// valid token is available. truncated is true when the page cap was hit
	// with more items still upstream.
	ListUserPhotos(ctx context.Context) (photos []Photo, truncated bool, err error)
}

// PhotoBytesProvider is an optional connector capability used by the photo
// share / ownership-claim PoC (issue #20). Implementations must treat bytes
// as ephemeral: fetch for this call only and do not store or cache them.
// https://github.com/fudomunro/aegis/issues/20
type PhotoBytesProvider interface {
	// FetchPhotoBytes downloads one original photo (by service-local ID)
	// and returns the bytes plus a MIME type. Return ErrUnauthorized when
	// no valid credential is available. Callers must not retain the slice
	// beyond the share/claim request that needed it.
	FetchPhotoBytes(ctx context.Context, id string) (data []byte, mimeType string, err error)
}

// PhotoShareRequest is the neutral input for publishing one photo to a
// share target (issue #20 PoC). Data is ephemeral caller-supplied bytes.
type PhotoShareRequest struct {
	// SourceService / SourceID name the photo origin for the claim.
	SourceService string `json:"source_service,omitempty"`
	SourceID      string `json:"source_id,omitempty"`
	// Data is the original (or approved derivative) image bytes.
	Data []byte `json:"-"`
	// MimeType is the image media type (e.g. "image/jpeg").
	MimeType string `json:"mime_type,omitempty"`
	// Caption is optional post text.
	Caption string `json:"caption,omitempty"`
	// Alt is optional image alt text.
	Alt string `json:"alt,omitempty"`
	// ContentSHA256 is the lowercase hex digest of Data, when computed.
	ContentSHA256 string `json:"content_sha256,omitempty"`
}

// PhotoShareResult is the neutral result of publishing a photo.
type PhotoShareResult struct {
	// Service is the share-target connector ID (e.g. "bluesky").
	Service string `json:"service"`
	// PostURI is the provider-native record URI when available (AT URI).
	PostURI string `json:"post_uri,omitempty"`
	// PostURL is a human-openable web URL for the share.
	PostURL string `json:"post_url,omitempty"`
	// PostID is a provider-local record/rkey identifier.
	PostID string `json:"post_id,omitempty"`
	// SharedAt is the publish timestamp in RFC3339, when known.
	SharedAt string `json:"shared_at,omitempty"`
}

// PhotoShareTarget is implemented by connectors that can publish a photo
// (issue #20 PoC). Bytes are provided for this call only.
type PhotoShareTarget interface {
	SharePhoto(ctx context.Context, req PhotoShareRequest) (PhotoShareResult, error)
}

// CalendarEventProvider is implemented by connectors that can return the
// user's calendar entries in Aegis's neutral format. The aggregate library
// endpoint uses this so agents can ask for "my calendar" once and receive
// every connected service. Each returned CalendarEvent should carry one
// CalendarEventSource for that service. Aegis never stores calendar content
// — the authoritative copy stays on the source service (issue #16).
// https://github.com/fudomunro/aegis/issues/65
type CalendarEventProvider interface {
	// ListCalendarEvents fetches calendar entries from the upstream service
	// and maps them into neutral CalendarEvent values (service identity in
	// Sources). Return ErrUnauthorized when no valid token is available.
	// truncated is true when the page cap was hit with more items still upstream.
	ListCalendarEvents(ctx context.Context) (events []CalendarEvent, truncated bool, err error)
}

// TransactionProvider is implemented by connectors that can return the user's
// financial transactions in Aegis's neutral format (issue #141). The aggregate
// library endpoint uses this so agents can ask "did we go over on groceries?"
// once and receive every connected source. Each returned Transaction should
// carry one TransactionSource for that service. Amounts are signed decimal
// strings — never float JSON numbers.
type TransactionProvider interface {
	// ListUserTransactions returns neutral transactions (one
	// TransactionSource each). Return ErrUnauthorized when no valid token
	// is available. truncated is true when the page cap was hit with more
	// items still upstream.
	ListUserTransactions(ctx context.Context) (transactions []Transaction, truncated bool, err error)
}

// PlaceVisitProvider is implemented by connectors that can return the user's
// location history as visits and trips in Aegis's neutral format (issue #142).
// The aggregate library endpoint uses this so agents can answer "where was I?"
// with evidence rather than booking intent. Each returned PlaceVisit should
// carry one PlaceVisitSource for that service. Aegis models visits and trips —
// never raw GPS firehoses — and defaults the places permission to none.
type PlaceVisitProvider interface {
	// ListUserPlaceVisits returns neutral visits/trips (one
	// PlaceVisitSource each). Return ErrUnauthorized when no valid token
	// is available. truncated is true when the page cap was hit with more
	// items still upstream.
	ListUserPlaceVisits(ctx context.Context) (visits []PlaceVisit, truncated bool, err error)
}

// DocumentProvider is implemented by connectors that can return the user's
// documents (receipts, warranties, letters) in Aegis's neutral format
// (issue #143). The aggregate library endpoint uses this so agents can ask
// "where is my lease?" once and receive every connected source. Each returned
// Document should carry one DocumentSource for that service. Aegis never
// stores document bytes — the authoritative copy stays on the source service.
type DocumentProvider interface {
	// ListUserDocuments returns neutral documents (one DocumentSource each).
	// Excerpt may carry a short OCR preview; full text is never required
	// here. Return ErrUnauthorized when no valid token is available.
	// truncated is true when the page cap was hit with more items still
	// upstream.
	ListUserDocuments(ctx context.Context) (documents []Document, truncated bool, err error)
	// FetchDocumentText returns the full extracted text for one
	// service-local document ID. Used only by the gated text endpoint.
	FetchDocumentText(ctx context.Context, id string) (text string, truncated bool, err error)
}

// PlaylistServiceName returns the display name of the first source, or "".
func PlaylistServiceName(p Playlist) string {
	if len(p.Sources) > 0 {
		return p.Sources[0].ServiceName
	}
	return ""
}

// TrackServiceName returns the display name of the first source, or "".
func TrackServiceName(t Track) string {
	if len(t.Sources) > 0 {
		return t.Sources[0].ServiceName
	}
	return ""
}

// AlbumServiceName returns the display name of the first source, or "".
func AlbumServiceName(a Album) string {
	if len(a.Sources) > 0 {
		return a.Sources[0].ServiceName
	}
	return ""
}

// ArtistServiceName returns the display name of the first source, or "".
func ArtistServiceName(a Artist) string {
	if len(a.Sources) > 0 {
		return a.Sources[0].ServiceName
	}
	return ""
}

// DocumentServiceName returns the display name of the first source, or "".
func DocumentServiceName(d Document) string {
	if len(d.Sources) > 0 {
		return d.Sources[0].ServiceName
	}
	return ""
}

// Cursor-paged reads and placement writes (issue #254 mirror rethink).
// Mirroring is not a kind: it is reconciliation over kinds. Core
// plans what should exist where and schedules the work; connectors expose
// the operations below — read a list in pages, place items into a playlist,
// save liked tracks — and report per-item skips so planning can react.

// TrackPage is one cursor page of service-local tracks.
type TrackPage struct {
	Tracks []Track `json:"tracks,omitempty"`
	// Total is the upstream item count, when known.
	Total int `json:"total,omitempty"`
	// Next is the opaque cursor for the following page; "" ends the list.
	Next string `json:"next,omitempty"`
}

// PlaylistItemsProvider is implemented by connectors that can page through
// the tracks of one service-local playlist. Reconciliation reads source
// lists this way and re-reads targets to see what is already placed.
type PlaylistItemsProvider interface {
	// ListPlaylistItems returns one page of a playlist's tracks. ref carries
	// the playlist's service-local identity (name/URL in Sources); only the
	// page at the start cursor needs to fill it. Return ErrUnauthorized when
	// no valid token is available.
	ListPlaylistItems(ctx context.Context, playlistID, cursor string, limit int) (ref Playlist, page TrackPage, err error)
}

// LikedTrackPageProvider is the cursor-paged form of LikedTrackProvider.
// Chained sync jobs step one upstream page at a time instead of re-walking
// the whole list on every step.
type LikedTrackPageProvider interface {
	// ListUserLikedTracksPage returns one page of the user's liked/saved
	// tracks. Return ErrUnauthorized when no valid token is available.
	ListUserLikedTracksPage(ctx context.Context, cursor string, limit int) (page TrackPage, err error)
}

// PlaylistMeta is a playlist's live version descriptor: what a freshness
// check compares against its recorded watermark.
type PlaylistMeta struct {
	Name string `json:"name"`
	// ItemCount is the playlist's current item count.
	ItemCount int `json:"item_count"`
	// VersionToken is the provider's change token (e.g. Spotify's
	// snapshot_id), when the API exposes one.
	VersionToken string `json:"version_token,omitempty"`
}

// PlaylistMetaProvider is implemented by connectors that can report a
// playlist's live version descriptor in one cheap metadata call (no item
// paging) — the read freshness checks use to answer "is this backup still
// current?" without re-syncing anything.
type PlaylistMetaProvider interface {
	// GetPlaylistMeta returns the playlist's current name, item count, and
	// version token. ok is false when the playlist does not exist. Return
	// ErrUnauthorized when no valid token is available.
	GetPlaylistMeta(ctx context.Context, playlistID string) (meta PlaylistMeta, ok bool, err error)
	// SavedTrackCount returns how many items the user's liked/saved list
	// currently holds — the comparable token for liked-tracks copies.
	SavedTrackCount(ctx context.Context) (n int, err error)
}

// SnapshotProber is implemented by ingest sources whose copy freshness is
// "the recorded export location still exists" (issue #286, the
// PlaylistMetaProvider pattern from #264). The generic core checker asks
// the source to probe its own location semantics.
type SnapshotProber interface {
	// SnapshotProbe reports whether the export recorded at location is
	// still present, and its display name for the copy version.
	SnapshotProbe(location string) (exists bool, name string, err error)
}

// PlaceSkip is one item a placement operation could not place.
type PlaceSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// PlacePlaylistItemsResult reports a playlist placement.
type PlacePlaylistItemsResult struct {
	// PlaylistID is the service-local target playlist (created or appended).
	PlaylistID string `json:"playlist_id"`
	// URL is a deep link to the target playlist, when available.
	URL string `json:"url,omitempty"`
	// Copied is how many tracks were placed.
	Copied int `json:"copied"`
	// Skips are items that could not be placed (no catalog match, …).
	Skips []PlaceSkip `json:"skips,omitempty"`
}

// PlaylistItemWriteProvider is implemented by connectors that can create a
// playlist and place tracks in it. Placement matches by the connector's own
// catalog search when a track carries no service-local reference.
type PlaylistItemWriteProvider interface {
	// PlacePlaylistItems creates a playlist named name when playlistID is
	// empty, else appends to it — appending is what lets a chained job spread
	// across steps without creating a playlist per batch. Return
	// ErrUnauthorized when no valid token is available.
	PlacePlaylistItems(ctx context.Context, playlistID, name string, tracks []Track) (PlacePlaylistItemsResult, error)
}

// SaveLikedTracksResult reports a liked/saved placement.
type SaveLikedTracksResult struct {
	Copied int         `json:"copied"`
	Skips  []PlaceSkip `json:"skips,omitempty"`
}

// LikedTrackWriteProvider is implemented by connectors that can save/like
// tracks on the user's account.
type LikedTrackWriteProvider interface {
	// SaveLikedTracks marks the given tracks as liked/saved. Matching is by
	// service-local reference when present, else catalog search. Return
	// ErrUnauthorized when no valid token is available.
	SaveLikedTracks(ctx context.Context, tracks []Track) (SaveLikedTracksResult, error)
}

// PlaceConflict is one item placement refused to overwrite: the remote
// changed since the caller's watermark (If-Match / etag mismatch).
type PlaceConflict struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	ID      string `json:"id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// PlaceRecord is one successfully placed item with its target-side
// watermark, so the caller can stamp copy-ledger state.
type PlaceRecord struct {
	Name       string `json:"name"`
	ExternalID string `json:"external_id,omitempty"`
	ETag       string `json:"etag,omitempty"`
}

// PlaceEntitiesResult reports an entity placement (contacts, events).
type PlaceEntitiesResult struct {
	// Copied is how many items were placed.
	Copied int `json:"copied"`
	// Placed carries per-item watermarks for the successful placements.
	Placed []PlaceRecord `json:"placed,omitempty"`
	// Skips are items that could not be placed (provider error per item).
	Skips []PlaceSkip `json:"skips,omitempty"`
	// Conflicts are items refused because the remote changed underfoot.
	Conflicts []PlaceConflict `json:"conflicts,omitempty"`
}

// ContactWriteProvider is implemented by connectors that can place contacts
// on the user's account. Placement is optimistic-concurrency safe: a record
// carrying this service's local reference (and etag) in Sources is updated
// with If-Match semantics and reported as a PlaceConflict when the remote
// changed; records without one are created.
type ContactWriteProvider interface {
	// PlaceContacts creates or updates the given contacts. Return
	// ErrUnauthorized when no valid token is available.
	PlaceContacts(ctx context.Context, contacts []Contact) (PlaceEntitiesResult, error)
}

// CalendarEventWriteProvider is implemented by connectors that can place
// events on the user's account, with the same create/update and conflict
// semantics as ContactWriteProvider. The iCal UID falls back across sources
// and EntityID so a copied event keeps its identity across services.
type CalendarEventWriteProvider interface {
	// PlaceEvents creates or updates the given events. Return
	// ErrUnauthorized when no valid token is available.
	PlaceEvents(ctx context.Context, events []CalendarEvent) (PlaceEntitiesResult, error)
}
