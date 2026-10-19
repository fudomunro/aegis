package types

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
	// Copy is durable copy-ledger metadata when Aegis knows this location
	// holds a tracked copy (issue #76). Nil when untracked.
	Copy *CopyInfo `json:"copy,omitempty"`
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
	// https://github.com/fudomunro/aegis/issues/4
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
	Canonical bool `json:"canonical,omitempty"`
	// Copy is durable copy-ledger metadata when Aegis knows this location
	// holds a tracked copy (issue #76). Nil when untracked.
	Copy  *CopyInfo      `json:"copy,omitempty"`
	Extra map[string]any `json:"extra,omitempty"`
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
// https://github.com/fudomunro/aegis/issues/12
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
	// Copy is durable copy-ledger metadata when Aegis knows this location
	// holds a tracked copy (issue #76). Nil when untracked.
	Copy *CopyInfo `json:"copy,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// ContactList is the neutral response for listing contacts — either for a
// single service or aggregated across all connected services.
type ContactList struct {
	Contacts []Contact `json:"contacts"`
	LibraryListMeta
}

// Photo is a user-facing photo (or video still). Service identity lives in
// Sources. Aegis never stores photo bytes — the authoritative copy stays on
// a non-Aegis service marked Canonical (issue #16). Ownership / provenance
// claims are intentionally deferred: https://github.com/fudomunro/aegis/issues/20
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
	// Canonical when the blob lives outside Aegis.
	Sources []PhotoSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// PhotoSource is a service-specific location of a photo. Bytes are never
// stored in Aegis; Canonical/BlobURL point at the real copy.
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
	// This is the non-Aegis-canonical slice of issue #16: photo binaries
	// stay on the source service, not in Aegis's cache or disk.
	Canonical bool `json:"canonical,omitempty"`
	// Copy is durable copy-ledger metadata when Aegis knows this location
	// holds a tracked copy (issue #76). Nil when untracked.
	Copy *CopyInfo `json:"copy,omitempty"`
	// BlobURL points at the retrievable image when bytes live outside Aegis.
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
	LibraryListMeta
}

// PhotoShareClaim is the PoC ownership/provenance claim produced when a
// photo is published to a share target (issue #20). It is intentionally
// lightweight: a content hash of the shared bytes plus a public post
// locator. It is not a cryptographic proof and does not yet carry a
// signature or trusted timestamp.
// https://github.com/fudomunro/aegis/issues/20
type PhotoShareClaim struct {
	// Type names the claim scheme. PoC value: "sha256+public-post".
	Type string `json:"type"`
	// SHA256 is the lowercase hex digest of the shared image bytes.
	SHA256 string `json:"sha256"`
	// MimeType of the shared bytes.
	MimeType string `json:"mime_type,omitempty"`
	// ByteSize of the shared payload.
	ByteSize int `json:"byte_size"`
	// SourceService / SourceID identify the Aegis photo being claimed.
	SourceService string `json:"source_service,omitempty"`
	SourceID      string `json:"source_id,omitempty"`
	// TargetService is where the photo was published (e.g. "bluesky").
	TargetService string `json:"target_service"`
	// PostURI / PostURL locate the public share.
	PostURI string `json:"post_uri,omitempty"`
	PostURL string `json:"post_url,omitempty"`
	// SharedAt is the publish timestamp in RFC3339.
	SharedAt string `json:"shared_at,omitempty"`
	// Note explains the PoC limits for agents and humans.
	Note string `json:"note,omitempty"`
}

// PhotoShareResponse is the agent-facing result of POST /v1/library/photos/share.
type PhotoShareResponse struct {
	// TargetService is the share destination connector ID.
	TargetService string `json:"target_service"`
	// SourceService / SourceID echo the shared photo identity.
	SourceService string `json:"source_service"`
	SourceID      string `json:"source_id"`
	// PostURI / PostURL / PostID locate the published share.
	PostURI string `json:"post_uri,omitempty"`
	PostURL string `json:"post_url,omitempty"`
	PostID  string `json:"post_id,omitempty"`
	// DryRun is true when the share was planned but not published.
	DryRun bool `json:"dry_run,omitempty"`
	// Claim is the ownership/provenance PoC (issue #20). Nil on dry-run.
	Claim *PhotoShareClaim `json:"claim,omitempty"`
}

// CalendarEvent is a user-facing calendar entry (meeting, appointment,
// all-day block). Service identity lives in Sources. Aegis does not own
// calendar content — the authoritative copy stays on a non-Aegis service
// marked Canonical (issue #16).
// https://github.com/fudomunro/aegis/issues/65
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
	// Copy is durable copy-ledger metadata when Aegis knows this location
	// holds a tracked copy (issue #76). Nil when untracked.
	Copy *CopyInfo `json:"copy,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// CalendarEventList is the neutral response for listing calendar events —
// either for a single service or aggregated across calendar-capable services.
type CalendarEventList struct {
	Events []CalendarEvent `json:"events"`
	LibraryListMeta
}

// FreeBusyBlock is one busy interval on the user's calendar (issue #128).
// It carries availability only — never titles, attendees, locations, or
// descriptions — so scheduling apps can find open slots without reading
// event content.
type FreeBusyBlock struct {
	// StartAt is the busy start in RFC3339 (date-only all-day events use
	// midnight UTC of that date).
	StartAt string `json:"start_at,omitempty"`
	// EndAt is the busy end in RFC3339 when known.
	EndAt string `json:"end_at,omitempty"`
	// AllDay marks date-bounded blocks without a clock time.
	AllDay bool `json:"all_day,omitempty"`
}

// FreeBusyList is the neutral free/busy response for scheduling-style
// consumers (issue #128). Blocks are availability only.
type FreeBusyList struct {
	Busy []FreeBusyBlock `json:"busy"`
	LibraryListMeta
}

// MessageChannel values for the neutral message type. Email is the first
// named-service path (Gmail); SMS/WhatsApp remain reserved for connectors
// that can authenticate a real user history (issue #60). Social covers
// person-to-person writing on social networks (the user's own posts/replies
// and replies addressed to them) — not app or service notifications.
const (
	MessageChannelEmail    = "email"
	MessageChannelSMS      = "sms"
	MessageChannelWhatsApp = "whatsapp"
	MessageChannelSocial   = "social"
	MessageChannelSignal   = "signal"
)

// Message is a user-facing message: a text communication between people,
// either written by the user or written to the user by another person.
// Typical sources are email (Gmail), SMS/WhatsApp (when connected), and
// social posts/replies the user authored or received as conversation.
//
// Messages are not app or service notifications (likes, shipping updates,
// marketing blasts, system alerts). Those are out of scope for this type;
// when a Notifications data type lands, operators can reclassify
// notification-shaped mail/social activity there instead.
//
// Service identity lives in Sources. Aegis does not store message bodies as
// the home copy — the authoritative content stays on a non-Aegis service
// marked Canonical (issue #16). Library listings carry subject/snippet
// metadata; full body is only included when an agent fetches detail through
// the service proxy.
// https://github.com/fudomunro/aegis/issues/60
type Message struct {
	// Channel is the transport: "email", "sms", "whatsapp", or "social".
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
	// Copy is durable copy-ledger metadata when Aegis knows this location
	// holds a tracked copy (issue #76). Nil when untracked.
	Copy *CopyInfo `json:"copy,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// MessageList is the neutral response for listing messages — either for a
// single service or aggregated across message-capable services.
type MessageList struct {
	Messages []Message `json:"messages"`
	LibraryListMeta
}

// Document is a user-facing document: a file the user keeps for life admin —
// receipt, warranty, lease, insurance policy, tax letter, medical letter.
// Service identity lives in Sources. Aegis does not store document bytes or
// run OCR — the authoritative copy stays on a non-Aegis service marked
// Canonical (issue #16). Library listings carry metadata and a short excerpt;
// full text is only returned by the explicit per-document text endpoint.
// https://github.com/fudomunro/aegis/issues/143
type Document struct {
	// Title is the document title or original filename.
	Title string `json:"title,omitempty"`
	// MimeType is the media type (e.g. "application/pdf").
	MimeType string `json:"mime_type,omitempty"`
	// CreatedAt is the document date in RFC3339, when known.
	CreatedAt string `json:"created_at,omitempty"`
	// ModifiedAt is the last-modified time in RFC3339, when known.
	ModifiedAt string `json:"modified_at,omitempty"`
	// Correspondent is the sender/issuer (vendor, agency, landlord).
	Correspondent string `json:"correspondent,omitempty"`
	// DocumentType is the provider's classification (receipt, invoice, …).
	DocumentType string `json:"document_type,omitempty"`
	// Tags are provider tags/labels (Paperless-ngx tags).
	Tags []string `json:"tags,omitempty"`
	// Excerpt is a short OCR/text preview. Stripped at metadata access;
	// full text is never included in list responses.
	Excerpt string `json:"excerpt,omitempty"`
	// People lists known people referenced by the document (Contact identity).
	People []Contact `json:"people,omitempty"`
	// EntityID is a durable cross-service id when a provider supplies one
	// (content checksum, then archive serial). Preferred over title matching.
	EntityID string `json:"entity_id,omitempty"`
	// Match records cross-service identity: "checksum", "archive_serial",
	// or "title_created". Empty on single-source documents.
	Match string `json:"match,omitempty"`
	// Sources lists service-local copies of this document.
	Sources []DocumentSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// DocumentSource is a service-specific location of a document. Bytes are
// never stored in Aegis; Canonical/BlobURL point at the real copy.
type DocumentSource struct {
	// Service is the connector ID: "paperless", …
	Service string `json:"service"`
	// ServiceName is the human-readable label: "Paperless-ngx".
	ServiceName string `json:"service_name,omitempty"`
	// ID is the service-local document ID.
	ID string `json:"id,omitempty"`
	// URL is a deep link to the document on the service, when available.
	URL string `json:"url,omitempty"`
	// Canonical is true when this service holds the authoritative document
	// bytes (issue #16). Set by connectors or config canonical_for.
	Canonical bool `json:"canonical,omitempty"`
	// Copy is durable copy-ledger metadata when Aegis knows this location
	// holds a tracked copy (issue #76). Nil when untracked.
	Copy *CopyInfo `json:"copy,omitempty"`
	// BlobURL points at the retrievable file when bytes live outside Aegis.
	BlobURL string `json:"blob_url,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// DocumentList is the neutral response for listing documents — either for a
// single service or aggregated across document-capable services.
type DocumentList struct {
	Documents []Document `json:"documents"`
	LibraryListMeta
}

// DocumentText is the full extracted text of one document (issue #143).
// Returned only by GET /v1/library/documents/{service}/{id}/text, gated
// separately so metadata access never implies reading content.
type DocumentText struct {
	// Service is the connector ID that holds the document.
	Service string `json:"service"`
	// ID is the service-local document ID.
	ID string `json:"id"`
	// Title is the document title, when known.
	Title string `json:"title,omitempty"`
	// Text is the full OCR/extracted text from the source service.
	Text string `json:"text"`
	// Truncated is true when the provider cut the text short.
	Truncated bool `json:"truncated,omitempty"`
}

// LibraryServiceStatus reports per-service results for an aggregate call.
// Extra Name / LastSyncedAt / Canonical / Role fields feed the library
// source-overview header (issue #162).
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
	// Name is the human-readable service label for the library source overview.
	Name string `json:"name,omitempty"`
	// LastSyncedAt is when Aegis last successfully ingested or fetched this
	// source (RFC3339). Empty when unknown or the source is live-only.
	LastSyncedAt string `json:"last_synced_at,omitempty"`
	// Canonical is true when this service is the configured home for the kind
	// (issue #16).
	Canonical bool `json:"canonical,omitempty"`
	// Role labels the copy relationship for mirrored kinds: "canonical",
	// "mirror", or empty when the kind is not mirrored.
	Role string `json:"role,omitempty"`
	// Parent, when set, nests this source under another source ID in the
	// overview (e.g. Google Takeout under Gmail for messages).
	Parent string `json:"parent,omitempty"`
}

// PlaylistList is the neutral response for listing playlists — either for a
// single service or aggregated across all connected services.
type PlaylistList struct {
	Playlists []Playlist `json:"playlists"`
	LibraryListMeta
}

// TrackList is the neutral response for listing liked/saved tracks — either
// for a single service or aggregated across all connected services.
type TrackList struct {
	Tracks []Track `json:"tracks"`
	LibraryListMeta
}

// AlbumList is the neutral response for listing saved albums.
type AlbumList struct {
	Albums []Album `json:"albums"`
	LibraryListMeta
}

// ArtistList is the neutral response for listing followed/top artists.
type ArtistList struct {
	Artists []Artist `json:"artists"`
	LibraryListMeta
}

// Playlist list status values used in LibraryServiceStatus.Status.
const (
	LibraryStatusOK           = "ok"
	LibraryStatusUnauthorized = "unauthorized"
	LibraryStatusError        = "error"
	LibraryStatusSkipped      = "skipped"
)

// TransactionAccount is a neutral account reference. Never include full
// account or card numbers — only display name, coarse type, and last 4
// digits when a provider supplies them (issue #141).
type TransactionAccount struct {
	Name  string `json:"name,omitempty"`
	Type  string `json:"type,omitempty"`
	Last4 string `json:"last4,omitempty"`
}

// Transaction is a user-facing financial transaction (issue #141).
// Amount is a signed decimal string (negative = outflow) so agents never do
// float money math on JSON numbers. Service identity lives in Sources.
// Aegis does not store statements or payment instruments — only line-item
// metadata needed for agent reasoning.
type Transaction struct {
	// PostedAt is the settled date/time in RFC3339 (or date-only when the
	// provider only knows a calendar day).
	PostedAt string `json:"posted_at,omitempty"`
	// AuthorizedAt is the authorization date when known (pending charges).
	AuthorizedAt string `json:"authorized_at,omitempty"`
	// Amount is a signed decimal string, e.g. "-45.67". Negative is outflow.
	Amount string `json:"amount"`
	// Currency is the ISO 4217 code when known (USD, EUR, …).
	Currency string `json:"currency,omitempty"`
	// Description is the raw payee/description line from the provider.
	Description string `json:"description,omitempty"`
	// Merchant is a normalized merchant name when the provider supplies one.
	Merchant string `json:"merchant,omitempty"`
	// Category lists provider categories (YNAB category, Quicken L field).
	// Multi-valued so a YNAB split can name every category without splitting
	// the amount — the full line-item total stays on the transaction and
	// agents dig into YNAB for per-category amounts. No Aegis taxonomy.
	Category []string `json:"category,omitempty"`
	// Memo is a free-form note attached to the line item.
	Memo string `json:"memo,omitempty"`
	// Account is a coarse account reference (name/type/last4 only).
	Account *TransactionAccount `json:"account,omitempty"`
	// Status is "pending" or "posted".
	Status string `json:"status,omitempty"`
	// EntityID is a durable cross-service id when a provider supplies one
	// (YNAB import_id, Quicken FITID). Preferred over amount/date matching.
	EntityID string `json:"entity_id,omitempty"`
	// Match records cross-service identity: "provider_id" or "amount_date_merchant".
	// Empty on single-source items.
	Match string `json:"match,omitempty"`
	// Sources lists service-local copies of this transaction.
	Sources []TransactionSource `json:"sources,omitempty"`
	// Extra holds other service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// TransactionSource is a service-specific location of a transaction.
type TransactionSource struct {
	// Service is the connector ID: "ynab", "quicken", …
	Service string `json:"service"`
	// ServiceName is the human-readable label: "YNAB", "Quicken".
	ServiceName string `json:"service_name,omitempty"`
	// ID is the service-local transaction ID.
	ID string `json:"id,omitempty"`
	// URL is a deep link when available.
	URL string `json:"url,omitempty"`
	// AccountID is the service-local account id (not a card/account number).
	AccountID string `json:"account_id,omitempty"`
	// Canonical is true when this service is the authoritative location
	// (issue #16). Set by connectors or config canonical_for.
	Canonical bool `json:"canonical,omitempty"`
	// Copy is durable copy-ledger metadata when tracked (issue #76).
	Copy *CopyInfo `json:"copy,omitempty"`
	// Extra holds additional service-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// TransactionSummary is the reduced money view for AccessSummary consumers
// (issue #141): totals only, no line items, no account identifiers.
type TransactionSummary struct {
	// Count is the number of underlying line items rolled into the totals.
	Count int `json:"count,omitempty"`
	// Currency is the ISO code used for the money totals when uniform.
	Currency string `json:"currency,omitempty"`
	// TotalAmount is the signed net amount across filtered items.
	TotalAmount string `json:"total_amount,omitempty"`
	// ByCategory maps provider category → signed total amount.
	ByCategory map[string]string `json:"by_category,omitempty"`
	// ByMonth maps YYYY-MM → signed total amount.
	ByMonth map[string]string `json:"by_month,omitempty"`
}

// TransactionList is the neutral response for listing transactions — either
// for a single service or aggregated across transaction-capable services.
type TransactionList struct {
	Transactions []Transaction `json:"transactions"`
	// Summary is set when the request is summary-only (issue #141) or when
	// callers ask for rollups alongside line items. Built from the full
	// filtered set, not the current page.
	Summary *TransactionSummary `json:"summary,omitempty"`
	LibraryListMeta
}

// Place is the location a visit or trip refers to (issue #142). Name and
// address are provider-supplied; Locality/Country are the city/country level
// used by the city permission redaction. Lat/Lng are optional and omitted
// entirely at city and read access levels.
type Place struct {
	Name     string  `json:"name,omitempty"`
	Address  string  `json:"address,omitempty"`
	Locality string  `json:"locality,omitempty"`
	Country  string  `json:"country,omitempty"`
	Lat      float64 `json:"lat,omitempty"`
	Lng      float64 `json:"lng,omitempty"`
}

// PlaceVisit is a user-facing visit or trip window (issue #142). Aegis models
// visits and trips, not raw GPS streams — the import archive may hold points
// but the library surface exposes windows with a place.
type PlaceVisit struct {
	// StartAt / EndAt bound the visit or trip window (RFC3339).
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at,omitempty"`
	// Place is where the visit/trip occurred (or the trip's primary place).
	Place Place `json:"place"`
	// Kind is "visit" (dwell at a place) or "trip" (travel between places).
	Kind string `json:"kind"`
	// Mode is the travel mode for trips: flight, drive, walk, bicycle, train, bus.
	Mode string `json:"mode,omitempty"`
	// Confidence is the provider confidence when available (high/medium/low).
	Confidence string `json:"confidence,omitempty"`
	// EntityID is a durable catalog ID when a provider supplies one.
	EntityID string `json:"entity_id,omitempty"`
	// Match records how multi-source identity was established.
	Match   string             `json:"match,omitempty"`
	Sources []PlaceVisitSource `json:"sources,omitempty"`
	Extra   map[string]any     `json:"extra,omitempty"`
}

// PlaceVisitSource is a service-specific record of a visit or trip. Point
// IDs are raw provider identifiers (Timeline placeId, OwnTracks tst+tid, …)
// and are redacted at city access level.
type PlaceVisitSource struct {
	Service     string `json:"service"`
	ServiceName string `json:"service_name,omitempty"`
	ID          string `json:"id,omitempty"`
	URL         string `json:"url,omitempty"`
	// Canonical is true when this service is the configured home for places.
	Canonical bool      `json:"canonical,omitempty"`
	Copy      *CopyInfo `json:"copy,omitempty"`
	// PointIDs are raw provider point identifiers for the visit (issue #142).
	PointIDs []string       `json:"point_ids,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`
}

// PlaceVisitList is the neutral response for listing visits and trips.
type PlaceVisitList struct {
	Visits []PlaceVisit `json:"visits"`
	LibraryListMeta
}

// Place kind values.
const (
	PlaceKindVisit = "visit"
	PlaceKindTrip  = "trip"
)

// Trip mode values (normalized lowercase).
const (
	TripModeFlight  = "flight"
	TripModeDrive   = "drive"
	TripModeWalk    = "walk"
	TripModeBicycle = "bicycle"
	TripModeTrain   = "train"
	TripModeBus     = "bus"
	TripModeUnknown = "unknown"
)

// CopyInfo is the library-facing projection of a ledger row, stamped onto
// sources[] only when a matching copy exists (issue #76 / #90). The SQLite
// ledger itself stays in core.
type CopyInfo struct {
	Role       string `json:"role,omitempty"`
	SyncMode   string `json:"sync_mode,omitempty"`
	SyncStatus string `json:"sync_status,omitempty"`
	AsOf       string `json:"as_of,omitempty"`
	CheckedAt  string `json:"checked_at,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Note       string `json:"note,omitempty"`
}

// CanonicalService returns the first source marked canonical, else the first
// source service ID, else "".
func (p Playlist) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(p.Sources),
		func(i int) bool { return p.Sources[i].Canonical },
		func(i int) string { return p.Sources[i].Service },
	)
}

// CanonicalService returns the first source marked canonical, else primary.
func (t Track) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(t.Sources),
		func(i int) bool { return t.Sources[i].Canonical },
		func(i int) string { return t.Sources[i].Service },
	)
}

// CanonicalService returns the first source marked canonical, else primary.
func (c Contact) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(c.Sources),
		func(i int) bool { return c.Sources[i].Canonical },
		func(i int) string { return c.Sources[i].Service },
	)
}

// CanonicalService returns the first source marked canonical, else primary.
func (p Photo) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(p.Sources),
		func(i int) bool { return p.Sources[i].Canonical },
		func(i int) string { return p.Sources[i].Service },
	)
}

// CanonicalService returns the first source marked canonical, else primary.
func (m Message) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(m.Sources),
		func(i int) bool { return m.Sources[i].Canonical },
		func(i int) string { return m.Sources[i].Service },
	)
}

// CanonicalService returns the first source marked canonical, else primary.
func (e CalendarEvent) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(e.Sources),
		func(i int) bool { return e.Sources[i].Canonical },
		func(i int) string { return e.Sources[i].Service },
	)
}

func firstCanonicalOrPrimary(n int, isCanonical func(int) bool, serviceID func(int) string) string {
	if n == 0 {
		return ""
	}
	for i := 0; i < n; i++ {
		if isCanonical(i) {
			return serviceID(i)
		}
	}
	return serviceID(0)
}
