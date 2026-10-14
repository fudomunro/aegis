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
