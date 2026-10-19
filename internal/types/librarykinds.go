package types

// BuiltinLibraryKinds is the registry for every unified library dataset
// (issue #183). Order is stable and used for route registration and
// pre-request injection priority.
//
// This is the single source for per-kind policy flags (AppReadable,
// CanonicalAllowed, AllowedLevels, DefaultNone). Derived lists — reserved
// UDT ids, app-token paths, canonical_for keys, access-level allowlists —
// are computed from this slice. Do not copy the kind list by hand.
//
// Runtime visibility predicates (HasProviders / Readable) live on the core
// registration wrapper (kindRuntime), not here (issue #15).

var BuiltinLibraryKinds = []LibraryKind{
	{
		ID:              "playlists",
		Path:            "/v1/library/playlists",
		PermType:        "playlists",
		Label:           "playlists",
		Usage:           "playlists or their music library",
		Summary:         "all user playlists from every connected service in one neutral list (name, description, owner, track_count, sources[])",
		SyncRule:        "Same-named playlists are synced into one entry with multiple sources[].",
		ExampleQuestion: "what playlists do I have?",
		Keywords: []string{
			"playlist", "play list", "music library", "my library", "saved playlists",
		},
		EmptyMsg:         "no playlist-capable services configured",
		DenyMsg:          "denied: data grant for agent access to playlists is not allowed",
		FailMsg:          "failed to fetch playlists",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    nil,
		DefaultNone:      false,
	},
	{
		ID:              "tracks",
		Path:            "/v1/library/tracks",
		PermType:        "tracks",
		Label:           "liked tracks",
		Usage:           "songs they like, saved tracks, or favorites",
		Summary:         "all liked/saved tracks from every connected music service in one neutral list (name, artists, album, duration, entity_id, sources[])",
		SyncRule:        "Tracks merge on durable entity_id (ISRC or MusicBrainz recording MBID) when present; otherwise same title + primary artist when durations agree (match=name).",
		ExampleQuestion: "what songs have I liked?",
		Keywords: []string{
			"liked song", "liked track", "liked music",
			"saved song", "saved track",
			"favorite song", "favourite song", "favorite track", "favourite track",
			"have i liked", "songs i like", "tracks i like",
			"songs i've liked", "tracks i've liked",
			"songs have i liked", "tracks have i liked",
			"my likes", "my favorites", "my favourites",
		},
		EmptyMsg:         "no liked-track-capable services configured",
		DenyMsg:          "denied: data grant for agent access to tracks is not allowed",
		FailMsg:          "failed to fetch liked tracks",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    nil,
		DefaultNone:      false,
	},
	{
		ID:              "albums",
		Path:            "/v1/library/albums",
		PermType:        "albums",
		Label:           "saved albums",
		Usage:           "albums they saved or their album library",
		Summary:         "all saved albums from every connected music service in one neutral list (name, artists, entity_id, sources[])",
		SyncRule:        "Albums merge on durable entity_id (UPC or MusicBrainz release-group MBID) when present; otherwise same title + primary artist (match=name).",
		ExampleQuestion: "what albums have I saved?",
		Keywords: []string{
			"saved album", "liked album", "favorite album", "favourite album",
			"albums i've saved", "albums have i saved", "albums i saved",
			"my albums", "album library",
		},
		EmptyMsg:         "no album-capable services configured",
		DenyMsg:          "denied: data grant for agent access to albums is not allowed",
		FailMsg:          "failed to fetch albums",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    nil,
		DefaultNone:      false,
	},
	{
		ID:              "artists",
		Path:            "/v1/library/artists",
		PermType:        "artists",
		Label:           "followed artists",
		Usage:           "artists they follow or favorite artists",
		Summary:         "all followed/top artists from every connected music service in one neutral list (name, entity_id, sources[])",
		SyncRule:        "Artists merge on durable entity_id (MusicBrainz MBID) when resolved (match=mbid); otherwise same normalized name (match=name).",
		ExampleQuestion: "what artists do I follow?",
		Keywords: []string{
			"followed artist", "artists i follow", "artists do i follow",
			"who do i follow", "do i follow", "artists i've followed",
			"favorite artist", "favourite artist", "my artists",
			"top artists", "artists i like",
		},
		EmptyMsg:         "no artist-capable services configured",
		DenyMsg:          "denied: data grant for agent access to artists is not allowed",
		FailMsg:          "failed to fetch artists",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    nil,
		DefaultNone:      false,
	},
	{
		ID:              "contacts",
		Path:            "/v1/library/contacts",
		PermType:        "contacts",
		Label:           "contacts",
		Usage:           "people, contacts, or their address book",
		Summary:         "all user contacts from every connected service in one neutral list (name, emails, phones, handles, organization, sources[])",
		SyncRule:        "Contacts that share an email, phone, handle, or name are synced into one entry with multiple sources[].",
		ExampleQuestion: "who is in my contacts?",
		Keywords: []string{
			"my contacts", "address book", "contact list", "contacts list",
			"who is in my contacts", "people i know",
		},
		EmptyMsg:         "no contact-capable services configured",
		DenyMsg:          "denied: data grant for agent access to contacts is not allowed",
		FailMsg:          "failed to fetch contacts",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    nil,
		DefaultNone:      false,
	},
	{
		ID:              "photos",
		Path:            "/v1/library/photos",
		PermType:        "photos",
		Label:           "photos",
		Usage:           "photos, pictures, or their photo library",
		Summary:         "all user photos from every connected photo service in one neutral list (taken_at, dimensions, mime_type, filename, people[] as Contact identities, sources[] with canonical blob location). Google Photos contributes the user's last picker selection.",
		SyncRule:        "Photos with the same content hash or the same taken_at plus dimensions are synced into one entry with multiple sources[]. people[] entries merge with the same contact identity matching used by /v1/library/contacts. Photo bytes are never stored in Aegis. Google Photos uses the Picker API: the user must pick photos once before they appear here.",
		ExampleQuestion: "what photos do I have?",
		Keywords: []string{
			"my photos", "photo library", "my pictures", "picture library",
			"photos from", "pictures from", "photos do i have",
			"photos i have", "list my photos", "list my pictures",
			"google photos", "camera roll",
			// Singular / collection phrasing missed before issue #125
			// ("oldest photo in my collection" matched nothing).
			"photo", "picture",
			"photo collection", "photos in my", "pictures in my",
		},
		EmptyMsg:         "no photo-capable services configured",
		DenyMsg:          "denied: data grant for agent access to photos is not allowed",
		FailMsg:          "failed to fetch photos",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    nil,
		DefaultNone:      false,
	},
	{
		ID:              "events",
		Path:            "/v1/library/events",
		PermType:        "events",
		Label:           "calendar events",
		Usage:           "calendar events, meetings, appointments, or their schedule",
		Summary:         "all calendar events from every connected calendar service in one neutral list (title, description, start_at, end_at, all_day, location, status, organizer, attendees as Contact identities, sources[])",
		SyncRule:        "Events merge on durable iCalUID when present (match=ical_uid); otherwise same title + start time + all-day flag (match=title_start). Event content stays on the source service.",
		ExampleQuestion: "what is on my calendar?",
		Keywords: []string{
			"my calendar", "my schedule", "on my calendar",
			"calendar events", "upcoming events", "upcoming meetings",
			"what meetings", "meetings do i have", "meetings i have",
			"appointments", "my events", "events this week",
			"google calendar", "what's on my calendar", "whats on my calendar",
		},
		EmptyMsg:         "no calendar-capable services configured",
		DenyMsg:          "denied: data grant for agent access to events is not allowed",
		FailMsg:          "failed to fetch calendar events",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    []string{AccessFreeBusy},
		DefaultNone:      false,
	},
	{
		ID:              "messages",
		Path:            "/v1/library/messages",
		PermType:        "messages",
		Label:           "messages",
		Usage:           "emails, messages, inbox, mail, or recent correspondence between people (not app notifications)",
		Summary:         "the user's mail collection from every message source in one neutral list (channel, subject, snippet, from/to/cc as Contact identities, sent_at, direction, read, entity_id, sources[]). Sources include live Gmail and the local archive (Gmail sync + Google Takeout). Messages are person-to-person text — not app/service notifications.",
		SyncRule:        "Gmail and Google Takeout are sources of one mail collection, synchronized on RFC Message-ID (match=message_id); otherwise same subject + from + sent_at (match=subject_from_time). Message bodies stay on the source service. Messages are person-to-person text (sent by or to the user) — app/service notifications are out of scope (future Notifications type). SMS/WhatsApp connectors are not implemented yet. Bluesky contributes the user's own posts and replies only.",
		ExampleQuestion: "what emails have I received recently?",
		Keywords: []string{
			"my email", "my emails", "my mail", "my inbox",
			"recent email", "recent emails", "recent messages",
			"emails have i", "emails i have", "emails i received",
			"check my email", "check my mail", "check my inbox",
			"messages in my inbox", "unread email", "unread emails",
			"google mail", "gmail inbox", "correspondence",
		},
		EmptyMsg:         "no message-capable services configured",
		DenyMsg:          "denied: data grant for agent access to messages is not allowed",
		FailMsg:          "failed to fetch messages",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    nil,
		DefaultNone:      false,
	},
	{
		ID:              "transactions",
		Path:            "/v1/library/transactions",
		PermType:        "transactions",
		Label:           "transactions",
		Usage:           "transactions, charges, spending, bank activity, budget line items, or money history",
		Summary:         "all financial transactions from every connected source in one neutral list (posted_at, authorized_at, amount as a signed decimal string, currency, description, merchant, category[] (multi-valued for YNAB splits), status, sources[]). Account fields never include full account numbers.",
		SyncRule:        "Transactions merge on durable provider_id (YNAB import_id, Quicken FITID) when present; otherwise same amount + posted day + normalized merchant (match=amount_date_merchant), with an adjacent-day (±1) pass for cross-provider duplicates. No Aegis category taxonomy — provider category only.",
		ExampleQuestion: "did we go over on groceries this month?",
		Keywords: []string{
			"transaction", "transactions", "my transactions",
			"spending", "my spending", "how much did i spend",
			"how much have i spent", "what did i spend",
			"charge", "charges", "card charge", "bank charge",
			"bank", "banking", "bank account", "bank activity",
			"card", "credit card", "debit card",
			"budget", "budgeting", "my budget",
			"money", "expenses", "expense", "spent",
			"groceries", "subscription", "subscriptions",
			"ynab", "quicken", "mint",
		},
		EmptyMsg:         "no transaction-capable services configured",
		DenyMsg:          "denied: data grant for agent access to transactions is not allowed",
		FailMsg:          "failed to fetch transactions",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    []string{AccessSummary},
		DefaultNone:      false,
	},
	{
		ID:              "places",
		Path:            "/v1/library/places",
		PermType:        "places",
		Label:           "places",
		Usage:           "where I was, location history, visits, trips, places I've been, travel chronology, time at a place, or residence/absence timelines",
		Summary:         "visits and trips from every connected source in one neutral list (start_at, end_at, place{name,address,locality,country,lat,lng}, kind=visit|trip, mode for trips, confidence, sources[]). Models visit windows — not raw GPS streams. Coordinates require the precise permission.",
		SyncRule:        "Visits merge on durable provider_id when present; otherwise same start/end window + normalized place (match=window_place). Photos can be joined to visits by taken_at inside the window.",
		ExampleQuestion: "where was I last summer?",
		Keywords: []string{
			"where was i", "where was i?", "where have i been",
			"where did i go", "where did we go", "where were we",
			"location history", "my locations", "places i've been",
			"places i have been", "visit", "visits", "trip", "trips",
			"travel history", "travel chronology", "my travel",
			"was i out of town", "out of town", "was i away",
			"when was i in", "when did i visit", "when did we visit",
			"how long was i in", "how long did we stay",
			"timeline", "google timeline", "maps timeline",
			"owntracks", "dawarich", "significant locations",
			"residence", "absence timeline", "when was i home",
			"which beach was that", "where was that photo taken",
		},
		EmptyMsg:         "no places-capable services configured (enable places permissions and connect OwnTracks or import a Google Timeline export)",
		DenyMsg:          "denied: data grant for agent access to places is not allowed (places default to none — enable city, read, or precise)",
		FailMsg:          "failed to fetch places",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    []string{AccessCity, AccessPrecise},
		DefaultNone:      true,
	},
	{
		ID:              "documents",
		Path:            "/v1/library/documents",
		PermType:        "documents",
		Label:           "documents",
		Usage:           "documents, receipts, warranties, leases, insurance policies, tax letters, medical letters, or paperwork",
		Summary:         "user documents from every connected source in one neutral list (title, mime_type, created_at, modified_at, correspondent, document_type, tags[], excerpt when read is allowed, people[] as Contact identities, sources[] with blob_url on the canonical source). Document bytes stay on the source service. Full text is only at GET /v1/library/documents/{service}/{id}/text.",
		SyncRule:        "Documents merge on durable entity_id (content checksum, then archive serial) when present; otherwise same title + created_at (match=title_created). metadata access returns titles/dates/tags only — no excerpt or text. Aegis does not store or OCR document files.",
		ExampleQuestion: "where is my lease?",
		Keywords: []string{
			"my documents", "document library", "paperwork",
			"receipt", "receipts", "warranty", "warranties",
			"lease", "leases", "rental agreement",
			"insurance policy", "insurance", "policy document",
			"tax letter", "tax document", "tax form",
			"medical letter", "medical document",
			"invoice", "invoices", "statement", "statements",
			"where is my", "find my document", "find my receipt",
			"find my warranty", "find my lease", "find my policy",
			"paperless",
		},
		EmptyMsg:         "no document-capable services configured",
		DenyMsg:          "denied: data grant for agent access to documents is not allowed",
		FailMsg:          "failed to fetch documents",
		AppReadable:      true,
		CanonicalAllowed: true,
		AllowedLevels:    []string{AccessMetadata},
		DefaultNone:      false,
	},
}

// BuiltinKindByID returns the registered built-in kind, or nil.
func BuiltinKindByID(id string) *LibraryKind {
	for i := range BuiltinLibraryKinds {
		if BuiltinLibraryKinds[i].ID == id {
			return &BuiltinLibraryKinds[i]
		}
	}
	return nil
}

// BuiltinKindIDs returns every built-in kind ID in registry order.
func BuiltinKindIDs() []string {
	out := make([]string, 0, len(BuiltinLibraryKinds))
	for _, k := range BuiltinLibraryKinds {
		out = append(out, k.ID)
	}
	return out
}

// BuiltinKindPaths returns every built-in agent path in registry order.
func BuiltinKindPaths() []string {
	out := make([]string, 0, len(BuiltinLibraryKinds))
	for _, k := range BuiltinLibraryKinds {
		out = append(out, k.Path)
	}
	return out
}

// AppReadableKindPaths returns list paths third-party app tokens may GET.
func AppReadableKindPaths() []string {
	out := make([]string, 0, len(BuiltinLibraryKinds))
	for _, k := range BuiltinLibraryKinds {
		if k.AppReadable {
			out = append(out, k.Path)
		}
	}
	return out
}

// CanonicalKindIDs returns kinds allowed in a service's canonical_for list.
func CanonicalKindIDs() []string {
	out := make([]string, 0, len(BuiltinLibraryKinds))
	for _, k := range BuiltinLibraryKinds {
		if k.CanonicalAllowed {
			out = append(out, k.ID)
		}
	}
	return out
}

// IsCanonicalKindID reports whether the kind may appear in canonical_for.
func IsCanonicalKindID(id string) bool {
	k := BuiltinKindByID(id)
	return k != nil && k.CanonicalAllowed
}

// KindAllowsLevel reports whether a built-in type honors the optional
// access level (freebusy, summary, city, precise, metadata).
func KindAllowsLevel(dataType, level string) bool {
	k := BuiltinKindByID(dataType)
	if k == nil {
		return false
	}
	for _, l := range k.AllowedLevels {
		if l == level {
			return true
		}
	}
	return false
}

// KindDefaultsNone reports whether a built-in type is opt-in (hidden until
// named). User-defined kinds are always opt-in via MarkOptInDataType.
func KindDefaultsNone(dataType string) bool {
	k := BuiltinKindByID(dataType)
	return k != nil && k.DefaultNone
}

// KindsAllowingLevel lists built-in kind IDs that honor level (for error
// messages and docs).
func KindsAllowingLevel(level string) []string {
	var out []string
	for _, k := range BuiltinLibraryKinds {
		for _, l := range k.AllowedLevels {
			if l == level {
				out = append(out, k.ID)
				break
			}
		}
	}
	return out
}
