package main

// Playlist is Guardian's service-neutral representation of a user playlist.
// Field names are chosen for agent/user comprehension, not raw API fidelity.
// Services may attach API-specific fields under Extra.
type Playlist struct {
	// Service is the connector ID: "spotify", "youtube-music", "amazon-music".
	Service string `json:"service"`
	// ServiceName is the human-readable service label: "Spotify".
	ServiceName string `json:"service_name"`
	// ID is the service-local playlist ID. Use with that service's endpoints.
	ID string `json:"id"`
	// Name is the user-facing playlist title.
	Name string `json:"name"`
	// Description is the playlist description, if any.
	Description string `json:"description,omitempty"`
	// Owner is the playlist owner's display name, when known.
	Owner string `json:"owner,omitempty"`
	// TrackCount is the number of tracks/items in the playlist.
	TrackCount int `json:"track_count"`
	// URL is a deep link to the playlist on the service, when available.
	URL string `json:"url,omitempty"`
	// Extra holds service-specific fields that don't fit the neutral shape.
	Extra map[string]any `json:"extra,omitempty"`
}

// Track is Guardian's service-neutral representation of a song, video, or
// other playable item inside a playlist or library.
type Track struct {
	// ID is the service-local track/video ID, when known.
	ID string `json:"id,omitempty"`
	// Name is the user-facing track or video title.
	Name string `json:"name"`
	// Artists lists artist or channel names.
	Artists []string `json:"artists,omitempty"`
	// Album is the album title, when known.
	Album string `json:"album,omitempty"`
	// DurationMs is length in milliseconds, when the service provides it.
	DurationMs int `json:"duration_ms,omitempty"`
	// Duration is a human-readable length (e.g. ISO-8601 for YouTube).
	Duration string `json:"duration,omitempty"`
	// Extra holds service-specific fields that don't fit the neutral shape.
	Extra map[string]any `json:"extra,omitempty"`
}

// PlaylistDetail is a playlist plus its tracks.
type PlaylistDetail struct {
	Playlist
	// Tracks are the loaded tracks/items. May be partial if the playlist is long.
	Tracks []Track `json:"tracks,omitempty"`
	// TotalTracks is the full count from the service (may exceed len(Tracks)).
	TotalTracks int `json:"total_tracks,omitempty"`
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
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// Playlist list status values used in LibraryServiceStatus.Status.
const (
	LibraryStatusOK           = "ok"
	LibraryStatusUnauthorized = "unauthorized"
	LibraryStatusError        = "error"
	LibraryStatusSkipped      = "skipped"
)
