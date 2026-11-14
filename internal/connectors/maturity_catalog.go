package connectors

import "github.com/fudomunro/aegis/internal/shared"

// Connection maturity catalog (issue #247): the per-connection declaration
// of what each service offers in theory and what Aegis serves through the
// connection. The dashboard grades it into the maturity mark; nothing here
// is runtime data, so both the release-published build and the instance
// build read the same coverage statement.
//
// Entries are hand-declared next to the connectors they describe, the same
// class of metadata as Capabilities(). core's maturity_test.go holds them
// honest against the connectors themselves: every callable capability and
// every implemented library feed must be marked supported, and no entry may
// claim support for a kind the connector does not recognize at all. Field
// coverage is graded against MaturityContentFields.
//
// Add a kind row when a service offers a recognizable kind of thing
// Aegis does not serve — shows, audiobooks, photo albums, drafts, posts,
// budgets — because the unsupported gap is the point of the catalog. Do not
// add rows for concepts a supported type already carries as populated
// fields (Paperless tags and correspondents live on Document, so the
// documents row grades them). A connection whose only offered type is
// fully served and fully filled does read as "complete"; the per-type table
// on the page is what shows how narrow that is.

// ConnectionMaturityCatalog returns the maturity declaration for every
// connector-owned service, whether the transport is a live API or a local
// import store. Sample fixtures and the drift-injection harness are
// excluded: they are test surfaces, not connections to mature.
func ConnectionMaturityCatalog() []ConnectionMaturity {
	out := []ConnectionMaturity{
		spotifyMaturity(),
		youtubeMusicMaturity(),
		amazonMusicMaturity(),
		googleGmailMaturity(),
		googlePhotosMaturity(),
		googleContactsMaturity(),
		googleCalendarMaturity(),
		googleDriveMaturity(),
		immichMaturity(),
		blueskyMaturity(),
		ynabMaturity(),
		ownTracksMaturity(),
		calDAVMaturity(),
		cardDAVMaturity(),
		paperlessMaturity(),
		githubMaturity(),
	}
	// Local import stores declare maturity beside the store (issue #393).
	for _, spec := range LocalImportStoreSpecs() {
		out = append(out, spec.Maturity())
	}
	return out
}

func signalMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   SignalServiceID,
		Transport: TransportArchive,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionImport,
			Label: "Import Signal chats",
		},
		Kinds: []KindMaturity{
			{Name: "messages", Description: "Imported chat messages", Supported: true,
				Fields: FieldCoverageFor("messages", "channel", "subject", "snippet", "body", "from", "to",
					"sent_at", "received_at", "direction", "read", "thread_id", "labels")},
			{Name: "contacts", Description: "Imported contacts", Supported: true,
				Fields: FieldCoverageFor("contacts", "name", "emails", "phones", "handles", "organization")},
			{Name: "groups", Description: "Group threads and members", Supported: false},
			{Name: "attachments", Description: "Media and file attachments", Supported: false},
		},
	}
}

func quickenMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   QuickenServiceID,
		Transport: TransportArchive,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionImport,
			Label: "Import Quicken exports",
		},
		Kinds: []KindMaturity{
			{Name: "transactions", Description: "Imported QIF/QFX line items", Supported: true,
				Fields: FieldCoverageFor("transactions", "posted_at", "amount", "currency", "description",
					"merchant", "category", "memo", "account", "status", "entity_id")},
			{Name: "accounts", Description: "Account registers and balances", Supported: false},
			{Name: "investments", Description: "Holdings and investment activity", Supported: false},
		},
	}
}

func timelineMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   TimelineServiceID,
		Transport: TransportArchive,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionImport,
			Label: "Import Timeline exports",
		},
		Kinds: []KindMaturity{
			{Name: "places", Description: "Imported Timeline visits and trips", Supported: true,
				Fields: FieldCoverageFor("places", "start_at", "end_at", "place", "kind", "mode", "confidence", "entity_id")},
			{Name: "location_points", Description: "Raw Timeline location points", Supported: false},
		},
	}
}

func linkedinMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   LinkedInServiceID,
		Transport: TransportArchive,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionImport,
			Label: "Import LinkedIn connections",
		},
		Kinds: []KindMaturity{
			{Name: "contacts", Description: "Imported LinkedIn connections", Supported: true,
				Fields: FieldCoverageFor("contacts", "name", "emails", "phones", "handles", "organization")},
			{Name: "messaging", Description: "InMail and direct messages", Supported: false},
			{Name: "posts", Description: "Posts and feed activity", Supported: false},
			{Name: "jobs", Description: "Job applications and saved jobs", Supported: false},
			{Name: "recommendations", Description: "Given and received recommendations", Supported: false},
		},
	}
}

func spotifyMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   SpotifyServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Spotify",
		},
		Kinds: []KindMaturity{
			{Name: "user", Description: "Current user profile", Supported: true},
			{Name: "tracks", Description: "Saved and top tracks", Supported: true,
				Fields: FieldCoverageFor("tracks", "name", "artists", "album", "duration_ms", "entity_id")},
			{Name: "albums", Description: "Saved albums", Supported: true,
				Fields: FieldCoverageFor("albums", "name", "artists", "entity_id")},
			{Name: "artists", Description: "Followed and top artists", Supported: true,
				Fields: FieldCoverageFor("artists", "name")},
			{Name: "playlists", Description: "User playlists", Supported: true,
				Fields: FieldCoverageFor("playlists", "name", "description", "owner", "track_count")},
			{Name: "search", Description: "Catalog search", Supported: true},
			{Name: "library", Description: "Saved music collection", Supported: true},
			{Name: "recommendations", Description: "Personalized suggestions", Supported: true},
			{Name: "player", Description: "Playback state and devices", Supported: true},
			{Name: "shows", Description: "Podcasts and episodes", Supported: false},
			{Name: "audiobooks", Description: "Audiobook catalog and saved books", Supported: false},
		},
	}
}

func youtubeMusicMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   YouTubeMusicServiceID,
		Transport: TransportReverseEngineered,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect YouTube Music",
		},
		Kinds: []KindMaturity{
			{Name: "user", Description: "Current user profile", Supported: true},
			{Name: "tracks", Description: "Liked songs and videos", Supported: true,
				Fields: FieldCoverageFor("tracks", "name", "artists", "duration_ms", "duration")},
			{Name: "artists", Description: "Subscribed channels as artists", Supported: true,
				Fields: FieldCoverageFor("artists", "name")},
			{Name: "playlists", Description: "User playlists", Supported: true,
				Fields: FieldCoverageFor("playlists", "name", "description", "owner", "track_count")},
			{Name: "albums", Description: "Albums in the catalog", Supported: false},
			{Name: "search", Description: "Catalog search", Supported: true},
			{Name: "library", Description: "Saved music collection", Supported: true},
			{Name: "channels", Description: "Channel subscriptions", Supported: true},
			{Name: "podcasts", Description: "Podcast shows and episodes", Supported: false},
		},
	}
}

func amazonMusicMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   AmazonMusicServiceID,
		Transport: TransportReverseEngineered,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Amazon Music",
		},
		Kinds: []KindMaturity{
			{Name: "user", Description: "Current user profile", Supported: true},
			{Name: "tracks", Description: "Songs, including playlist items", Supported: true,
				Fields: FieldCoverageFor("tracks", "name", "artists", "album", "duration_ms", "duration", "entity_id")},
			{Name: "albums", Description: "Albums", Supported: true,
				Fields: FieldCoverageFor("albums", "name")},
			{Name: "artists", Description: "Artists", Supported: true,
				Fields: FieldCoverageFor("artists", "name")},
			{Name: "playlists", Description: "User playlists", Supported: true,
				Fields: FieldCoverageFor("playlists", "name", "description", "owner", "track_count")},
			{Name: "search", Description: "Catalog search", Supported: true},
			{Name: "library", Description: "Saved music collection", Supported: true},
			{Name: "recommendations", Description: "Personalized suggestions", Supported: true},
			{Name: "shows", Description: "Podcasts and stations", Supported: false},
		},
	}
}

func googleGmailMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   GoogleGmailServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Gmail",
		},
		Kinds: []KindMaturity{
			{Name: "messages", Description: "Mail messages and threads", Supported: true,
				Fields: FieldCoverageFor("messages", "channel", "subject", "snippet", "body", "from", "to", "cc",
					"sent_at", "received_at", "direction", "read", "thread_id", "entity_id", "labels")},
			{Name: "documents", Description: "Attachments surfaced as documents", Supported: true,
				Fields: FieldCoverageFor("documents", "title", "mime_type", "created_at", "correspondent", "document_type", "excerpt", "people", "entity_id")},
			{Name: "drafts", Description: "Unsent draft messages", Supported: false},
		},
	}
}

func googlePhotosMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   GooglePhotosServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Google Photos",
		},
		Kinds: []KindMaturity{
			{Name: "photos", Description: "Picked library media items", Supported: true,
				Fields: FieldCoverageFor("photos", "taken_at", "width", "height", "mime_type", "filename")},
			{Name: "albums", Description: "Photo albums", Supported: false},
			{Name: "people", Description: "Face groupings and people", Supported: false},
		},
	}
}

func googleContactsMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   GoogleContactsServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Google Contacts",
		},
		Kinds: []KindMaturity{
			{Name: "contacts", Description: "People API contacts", Supported: true,
				Fields: FieldCoverageFor("contacts", "name", "emails", "phones", "organization")},
		},
	}
}

func githubMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   GitHubServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect GitHub",
		},
		Kinds: []KindMaturity{
			{Name: "contacts", Description: "Organization members and followed users", Supported: true,
				Fields: FieldCoverageFor("contacts", "name", "emails", "handles", "organization")},
			{Name: "repositories", Description: "Source repositories and code", Supported: false},
			{Name: "issues", Description: "Issues and pull requests", Supported: false},
			{Name: "activity", Description: "Contribution and event feed", Supported: false},
		},
	}
}

func googleCalendarMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   GoogleCalendarServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Google Calendar",
		},
		Kinds: []KindMaturity{
			{Name: "events", Description: "Calendar entries", Supported: true,
				Fields: FieldCoverageFor("events", "title", "description", "start_at", "end_at", "all_day",
					"location", "status", "calendar_id", "calendar_name", "organizer", "attendees", "entity_id")},
			{Name: "freebusy", Description: "Free/busy availability blocks", Supported: false},
		},
	}
}

func googleDriveMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   GoogleDriveServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Google Drive",
		},
		Kinds: []KindMaturity{
			{Name: "documents", Description: "Drive files and exports", Supported: true,
				Fields: FieldCoverageFor("documents", "title", "mime_type", "created_at", "modified_at",
					"correspondent", "document_type", "excerpt", "people", "entity_id")},
		},
	}
}

func immichMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   ImmichServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionSetup,
			Label: "Set up Immich",
		},
		Kinds: []KindMaturity{
			{Name: "photos", Description: "Library assets", Supported: true,
				Fields: FieldCoverageFor("photos", "taken_at", "width", "height", "mime_type", "filename", "caption")},
			{Name: "albums", Description: "Photo albums and shared albums", Supported: false},
			{Name: "people", Description: "Recognized people and faces", Supported: false},
		},
	}
}

func blueskyMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   BlueskyServiceID,
		Transport: TransportProtocol,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Bluesky",
		},
		Kinds: []KindMaturity{
			{Name: "messages", Description: "Notifications and chat messages", Supported: true,
				Fields: FieldCoverageFor("messages", "channel", "subject", "snippet", "body", "from",
					"sent_at", "received_at", "direction", "read", "thread_id", "entity_id", "labels")},
			{Name: "photos", Description: "Post images and photo sharing", Supported: true,
				Fields: FieldCoverageFor("photos", "taken_at", "width", "height", "mime_type", "filename", "caption")},
			{Name: "contacts", Description: "Profiles and follows", Supported: true,
				Fields: FieldCoverageFor("contacts", "name", "handles")},
			{Name: "posts", Description: "Feed posts and threads", Supported: false},
			{Name: "feeds", Description: "Custom feeds and timelines", Supported: false},
			{Name: "likes", Description: "Likes and reposts", Supported: false},
		},
	}
}

func ynabMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   YNABServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionSetup,
			Label: "Set up YNAB",
		},
		Kinds: []KindMaturity{
			{Name: "transactions", Description: "Budget transactions", Supported: true,
				Fields: FieldCoverageFor("transactions", "posted_at", "amount", "currency", "description",
					"merchant", "category", "memo", "account", "status", "entity_id")},
			{Name: "accounts", Description: "Budget accounts and balances", Supported: false},
			{Name: "budgets", Description: "Budgets themselves", Supported: false},
			{Name: "categories", Description: "Category groups and targets", Supported: false},
		},
	}
}

func ownTracksMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   OwnTracksServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect OwnTracks",
		},
		Kinds: []KindMaturity{
			{Name: "places", Description: "Recorder visits and trips", Supported: true,
				Fields: FieldCoverageFor("places", "start_at", "end_at", "place", "kind", "mode", "confidence", "entity_id")},
			{Name: "location_points", Description: "Raw GPS samples", Supported: false},
		},
	}
}

func calDAVMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   CalDAVServiceID,
		Transport: TransportProtocol,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect CalDAV",
		},
		Kinds: []KindMaturity{
			{Name: "events", Description: "Calendar entries", Supported: true,
				Fields: FieldCoverageFor("events", "title", "description", "start_at", "end_at", "all_day",
					"location", "status", "calendar_id", "calendar_name", "organizer", "attendees", "entity_id")},
			{Name: "todos", Description: "VTODO tasks", Supported: false},
			{Name: "journals", Description: "VJOURNAL entries", Supported: false},
		},
	}
}

func cardDAVMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   CardDAVServiceID,
		Transport: TransportProtocol,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect CardDAV",
		},
		Kinds: []KindMaturity{
			{Name: "contacts", Description: "Address book contacts", Supported: true,
				Fields: FieldCoverageFor("contacts", "name", "emails", "phones", "handles", "organization")},
		},
	}
}

func paperlessMaturity() ConnectionMaturity {
	return ConnectionMaturity{
		Service:   PaperlessServiceID,
		Transport: TransportAPI,
		Start: shared.OwnershipAction{
			Kind:  shared.OwnershipActionConnect,
			Label: "Connect Paperless",
		},
		Kinds: []KindMaturity{
			{Name: "documents", Description: "Archived documents", Supported: true,
				Fields: FieldCoverageFor("documents", "title", "mime_type", "created_at", "modified_at",
					"correspondent", "document_type", "tags", "excerpt", "people", "entity_id")},
		},
	}
}

// MaturityByService indexes the catalog by connector ID.
func MaturityByService() map[string]ConnectionMaturity {
	cat := ConnectionMaturityCatalog()
	out := make(map[string]ConnectionMaturity, len(cat))
	for _, m := range cat {
		out[m.Service] = m
	}
	return out
}
