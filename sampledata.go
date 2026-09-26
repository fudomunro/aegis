package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Synthetic sample fixtures for query testing (issue #77).
//
// Everything here is fictional: example.com addresses, invented song titles,
// and calendar entries that never happened. Generators are deterministic so
// tests can assert on known oldest/newest rows, merge keys, and filter hits
// without OAuth, provider quotas, or real personal mail in git.

// SampleMailSet is a complete mail fixture bundle. OldestID / NewestID pin the
// sort extremes so query tests can assert "oldest message" answers without
// scanning the whole table.
type SampleMailSet struct {
	Records  []MailArchiveRecord
	OldestID string
	NewestID string
}

// SampleLibrarySet holds neutral library fixtures covering every kind plus
// deliberate merge pairs (ISRC tracks, contact identity chains, Message-IDs).
type SampleLibrarySet struct {
	Playlists []Playlist      `json:"playlists"`
	Tracks    []Track         `json:"tracks"`
	Albums    []Album         `json:"albums"`
	Artists   []Artist        `json:"artists"`
	Contacts  []Contact       `json:"contacts"`
	Photos    []Photo         `json:"photos"`
	Events    []CalendarEvent `json:"events"`
	Messages  []Message       `json:"messages"`
}

// SampleMailCount is the default small-set size used by CI-shaped runs and the
// -seed-sample CLI (well under 1MB once stored).
const SampleMailCount = 24

// SampleMailStressCount is the optional local-perf set size (issue #75).
const SampleMailStressCount = 10000

// sampleTimeBase anchors generated timestamps so date-window queries have a
// stable clock. Years span 2019–2024 so after/before tests stay readable.
func sampleTimeBase() time.Time {
	return time.Date(2019, 1, 7, 9, 0, 0, 0, time.UTC)
}

// sampleMailTimestamp returns a deterministic send time for index i: one day
// apart, so sort=asc/desc and date windows are trivial to reason about.
func sampleMailTimestamp(i int) time.Time {
	return sampleTimeBase().AddDate(0, 0, i*7)
}

// GenerateSampleMail builds a deterministic mail set of n normal rows plus the
// edge-case bundle (unicode, HTML-only, huge body, missing sent_at, spam/trash).
// n is clamped to at least 1; stress callers pass SampleMailStressCount or more.
func GenerateSampleMail(n int) SampleMailSet {
	if n < 1 {
		n = 1
	}
	recs := make([]MailArchiveRecord, 0, n+len(sampleEdgeCaseMail()))
	subjects := []string{
		"Quarterly report draft",
		"Invoice due for widgets",
		"Lunch plans tomorrow",
		"Flight itinerary confirmation",
		"Code review request",
		"Standup notes",
		"Weekend hiking trip",
		"Budget spreadsheet attached",
		"Welcome to the neighborhood",
		"Library hold ready for pickup",
		"Project kickoff agenda",
		"Security alert for your account",
	}
	bodyBits := []string{
		"Please review the attached numbers before Friday.",
		"The invoice total is $128.40 and is due net-30.",
		"Tacos sound great — see you at noon.",
		"Your flight departs at 14:20 from gate B7.",
		"Could you look at the failing test in auth_test.go?",
		"Highlights: cache TTL shortened; OAuth refresh singleflight landed.",
	}
	froms := []struct{ name, email string }{
		{"Ada Lovelace", "ada@example.com"},
		{"Grace Hopper", "grace@example.com"},
		{"Alan Turing", "alan@example.com"},
		{"Katherine Johnson", "katherine@example.com"},
		{"Me", "me@example.com"},
	}

	for i := 0; i < n; i++ {
		sent := sampleMailTimestamp(i)
		from := froms[i%len(froms)]
		subject := subjects[i%len(subjects)]
		body := bodyBits[i%len(bodyBits)]
		labels := []string{"INBOX"}
		direction := "inbound"
		if i%5 == 4 {
			labels = []string{"SENT"}
			direction = "outbound"
			from = froms[len(froms)-1]
		}
		if i%7 == 3 {
			labels = append(labels, "UNREAD")
		}
		id := fmt.Sprintf("msg-%04d", i+1)
		payload := fmt.Sprintf(`{"id":%q,"subject":%q,"synthetic":true}`, id, subject)
		gz, _ := GzipPayload([]byte(payload))
		rec := MailArchiveRecord{
			Service:     googleGmailServiceID,
			ServiceName: googleGmailServiceName,
			ID:          id,
			ThreadID:    fmt.Sprintf("thr-%04d", (i/3)+1),
			MessageID:   fmt.Sprintf("<%s@synthetic.example>", id),
			Mailbox:     labels[0],
			Labels:      labels,
			Subject:     subject,
			Snippet:     snippetOf(body),
			FromName:    from.name,
			FromEmail:   from.email,
			To: []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Me", []string{"me@example.com"}, nil),
			},
			SentAt:     sent.UTC().Format(time.RFC3339),
			ReceivedAt: sent.UTC().Add(2 * time.Second).Format(time.RFC3339),
			Direction:  direction,
			Read:       !hasString(labels, "UNREAD"),
			BodyText:   body,
			PayloadGzip: gz,
		}
		if i%6 == 2 {
			rec.CC = []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Grace Hopper", []string{"grace@example.com"}, nil),
			}
		}
		recs = append(recs, rec)
	}

	edge := sampleEdgeCaseMail()
	recs = append(recs, edge...)
	return SampleMailSet{
		Records:  recs,
		OldestID: recs[0].ID,
		NewestID: lastSentID(recs),
	}
}

// sampleEdgeCaseMail covers the issue's edge-case table: missing sent_at,
// missing entity id, huge body, unicode/RTL subjects, HTML-only, spam/trash.
func sampleEdgeCaseMail() []MailArchiveRecord {
	huge := strings.Repeat("padding paragraph for the stress body. ", 800) // ~32KB
	mk := func(rec MailArchiveRecord) MailArchiveRecord {
		if rec.Service == "" {
			rec.Service = googleGmailServiceID
		}
		if rec.ServiceName == "" {
			rec.ServiceName = googleGmailServiceName
		}
		if rec.PayloadGzip == nil {
			gz, _ := GzipPayload([]byte(`{"synthetic":true,"edge":true}`))
			rec.PayloadGzip = gz
		}
		return rec
	}
	return []MailArchiveRecord{
		mk(MailArchiveRecord{
			ID:        "edge-unicode-rtl",
			ThreadID:  "thr-edge-1",
			MessageID: "<edge-unicode-rtl@synthetic.example>",
			Mailbox:   "INBOX",
			Labels:    []string{"INBOX", "UNREAD"},
			Subject:   "مرحبا بالعالم — שלום עולם — 你好，世界",
			Snippet:   "Unicode and RTL subject with mixed scripts.",
			FromName:  "Nadia Farouk",
			FromEmail: "nadia@example.com",
			To: []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Me", []string{"me@example.com"}, nil),
			},
			SentAt:     "2021-06-15T08:30:00Z",
			ReceivedAt: "2021-06-15T08:30:02Z",
			Direction:  "inbound",
			BodyText:   "Hello in several scripts: مرحبا שלום 你好",
		}),
		mk(MailArchiveRecord{
			ID:        "edge-html-only",
			ThreadID:  "thr-edge-2",
			MessageID: "<edge-html-only@synthetic.example>",
			Mailbox:   "INBOX",
			Labels:    []string{"INBOX"},
			Subject:   "HTML-only newsletter",
			Snippet:   "This message had no plain-text part.",
			FromName:  "Devs at Example",
			FromEmail: "news@example.com",
			To: []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Me", []string{"me@example.com"}, nil),
			},
			SentAt:     "2022-03-03T11:15:00Z",
			ReceivedAt: "2022-03-03T11:15:04Z",
			Direction:  "inbound",
			// HTML-only: body text may be empty or extracted tags; we keep a
			// short extracted text so FTS still has something to hit.
			BodyText: "special offer widgets",
		}),
		mk(MailArchiveRecord{
			ID:        "edge-huge-body",
			ThreadID:  "thr-edge-3",
			MessageID: "<edge-huge-body@synthetic.example>",
			Mailbox:   "INBOX",
			Labels:    []string{"INBOX"},
			Subject:   "Huge body archive dump",
			Snippet:   snippetOf(huge),
			FromName:  "Ada Lovelace",
			FromEmail: "ada@example.com",
			To: []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Me", []string{"me@example.com"}, nil),
			},
			SentAt:     "2022-08-20T16:00:00Z",
			ReceivedAt: "2022-08-20T16:00:10Z",
			Direction:  "inbound",
			BodyText:   huge,
		}),
		mk(MailArchiveRecord{
			ID:        "edge-missing-sent",
			ThreadID:  "thr-edge-4",
			MessageID: "<edge-missing-sent@synthetic.example>",
			Mailbox:   "INBOX",
			Labels:    []string{"INBOX"},
			Subject:   "Message with no Date header",
			Snippet:   "sent_at is empty on purpose.",
			FromName:  "Broken Header",
			FromEmail: "broken@example.com",
			To: []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Me", []string{"me@example.com"}, nil),
			},
			Direction: "unknown",
			BodyText:  "No send time recorded.",
		}),
		mk(MailArchiveRecord{
			ID:        "edge-missing-entity",
			ThreadID:  "thr-edge-5",
			Mailbox:   "INBOX",
			Labels:    []string{"INBOX"},
			Subject:   "No Message-ID header",
			Snippet:   "entity_id / message_id empty.",
			FromName:  "Broken Header",
			FromEmail: "broken@example.com",
			To: []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Me", []string{"me@example.com"}, nil),
			},
			SentAt:    "2023-01-12T09:00:00Z",
			Direction: "inbound",
			BodyText:  "Identity falls back to subject+from+time in library merge.",
		}),
		mk(MailArchiveRecord{
			ID:        "edge-spam",
			ThreadID:  "thr-edge-6",
			MessageID: "<edge-spam@synthetic.example>",
			Mailbox:   "SPAM",
			Labels:    []string{"SPAM"},
			Subject:   "You won a prize",
			Snippet:   "Classic spam shape for label filters.",
			FromName:  "Spammer",
			FromEmail: "spam@example.com",
			To: []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Me", []string{"me@example.com"}, nil),
			},
			SentAt:    "2023-04-01T02:02:02Z",
			Direction: "inbound",
			BodyText:  "click here to claim your prize widgets",
		}),
		mk(MailArchiveRecord{
			ID:        "edge-trash",
			ThreadID:  "thr-edge-7",
			MessageID: "<edge-trash@synthetic.example>",
			Mailbox:   "TRASH",
			Labels:    []string{"TRASH"},
			Subject:   "Deleted draft",
			Snippet:   "Trash label for filter coverage.",
			FromName:  "Me",
			FromEmail: "me@example.com",
			To: []Contact{
				newContact(googleGmailServiceID, googleGmailServiceName, "", "Ada Lovelace", []string{"ada@example.com"}, nil),
			},
			SentAt:    "2023-09-09T09:09:09Z",
			Direction: "outbound",
			BodyText:  "Please ignore this deleted note.",
		}),
	}
}

func snippetOf(body string) string {
	if len(body) <= 200 {
		return body
	}
	return body[:200]
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// lastSentID returns the record id with the lexicographically greatest non-empty
// sent_at (stable "newest" pin for sort=desc tests). Ties break on id.
func lastSentID(recs []MailArchiveRecord) string {
	best := ""
	bestSent := ""
	for _, r := range recs {
		if r.SentAt == "" {
			continue
		}
		if r.SentAt > bestSent || (r.SentAt == bestSent && r.ID > best) {
			bestSent = r.SentAt
			best = r.ID
		}
	}
	return best
}

// GenerateSampleLibrary builds neutral fixtures for every library kind, with
// multi-source merge pairs pre-seeded (ISRC tracks, contact identity chains,
// duplicate Message-IDs).
func GenerateSampleLibrary() SampleLibrarySet {
	// --- Tracks: shared ISRC across services, name-only pair, distinct ISRCs ---
	sharedISRC := "USSYN17000001"
	tracks := []Track{
		{
			Name:     "So What",
			Artists:  []Artist{newArtist("spotify", "Spotify", "", "Miles Davis")},
			EntityID: sharedISRC,
			Sources: []TrackSource{
				{Service: "spotify", ServiceName: "Spotify", ID: "sp-track-1", URL: "https://open.spotify.com/track/sp-track-1", Canonical: true},
			},
		},
		{
			Name:     "So What (Remastered)",
			Artists:  []Artist{newArtist("amazon-music", "Amazon Music", "", "Miles Davis")},
			EntityID: sharedISRC,
			Sources: []TrackSource{
				{Service: "amazon-music", ServiceName: "Amazon Music", ID: "am-track-1"},
			},
		},
		{
			Name:    "Take Five",
			Artists: []Artist{newArtist("spotify", "Spotify", "", "Dave Brubeck")},
			Sources: []TrackSource{
				{Service: "spotify", ServiceName: "Spotify", ID: "sp-track-2", Canonical: true},
			},
		},
		{
			Name:    "Take Five",
			Artists: []Artist{newArtist("youtube-music", "YouTube Music", "", "Dave Brubeck")},
			Sources: []TrackSource{
				{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-track-2"},
			},
		},
		{
			Name:     "Song A",
			Artists:  []Artist{newArtist("spotify", "Spotify", "", "Artist A")},
			EntityID: "USSYN00000001",
			Sources: []TrackSource{
				{Service: "spotify", ServiceName: "Spotify", ID: "sp-track-a", Canonical: true},
			},
		},
		{
			Name:     "Song A",
			Artists:  []Artist{newArtist("spotify", "Spotify", "", "Artist A")},
			EntityID: "USSYN00000002",
			Sources: []TrackSource{
				{Service: "spotify", ServiceName: "Spotify", ID: "sp-track-b", Canonical: true},
			},
		},
	}

	// --- Contacts: email → phone → handle chain via shared keys (union-find) ---
	// A (email+phone) ~ B (phone+handle) ~ C (handle). Name-only stays separate.
	contacts := []Contact{
		{
			Name:    "Ada Lovelace",
			Emails:  []string{"Ada@Example.com"},
			Phones:  []string{"+1 555 0100"},
			Sources: []ContactSource{{Service: "google-contacts", ServiceName: "Google Contacts", ID: "c-ada-1", Name: "Ada Lovelace", Emails: []string{"ada@example.com"}, Phones: []string{"+1 555 0100"}, Canonical: true}},
		},
		{
			Name:    "A. Lovelace",
			Phones:  []string{"+1 555 0100"},
			Handles: []string{"ada_codes"},
			Sources: []ContactSource{{Service: "immich", ServiceName: "Immich", ID: "c-ada-2", Name: "A. Lovelace", Phones: []string{"+15550100"}}},
		},
		{
			Name:    "Ada L.",
			Handles: []string{"@ada_codes"},
			Sources: []ContactSource{{Service: "spotify", ServiceName: "Spotify", ID: "c-ada-3", Name: "Ada L."}},
		},
		{
			Name:    "Grace Hopper",
			Emails:  []string{"grace@example.com"},
			Phones:  []string{"+1 555 0199"},
			Sources: []ContactSource{{Service: "google-contacts", ServiceName: "Google Contacts", ID: "c-grace-1", Name: "Grace Hopper", Emails: []string{"grace@example.com"}, Phones: []string{"+1 555 0199"}, Canonical: true}},
		},
		{
			Name:    "Grace H.",
			Emails:  []string{"grace.hopper@example.org"},
			Phones:  []string{"+1 555 0199"},
			Sources: []ContactSource{{Service: "immich", ServiceName: "Immich", ID: "c-grace-2", Name: "Grace H."}},
		},
		{
			Name:    "Unrelated Sam",
			Emails:  []string{"sam@example.com"},
			Sources: []ContactSource{{Service: "google-contacts", ServiceName: "Google Contacts", ID: "c-sam", Name: "Unrelated Sam", Canonical: true}},
		},
	}

	// --- Messages: duplicate Message-IDs and subject+from+time fallback ---
	sent := "2024-02-01T10:00:00Z"
	messages := []Message{
		{
			Channel:  MessageChannelEmail,
			Subject:  "Ship the release",
			Snippet:  "Release checklist is green.",
			SentAt:   sent,
			EntityID: "<ship-release@synthetic.example>",
			From:     &Contact{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}},
			Labels:   []string{"INBOX"},
			Sources: []MessageSource{
				{Service: googleGmailServiceID, ServiceName: googleGmailServiceName, ID: "gm-msg-1", Canonical: true, Extra: map[string]any{"message_id": "<ship-release@synthetic.example>"}},
			},
		},
		{
			Channel:  MessageChannelEmail,
			Subject:  "Ship the release",
			Snippet:  "Release checklist is green.",
			SentAt:   sent,
			EntityID: "<ship-release@synthetic.example>",
			From:     &Contact{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}},
			Labels:   []string{"INBOX", "TAKEOUT"},
			Sources: []MessageSource{
				{Service: takeoutServiceID, ServiceName: takeoutServiceName, ID: "msgid:ship-release@synthetic.example", Extra: map[string]any{"message_id": "<ship-release@synthetic.example>"}},
			},
		},
		{
			Channel: MessageChannelEmail,
			Subject: "Invoice reminder",
			Snippet: "Friendly reminder about the widgets invoice.",
			SentAt:  "2024-02-02T10:00:00Z",
			From:    &Contact{Name: "Billing", Emails: []string{"billing@example.com"}},
			Labels:  []string{"INBOX"},
			Sources: []MessageSource{
				{Service: googleGmailServiceID, ServiceName: googleGmailServiceName, ID: "gm-msg-2", Canonical: true},
			},
		},
		{
			Channel: MessageChannelEmail,
			Subject: "Invoice reminder",
			Snippet: "Friendly reminder about the widgets invoice.",
			SentAt:  "2024-02-02T10:00:00Z",
			From:    &Contact{Name: "Billing", Emails: []string{"billing@example.com"}},
			Labels:  []string{"INBOX", "TAKEOUT"},
			Sources: []MessageSource{
				{Service: takeoutServiceID, ServiceName: takeoutServiceName, ID: "blob:invoice-reminder"},
			},
		},
		{
			Channel: MessageChannelEmail,
			Subject: "Unrelated note",
			Snippet: "Nothing to merge here.",
			SentAt:  "2024-02-03T10:00:00Z",
			From:    &Contact{Name: "Me", Emails: []string{"me@example.com"}},
			Labels:  []string{"SENT"},
			Sources: []MessageSource{
				{Service: googleGmailServiceID, ServiceName: googleGmailServiceName, ID: "gm-msg-3", Canonical: true},
			},
		},
	}

	// --- Playlists: same name across services merges ---
	playlists := []Playlist{
		{
			Name:       "Jazz Vibes",
			TrackCount: 10,
			Sources:    []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-pl-1", Canonical: true}},
		},
		{
			Name:       "Jazz Vibes",
			TrackCount: 12,
			Sources:    []PlaylistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-pl-1"}},
		},
		{
			Name:       "Focus Mix",
			TrackCount: 25,
			Sources:    []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-pl-2", Canonical: true}},
		},
	}

	albums := []Album{
		{
			Name:     "Kind of Blue",
			EntityID: "USSYNALBUM0001",
			Sources:  []AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-al-1"}},
		},
		{
			Name:     "Kind of Blue",
			EntityID: "USSYNALBUM0001",
			Sources:  []AlbumSource{{Service: "amazon-music", ServiceName: "Amazon Music", ID: "am-al-1"}},
		},
		{
			Name:    "Time Out",
			Sources: []AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-al-2"}},
		},
	}

	artists := []Artist{
		{
			Name:     "Miles Davis",
			EntityID: "00000000-0000-0000-0000-000000000001",
			Sources:  []ArtistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-ar-1", Name: "Miles Davis"}},
		},
		{
			Name:     "Miles Davis",
			EntityID: "00000000-0000-0000-0000-000000000001",
			Sources:  []ArtistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-ar-1", Name: "Miles Davis - Topic"}},
		},
		{
			Name:    "Dave Brubeck",
			Sources: []ArtistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-ar-2", Name: "Dave Brubeck"}},
		},
	}

	photos := []Photo{
		{
			TakenAt:  "2023-07-04T18:30:00Z",
			Width:    4032,
			Height:   3024,
			MimeType: "image/jpeg",
			Filename: "IMG_0001.jpg",
			Caption:  "Fireworks over the harbor",
			People: []Contact{
				{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}},
			},
			Sources: []PhotoSource{
				{Service: "google-photos", ServiceName: "Google Photos", ID: "gp-1", Canonical: true, BlobURL: "https://photos.example.invalid/b/gp-1"},
			},
		},
		{
			TakenAt:  "2023-07-04T18:30:00Z",
			Width:    4032,
			Height:   3024,
			MimeType: "image/jpeg",
			Filename: "IMG_0001.jpg",
			Sources: []PhotoSource{
				{Service: "immich", ServiceName: "Immich", ID: "im-1", Canonical: true, BlobURL: "https://immich.example.invalid/api/assets/im-1"},
			},
		},
	}

	events := []CalendarEvent{
		{
			Title:   "Project kickoff",
			StartAt: "2024-03-11T15:00:00Z",
			EndAt:   "2024-03-11T16:00:00Z",
			Location: "Room 4B",
			Status:  "confirmed",
			EntityID: "kickoff-2024@synthetic.example",
			Organizer: &Contact{Name: "Grace Hopper", Emails: []string{"grace@example.com"}},
			Attendees: []Contact{
				{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}},
				{Name: "Me", Emails: []string{"me@example.com"}},
			},
			Sources: []CalendarEventSource{
				{Service: "google-calendar", ServiceName: "Google Calendar", ID: "ev-1", CalendarID: "primary", Canonical: true},
			},
		},
		{
			Title:    "All-hands",
			StartAt:  "2024-03-15T00:00:00Z",
			EndAt:    "2024-03-16T00:00:00Z",
			AllDay:   true,
			Status:   "confirmed",
			EntityID: "allhands-2024@synthetic.example",
			Sources: []CalendarEventSource{
				{Service: "google-calendar", ServiceName: "Google Calendar", ID: "ev-2", CalendarID: "primary", Canonical: true},
			},
		},
	}

	return SampleLibrarySet{
		Playlists: playlists,
		Tracks:    tracks,
		Albums:    albums,
		Artists:   artists,
		Contacts:  contacts,
		Photos:    photos,
		Events:    events,
		Messages:  messages,
	}
}

// SeedMailArchive upserts every fixture row into the archive. Rows are
// synthetic; callers own the archive path (prefer the OS data dir, issue #74).
func SeedMailArchive(a *MailArchive, recs []MailArchiveRecord) error {
	if a == nil {
		return fmt.Errorf("mail archive disabled")
	}
	for _, rec := range recs {
		if err := a.Upsert(rec); err != nil {
			return fmt.Errorf("seed %s/%s: %w", rec.Service, rec.ID, err)
		}
	}
	return nil
}

// WriteSampleMbox writes records as a Gmail-Takeout-shaped mbox so the real
// importer (mail_takeout.go) can load the same synthetic set.
func WriteSampleMbox(path string, recs []MailArchiveRecord) error {
	var b strings.Builder
	b.WriteString("# Aegis synthetic sample mailbox (issue #77). All messages are fictional.\n")
	for _, rec := range recs {
		// mbox "From " separator line; content hash path covers missing Message-ID.
		fmt.Fprintf(&b, "From MAILER-DAEMON %s\n", mboxFromLineDate(rec.SentAt))
		if rec.FromEmail != "" {
			if rec.FromName != "" {
				fmt.Fprintf(&b, "From: %s <%s>\n", rec.FromName, rec.FromEmail)
			} else {
				fmt.Fprintf(&b, "From: %s\n", rec.FromEmail)
			}
		}
		for _, to := range rec.To {
			for _, e := range to.Emails {
				fmt.Fprintf(&b, "To: %s\n", e)
			}
		}
		for _, cc := range rec.CC {
			for _, e := range cc.Emails {
				fmt.Fprintf(&b, "Cc: %s\n", e)
			}
		}
		fmt.Fprintf(&b, "Subject: %s\n", rec.Subject)
		if rec.MessageID != "" {
			fmt.Fprintf(&b, "Message-ID: %s\n", rec.MessageID)
		}
		if rec.SentAt != "" {
			if t, err := time.Parse(time.RFC3339, rec.SentAt); err == nil {
				fmt.Fprintf(&b, "Date: %s\n", t.Format(time.RFC1123Z))
			}
		}
		if len(rec.Labels) > 0 {
			quoted := make([]string, len(rec.Labels))
			for j, l := range rec.Labels {
				quoted[j] = `"` + l + `"`
			}
			fmt.Fprintf(&b, "X-GM-LABELS: %s\n", strings.Join(quoted, " "))
		}
		b.WriteString("Content-Type: text/plain; charset=utf-8\n\n")
		body := rec.BodyText
		if body == "" {
			body = rec.Snippet
		}
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func mboxFromLineDate(sentAt string) string {
	if t, err := time.Parse(time.RFC3339, sentAt); err == nil {
		return t.Format("Mon Jan  2 15:04:05 2006")
	}
	return "Mon Jan  2 15:04:05 2006"
}

// WriteSampleLibraryJSON dumps the neutral library fixtures next to the mail
// archive so local dev can inspect shapes or feed mock upstreams. The file is
// synthetic and safe to regenerate; do not commit personal data here.
func WriteSampleLibraryJSON(path string, lib SampleLibrarySet) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := marshalSampleLibrary(lib)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func marshalSampleLibrary(lib SampleLibrarySet) ([]byte, error) {
	return json.MarshalIndent(lib, "", "  ")
}

// WriteSampleFiles writes sample-mail.mbox and sample-library.json under dir
// and returns the mail set used. The caller seeds the live mail archive
// separately so WAL state stays on the configured path (issue #74).
func WriteSampleFiles(dir string, mailCount int) (mboxPath, libraryPath string, set SampleMailSet, err error) {
	if dir == "" {
		return "", "", SampleMailSet{}, fmt.Errorf("seed sample: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", SampleMailSet{}, err
	}
	mboxPath = filepath.Join(dir, "sample-mail.mbox")
	libraryPath = filepath.Join(dir, "sample-library.json")
	set = GenerateSampleMail(mailCount)
	if err := WriteSampleMbox(mboxPath, set.Records); err != nil {
		return "", "", SampleMailSet{}, err
	}
	if err := WriteSampleLibraryJSON(libraryPath, GenerateSampleLibrary()); err != nil {
		return "", "", SampleMailSet{}, err
	}
	return mboxPath, libraryPath, set, nil
}

// SeedSampleArchive opens (or creates) the archive at archivePath and upserts
// the generated mail set. Empty archivePath disables the archive and returns
// a clear error — sample mail must land somewhere queryable.
func SeedSampleArchive(archivePath string, mailCount int) (SampleMailSet, error) {
	archivePath = strings.TrimSpace(archivePath)
	if archivePath == "" {
		return SampleMailSet{}, fmt.Errorf("mail archive path is required to seed sample mail")
	}
	a, err := OpenMailArchive(archivePath)
	if err != nil {
		return SampleMailSet{}, err
	}
	if a == nil {
		return SampleMailSet{}, fmt.Errorf("mail archive disabled — set -mail-archive")
	}
	defer a.Close()
	set := GenerateSampleMail(mailCount)
	if err := SeedMailArchive(a, set.Records); err != nil {
		return SampleMailSet{}, err
	}
	return set, nil
}
