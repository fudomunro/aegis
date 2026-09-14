package main

// Playlist is the user-facing playlist. Service identity (connector ID,
// service-local ID, deep link) is never top-level — it lives in Sources so
// one logical playlist can point at copies on Spotify, YouTube Music, etc.
type Playlist struct {
	// Name is the user-facing playlist title.
	Name string `json:"name"`
	// Description is the playlist description, if any.
	Description string `json:"description,omitempty"`
	// Owner is the playlist owner's display name, when known.
	Owner string `json:"owner,omitempty"`
	// TrackCount is the number of items. For multi-source (synced) playlists
	// this is the max count across services.
	TrackCount int `json:"track_count"`
	// Sources lists every service-local copy of this playlist. A single-service
	// list has one entry; a synced library entry may have several.
	Sources []PlaylistSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields that don't fit the neutral shape.
	Extra map[string]any `json:"extra,omitempty"`
}

// PlaylistSource is a service-specific location of a playlist. All of these
// fields are service data, not part of the user-facing playlist identity.
type PlaylistSource struct {
	// Service is the connector ID: "spotify", "youtube-music", "amazon-music".
	Service string `json:"service"`
	// ServiceName is the human-readable label: "Spotify".
	ServiceName string `json:"service_name,omitempty"`
	// ID is the service-local playlist ID. Use with that service's endpoints.
	ID string `json:"id"`
	// URL is a deep link to the playlist on the service, when available.
	URL string `json:"url,omitempty"`
	// Extra holds additional service-specific fields (uri, published_at, …).
	Extra map[string]any `json:"extra,omitempty"`
}

// Track is the user-facing song/video. Service IDs live in Sources.
type Track struct {
	// Name is the user-facing track or video title.
	Name string `json:"name"`
	// Artists are unified artist identities (see matchArtists / NormalizeIdentityKey).
	Artists []Artist `json:"artists,omitempty"`
	// Album is the unified album identity, when known.
	Album *Album `json:"album,omitempty"`
	// DurationMs is length in milliseconds, when the service provides it.
	DurationMs int `json:"duration_ms,omitempty"`
	// Duration is a human-readable length (e.g. ISO-8601 for YouTube).
	Duration string `json:"duration,omitempty"`
	// Sources lists service-local copies / IDs for this track.
	Sources []TrackSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// TrackSource is a service-specific location of a track or video.
type TrackSource struct {
	Service     string         `json:"service"`
	ServiceName string         `json:"service_name,omitempty"`
	ID          string         `json:"id,omitempty"`
	URL         string         `json:"url,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// Artist is a unified artist/channel identity. Matching across services is
// currently name-based (NormalizeIdentityKey); IDs are service-local only.
// Follow-up: https://github.com/fudomunro/aperture-guardian/issues/4
type Artist struct {
	// Name is the preferred display name (first service that reported it).
	Name string `json:"name"`
	// Sources lists service-local representations of this artist.
	Sources []ArtistSource `json:"sources,omitempty"`
}

// ArtistSource is a service-local artist/channel reference.
type ArtistSource struct {
	Service     string `json:"service"`
	ServiceName string `json:"service_name,omitempty"`
	ID          string `json:"id,omitempty"`
	// Name is how this service spells the artist.
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Album is a unified album identity (name-based matching for now).
type Album struct {
	Name string `json:"name"`
	// Artists credit the album, when known (used by the library view).
	Artists []Artist      `json:"artists,omitempty"`
	Sources []AlbumSource `json:"sources,omitempty"`
}

// AlbumSource is a service-local album reference.
type AlbumSource struct {
	Service     string `json:"service"`
	ServiceName string `json:"service_name,omitempty"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	URL         string `json:"url,omitempty"`
}

// PlaylistDetail is a playlist plus its tracks.
type PlaylistDetail struct {
	Playlist
	// Tracks are the loaded tracks/items. May be partial if the playlist is long.
	Tracks []Track `json:"tracks,omitempty"`
	// TotalTracks is the full count (may exceed len(Tracks)).
	TotalTracks int `json:"total_tracks,omitempty"`
}

// Contact is a person or other entity represented in personal data — someone
// in a photo, an email correspondent, a playlist collaborator, etc. Service
// identity lives in Sources, same as Playlist/Artist. Cross-service matching
// prefers email, then phone, then handle, then display name.
// https://github.com/fudomunro/aperture-guardian/issues/12
type Contact struct {
	// Name is the preferred display name (first non-empty across sources).
	Name string `json:"name,omitempty"`
	// Emails are known addresses. Stored lowercased for stable matching.
	Emails []string `json:"emails,omitempty"`
	// Phones are known numbers in digit form (leading + preserved when present).
	Phones []string `json:"phones,omitempty"`
	// Handles are other identifiers (usernames, social IDs, service accounts).
	Handles []string `json:"handles,omitempty"`
	// Organization is a company, team, or group affiliation when known.
	Organization string `json:"organization,omitempty"`
	// Sources lists service-local representations of this contact.
	Sources []ContactSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields that don't fit the neutral shape.
	Extra map[string]any `json:"extra,omitempty"`
}

// ContactSource is a service-local representation of a contact. All of these
// fields are service data, not part of the user-facing contact identity.
type ContactSource struct {
	// Service is the connector ID: "google", "outlook", "icloud", …
	Service string `json:"service"`
	// ServiceName is the human-readable label: "Google Contacts".
	ServiceName string `json:"service_name,omitempty"`
	// ID is the service-local contact ID. Use with that service's endpoints.
	ID string `json:"id,omitempty"`
	// Name is how this service spells the contact.
	Name string `json:"name,omitempty"`
	// Emails are addresses this service reports for the contact.
	Emails []string `json:"emails,omitempty"`
	// Phones are phone numbers this service reports for the contact.
	Phones []string `json:"phones,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// ContactList is the neutral response for listing contacts — either for a
// single service or aggregated across all connected services.
type ContactList struct {
	Contacts []Contact `json:"contacts"`
	// Total is the number of contacts in this response.
	Total int `json:"total"`
	// Synced is true when the library merged contacts that shared an identity
	// key (email, phone, handle, or name) into multi-source entries.
	Synced bool `json:"synced,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// LibraryServiceStatus reports per-service results for an aggregate call.
type LibraryServiceStatus struct {
	// Status is "ok", "unauthorized", "error", or "skipped".
	Status string `json:"status"`
	// Count is the number of items returned for this service when Status is "ok".
	Count int `json:"count,omitempty"`
	// Error is a short reason when Status is "error" or "unauthorized".
	Error string `json:"error,omitempty"`
}

// PlaylistList is the neutral response for listing playlists — either for a
// single service or aggregated across all connected services.
type PlaylistList struct {
	Playlists []Playlist `json:"playlists"`
	// Total is the number of playlists in this response.
	Total int `json:"total"`
	// Synced is true when the library merged same-name playlists across
	// services into multi-source entries.
	Synced bool `json:"synced,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// TrackList is the neutral response for listing liked/saved tracks — either
// for a single service or aggregated across all connected services.
type TrackList struct {
	Tracks []Track `json:"tracks"`
	// Total is the number of tracks in this response.
	Total int `json:"total"`
	// Synced is true when the library merged same-named tracks across
	// services into multi-source entries.
	Synced bool `json:"synced,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// AlbumList is the neutral response for listing saved albums.
type AlbumList struct {
	Albums    []Album                         `json:"albums"`
	Total     int                             `json:"total"`
	Synced    bool                            `json:"synced,omitempty"`
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// ArtistList is the neutral response for listing followed/top artists.
type ArtistList struct {
	Artists   []Artist                        `json:"artists"`
	Total     int                             `json:"total"`
	Synced    bool                            `json:"synced,omitempty"`
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// Playlist list status values used in LibraryServiceStatus.Status.
const (
	LibraryStatusOK           = "ok"
	LibraryStatusUnauthorized = "unauthorized"
	LibraryStatusError        = "error"
	LibraryStatusSkipped      = "skipped"
)
