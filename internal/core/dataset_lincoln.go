package core

import (
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// Abraham Lincoln demo dataset pack (issue #217).
//
// Event dates and public facts follow the historical record (1861–1865).
// Correspondence is mostly original text in a mid-19th-century register;
// a few short fragments of well-known public-domain lines are used where
// they are the point of the query. Emails use the reserved
// lincoln.example domain.

// LincolnDataset is the data-scope partition id for this pack.
const LincolnDataset = "lincoln"

// GenerateLincolnDataset returns the library fixtures for the Lincoln pack.
func GenerateLincolnDataset(dataset string) (SampleLibrarySet, bool) {
	if strings.TrimSpace(dataset) != LincolnDataset {
		return SampleLibrarySet{}, false
	}
	return lincolnLibrary(), true
}

// GenerateLincolnMail builds the mail archive rows for the Lincoln pack.
func GenerateLincolnMail(dataset string) SampleMailSet {
	if strings.TrimSpace(dataset) != LincolnDataset {
		return SampleMailSet{}
	}
	return lotRMail(LincolnDataset, lincolnMailRows())
}

func lincolnContact(id, name string, emails, phones []string) types.Contact {
	return types.NewContact("google-contacts", "Google Contacts", id, name, emails, phones)
}

func lincolnLibrary() SampleLibrarySet {
	contacts := []types.Contact{
		lincolnContact("c-lincoln", "Abraham Lincoln", []string{"lincoln@lincoln.example"}, nil),
		lincolnContact("c-mary", "Mary Todd Lincoln", []string{"mary@lincoln.example"}, []string{"+12170001"}),
		lincolnContact("c-seward", "William H. Seward", []string{"seward@lincoln.example"}, []string{"+12170002"}),
		lincolnContact("c-stanton", "Edwin M. Stanton", []string{"stanton@lincoln.example"}, []string{"+12170003"}),
		lincolnContact("c-grant", "Ulysses S. Grant", []string{"grant@lincoln.example"}, []string{"+12170004"}),
		lincolnContact("c-sherman", "William T. Sherman", []string{"sherman@lincoln.example"}, nil),
		lincolnContact("c-douglass", "Frederick Douglass", []string{"douglass@lincoln.example"}, nil),
		lincolnContact("c-nicolay", "John G. Nicolay", []string{"nicolay@lincoln.example"}, nil),
		lincolnContact("c-hay", "John Hay", []string{"hay@lincoln.example"}, nil),
		// Merge pair: Grant also appears as "Captain Grant" with the same phone.
		lincolnContact("c-grant-capt", "Captain Grant", nil, []string{"+12170004"}),
	}

	events := []types.CalendarEvent{
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-inaug1",
			"First Inauguration", "1861-03-04T12:00:00Z", "1861-03-04T15:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-antietam",
			"Battle of Antietam (awaiting news)", "1862-09-17T06:00:00Z", "1862-09-17T22:00:00Z", true),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-emancipation",
			"Emancipation Proclamation takes effect", "1863-01-01T00:00:00Z", "1863-01-01T23:59:00Z", true),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-gettysburg",
			"Gettysburg dedication remarks", "1863-11-19T13:00:00Z", "1863-11-19T15:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-grant-chief",
			"Grant named general-in-chief", "1864-03-09T11:00:00Z", "1864-03-09T12:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-citypoint",
			"Visit to City Point (Grant’s headquarters)", "1865-03-24T12:00:00Z", "1865-03-28T12:00:00Z", true),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-inaug2",
			"Second Inauguration", "1865-03-04T12:00:00Z", "1865-03-04T14:30:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-appomattox",
			"News from Appomattox Court House", "1865-04-09T17:00:00Z", "1865-04-09T19:00:00Z", false),
		types.NewCalendarEvent("google-calendar", "Google Calendar", "ev-fords",
			"Ford’s Theatre (evening)", "1865-04-14T20:00:00Z", "1865-04-14T23:30:00Z", false),
	}

	places := []types.PlaceVisit{
		{
			StartAt:    "1861-02-11T14:00:00Z",
			EndAt:      "1861-02-23T18:00:00Z",
			Kind:       types.PlaceKindTrip,
			Mode:       "train",
			Confidence: "high",
			Place:      types.Place{Name: "Springfield to Washington (inaugural journey)", Locality: "Springfield", Country: "United States"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-inaug-train", Canonical: true}},
		},
		{
			StartAt:    "1863-11-18T18:00:00Z",
			EndAt:      "1863-11-20T12:00:00Z",
			Kind:       types.PlaceKindVisit,
			Confidence: "high",
			Place:      types.Place{Name: "Gettysburg, Pennsylvania", Locality: "Gettysburg", Country: "United States"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-gettysburg", Canonical: true}},
		},
		{
			StartAt:    "1865-03-24T12:00:00Z",
			EndAt:      "1865-03-28T12:00:00Z",
			Kind:       types.PlaceKindVisit,
			Confidence: "medium",
			Place:      types.Place{Name: "City Point, Virginia", Locality: "City Point", Country: "United States"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-citypoint", Canonical: true}},
		},
		{
			StartAt:    "1865-04-04T12:00:00Z",
			EndAt:      "1865-04-05T12:00:00Z",
			Kind:       types.PlaceKindVisit,
			Confidence: "medium",
			Place:      types.Place{Name: "Richmond, Virginia", Locality: "Richmond", Country: "United States"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-richmond", Canonical: true}},
		},
		{
			StartAt:    "1861-03-04T00:00:00Z",
			EndAt:      "1865-04-14T23:59:00Z",
			Kind:       types.PlaceKindVisit,
			Confidence: "high",
			Place:      types.Place{Name: "Executive Mansion, Washington", Address: "1600 Pennsylvania Avenue", Locality: "Washington", Country: "United States"},
			Sources:    []types.PlaceVisitSource{{Service: "google-timeline", ServiceName: "Google Timeline", ID: "tl-whitehouse", Canonical: true}},
		},
	}

	photos := []types.Photo{
		types.NewPhoto("google-photos", "Google Photos", "gp-brady-1863", "brady-portrait-1863.jpg", "image/jpeg", "1863-02-08T15:00:00Z", 2400, 3000),
		types.NewPhoto("google-photos", "Google Photos", "gp-gettysburg-crowd", "gettysburg-dedication.jpg", "image/jpeg", "1863-11-19T13:30:00Z", 2400, 1600),
		types.NewPhoto("google-photos", "Google Photos", "gp-citypoint", "city-point-headquarters.jpg", "image/jpeg", "1865-03-25T10:00:00Z", 2000, 1400),
		types.NewPhoto("google-photos", "Google Photos", "gp-richmond", "richmond-ruins-1865.jpg", "image/jpeg", "1865-04-04T14:00:00Z", 2200, 1500),
	}
	photos[0].Caption = "Studio portrait, Washington, 1863"
	photos[1].Caption = "Crowd at the Gettysburg dedication"
	photos[2].Caption = "Headquarters at City Point during the visit"
	photos[3].Caption = "Richmond after the evacuation"

	tracks := []types.Track{
		{
			Name:    "The Battle Cry of Freedom",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-union-glee", "Union Glee Club")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-battle-cry", Canonical: true}},
		},
		{
			Name:    "John Brown’s Body",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-union-glee", "Union Glee Club")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-john-brown", Canonical: true}},
		},
		{
			Name:    "Home, Sweet Home",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar-brass-band", "Fifth Regiment Brass Band")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-home-sweet", Canonical: true}},
		},
	}
	playlists := []types.Playlist{
		{
			Name: "Songs of the Union",
			Sources: []types.PlaylistSource{
				{Service: "spotify", ServiceName: "Spotify", ID: "pl-union", Canonical: true},
			},
		},
	}
	albums := []types.Album{
		{Name: "Campaign Airs, 1860–1864", Sources: []types.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "al-campaign"}}},
	}
	artists := []types.Artist{
		types.NewArtist("spotify", "Spotify", "ar-union-glee", "Union Glee Club"),
		types.NewArtist("spotify", "Spotify", "ar-brass-band", "Fifth Regiment Brass Band"),
	}

	documents := []types.Document{
		{
			Title:         "Memorandum on reconstruction (draft)",
			MimeType:      "text/plain",
			CreatedAt:     "1863-12-16T11:00:00Z",
			Correspondent: "Abraham Lincoln",
			DocumentType:  "memorandum",
			Tags:          []string{"reconstruction", "draft"},
			Excerpt:       "If the people of any State choose to return to the Union, let them take an oath of loyalty and set up a government — the rest can follow.",
			Sources: []types.DocumentSource{
				{Service: "google-drive", ServiceName: "Google Drive", ID: "gd-recon", Canonical: true},
			},
		},
		{
			Title:         "Gettysburg dedication — closing lines",
			MimeType:      "text/plain",
			CreatedAt:     "1863-11-19T14:00:00Z",
			Correspondent: "Abraham Lincoln",
			DocumentType:  "remarks",
			Tags:          []string{"gettysburg", "public"},
			Excerpt:       "…government of the people, by the people, for the people, shall not perish from the earth.",
			Sources: []types.DocumentSource{
				{Service: "google-drive", ServiceName: "Google Drive", ID: "gd-gettysburg", Canonical: true},
			},
		},
		{
			Title:         "Second Inaugural — closing lines",
			MimeType:      "text/plain",
			CreatedAt:     "1865-03-04T13:30:00Z",
			Correspondent: "Abraham Lincoln",
			DocumentType:  "remarks",
			Tags:          []string{"inaugural", "public"},
			Excerpt:       "With malice toward none, with charity for all… let us strive on to finish the work we are in.",
			Sources: []types.DocumentSource{
				{Service: "google-drive", ServiceName: "Google Drive", ID: "gd-inaug2", Canonical: true},
			},
		},
	}

	messages := []types.Message{
		lincolnLibraryMessage("msg-seward-1861", "Do not let the foreign powers drift",
			"William H. Seward", "seward@lincoln.example", "Abraham Lincoln", "lincoln@lincoln.example",
			"1861-03-29T10:00:00Z", "Sir — I urge a firm but quiet hand abroad. A single rash dispatch may cost us more than a battle."),
		lincolnLibraryMessage("msg-grant-1863", "You were right",
			"Abraham Lincoln", "lincoln@lincoln.example", "Ulysses S. Grant", "grant@lincoln.example",
			"1863-07-13T09:00:00Z", "I write only to say I was not anxious to push you before Vicksburg fell. You were right, and I was wrong."),
		lincolnLibraryMessage("msg-mary-1864", "Tad and I are well",
			"Mary Todd Lincoln", "mary@lincoln.example", "Abraham Lincoln", "lincoln@lincoln.example",
			"1864-06-12T20:00:00Z", "Dear Husband — Tad and I are well. Come home when the papers allow, and do not forget the strawberries."),
	}

	transactions := []types.Transaction{
		{
			PostedAt:    "1863-11-18T12:00:00Z",
			Amount:      "-2.50",
			Currency:    "USD",
			Description: "Rail ticket Washington to Gettysburg",
			Merchant:    "Northern Central Railway",
			Category:    []string{"travel"},
			Sources:     []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "ynab-rail-gb", Canonical: true}},
		},
		{
			PostedAt:    "1865-04-14T18:30:00Z",
			Amount:      "-1.00",
			Currency:    "USD",
			Description: "Theatre tickets — Ford’s",
			Merchant:    "Ford’s Theatre",
			Category:    []string{"entertainment"},
			Sources:     []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "ynab-fords", Canonical: true}},
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

func lincolnLibraryMessage(id, subject, fromName, fromEmail, toName, toEmail, sentAt, body string) types.Message {
	return lotRLibraryMessage(id, subject, fromName, fromEmail, toName, toEmail, sentAt, body)
}

func lincolnMailRows() []SampleMailRow {
	return []SampleMailRow{
		{
			ID: "lc-0001", ThreadID: "thr-lc-seward", MessageID: "lc-0001@lincoln.example",
			Subject: "Do not let the foreign powers drift", Snippet: "A single rash dispatch may cost us more than a battle.",
			FromName: "William H. Seward", FromEmail: "seward@lincoln.example",
			ToName: "Abraham Lincoln", ToEmail: "lincoln@lincoln.example",
			SentAt: "1861-03-29T10:00:00Z", ReceivedAt: "1861-03-29T10:00:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX", "STATE"},
			Body: "Sir — I urge a firm but quiet hand abroad. A single rash dispatch may cost us more than a battle. Europe watches to see whether we mean to hold together.\n\nW. H. Seward",
		},
		{
			ID: "lc-0002", ThreadID: "thr-lc-stanton", MessageID: "lc-0002@lincoln.example",
			Subject: "Telegraph office reports — Antietam", Snippet: "Dispatches are coming in slowly. I will bring the map when it is clearer.",
			FromName: "Edwin M. Stanton", FromEmail: "stanton@lincoln.example",
			ToName: "Abraham Lincoln", ToEmail: "lincoln@lincoln.example",
			SentAt: "1862-09-17T21:00:00Z", ReceivedAt: "1862-09-17T21:00:03Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX", "WAR"},
			Body: "Mr. President — Dispatches from the Antietam are coming in slowly. I will bring the map and the telegraph rolls as soon as they are legible. We may have held the line.\n\nE. M. Stanton",
		},
		{
			ID: "lc-0003", ThreadID: "thr-lc-emancipation", MessageID: "lc-0003@lincoln.example",
			Subject: "The Proclamation goes into effect today", Snippet: "I have put my name to it. Now the war must mean something more than reunion.",
			FromName: "Abraham Lincoln", FromEmail: "lincoln@lincoln.example",
			ToName: "Frederick Douglass", ToEmail: "douglass@lincoln.example",
			SentAt: "1863-01-01T09:00:00Z", ReceivedAt: "1863-01-01T09:00:02Z",
			Direction: "outbound", Read: true, Labels: []string{"SENT"},
			Body: "My friend — The Proclamation goes into effect today. I have put my name to it. Now the war must mean something more than reunion. I shall need your voice still.\n\nA. Lincoln",
		},
		{
			ID: "lc-0004", ThreadID: "thr-lc-grant-vicksburg", MessageID: "lc-0004@lincoln.example",
			Subject: "You were right", Snippet: "I was not anxious to push you before Vicksburg fell.",
			FromName: "Abraham Lincoln", FromEmail: "lincoln@lincoln.example",
			ToName: "Ulysses S. Grant", ToEmail: "grant@lincoln.example",
			SentAt: "1863-07-13T09:00:00Z", ReceivedAt: "1863-07-13T09:00:01Z",
			Direction: "outbound", Read: true, Labels: []string{"SENT", "GRANT"},
			Body: "General — I write only to say I was not anxious to push you before Vicksburg fell. You were right, and I was wrong. The country is in your debt.\n\nA. Lincoln",
		},
		{
			ID: "lc-0005", ThreadID: "thr-lc-gettysburg", MessageID: "lc-0005@lincoln.example",
			Subject: "Remarks for Thursday — short", Snippet: "I shall be brief. The world will little note what I say here.",
			FromName: "Abraham Lincoln", FromEmail: "lincoln@lincoln.example",
			ToName: "John G. Nicolay", ToEmail: "nicolay@lincoln.example",
			SentAt: "1863-11-18T22:00:00Z", ReceivedAt: "1863-11-18T22:00:01Z",
			Direction: "outbound", Read: true, Labels: []string{"SENT"},
			Body: "Nicolay — I shall be brief. The world will little note, nor long remember what I say here, but it can never forget what they did here. Have the proof with me on the train.\n\nA. L.",
		},
		{
			ID: "lc-0006", ThreadID: "thr-lc-grant-appoint", MessageID: "lc-0006@lincoln.example",
			Subject: "You are now general-in-chief", Snippet: "Come to Washington when you can leave the front without hurt.",
			FromName: "Abraham Lincoln", FromEmail: "lincoln@lincoln.example",
			ToName: "Ulysses S. Grant", ToEmail: "grant@lincoln.example",
			SentAt: "1864-03-09T11:30:00Z", ReceivedAt: "1864-03-09T11:30:01Z",
			Direction: "outbound", Read: true, Labels: []string{"SENT", "GRANT"},
			Body: "General — You are now general-in-chief of the armies. Come to Washington when you can leave the front without hurt to the service. I will not pretend to direct you.\n\nA. Lincoln",
		},
		{
			ID: "lc-0007", ThreadID: "thr-lc-mary", MessageID: "lc-0007@lincoln.example",
			Subject: "Tad and I are well", Snippet: "Come home when the papers allow, and do not forget the strawberries.",
			FromName: "Mary Todd Lincoln", FromEmail: "mary@lincoln.example",
			ToName: "Abraham Lincoln", ToEmail: "lincoln@lincoln.example",
			SentAt: "1864-06-12T20:00:00Z", ReceivedAt: "1864-06-12T20:00:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX", "FAMILY"},
			Body: "Dear Husband — Tad and I are well. The house is quiet without you. Come home when the papers allow, and do not forget the strawberries.\n\nMary",
		},
		{
			ID: "lc-0008", ThreadID: "thr-lc-inaug2", MessageID: "lc-0008@lincoln.example",
			Subject: "With malice toward none", Snippet: "I mean every word. Let the work be finished kindly.",
			FromName: "Abraham Lincoln", FromEmail: "lincoln@lincoln.example",
			ToName: "William H. Seward", ToEmail: "seward@lincoln.example",
			SentAt: "1865-03-04T14:00:00Z", ReceivedAt: "1865-03-04T14:00:01Z",
			Direction: "outbound", Read: true, Labels: []string{"SENT"},
			Body: "Seward — I mean every word of the close: with malice toward none, with charity for all. Let the work be finished kindly.\n\nA. Lincoln",
		},
		{
			ID: "lc-0009", ThreadID: "thr-lc-appomattox", MessageID: "lc-0009@lincoln.example",
			Subject: "News from Appomattox", Snippet: "Lee has surrendered to Grant. The telegraph is ringing.",
			FromName: "Edwin M. Stanton", FromEmail: "stanton@lincoln.example",
			ToName: "Abraham Lincoln", ToEmail: "lincoln@lincoln.example",
			SentAt: "1865-04-09T18:30:00Z", ReceivedAt: "1865-04-09T18:30:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX", "WAR"},
			Body: "Mr. President — News from Appomattox Court House: Lee has surrendered to Grant. The telegraph is ringing. I thought you would wish to know at once.\n\nE. M. Stanton",
		},
		{
			ID: "lc-0010", ThreadID: "thr-lc-douglass", MessageID: "lc-0010@lincoln.example",
			Subject: "You have earned the gratitude of a race", Snippet: "I have often been criticized, but never by you unjustly.",
			FromName: "Frederick Douglass", FromEmail: "douglass@lincoln.example",
			ToName: "Abraham Lincoln", ToEmail: "lincoln@lincoln.example",
			SentAt: "1865-04-12T16:00:00Z", ReceivedAt: "1865-04-12T16:00:02Z",
			Direction: "inbound", Read: true, Labels: []string{"INBOX"},
			Body: "Mr. President — Allow me to say you have earned the gratitude of a race. I have often been criticized for my haste, but never by you unjustly.\n\nFrederick Douglass",
		},
	}
}
