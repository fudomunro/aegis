package types

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
