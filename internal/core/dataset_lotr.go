package core

import (
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// Lord of the Rings demo dataset pack (issue #217).
//
// All correspondence is original invented text in a general epic-fantasy
// register — not quotes or translations of any copyrighted work. Email uses
// the reserved middle-earth.example domain. Timestamps use 2023–2024 so date
// windows and sort queries read naturally for agents.
//
// Two partitions demonstrate data-scope isolation with familiar names:
//   - shire  — Frodo's circle (hobbits, Bilbo, early journey)
//   - gondor — the wider war (Gandalf, Aragorn, the South-kingdom)

// LotR dataset ids registered as sample://library services.
const (
	LotRDatasetShire  = "shire"
	LotRDatasetGondor = "gondor"
)

// IsLotRDataset reports whether id names a LotR demo partition.
func IsLotRDataset(id string) bool {
	switch strings.TrimSpace(id) {
	case LotRDatasetShire, LotRDatasetGondor:
		return true
	}
	return false
}

// GenerateLotRDataset returns the library fixtures for one LotR partition.
// The second result is false for unknown ids (callers fall back to the
// generic sample set).
func GenerateLotRDataset(dataset string) (SampleLibrarySet, bool) {
	switch strings.TrimSpace(dataset) {
	case LotRDatasetShire:
		return lotRShireLibrary(), true
	case LotRDatasetGondor:
		return lotRGondorLibrary(), true
	}
	return SampleLibrarySet{}, false
}

// GenerateLotRMail builds the mail archive rows for one LotR partition.
// Row ids are namespaced so both partitions share one archive safely.
func GenerateLotRMail(dataset string) SampleMailSet {
	switch strings.TrimSpace(dataset) {
	case LotRDatasetShire:
		return lotRMail(LotRDatasetShire, lotRShireMailRows())
	case LotRDatasetGondor:
		return lotRMail(LotRDatasetGondor, lotRGondorMailRows())
	}
	return SampleMailSet{}
}

func lotRContact(service, id, name string, emails, phones []string) types.Contact {
	return types.NewContact(service, "Google Contacts", id, name, emails, phones)
}

func lotRMail(dataset string, rows []SampleMailRow) SampleMailSet {
	recs := make([]MailArchiveRecord, 0, len(rows))
	for _, row := range rows {
		rec := MailArchiveRecord{
			Service:     "google-gmail",
			ServiceName: "Gmail",
			ID:          dataset + ":" + row.ID,
			ThreadID:    row.ThreadID,
			MessageID:   row.MessageID,
			Mailbox:     row.Mailbox,
			Labels:      row.Labels,
			Subject:     row.Subject,
			Snippet:     row.Snippet,
			FromName:    row.FromName,
			FromEmail:   row.FromEmail,
			SentAt:      row.SentAt,
			ReceivedAt:  row.ReceivedAt,
			Direction:   row.Direction,
			Read:        row.Read,
			BodyText:    row.Body,
			Dataset:     dataset,
		}
		if row.ToName != "" || row.ToEmail != "" {
			rec.To = []types.Contact{types.NewContact("google-gmail", "Gmail", "", row.ToName, []string{row.ToEmail}, nil)}
		}
		recs = append(recs, rec)
	}
	set := SampleMailSet{Records: recs}
	if len(recs) > 0 {
		set.OldestID = recs[0].ID
		set.NewestID = recs[len(recs)-1].ID
	}
	return set
}

// SampleMailRow is one themed message before archive bookkeeping.
type SampleMailRow struct {
	ID, ThreadID, MessageID, Mailbox string
	Labels                           []string
	Subject, Snippet                 string
	FromName, FromEmail              string
	ToName, ToEmail                  string
	SentAt, ReceivedAt               string
	Direction                        string
	Read                             bool
	Body                             string
}

func lotRShireLibrary() SampleLibrarySet {
	const svc = "google-contacts"
	contacts := []types.Contact{
		lotRContact(svc, "c-frodo", "Frodo Baggins", []string{"frodo@middle-earth.example"}, []string{"+64110001"}),
		lotRContact(svc, "c-sam", "Samwise Gamgee", []string{"sam@middle-earth.example"}, []string{"+64110002"}),
		lotRContact(svc, "c-merry", "Meriadoc Brandybuck", []string{"merry@middle-earth.example"}, nil),
		lotRContact(svc, "c-pippin", "Peregrin Took", []string{"pippin@middle-earth.example"}, nil),
		lotRContact(svc, "c-bilbo", "Bilbo Baggins", []string{"bilbo@middle-earth.example"}, nil),
		lotRContact(svc, "c-gandalf", "Gandalf", []string{"gandalf@middle-earth.example"}, nil),
		lotRContact(svc, "c-rosie", "Rosie Cotton", []string{"rosie@middle-earth.example"}, nil),
		// Merge pair: Aragorn appears under a ranger alias with the same phone.
		lotRContact(svc, "c-strider", "Strider", nil, []string{"+64110099"}),
		lotRContact(svc, "c-aragorn", "Aragorn", []string{"aragorn@middle-earth.example"}, []string{"+64110099"}),
	}

	events := []types.CalendarEvent{
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-farewell",
			"Farewell party for Bilbo", "2023-04-22T18:00:00Z", "2023-04-22T23:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-departure",
			"Leave Bag End at first light", "2023-04-25T05:30:00Z", "2023-04-25T09:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-bree",
			"Meet at the Prancing Pony, Bree", "2023-04-29T20:00:00Z", "2023-04-29T22:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-rivendell",
			"Council of Elrond (hobbit seats)", "2023-05-08T09:00:00Z", "2023-05-08T17:00:00Z", true),
	}

	places := []types.PlaceVisit{
		{
			StartAt:    "2023-04-25T05:30:00Z",
			EndAt:      "2023-04-25T18:00:00Z",
			Kind:       types.PlaceKindTrip,
			Mode:       "walk",
			Confidence: "high",
			Place:      types.Place{Name: "Hobbiton to Bywater", Locality: "The Shire", Country: "Eriador"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-shire-leg1", Canonical: true}},
		},
		{
			StartAt:    "2023-04-29T12:00:00Z",
			EndAt:      "2023-04-30T10:00:00Z",
			Kind:       types.PlaceKindVisit,
			Confidence: "high",
			Place:      types.Place{Name: "Bree (Prancing Pony)", Locality: "Bree", Country: "Eriador"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-bree", Canonical: true}},
		},
		{
			StartAt:    "2023-05-07T16:00:00Z",
			EndAt:      "2023-05-12T12:00:00Z",
			Kind:       types.PlaceKindVisit,
			Confidence: "medium",
			Place:      types.Place{Name: "Rivendell", Locality: "Imladris", Country: "Eriador"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-rivendell", Canonical: true}},
		},
	}

	photos := []types.Photo{
		types.NewPhoto("google-photos", "Google Photos", "gp-bag-end", "bag-end-morning.jpg", "image/jpeg", "2023-04-25T05:45:00Z", 2048, 1365),
		types.NewPhoto("google-photos", "Google Photos", "gp-pony", "prancing-pony-sign.jpg", "image/jpeg", "2023-04-29T20:15:00Z", 1600, 1200),
		types.NewPhoto("google-photos", "Google Photos", "gp-rivendell", "rivendell-falls.jpg", "image/jpeg", "2023-05-08T11:00:00Z", 2400, 1600),
	}
	photos[0].Caption = "Bag End in the early light before departure"
	photos[1].Caption = "Sign of the Prancing Pony, Bree"
	photos[2].Caption = "The falls above Rivendell on council morning"

	tracks := []types.Track{
		{
			Name:    "The Road from the Door",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-walker", "The Walking Songsters")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-road", Canonical: true}},
		},
		{
			Name:    "Smoke Rings Over Bywater",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-green", "Green Dragon Players")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-smoke", Canonical: true}},
		},
		{
			Name:    "Harvest Dance in Hobbiton",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-green", "Green Dragon Players")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-harvest", Canonical: true}},
		},
	}
	playlists := []types.Playlist{
		{
			Name: "Walking Songs for the Road",
			Sources: []types.PlaylistSource{
				{Service: "spotify", ServiceName: "Spotify", ID: "pl-walking", Canonical: true},
			},
		},
	}
	albums := []types.Album{
		{Name: "Songs of the Shire", Sources: []types.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "al-shire"}}},
	}
	artists := []types.Artist{
		types.NewArtist("spotify", "Spotify", "ar-green", "Green Dragon Players"),
		types.NewArtist("spotify", "Spotify", "ar-walker", "The Walking Songsters"),
	}

	documents := []types.Document{
		{
			Title:         "Note on the inheritance (private)",
			MimeType:      "text/plain",
			CreatedAt:     "2023-04-10T12:00:00Z",
			Correspondent: "Bilbo Baggins",
			DocumentType:  "note",
			Tags:          []string{"private", "inheritance"},
			Excerpt:       "Keep this matter quiet. The road will be long, and I would not have the whole Shire talking.",
			Sources: []types.DocumentSource{
				{Service: "google-drive", ServiceName: "Google Drive", ID: "gd-inherit", Canonical: true},
			},
		},
		{
			Title:         "Packing list for the journey",
			MimeType:      "text/plain",
			CreatedAt:     "2023-04-24T21:00:00Z",
			Correspondent: "Samwise Gamgee",
			DocumentType:  "note",
			Tags:          []string{"packing"},
			Excerpt:       "Rope, salt, potatoes, and the grey cloak. Do not forget the second breakfast rations.",
			Sources: []types.DocumentSource{
				{Service: "google-drive", ServiceName: "Google Drive", ID: "gd-packing", Canonical: true},
			},
		},
	}

	messages := []types.Message{
		lotRLibraryMessage("msg-bilbo-farewell", "A word before I go",
			"Bilbo Baggins", "bilbo@middle-earth.example", "Frodo Baggins", "frodo@middle-earth.example",
			"2023-04-20T14:00:00Z", "The party will be loud, and then it will be quiet. Keep the house in order and do not let the Sackville-Bagginses in."),
		lotRLibraryMessage("msg-gandalf-urgent", "Leave as soon as you can",
			"Gandalf", "gandalf@middle-earth.example", "Frodo Baggins", "frodo@middle-earth.example",
			"2023-04-24T08:30:00Z", "The time has come. Take the east road at dawn and do not linger at the ferry."),
		lotRLibraryMessage("msg-sam-rope", "I have the rope and the pans",
			"Samwise Gamgee", "sam@middle-earth.example", "Frodo Baggins", "frodo@middle-earth.example",
			"2023-04-24T19:10:00Z", "Everything on the list is packed twice over. I will be at the gate before the sun."),
	}

	transactions := []types.Transaction{
		{
			PostedAt:    "2023-04-21T12:00:00Z",
			Amount:      "-12.50",
			Currency:    "COP",
			Description: "Green Dragon — farewell round",
			Merchant:    "Green Dragon Inn",
			Category:    []string{"dining"},
			Sources:     []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "ynab-green", Canonical: true}},
		},
		{
			PostedAt:    "2023-04-24T09:00:00Z",
			Amount:      "-48.00",
			Currency:    "COP",
			Description: "Stocks for the road (salt, flour, rope)",
			Merchant:    "Bywater Market",
			Category:    []string{"supplies"},
			Sources:     []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "ynab-market", Canonical: true}},
		},
	}

	return SampleLibrarySet{
		Playlists:    playlists,
		Tracks:       tracks,
		Albums:       albums,
		Artists:      artists,
		Contacts:     contacts,
		Photos:       photos,
		Events:       events,
		Messages:     messages,
		Transactions: transactions,
		Places:       places,
		Documents:    documents,
	}
}

func lotRLibraryMessage(id, subject, fromName, fromEmail, toName, toEmail, sentAt, body string) types.Message {
	return types.Message{
		Channel:   types.MessageChannelEmail,
		Subject:   subject,
		Snippet:   snippetOf(body),
		Body:      body,
		From:      &types.Contact{Name: fromName, Emails: []string{fromEmail}},
		To:        []types.Contact{{Name: toName, Emails: []string{toEmail}}},
		SentAt:    sentAt,
		Direction: "inbound",
		Read:      true,
		EntityID:  id + "@middle-earth.example",
		Sources: []types.MessageSource{{
			Service:     "google-gmail",
			ServiceName: "Gmail",
			ID:          id,
			Canonical:   true,
			Extra:       map[string]any{"message_id": id + "@middle-earth.example"},
		}},
	}
}

func lotRGondorLibrary() SampleLibrarySet {
	const svc = "google-contacts"
	contacts := []types.Contact{
		lotRContact(svc, "c-gandalf-w", "Gandalf the White", []string{"gandalf@middle-earth.example"}, nil),
		lotRContact(svc, "c-aragorn-k", "Aragorn Elessar", []string{"aragorn@middle-earth.example"}, []string{"+64110099"}),
		lotRContact(svc, "c-elrond", "Elrond", []string{"elrond@middle-earth.example"}, nil),
		lotRContact(svc, "c-galadriel", "Galadriel", []string{"galadriel@middle-earth.example"}, nil),
		lotRContact(svc, "c-boromir", "Boromir", []string{"boromir@middle-earth.example"}, nil),
		lotRContact(svc, "c-faramir", "Faramir", []string{"faramir@middle-earth.example"}, nil),
		lotRContact(svc, "c-eowyn", "Éowyn", []string{"eowyn@middle-earth.example"}, nil),
		lotRContact(svc, "c-theoden", "Théoden", []string{"theoden@middle-earth.example"}, nil),
	}

	events := []types.CalendarEvent{
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-council",
			"Council of Elrond", "2023-05-08T09:00:00Z", "2023-05-08T17:00:00Z", true),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-helms",
			"Battle of the Hornburg (Helm's Deep)", "2023-06-15T18:00:00Z", "2023-06-16T06:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-pelennor",
			"Battle of the Pelennor Fields", "2023-06-25T06:00:00Z", "2023-06-25T22:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-coronation",
			"Coronation of the King", "2023-07-01T12:00:00Z", "2023-07-01T16:00:00Z", false),
	}

	places := []types.PlaceVisit{
		{
			StartAt:    "2023-06-14T12:00:00Z",
			EndAt:      "2023-06-16T08:00:00Z",
			Kind:       types.PlaceKindVisit,
			Confidence: "high",
			Place:      types.Place{Name: "Helm's Deep", Locality: "Rohan", Country: "Rohan"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-helms", Canonical: true}},
		},
		{
			StartAt:    "2023-06-24T18:00:00Z",
			EndAt:      "2023-06-26T10:00:00Z",
			Kind:       types.PlaceKindVisit,
			Confidence: "high",
			Place:      types.Place{Name: "Minas Tirith", Locality: "Gondor", Country: "Gondor"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-minas", Canonical: true}},
		},
		{
			StartAt:    "2023-05-20T09:00:00Z",
			EndAt:      "2023-05-22T17:00:00Z",
			Kind:       types.PlaceKindTrip,
			Mode:       "ride",
			Confidence: "medium",
			Place:      types.Place{Name: "Isengard (after the fall)", Locality: "Nan Curunír", Country: "Rohan"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-isengard", Canonical: true}},
		},
	}

	photos := []types.Photo{
		types.NewPhoto("google-photos", "Google Photos", "gp-helms", "hornburg-dawn.jpg", "image/jpeg", "2023-06-16T05:40:00Z", 2400, 1600),
		types.NewPhoto("google-photos", "Google Photos", "gp-white-tree", "white-tree-in-bloom.jpg", "image/jpeg", "2023-07-01T13:00:00Z", 1800, 1200),
	}
	photos[0].Caption = "Dawn after the siege at the Hornburg"
	photos[1].Caption = "The White Tree in bloom at the coronation"

	tracks := []types.Track{
		{
			Name:    "Horns at Dawn",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-rohan", "Riders of Rohan Choir")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-horns", Canonical: true}},
		},
		{
			Name:    "City of White Stone",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-gondor", "Minas Tirith Ensemble")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-city", Canonical: true}},
		},
		{
			Name:    "Song of the Grey Company",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-grey", "Grey Company")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-grey", Canonical: true}},
		},
	}
	playlists := []types.Playlist{
		{
			Name: "Songs of War and Return",
			Sources: []types.PlaylistSource{
				{Service: "spotify", ServiceName: "Spotify", ID: "pl-war", Canonical: true},
			},
		},
	}
	albums := []types.Album{
		{Name: "Music of the South-kingdom", Sources: []types.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "al-south"}}},
	}
	artists := []types.Artist{
		types.NewArtist("spotify", "Spotify", "ar-rohan", "Riders of Rohan Choir"),
		types.NewArtist("spotify", "Spotify", "ar-gondor", "Minas Tirith Ensemble"),
		types.NewArtist("spotify", "Spotify", "ar-grey", "Grey Company"),
	}

	documents := []types.Document{
		{
			Title:         "Dispatch from the Hornburg (draft)",
			MimeType:      "text/plain",
			CreatedAt:     "2023-06-16T08:30:00Z",
			Correspondent: "Gandalf",
			DocumentType:  "dispatch",
			Tags:          []string{"war", "rohan"},
			Excerpt:       "The wall held at the deeping wall. Riders came at first light and the field is ours for now.",
			Sources: []types.DocumentSource{
				{Service: "google-drive", ServiceName: "Google Drive", ID: "gd-helms", Canonical: true},
			},
		},
		{
			Title:         "Terms of the coronation proclamation",
			MimeType:      "text/plain",
			CreatedAt:     "2023-07-01T12:30:00Z",
			Correspondent: "Aragorn",
			DocumentType:  "proclamation",
			Tags:          []string{"gondor", "crown"},
			Excerpt:       "The days of the stewardship are ended. Let the city be opened and the tree tended.",
			Sources: []types.DocumentSource{
				{Service: "google-drive", ServiceName: "Google Drive", ID: "gd-coronation", Canonical: true},
			},
		},
	}

	messages := []types.Message{
		lotRLibraryMessage("msg-elrond-summon", "You are summoned to the council",
			"Elrond", "elrond@middle-earth.example", "Gandalf", "gandalf@middle-earth.example",
			"2023-05-06T10:00:00Z", "Come at once. Matters that concern the whole North must be spoken of openly."),
		lotRLibraryMessage("msg-boromir-dream", "A dream that will not leave me",
			"Boromir", "boromir@middle-earth.example", "Aragorn", "aragorn@middle-earth.example",
			"2023-05-08T08:00:00Z", "I sought counsel on a dream of a broken sword and a city in fire. I will ride north if I must."),
		lotRLibraryMessage("msg-eowyn-stead", "The king rides out",
			"Éowyn", "eowyn@middle-earth.example", "Faramir", "faramir@middle-earth.example",
			"2023-06-14T07:00:00Z", "We hold the keep as long as the horn lasts. Send word if the field turns."),
	}

	transactions := []types.Transaction{
		{
			PostedAt:    "2023-06-10T12:00:00Z",
			Amount:      "-250.00",
			Currency:    "COP",
			Description: "Remounts and grain for the muster",
			Merchant:    "Edoras Stables",
			Category:    []string{"supplies"},
			Sources:     []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "ynab-muster", Canonical: true}},
		},
	}

	return SampleLibrarySet{
		Playlists:    playlists,
		Tracks:       tracks,
		Albums:       albums,
		Artists:      artists,
		Contacts:     contacts,
		Photos:       photos,
		Events:       events,
		Messages:     messages,
		Transactions: transactions,
		Places:       places,
		Documents:    documents,
	}
}

func lotRShireMailRows() []SampleMailRow {
	return []SampleMailRow{
		{
			ID: "lr-s-0001", ThreadID: "thr-s-bilbo", MessageID: "lr-s-0001@middle-earth.example",
			Subject: "The party will be on the twenty-second", Snippet: "I intend to make a speech and then vanish if I can manage it.",
			FromName: "Bilbo Baggins", FromEmail: "bilbo@middle-earth.example",
			ToName: "Frodo Baggins", ToEmail: "frodo@middle-earth.example",
			SentAt: "2023-04-12T10:00:00Z", ReceivedAt: "2023-04-12T10:00:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX"},
			Body: "Dear Frodo,\n\nThe party will be on the twenty-second of this month. I intend to make a speech and then vanish if I can manage it. Do keep the gaffers from the best beer until I am done talking.\n\nYours,\nBilbo",
		},
		{
			ID: "lr-s-0002", ThreadID: "thr-s-gandalf", MessageID: "lr-s-0002@middle-earth.example",
			Subject: "Leave as soon as you can", Snippet: "The time has come. Take the east road at dawn.",
			FromName: "Gandalf", FromEmail: "gandalf@middle-earth.example",
			ToName: "Frodo Baggins", ToEmail: "frodo@middle-earth.example",
			SentAt: "2023-04-24T08:30:00Z", ReceivedAt: "2023-04-24T08:30:01Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX", "IMPORTANT"},
			Body: "Frodo,\n\nThe time has come. Take the east road at dawn and do not linger at the ferry. I will catch you up if I can, but do not wait for me past Bree.\n\nGandalf",
		},
		{
			ID: "lr-s-0003", ThreadID: "thr-s-sam", MessageID: "lr-s-0003@middle-earth.example",
			Subject: "I have the rope and the pans", Snippet: "Everything on the list is packed twice over.",
			FromName: "Samwise Gamgee", FromEmail: "sam@middle-earth.example",
			ToName: "Frodo Baggins", ToEmail: "frodo@middle-earth.example",
			SentAt: "2023-04-24T19:10:00Z", ReceivedAt: "2023-04-24T19:10:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX"},
			Body: "Mr. Frodo,\n\nEverything on the list is packed twice over — rope, salt, potatoes, and the grey cloaks. I will be at the gate before the sun. We will see this through.\n\nSam",
		},
		{
			ID: "lr-s-0004", ThreadID: "thr-s-merry", MessageID: "lr-s-0004@middle-earth.example",
			Subject: "Meet us at the Ferry", Snippet: "Pippin and I will be waiting by the stock at sunset.",
			FromName: "Meriadoc Brandybuck", FromEmail: "merry@middle-earth.example",
			ToName: "Frodo Baggins", ToEmail: "frodo@middle-earth.example",
			SentAt: "2023-04-25T12:00:00Z", ReceivedAt: "2023-04-25T12:00:03Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX"},
			Body: "Frodo,\n\nPippin and I will be waiting by the stock at sunset. Bring the ponies. We know more than you think, and we are coming anyway.\n\nMerry",
		},
		{
			ID: "lr-s-0005", ThreadID: "thr-s-bree", MessageID: "lr-s-0005@middle-earth.example",
			Subject: "Room at the Prancing Pony", Snippet: "Ask for Barliman and say Gandalf sent you.",
			FromName: "Gandalf", FromEmail: "gandalf@middle-earth.example",
			ToName: "Frodo Baggins", ToEmail: "frodo@middle-earth.example",
			SentAt: "2023-04-28T16:00:00Z", ReceivedAt: "2023-04-28T16:00:01Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX", "TRAVEL"},
			Body: "If you reach Bree before me, ask for Barliman and say Gandalf sent you. Keep the name Underhill for the register. I will find you.\n\nG",
		},
		{
			ID: "lr-s-0006", ThreadID: "thr-s-rosie", MessageID: "lr-s-0006@middle-earth.example",
			Subject: "Come back safe", Snippet: "The garden will keep. You mind the road.",
			FromName: "Rosie Cotton", FromEmail: "rosie@middle-earth.example",
			ToName: "Samwise Gamgee", ToEmail: "sam@middle-earth.example",
			SentAt: "2023-04-25T06:00:00Z", ReceivedAt: "2023-04-25T06:00:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX"},
			Body: "Sam,\n\nThe garden will keep. You mind the road and mind Mr. Frodo. Come back safe.\n\nRosie",
		},
	}
}

func lotRGondorMailRows() []SampleMailRow {
	return []SampleMailRow{
		{
			ID: "lr-g-0001", ThreadID: "thr-g-council", MessageID: "lr-g-0001@middle-earth.example",
			Subject: "You are summoned to the council", Snippet: "Matters that concern the whole North must be spoken of openly.",
			FromName: "Elrond", FromEmail: "elrond@middle-earth.example",
			ToName: "Gandalf", ToEmail: "gandalf@middle-earth.example",
			SentAt: "2023-05-06T10:00:00Z", ReceivedAt: "2023-05-06T10:00:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX", "IMPORTANT"},
			Body: "Gandalf,\n\nCome at once. Matters that concern the whole North must be spoken of openly, and the heirs of Isildur will hear them.\n\nElrond",
		},
		{
			ID: "lr-g-0002", ThreadID: "thr-g-boromir", MessageID: "lr-g-0002@middle-earth.example",
			Subject: "A dream that will not leave me", Snippet: "I sought counsel on a dream of a broken sword.",
			FromName: "Boromir", FromEmail: "boromir@middle-earth.example",
			ToName: "Aragorn", ToEmail: "aragorn@middle-earth.example",
			SentAt: "2023-05-08T08:00:00Z", ReceivedAt: "2023-05-08T08:00:01Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX"},
			Body: "Aragorn,\n\nI sought counsel on a dream of a broken sword and a city in fire. I will ride north if I must. Gondor needs more than a ranger's word.\n\nBoromir",
		},
		{
			ID: "lr-g-0003", ThreadID: "thr-g-eowyn", MessageID: "lr-g-0003@middle-earth.example",
			Subject: "The king rides out", Snippet: "We hold the keep as long as the horn lasts.",
			FromName: "Éowyn", FromEmail: "eowyn@middle-earth.example",
			ToName: "Faramir", ToEmail: "faramir@middle-earth.example",
			SentAt: "2023-06-14T07:00:00Z", ReceivedAt: "2023-06-14T07:00:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX"},
			Body: "Faramir,\n\nThe king rides out to the Hornburg. We hold the keep as long as the horn lasts. Send word to the city if the field turns.\n\nÉowyn",
		},
		{
			ID: "lr-g-0004", ThreadID: "thr-g-gandalf-dawn", MessageID: "lr-g-0004@middle-earth.example",
			Subject: "Dawn after the siege", Snippet: "The wall held at the deeping wall.",
			FromName: "Gandalf", FromEmail: "gandalf@middle-earth.example",
			ToName: "Elrond", ToEmail: "elrond@middle-earth.example",
			SentAt: "2023-06-16T08:30:00Z", ReceivedAt: "2023-06-16T08:30:03Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX", "WAR"},
			Body: "Elrond,\n\nThe wall held at the deeping wall. Riders came at first light and the field is ours for now. I ride on — there is more to do before the summer ends.\n\nGandalf",
		},
		{
			ID: "lr-g-0005", ThreadID: "thr-g-coronation", MessageID: "lr-g-0005@middle-earth.example",
			Subject: "The city is opened", Snippet: "Let the tree be tended and the gates unbarred.",
			FromName: "Aragorn", FromEmail: "aragorn@middle-earth.example",
			ToName: "Galadriel", ToEmail: "galadriel@middle-earth.example",
			SentAt: "2023-07-01T13:00:00Z", ReceivedAt: "2023-07-01T13:00:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX"},
			Body: "Lady Galadriel,\n\nThe city is opened and the tree is in bloom. Let this be a sign that the long watch is ending. You and yours are welcome at the white tower.\n\nAragorn",
		},
	}
}
