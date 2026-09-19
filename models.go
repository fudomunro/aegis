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
	// Match records cross-service identity: "link" (explicit user link) or
	// "name" (title equality). Empty on single-source playlists.
	Match string `json:"match,omitempty"`
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
	// Canonical is true when this service is the authoritative location for
	// the playlist (issue #16). Set by connectors or config canonical_for.
	Canonical bool `json:"canonical,omitempty"`
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
	// EntityID is a durable catalog ID when a service provides one (ISRC).
	// Preferred cross-service identity over display-name matching.
	// https://github.com/fudomunro/aperture-guardian/issues/4
	EntityID string `json:"entity_id,omitempty"`
	// Match records how this unified track's cross-service identity was
	// established: "isrc" (shared EntityID) or "name" (title+artist fallback).
	// Empty on single-source tracks.
	Match string `json:"match,omitempty"`
	// Sources lists service-local copies / IDs for this track.
	Sources []TrackSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// TrackSource is a service-specific location of a track or video.
type TrackSource struct {
	Service     string `json:"service"`
	ServiceName string `json:"service_name,omitempty"`
	ID          string `json:"id,omitempty"`
	URL         string `json:"url,omitempty"`
	// Canonical is true when this service is the authoritative location for
	// the track (issue #16). Set by connectors or config canonical_for.
	Canonical bool           `json:"canonical,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// Artist is a unified artist/channel identity. Matching prefers EntityID
// (MusicBrainz MBID when resolved) then normalized name (issue #4).
type Artist struct {
	// Name is the preferred display name (first service that reported it).
	Name string `json:"name"`
	// EntityID is a durable catalog ID (MusicBrainz MBID) when resolved.
	EntityID string `json:"entity_id,omitempty"`
	// Match records cross-service identity: "mbid" or "name". Empty on singles.
	Match string `json:"match,omitempty"`
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

// Album is a unified album identity. Matching prefers EntityID (UPC/barcode)
// then title+primary artist (issue #4).
type Album struct {
	Name string `json:"name"`
	// Artists credit the album, when known (used by the library view).
	Artists []Artist `json:"artists,omitempty"`
	// EntityID is a durable catalog ID (UPC/barcode) when a service provides one.
	EntityID string `json:"entity_id,omitempty"`
	// Match records cross-service identity: "upc" or "name". Empty on singles.
	Match   string        `json:"match,omitempty"`
	Sources []AlbumSource `json:"sources,omitempty"`
}

// AlbumSource is a service-local album reference.
type AlbumSource struct {
	Service     string         `json:"service"`
	ServiceName string         `json:"service_name,omitempty"`
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	URL         string         `json:"url,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
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
	// Canonical is true when this service is the authoritative location for
	// the contact (issue #16). Set by connectors or config canonical_for.
	Canonical bool `json:"canonical,omitempty"`
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
	// Truncated is true when any service hit its page cap (or left an
	// unfollowed continuation) with more items still upstream.
	Truncated bool `json:"truncated,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// Photo is a user-facing photo (or video still). Service identity lives in
// Sources. Guardian never stores photo bytes — the authoritative copy stays on
// a non-Guardian service marked Canonical (issue #16). Ownership / provenance
// claims are intentionally deferred: https://github.com/fudomunro/aperture-guardian/issues/20
type Photo struct {
	// TakenAt is the capture time in RFC3339, when the service provides it.
	TakenAt string `json:"taken_at,omitempty"`
	// Width and Height are pixel dimensions, when known.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// MimeType is the media type (e.g. "image/jpeg").
	MimeType string `json:"mime_type,omitempty"`
	// Filename is the original filename or service display name.
	Filename string `json:"filename,omitempty"`
	// Caption is the user-visible description, when present.
	Caption string `json:"caption,omitempty"`
	// People lists known people in the photo (Contact identity matching).
	People []Contact `json:"people,omitempty"`
	// Sources lists service-local copies. At least one source should be
	// Canonical when the blob lives outside Guardian.
	Sources []PhotoSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// PhotoSource is a service-specific location of a photo. Bytes are never
// stored in Guardian; Canonical/BlobURL point at the real copy.
type PhotoSource struct {
	// Service is the connector ID: "google-photos", …
	Service string `json:"service"`
	// ServiceName is the human-readable label: "Google Photos".
	ServiceName string `json:"service_name,omitempty"`
	// ID is the service-local media item ID.
	ID string `json:"id,omitempty"`
	// URL is a deep link to the item on the service, when available.
	URL string `json:"url,omitempty"`
	// Canonical is true when this service holds the authoritative photo bytes.
	// This is the non-Guardian-canonical slice of issue #16: photo binaries
	// stay on the source service, not in Guardian's cache or disk.
	Canonical bool `json:"canonical,omitempty"`
	// BlobURL points at the retrievable image when bytes live outside Guardian.
	// Some services (Google Photos baseUrl) issue short-lived URLs.
	BlobURL string `json:"blob_url,omitempty"`
	// ThumbnailURL is a smaller preview, when available.
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// PhotoList is the neutral response for listing photos — either for a single
// service or aggregated across all connected photo-capable services.
type PhotoList struct {
	Photos []Photo `json:"photos"`
	// Total is the number of photos in this response.
	Total int `json:"total"`
	// Synced is true when the library merged photos that shared an identity
	// key (content hash, or taken_at + dimensions) into multi-source entries.
	Synced bool `json:"synced,omitempty"`
	// Truncated is true when any service hit its page cap with more items
	// still upstream (or the Google Photos pick itself is only a selection).
	Truncated bool `json:"truncated,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// CalendarEvent is a user-facing calendar entry (meeting, appointment,
// all-day block). Service identity lives in Sources. Guardian does not own
// calendar content — the authoritative copy stays on a non-Guardian service
// marked Canonical (issue #16).
// https://github.com/fudomunro/aperture-guardian/issues/65
type CalendarEvent struct {
	// Title is the event summary/title.
	Title string `json:"title,omitempty"`
	// Description is the event body / notes, when present.
	Description string `json:"description,omitempty"`
	// StartAt is the start time in RFC3339 (date-only all-day events use
	// midnight UTC of that date).
	StartAt string `json:"start_at,omitempty"`
	// EndAt is the end time in RFC3339 when known.
	EndAt string `json:"end_at,omitempty"`
	// AllDay marks date-bounded events without a clock time.
	AllDay bool `json:"all_day,omitempty"`
	// Location is a free-form place, room, or meeting link when known.
	Location string `json:"location,omitempty"`
	// Status is the provider status when known (confirmed, tentative, cancelled).
	Status string `json:"status,omitempty"`
	// CalendarID is the service-local calendar this entry belongs to.
	CalendarID string `json:"calendar_id,omitempty"`
	// CalendarName is the human calendar title, when known.
	CalendarName string `json:"calendar_name,omitempty"`
	// Organizer is the event organizer as a Contact, when known.
	Organizer *Contact `json:"organizer,omitempty"`
	// Attendees are invitees as Contact values (same identity matching as
	// /v1/library/contacts and Photo.people[]).
	Attendees []Contact `json:"attendees,omitempty"`
	// EntityID is a durable cross-service id when a provider supplies one
	// (Google Calendar iCalUID). Preferred over title+time matching.
	EntityID string `json:"entity_id,omitempty"`
	// Match records cross-service identity: "ical_uid" or "title_start".
	// Empty on single-source events.
	Match string `json:"match,omitempty"`
	// Sources lists service-local copies of this event.
	Sources []CalendarEventSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// CalendarEventSource is a service-specific location of a calendar event.
type CalendarEventSource struct {
	// Service is the connector ID: "google-calendar", …
	Service string `json:"service"`
	// ServiceName is the human-readable label: "Google Calendar".
	ServiceName string `json:"service_name,omitempty"`
	// ID is the service-local event ID.
	ID string `json:"id,omitempty"`
	// CalendarID is the service-local calendar id for this copy.
	CalendarID string `json:"calendar_id,omitempty"`
	// URL is a deep link to the event on the service, when available.
	URL string `json:"url,omitempty"`
	// Status is this service's status for the copy, when known.
	Status string `json:"status,omitempty"`
	// Canonical is true when this service is the authoritative location for
	// the event (issue #16). Set by connectors or config canonical_for.
	Canonical bool `json:"canonical,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// CalendarEventList is the neutral response for listing calendar events —
// either for a single service or aggregated across calendar-capable services.
type CalendarEventList struct {
	Events []CalendarEvent `json:"events"`
	// Total is the number of events in this response.
	Total int `json:"total"`
	// Synced is true when the library merged events that shared an identity
	// key (iCalUID, or title + start) into multi-source entries.
	Synced bool `json:"synced,omitempty"`
	// Truncated is true when any service hit its page cap with more items
	// still upstream (or left the time window incomplete).
	Truncated bool `json:"truncated,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// MessageChannel values for the neutral message type. Email is the first
// named-service path (Gmail); SMS/WhatsApp remain reserved for connectors
// that can authenticate a real user history (issue #60).
const (
	MessageChannelEmail    = "email"
	MessageChannelSMS      = "sms"
	MessageChannelWhatsApp = "whatsapp"
)

// Message is a user-facing message (email, and later SMS/WhatsApp when a
// named connector exists). Service identity lives in Sources. Guardian does
// not store message bodies as the home copy — the authoritative content stays
// on a non-Guardian service marked Canonical (issue #16). Library listings
// carry subject/snippet metadata; full body is only included when an agent
// fetches detail through the service proxy.
// https://github.com/fudomunro/aperture-guardian/issues/60
type Message struct {
	// Channel is the transport: "email", "sms", or "whatsapp".
	Channel string `json:"channel,omitempty"`
	// Subject is the email subject or chat topic, when present.
	Subject string `json:"subject,omitempty"`
	// Snippet is a short service-supplied preview of the body.
	Snippet string `json:"snippet,omitempty"`
	// Body is the full body text when a detail fetch includes it.
	// Library list responses usually leave this empty.
	Body string `json:"body,omitempty"`
	// From is the sender as a Contact, when known.
	From *Contact `json:"from,omitempty"`
	// To are primary recipients as Contact values.
	To []Contact `json:"to,omitempty"`
	// CC are carbon-copy recipients as Contact values.
	CC []Contact `json:"cc,omitempty"`
	// SentAt is the send time in RFC3339, when known.
	SentAt string `json:"sent_at,omitempty"`
	// ReceivedAt is the time the service recorded receipt (RFC3339).
	ReceivedAt string `json:"received_at,omitempty"`
	// Direction is "inbound", "outbound", or "unknown".
	Direction string `json:"direction,omitempty"`
	// Read is true when the message is not marked unread on the source.
	Read bool `json:"read,omitempty"`
	// ThreadID is the service-local thread/conversation id, when known.
	ThreadID string `json:"thread_id,omitempty"`
	// EntityID is a durable cross-service id when a provider supplies one
	// (RFC 5322 Message-ID for email). Preferred over subject matching.
	EntityID string `json:"entity_id,omitempty"`
	// Match records cross-service identity: "message_id" or "subject_from_time".
	// Empty on single-source messages.
	Match string `json:"match,omitempty"`
	// Labels are service-local labels/tags (INBOX, SENT, UNREAD, …).
	Labels []string `json:"labels,omitempty"`
	// Sources lists service-local copies of this message.
	Sources []MessageSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// MessageSource is a service-specific location of a message.
type MessageSource struct {
	// Service is the connector ID: "google-gmail", …
	Service string `json:"service"`
	// ServiceName is the human-readable label: "Gmail".
	ServiceName string `json:"service_name,omitempty"`
	// ID is the service-local message ID.
	ID string `json:"id,omitempty"`
	// ThreadID is the service-local thread id for this copy.
	ThreadID string `json:"thread_id,omitempty"`
	// URL is a deep link to the message on the service, when available.
	URL string `json:"url,omitempty"`
	// Mailbox is the service-local mailbox/folder when known (INBOX, …).
	Mailbox string `json:"mailbox,omitempty"`
	// Canonical is true when this service is the authoritative location for
	// the message (issue #16). Set by connectors or config canonical_for.
	Canonical bool `json:"canonical,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// MessageList is the neutral response for listing messages — either for a
// single service or aggregated across message-capable services.
type MessageList struct {
	Messages []Message `json:"messages"`
	// Total is the number of messages in this response.
	Total int `json:"total"`
	// Synced is true when the library merged messages that shared an identity
	// key (Message-ID, or subject+from+sent_at) into multi-source entries.
	Synced bool `json:"synced,omitempty"`
	// Truncated is true when any service hit its page/cap with more items
	// still upstream.
	Truncated bool `json:"truncated,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// LibraryServiceStatus reports per-service results for an aggregate call.
type LibraryServiceStatus struct {
	// Status is "ok", "unauthorized", "error", or "skipped".
	Status string `json:"status"`
	// Count is the number of items returned for this service when Status is "ok".
	Count int `json:"count,omitempty"`
	// Truncated is true when the connector hit its page cap with more items
	// still available upstream. Count then reflects only the fetched prefix.
	Truncated bool `json:"truncated,omitempty"`
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
	// Truncated is true when any service hit its page cap with more items
	// still upstream.
	Truncated bool `json:"truncated,omitempty"`
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
	// Truncated is true when any service hit its page cap with more items
	// still upstream. A heavy Spotify/YouTube library is the usual case.
	Truncated bool `json:"truncated,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// AlbumList is the neutral response for listing saved albums.
type AlbumList struct {
	Albums    []Album                         `json:"albums"`
	Total     int                             `json:"total"`
	Synced    bool                            `json:"synced,omitempty"`
	Truncated bool                            `json:"truncated,omitempty"`
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// ArtistList is the neutral response for listing followed/top artists.
type ArtistList struct {
	Artists   []Artist                        `json:"artists"`
	Total     int                             `json:"total"`
	Synced    bool                            `json:"synced,omitempty"`
	Truncated bool                            `json:"truncated,omitempty"`
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// Playlist list status values used in LibraryServiceStatus.Status.
const (
	LibraryStatusOK           = "ok"
	LibraryStatusUnauthorized = "unauthorized"
	LibraryStatusError        = "error"
	LibraryStatusSkipped      = "skipped"
)
