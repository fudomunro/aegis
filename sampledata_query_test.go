package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Query checklist for the mail archive and library merge (issue #77).
// These tests pin the cases that live half-imported data used to hide —
// especially "oldest message" needing sort=asc.

func seedQueryFixture(t *testing.T) (*MailArchive, SampleMailSet) {
	t.Helper()
	a := openTestMailArchive(t)
	// 12 normal rows + edge cases: enough for pagination and date windows.
	set := GenerateSampleMail(12)
	if err := SeedMailArchive(a, set.Records); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return a, set
}

func TestMailQuery_OldestAndNewest(t *testing.T) {
	a, set := seedQueryFixture(t)

	oldest, err := a.Search(MailSearchQuery{Sort: "asc", Limit: 1})
	if err != nil {
		t.Fatalf("asc: %v", err)
	}
	if len(oldest.Messages) != 1 {
		t.Fatalf("oldest hits = %d", len(oldest.Messages))
	}
	if oldest.Messages[0].Sources[0].ID != set.OldestID {
		t.Fatalf("oldest id = %q want %q (sent_at %q)",
			oldest.Messages[0].Sources[0].ID, set.OldestID, oldest.Messages[0].SentAt)
	}
	// Dateless rows must not win "oldest" (edge-missing-sent).
	if oldest.Messages[0].SentAt == "" {
		t.Fatalf("oldest has empty sent_at: %+v", oldest.Messages[0])
	}

	newest, err := a.Search(MailSearchQuery{Sort: "desc", Limit: 1})
	if err != nil {
		t.Fatalf("desc: %v", err)
	}
	if len(newest.Messages) != 1 {
		t.Fatalf("newest hits = %d", len(newest.Messages))
	}
	if newest.Messages[0].Sources[0].ID != set.NewestID {
		t.Fatalf("newest id = %q want %q (sent_at %q)",
			newest.Messages[0].Sources[0].ID, set.NewestID, newest.Messages[0].SentAt)
	}

	// Explicit "what is the oldest email" shape used by agents.
	first, err := a.Search(MailSearchQuery{Sort: "asc", Limit: 1, WithBody: true})
	if err != nil {
		t.Fatalf("oldest with body: %v", err)
	}
	if first.Messages[0].SentAt == "" && first.Messages[0].Subject == "" {
		t.Fatalf("oldest message empty: %+v", first.Messages[0])
	}
}

func TestMailQuery_DateWindow(t *testing.T) {
	a, _ := seedQueryFixture(t)

	// Rows land one week apart starting 2019-01-07; pick a mid-year window.
	window, err := a.Search(MailSearchQuery{
		After:  "2019-01-01",
		Before: "2019-03-01",
		Sort:   "asc",
	})
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if window.Total == 0 {
		t.Fatal("expected hits in Q1 2019")
	}
	for _, m := range window.Messages {
		if m.SentAt == "" {
			continue // edge-missing-sent can appear only if it matches other filters
		}
		if m.SentAt < "2019-01-01" || m.SentAt >= "2019-03-01" {
			t.Fatalf("out of window: %q", m.SentAt)
		}
	}

	// after/before exclusive-inclusive bounds from normalizeMailTimeBound.
	only, err := a.Search(MailSearchQuery{After: "2019-01-07", Before: "2019-01-08", Sort: "asc"})
	if err != nil {
		t.Fatalf("day window: %v", err)
	}
	if only.Total != 1 {
		t.Fatalf("day window total = %d, want 1 (%+v)", only.Total, only.Messages)
	}
}

func TestMailQuery_FromToLabelFilters(t *testing.T) {
	a, _ := seedQueryFixture(t)

	fromAda, err := a.Search(MailSearchQuery{From: "ada@example.com", Sort: "asc"})
	if err != nil {
		t.Fatalf("from: %v", err)
	}
	if fromAda.Total == 0 {
		t.Fatal("expected ada hits")
	}
	for _, m := range fromAda.Messages {
		if m.From == nil || !strings.Contains(strings.ToLower(m.From.Emails[0]), "ada") {
			// From filter is LIKE-based on from_email/from_name; empty From contact
			// only happens for rows without a sender — those must not appear.
			t.Fatalf("unexpected from row: %+v", m.From)
		}
	}

	toMe, err := a.Search(MailSearchQuery{To: "me@example.com"})
	if err != nil {
		t.Fatalf("to: %v", err)
	}
	if toMe.Total == 0 {
		t.Fatal("expected me@example.com hits")
	}

	spam, err := a.Search(MailSearchQuery{Labels: []string{"SPAM"}})
	if err != nil {
		t.Fatalf("label spam: %v", err)
	}
	if spam.Total != 1 {
		t.Fatalf("spam total = %d", spam.Total)
	}
	trash, err := a.Search(MailSearchQuery{Labels: []string{"TRASH"}})
	if err != nil {
		t.Fatalf("label trash: %v", err)
	}
	if trash.Total != 1 {
		t.Fatalf("trash total = %d", trash.Total)
	}
	inbox, err := a.Search(MailSearchQuery{Labels: []string{"INBOX"}})
	if err != nil {
		t.Fatalf("label inbox: %v", err)
	}
	if inbox.Total == 0 {
		t.Fatal("expected inbox hits")
	}
}

func TestMailQuery_FTS(t *testing.T) {
	a, _ := seedQueryFixture(t)

	multi, err := a.Search(MailSearchQuery{Q: "invoice widgets", Sort: "asc"})
	if err != nil {
		t.Fatalf("multi-word fts: %v", err)
	}
	if multi.Total == 0 {
		t.Fatal("expected invoice widgets hits")
	}

	unicode, err := a.Search(MailSearchQuery{Q: "שלום"})
	if err != nil {
		t.Fatalf("unicode fts: %v", err)
	}
	if unicode.Total == 0 {
		t.Fatal("expected RTL subject hit")
	}

	// Hostile operator strings must not error (mailFTSMatch strips operators).
	for _, q := range []string{
		`NEAR(a b) OR "x*"`,
		`subject:invoice`,
		`"unterminated`,
		`AND OR NOT`,
		`column:value*`,
		`((((`,
	} {
		if _, err := a.Search(MailSearchQuery{Q: q}); err != nil {
			t.Fatalf("hostile fts %q: %v", q, err)
		}
	}

	// Combined FTS + structured filters.
	combo, err := a.Search(MailSearchQuery{
		Q:      "report",
		From:   "ada@example.com",
		Labels: []string{"INBOX"},
	})
	if err != nil {
		t.Fatalf("combo: %v", err)
	}
	if combo.Total == 0 {
		t.Fatal("expected combo hit for Ada quarterly report")
	}
}

func TestMailQuery_PaginationStableOrder(t *testing.T) {
	a, _ := seedQueryFixture(t)

	var collected []string
	seen := map[string]bool{}
	for offset := 0; offset < 40; offset += 5 {
		page, err := a.Search(MailSearchQuery{Sort: "desc", Limit: 5, Offset: offset})
		if err != nil {
			t.Fatalf("page offset=%d: %v", offset, err)
		}
		for _, m := range page.Messages {
			id := m.Sources[0].Service + "/" + m.Sources[0].ID
			if seen[id] {
				t.Fatalf("duplicate id %s at offset %d", id, offset)
			}
			seen[id] = true
			collected = append(collected, id)
		}
		if page.Total < len(page.Messages) {
			t.Fatalf("total %d < page %d", page.Total, len(page.Messages))
		}
		if len(page.Messages) == 0 {
			break
		}
	}
	if len(collected) < 10 {
		t.Fatalf("pagination collected only %d rows", len(collected))
	}

	// Two overlapping pages must agree on order (stable sort keys).
	p0, err := a.Search(MailSearchQuery{Sort: "desc", Limit: 4, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	p1, err := a.Search(MailSearchQuery{Sort: "desc", Limit: 4, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(p0.Messages) < 2 || len(p1.Messages) < 2 {
		t.Fatal("need at least 2 rows per page")
	}
	for i := 1; i < len(p1.Messages); i++ {
		if p0.Messages[i].SentAt != p1.Messages[i-1].SentAt &&
			p0.Messages[i].Subject != p1.Messages[i-1].Subject {
			// Allow only if a missing-sent row sorts unstably; check IDs instead.
			if p0.Messages[i].Sources[0].ID != p1.Messages[i-1].Sources[0].ID {
				t.Fatalf("order shifted across pages: p0[%d]=%v p1[%d]=%v",
					i, p0.Messages[i].Sources, i-1, p1.Messages[i-1].Sources)
			}
		}
	}

	asc, err := a.Search(MailSearchQuery{Sort: "asc", Limit: 3, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(asc.Messages) == 0 {
		t.Fatal("asc page empty")
	}
}

func TestMailQuery_FetchByIDAndRaw(t *testing.T) {
	a, set := seedQueryFixture(t)
	srv := mailTestServer(t, a)

	id := set.Records[0].ID
	w := doMailReq(t, srv, http.MethodGet, "/v1/mail/messages/"+googleGmailServiceID+"/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("fetch id code = %d body=%s", w.Code, w.Body.String())
	}
	var msg Message
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg.Subject == "" {
		t.Fatalf("message missing subject: %+v", msg)
	}

	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/messages/"+googleGmailServiceID+"/"+id+"/raw", "")
	if w.Code != http.StatusOK {
		t.Fatalf("raw code = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "synthetic") {
		t.Fatalf("raw payload = %s", w.Body.String())
	}

	// HTTP search path mirrors the store API for the oldest-message question.
	w = doMailReq(t, srv, http.MethodGet, "/v1/mail/search?sort=asc&limit=1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("search http code = %d body=%s", w.Code, w.Body.String())
	}
	var searchResp MailSearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(searchResp.Messages) != 1 {
		t.Fatalf("http oldest hits = %d", len(searchResp.Messages))
	}
	if searchResp.Messages[0].Sources[0].ID != set.OldestID {
		t.Fatalf("http oldest id = %q want %q",
			searchResp.Messages[0].Sources[0].ID, set.OldestID)
	}
}

func TestLibraryMerge_ISRCContactsMessages(t *testing.T) {
	lib := GenerateSampleLibrary()

	// Tracks: shared ISRC merges to one entity with match=isrc.
	mergedTracks := MergeTracksSync(lib.Tracks)
	var isrc *Track
	for i := range mergedTracks {
		if mergedTracks[i].EntityID == "USSYN17000001" {
			isrc = &mergedTracks[i]
			break
		}
	}
	if isrc == nil {
		t.Fatalf("ISRC group missing: %+v", mergedTracks)
	}
	if isrc.Match != TrackMatchISRC || len(isrc.Sources) != 2 {
		t.Fatalf("isrc merge = match %q sources %d", isrc.Match, len(isrc.Sources))
	}

	// Distinct ISRCs with the same name stay separate.
	songA := 0
	for _, tr := range mergedTracks {
		if tr.Name == "Song A" {
			songA++
		}
	}
	if songA != 2 {
		t.Fatalf("Song A entities = %d, want 2 (distinct ISRCs)", songA)
	}

	// Name-only pair merges to one multi-source track.
	takeFive := 0
	for _, tr := range mergedTracks {
		if tr.Name == "Take Five" {
			takeFive++
			if len(tr.Sources) != 2 {
				t.Fatalf("Take Five sources = %+v", tr.Sources)
			}
		}
	}
	if takeFive != 1 {
		t.Fatalf("Take Five entities = %d, want 1", takeFive)
	}

	// Contacts: email → phone → handle → name union-find.
	mergedContacts := MergeContacts(lib.Contacts)
	var ada *Contact
	for i := range mergedContacts {
		if strings.Contains(strings.ToLower(mergedContacts[i].Name), "ada") {
			ada = &mergedContacts[i]
		}
	}
	if ada == nil {
		t.Fatal("Ada missing")
	}
	if len(ada.Sources) != 3 {
		t.Fatalf("Ada sources = %d, want 3 (email+phone+handle)", len(ada.Sources))
	}

	// Grace merges on shared phone across different emails.
	var grace *Contact
	for i := range mergedContacts {
		if strings.Contains(strings.ToLower(mergedContacts[i].Name), "grace") {
			grace = &mergedContacts[i]
		}
	}
	if grace == nil {
		t.Fatal("Grace missing")
	}
	if len(grace.Sources) != 2 {
		t.Fatalf("Grace sources = %d, want 2", len(grace.Sources))
	}

	// Dateless mail rows sort last so they cannot win oldest/newest.
	a, set := seedQueryFixture(t)
	defer a.Close()
	last, err := a.Search(MailSearchQuery{Sort: "asc", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Messages) == 0 {
		t.Fatal("empty asc page")
	}
	if last.Messages[len(last.Messages)-1].Sources[0].ID != "edge-missing-sent" {
		t.Fatalf("dateless row should sort last, got %q",
			last.Messages[len(last.Messages)-1].Sources[0].ID)
	}
	if last.Messages[0].Sources[0].ID != set.OldestID {
		t.Fatalf("asc first = %q want %q", last.Messages[0].Sources[0].ID, set.OldestID)
	}

	// Messages: Message-ID preferred; subject+from+time fallback.
	mergedMsgs := MergeMessages(lib.Messages)
	bySubject := map[string]Message{}
	for _, m := range mergedMsgs {
		bySubject[m.Subject] = m
	}
	ship := bySubject["Ship the release"]
	if ship.Match != "message_id" || len(ship.Sources) != 2 {
		t.Fatalf("ship = %+v", ship)
	}
	inv := bySubject["Invoice reminder"]
	if inv.Match != "subject_from_time" || len(inv.Sources) != 2 {
		t.Fatalf("invoice = %+v", inv)
	}
	if bySubject["Unrelated note"].Match != "" {
		t.Fatalf("single-source match should be empty: %+v", bySubject["Unrelated note"])
	}
}

func TestLibraryAggregates_SampleProviders(t *testing.T) {
	lib := GenerateSampleLibrary()
	cfg := &Config{Services: []Service{
		{ID: "spotify", Name: "Spotify", Action: "inject"},
		{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		{ID: "amazon-music", Name: "Amazon Music", Action: "inject"},
		{ID: "google-contacts", Name: "Google Contacts", Action: "inject"},
		{ID: "google-gmail", Name: "Gmail", Action: "inject"},
		{ID: takeoutServiceID, Name: takeoutServiceName, Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	// Split sample tracks across fake connectors so the aggregate merges them.
	srv.connectors = map[string]ServiceConnector{
		"spotify": &sampleTrackPlaylistProvider{
			playlists: filterPlaylists(lib.Playlists, "spotify"),
			tracks:    filterTracks(lib.Tracks, "spotify"),
		},
		"youtube-music": &sampleTrackPlaylistProvider{
			playlists: filterPlaylists(lib.Playlists, "youtube-music"),
			tracks:    filterTracks(lib.Tracks, "youtube-music"),
		},
		"amazon-music": &sampleTrackPlaylistProvider{
			playlists: nil,
			tracks:    filterTracks(lib.Tracks, "amazon-music"),
		},
		"google-contacts": &sampleContactProvider{
			contacts: filterContactsByService(lib.Contacts, "google-contacts"),
		},
		"google-gmail": &sampleMessageProvider{
			messages: filterMessagesByService(lib.Messages, googleGmailServiceID),
		},
		takeoutServiceID: &sampleMessageProvider{
			messages: filterMessagesByService(lib.Messages, takeoutServiceID),
		},
	}

	// Library tracks merge ISRC across the fake services.
	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tracks status = %d body=%s", w.Code, w.Body.String())
	}
	var trackList TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &trackList); err != nil {
		t.Fatalf("decode tracks: %v", err)
	}
	if trackList.Total == 0 {
		t.Fatal("no tracks aggregated")
	}
	foundISRC := false
	for _, tr := range trackList.Tracks {
		if tr.EntityID == "USSYN17000001" && len(tr.Sources) >= 2 {
			foundISRC = true
			if tr.Match != TrackMatchISRC {
				t.Fatalf("library isrc match = %q", tr.Match)
			}
		}
	}
	if !foundISRC {
		t.Fatalf("library tracks missing merged ISRC pair: %+v", trackList.Tracks)
	}

	// Messages merge Message-ID across gmail + takeout.
	req = httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMessages)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("messages status = %d body=%s", w.Code, w.Body.String())
	}
	var msgList MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &msgList); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if !msgList.Synced {
		t.Fatal("expected synced message merge")
	}
	merged := false
	for _, m := range msgList.Messages {
		if m.Match == "message_id" && len(m.Sources) >= 2 {
			merged = true
		}
	}
	if !merged {
		t.Fatalf("no message_id multi-source merge: %+v", msgList.Messages)
	}

	// Contacts merge identity chains.
	req = httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("contacts status = %d body=%s", w.Code, w.Body.String())
	}
	var contactList ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &contactList); err != nil {
		t.Fatalf("decode contacts: %v", err)
	}
	if contactList.Total == 0 {
		t.Fatal("no contacts aggregated")
	}
}

func TestLibraryAggregates_TruncatedAndPermissionDenied(t *testing.T) {
	lib := GenerateSampleLibrary()

	// Permission-denied: the only tracks service forbids agent access.
	deniedCfg := &Config{Services: []Service{
		{ID: "spotify", Name: "Spotify", Action: "inject", Permissions: Permissions{"tracks": AccessNone}},
	}}
	srv := testServerWithBase(deniedCfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &sampleTrackPlaylistProvider{tracks: filterTracks(lib.Tracks, "spotify")},
	}
	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("tracks denied status = %d body=%s", w.Code, w.Body.String())
	}

	// Truncated page-cap shape: one provider reports more items upstream.
	okCfg := &Config{Services: []Service{
		{ID: "spotify", Name: "Spotify", Action: "inject"},
		{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
	}}
	srv = testServerWithBase(okCfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &sampleTrackPlaylistProvider{
			tracks: filterTracks(lib.Tracks, "spotify"),
		},
		"youtube-music": &truncTrackProvider{
			tracks: filterTracks(lib.Tracks, "youtube-music"),
		},
	}
	req = httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("truncated tracks status = %d body=%s", w.Code, w.Body.String())
	}
	var trackList TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &trackList); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !trackList.Truncated {
		t.Fatalf("expected truncated=true: %+v", trackList.ByService)
	}
	if !trackList.ByService["youtube-music"].Truncated {
		t.Fatalf("youtube status = %+v", trackList.ByService["youtube-music"])
	}
}

type truncTrackProvider struct {
	sampleTrackPlaylistProvider
}

func (f *truncTrackProvider) ListUserLikedTracks(_ context.Context) ([]Track, bool, error) {
	return f.tracks, true, f.err
}

// --- tiny fake providers used by the sample aggregate test ---

type sampleContactProvider struct {
	contacts  []Contact
	truncated bool
	err       error
}

func (f *sampleContactProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (f *sampleContactProvider) SystemInstructions(string) string  { return "fake" }
func (f *sampleContactProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}
func (f *sampleContactProvider) ListUserContacts(_ context.Context) ([]Contact, bool, error) {
	return f.contacts, f.truncated, f.err
}

type sampleMessageProvider struct {
	messages  []Message
	truncated bool
	err       error
}

func (f *sampleMessageProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (f *sampleMessageProvider) SystemInstructions(string) string  { return "fake" }
func (f *sampleMessageProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}
func (f *sampleMessageProvider) ListMessages(_ context.Context) ([]Message, bool, error) {
	return f.messages, f.truncated, f.err
}

type sampleTrackPlaylistProvider struct {
	playlists []Playlist
	tracks    []Track
	err       error
}

func (f *sampleTrackPlaylistProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (f *sampleTrackPlaylistProvider) SystemInstructions(string) string  { return "fake" }
func (f *sampleTrackPlaylistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *sampleTrackPlaylistProvider) ListUserPlaylists(_ context.Context) ([]Playlist, bool, error) {
	return f.playlists, false, f.err
}

func (f *sampleTrackPlaylistProvider) ListUserLikedTracks(_ context.Context) ([]Track, bool, error) {
	return f.tracks, false, f.err
}

func filterTracks(tracks []Track, service string) []Track {
	var out []Track
	for _, tr := range tracks {
		for _, src := range tr.Sources {
			if src.Service == service {
				// Single-source copy for the fake provider.
				cp := tr
				cp.Sources = []TrackSource{src}
				cp.EntityID = tr.EntityID
				out = append(out, cp)
				break
			}
		}
	}
	return out
}

func filterPlaylists(list []Playlist, service string) []Playlist {
	var out []Playlist
	for _, p := range list {
		for _, src := range p.Sources {
			if src.Service == service {
				cp := p
				cp.Sources = []PlaylistSource{src}
				out = append(out, cp)
				break
			}
		}
	}
	return out
}

func filterContactsByService(list []Contact, service string) []Contact {
	var out []Contact
	for _, c := range list {
		for _, src := range c.Sources {
			if src.Service == service {
				cp := c
				cp.Sources = []ContactSource{src}
				out = append(out, cp)
				break
			}
		}
	}
	return out
}

func filterMessagesByService(list []Message, service string) []Message {
	var out []Message
	for _, m := range list {
		for _, src := range m.Sources {
			if src.Service == service {
				cp := m
				cp.Sources = []MessageSource{src}
				out = append(out, cp)
				break
			}
		}
	}
	return out
}
