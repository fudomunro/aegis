package connectors

import "github.com/fudomunro/aegis/internal/types"

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
// claim support for a type the connector does not recognize at all. Field
// coverage is graded against types.MaturityContentFields.
//
// Add a data type row when a service offers a recognizable kind of thing
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
func ConnectionMaturityCatalog() []types.ConnectionMaturity {
	return []types.ConnectionMaturity{
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
		signalMaturity(),
		quickenMaturity(),
		timelineMaturity(),
	}
}

func signalMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   SignalServiceID,
		Transport: types.TransportArchive,
		DataTypes: []types.DataMaturity{
			{Name: "messages", Description: "Imported chat messages", Supported: true,
				Fields: types.FieldCoverageFor("messages", "channel", "subject", "snippet", "body", "from", "to",
					"sent_at", "received_at", "direction", "read", "thread_id", "labels")},
			{Name: "contacts", Description: "Imported contacts", Supported: true,
				Fields: types.FieldCoverageFor("contacts", "name", "emails", "phones", "handles", "organization")},
			{Name: "groups", Description: "Group threads and members", Supported: false},
			{Name: "attachments", Description: "Media and file attachments", Supported: false},
		},
	}
}

func quickenMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   QuickenServiceID,
		Transport: types.TransportArchive,
		DataTypes: []types.DataMaturity{
			{Name: "transactions", Description: "Imported QIF/QFX line items", Supported: true,
				Fields: types.FieldCoverageFor("transactions", "posted_at", "amount", "currency", "description",
					"merchant", "category", "memo", "account", "status", "entity_id")},
			{Name: "accounts", Description: "Account registers and balances", Supported: false},
			{Name: "investments", Description: "Holdings and investment activity", Supported: false},
		},
	}
}

func timelineMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   TimelineServiceID,
		Transport: types.TransportArchive,
		DataTypes: []types.DataMaturity{
			{Name: "places", Description: "Imported Timeline visits and trips", Supported: true,
				Fields: types.FieldCoverageFor("places", "start_at", "end_at", "place", "kind", "mode", "confidence", "entity_id")},
			{Name: "location_points", Description: "Raw Timeline location points", Supported: false},
		},
	}
}

func spotifyMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   SpotifyServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "user", Description: "Current user profile", Supported: true},
			{Name: "tracks", Description: "Saved and top tracks", Supported: true,
				Fields: types.FieldCoverageFor("tracks", "name", "artists", "album", "duration_ms", "entity_id")},
			{Name: "albums", Description: "Saved albums", Supported: true,
				Fields: types.FieldCoverageFor("albums", "name", "artists", "entity_id")},
			{Name: "artists", Description: "Followed and top artists", Supported: true,
				Fields: types.FieldCoverageFor("artists", "name")},
			{Name: "playlists", Description: "User playlists", Supported: true,
				Fields: types.FieldCoverageFor("playlists", "name", "description", "owner", "track_count")},
			{Name: "search", Description: "Catalog search", Supported: true},
			{Name: "library", Description: "Saved music collection", Supported: true},
			{Name: "recommendations", Description: "Personalized suggestions", Supported: true},
			{Name: "player", Description: "Playback state and devices", Supported: true},
			{Name: "shows", Description: "Podcasts and episodes", Supported: false},
			{Name: "audiobooks", Description: "Audiobook catalog and saved books", Supported: false},
		},
	}
}

func youtubeMusicMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   YouTubeMusicServiceID,
		Transport: types.TransportReverseEngineered,
		DataTypes: []types.DataMaturity{
			{Name: "user", Description: "Current user profile", Supported: true},
			{Name: "tracks", Description: "Liked songs and videos", Supported: true,
				Fields: types.FieldCoverageFor("tracks", "name", "artists", "duration_ms", "duration")},
			{Name: "artists", Description: "Subscribed channels as artists", Supported: true,
				Fields: types.FieldCoverageFor("artists", "name")},
			{Name: "playlists", Description: "User playlists", Supported: true,
				Fields: types.FieldCoverageFor("playlists", "name", "description", "owner", "track_count")},
			{Name: "albums", Description: "Albums in the catalog", Supported: false},
			{Name: "search", Description: "Catalog search", Supported: true},
			{Name: "library", Description: "Saved music collection", Supported: true},
			{Name: "channels", Description: "Channel subscriptions", Supported: true},
			{Name: "podcasts", Description: "Podcast shows and episodes", Supported: false},
		},
	}
}

func amazonMusicMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   AmazonMusicServiceID,
		Transport: types.TransportReverseEngineered,
		DataTypes: []types.DataMaturity{
			{Name: "user", Description: "Current user profile", Supported: true},
			{Name: "tracks", Description: "Songs, including playlist items", Supported: true,
				Fields: types.FieldCoverageFor("tracks", "name", "artists", "album", "duration_ms", "duration", "entity_id")},
			{Name: "albums", Description: "Albums", Supported: true,
				Fields: types.FieldCoverageFor("albums", "name")},
			{Name: "artists", Description: "Artists", Supported: true,
				Fields: types.FieldCoverageFor("artists", "name")},
			{Name: "playlists", Description: "User playlists", Supported: true,
				Fields: types.FieldCoverageFor("playlists", "name", "description", "owner", "track_count")},
			{Name: "search", Description: "Catalog search", Supported: true},
			{Name: "library", Description: "Saved music collection", Supported: true},
			{Name: "recommendations", Description: "Personalized suggestions", Supported: true},
			{Name: "shows", Description: "Podcasts and stations", Supported: false},
		},
	}
}

func googleGmailMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   GoogleGmailServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "messages", Description: "Mail messages and threads", Supported: true,
				Fields: types.FieldCoverageFor("messages", "channel", "subject", "snippet", "body", "from", "to", "cc",
					"sent_at", "received_at", "direction", "read", "thread_id", "entity_id", "labels")},
			{Name: "documents", Description: "Attachments surfaced as documents", Supported: true,
				Fields: types.FieldCoverageFor("documents", "title", "mime_type", "created_at", "correspondent", "document_type", "excerpt", "people", "entity_id")},
			{Name: "drafts", Description: "Unsent draft messages", Supported: false},
		},
	}
}

func googlePhotosMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   GooglePhotosServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "photos", Description: "Picked library media items", Supported: true,
				Fields: types.FieldCoverageFor("photos", "taken_at", "width", "height", "mime_type", "filename")},
			{Name: "albums", Description: "Photo albums", Supported: false},
			{Name: "people", Description: "Face groupings and people", Supported: false},
		},
	}
}

func googleContactsMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   GoogleContactsServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "contacts", Description: "People API contacts", Supported: true,
				Fields: types.FieldCoverageFor("contacts", "name", "emails", "phones", "organization")},
		},
	}
}

func googleCalendarMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   GoogleCalendarServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "events", Description: "Calendar entries", Supported: true,
				Fields: types.FieldCoverageFor("events", "title", "description", "start_at", "end_at", "all_day",
					"location", "status", "calendar_id", "calendar_name", "organizer", "attendees", "entity_id")},
			{Name: "freebusy", Description: "Free/busy availability blocks", Supported: false},
		},
	}
}

func googleDriveMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   GoogleDriveServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "documents", Description: "Drive files and exports", Supported: true,
				Fields: types.FieldCoverageFor("documents", "title", "mime_type", "created_at", "modified_at",
					"correspondent", "document_type", "excerpt", "people", "entity_id")},
		},
	}
}

func immichMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   ImmichServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "photos", Description: "Library assets", Supported: true,
				Fields: types.FieldCoverageFor("photos", "taken_at", "width", "height", "mime_type", "filename", "caption")},
			{Name: "albums", Description: "Photo albums and shared albums", Supported: false},
			{Name: "people", Description: "Recognized people and faces", Supported: false},
		},
	}
}

func blueskyMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   BlueskyServiceID,
		Transport: types.TransportProtocol,
		DataTypes: []types.DataMaturity{
			{Name: "messages", Description: "Notifications and chat messages", Supported: true,
				Fields: types.FieldCoverageFor("messages", "channel", "subject", "snippet", "body", "from",
					"sent_at", "received_at", "direction", "read", "thread_id", "entity_id", "labels")},
			{Name: "photos", Description: "Post images and photo sharing", Supported: true,
				Fields: types.FieldCoverageFor("photos", "taken_at", "width", "height", "mime_type", "filename", "caption")},
			{Name: "contacts", Description: "Profiles and follows", Supported: true,
				Fields: types.FieldCoverageFor("contacts", "name", "handles")},
			{Name: "posts", Description: "Feed posts and threads", Supported: false},
			{Name: "feeds", Description: "Custom feeds and timelines", Supported: false},
			{Name: "likes", Description: "Likes and reposts", Supported: false},
		},
	}
}

func ynabMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   YNABServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "transactions", Description: "Budget transactions", Supported: true,
				Fields: types.FieldCoverageFor("transactions", "posted_at", "amount", "currency", "description",
					"merchant", "category", "memo", "account", "status", "entity_id")},
			{Name: "accounts", Description: "Budget accounts and balances", Supported: false},
			{Name: "budgets", Description: "Budgets themselves", Supported: false},
			{Name: "categories", Description: "Category groups and targets", Supported: false},
		},
	}
}

func ownTracksMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   OwnTracksServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "places", Description: "Recorder visits and trips", Supported: true,
				Fields: types.FieldCoverageFor("places", "start_at", "end_at", "place", "kind", "mode", "confidence", "entity_id")},
			{Name: "location_points", Description: "Raw GPS samples", Supported: false},
		},
	}
}

func calDAVMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   CalDAVServiceID,
		Transport: types.TransportProtocol,
		DataTypes: []types.DataMaturity{
			{Name: "events", Description: "Calendar entries", Supported: true,
				Fields: types.FieldCoverageFor("events", "title", "description", "start_at", "end_at", "all_day",
					"location", "status", "calendar_id", "calendar_name", "organizer", "attendees", "entity_id")},
			{Name: "todos", Description: "VTODO tasks", Supported: false},
			{Name: "journals", Description: "VJOURNAL entries", Supported: false},
		},
	}
}

func cardDAVMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   CardDAVServiceID,
		Transport: types.TransportProtocol,
		DataTypes: []types.DataMaturity{
			{Name: "contacts", Description: "Address book contacts", Supported: true,
				Fields: types.FieldCoverageFor("contacts", "name", "emails", "phones", "handles", "organization")},
		},
	}
}

func paperlessMaturity() types.ConnectionMaturity {
	return types.ConnectionMaturity{
		Service:   PaperlessServiceID,
		Transport: types.TransportAPI,
		DataTypes: []types.DataMaturity{
			{Name: "documents", Description: "Archived documents", Supported: true,
				Fields: types.FieldCoverageFor("documents", "title", "mime_type", "created_at", "modified_at",
					"correspondent", "document_type", "tags", "excerpt", "people", "entity_id")},
		},
	}
}
