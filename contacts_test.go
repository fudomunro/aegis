package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestNormalizeContactEmail(t *testing.T) {
	cases := map[string]string{
		"  Ada@Example.COM ": "ada@example.com",
		"ADA@example.com":    "ada@example.com",
		"":                   "",
	}
	for in, want := range cases {
		if got := NormalizeContactEmail(in); got != want {
			t.Errorf("NormalizeContactEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeContactPhone(t *testing.T) {
	cases := map[string]string{
		"+1 (555) 123-4567": "+15551234567",
		"555.123.4567":      "5551234567",
		"+44 20 7946 0958":  "+442079460958",
		"":                  "",
		"abc":               "",
	}
	for in, want := range cases {
		if got := NormalizeContactPhone(in); got != want {
			t.Errorf("NormalizeContactPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeContactHandle(t *testing.T) {
	if got := NormalizeContactHandle("  @AdaL "); got != "adal" {
		t.Fatalf("handle = %q, want adal", got)
	}
}

func TestContactIdentityKeys_PrefersEmailThenPhoneThenHandle(t *testing.T) {
	c := types.Contact{
		Name:    "Ada Lovelace",
		Emails:  []string{"Ada@Example.com"},
		Phones:  []string{"+1 (555) 123-4567"},
		Handles: []string{"@ada"},
	}
	keys := ContactIdentityKeys(c)
	want := []string{
		"email:ada@example.com",
		"phone:+15551234567",
		"handle:ada",
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
	if primaryContactKey(c) != "email:ada@example.com" {
		t.Fatalf("primary = %q", primaryContactKey(c))
	}
}

func TestContactIdentityKeys_NameOnly(t *testing.T) {
	c := types.Contact{Name: "  Grace   Hopper "}
	keys := ContactIdentityKeys(c)
	if len(keys) != 1 || keys[0] != "name:grace hopper" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestNewContact_Empty(t *testing.T) {
	if got := newContact("google", "Google", "id1", "  ", nil, nil); got.Name != "" {
		t.Fatalf("expected empty contact, got %+v", got)
	}
}

func TestMergeContacts_SharedEmailAcrossServices(t *testing.T) {
	a := newContact("google", "Google Contacts", "g1", "Ada Lovelace",
		[]string{"ada@example.com"}, nil)
	b := newContact("outlook", "Outlook", "o9", "Ada L.",
		[]string{"ADA@example.com"}, []string{"+1 (555) 123-4567"})
	merged := MergeContacts([]types.Contact{a, b})
	if len(merged) != 1 {
		t.Fatalf("got %d contacts, want 1: %+v", len(merged), merged)
	}
	c := merged[0]
	if c.Name != "Ada Lovelace" {
		t.Fatalf("name = %q", c.Name)
	}
	if len(c.Emails) != 1 || c.Emails[0] != "ada@example.com" {
		t.Fatalf("emails = %v", c.Emails)
	}
	if len(c.Phones) != 1 || c.Phones[0] != "+15551234567" {
		t.Fatalf("phones = %v", c.Phones)
	}
	if len(c.Sources) != 2 {
		t.Fatalf("sources = %+v", c.Sources)
	}
}

func TestMergeContacts_PhoneLinksDifferentEmails(t *testing.T) {
	a := newContact("google", "Google", "g1", "Bob",
		[]string{"bob@work.example"}, []string{"+15550001111"})
	b := newContact("outlook", "Outlook", "o1", "Robert",
		[]string{"bob@home.example"}, []string{"+1 (555) 000-1111"})
	merged := MergeContacts([]types.Contact{a, b})
	if len(merged) != 1 {
		t.Fatalf("phone should link contacts: %+v", merged)
	}
	if len(merged[0].Emails) != 2 {
		t.Fatalf("emails = %v", merged[0].Emails)
	}
}

func TestMergeContacts_NameFallbackSeparatesDistinctPeople(t *testing.T) {
	a := newContact("google", "Google", "g1", "Alex Kim",
		[]string{"alex1@example.com"}, nil)
	b := newContact("google", "Google", "g2", "Alex Kim",
		[]string{"alex2@example.com"}, nil)
	merged := MergeContacts([]types.Contact{a, b})
	if len(merged) != 2 {
		t.Fatalf("distinct emails must not merge by name alone: %+v", merged)
	}
}

func TestMergeContacts_UnionFindChainsOnSharedEmail(t *testing.T) {
	// A and B share email; B and C share phone → one entity via union-find.
	a := types.Contact{
		Name:    "Sam One",
		Emails:  []string{"sam@example.com"},
		Sources: []types.ContactSource{{Service: "google", ServiceName: "Google", ID: "a"}},
	}
	b := types.Contact{
		Name:    "Sam Two",
		Emails:  []string{"sam@example.com"},
		Phones:  []string{"+15550002222"},
		Sources: []types.ContactSource{{Service: "outlook", ServiceName: "Outlook", ID: "b"}},
	}
	c := types.Contact{
		Name:    "Sam Two",
		Phones:  []string{"+1 555 000 2222"},
		Sources: []types.ContactSource{{Service: "icloud", ServiceName: "iCloud", ID: "c"}},
	}
	merged := MergeContacts([]types.Contact{a, b, c})
	if len(merged) != 1 {
		t.Fatalf("chained keys should merge into one: %+v", merged)
	}
	if len(merged[0].Sources) != 3 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestMergeContacts_NameOnlyDoesNotJoinEmailContact(t *testing.T) {
	withEmail := types.Contact{
		Name:    "Alex Kim",
		Emails:  []string{"alex@example.com"},
		Sources: []types.ContactSource{{Service: "google", ServiceName: "Google", ID: "g"}},
	}
	nameOnly := types.Contact{
		Name:    "Alex Kim",
		Sources: []types.ContactSource{{Service: "outlook", ServiceName: "Outlook", ID: "o"}},
	}
	merged := MergeContacts([]types.Contact{withEmail, nameOnly})
	if len(merged) != 2 {
		t.Fatalf("name-only must not join an email-bearing contact: %+v", merged)
	}
}

func TestMergeContacts_Empty(t *testing.T) {
	if MergeContacts(nil) != nil {
		t.Fatal("nil in should yield nil out")
	}
}

type fakeContactProvider struct {
	contacts []types.Contact
	err      error
}

func (f *fakeContactProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakeContactProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeContactProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeContactProvider) ListUserContacts(context.Context) ([]types.Contact, bool, error) {
	return f.contacts, false, f.err
}

func TestHandleLibraryContacts_AggregatesServices(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "google", Name: "Google", Action: "inject"},
			{ID: "outlook", Name: "Outlook", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"google": &fakeContactProvider{
			contacts: []types.Contact{
				{
					Name:    "Ada Lovelace",
					Emails:  []string{"ada@example.com"},
					Sources: []types.ContactSource{{Service: "google", ServiceName: "Google", ID: "g1"}},
				},
			},
		},
		"outlook": &fakeContactProvider{
			contacts: []types.Contact{
				{
					Name:    "Ada Lovelace",
					Emails:  []string{"ada@example.com"},
					Phones:  []string{"+15551234567"},
					Sources: []types.ContactSource{{Service: "outlook", ServiceName: "Outlook", ID: "o1"}},
				},
				{
					Name:    "Grace Hopper",
					Emails:  []string{"grace@example.com"},
					Sources: []types.ContactSource{{Service: "outlook", ServiceName: "Outlook", ID: "o2"}},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp types.ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Ada merges across services; Grace is separate → 2 total.
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2: %+v", resp.Total, resp.Contacts)
	}
	if !resp.Synced {
		t.Fatal("expected synced=true")
	}
	// Ada is first (name sort: Ada, Grace).
	if resp.Contacts[0].Name != "Ada Lovelace" || len(resp.Contacts[0].Sources) != 2 {
		t.Fatalf("ada = %+v", resp.Contacts[0])
	}
	if resp.Contacts[1].Name != "Grace Hopper" {
		t.Fatalf("grace = %+v", resp.Contacts[1])
	}
	if resp.ByService["google"].Status != types.LibraryStatusOK || resp.ByService["google"].Count != 1 {
		t.Fatalf("google status = %+v", resp.ByService["google"])
	}
	if resp.ByService["outlook"].Status != types.LibraryStatusOK || resp.ByService["outlook"].Count != 2 {
		t.Fatalf("outlook status = %+v", resp.ByService["outlook"])
	}
}

func TestHandleLibraryContacts_NoProviders(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": NewSpotifyConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no contact-capable services") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestHandleLibraryContacts_DeniedByPermission(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "google", Name: "Google", Action: "inject",
				Permissions: types.Permissions{"contacts": types.AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"google": &fakeContactProvider{
			contacts: []types.Contact{{Name: "Ada", Sources: []types.ContactSource{{Service: "google", ID: "1"}}}},
		},
	}

	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryContacts_UnauthorizedService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "google", Name: "Google", Action: "inject"},
			{ID: "outlook", Name: "Outlook", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"google": &fakeContactProvider{
			contacts: []types.Contact{
				{Name: "Ada", Sources: []types.ContactSource{{Service: "google", ServiceName: "Google", ID: "g1"}}},
			},
		},
		"outlook": &fakeContactProvider{err: types.ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp types.ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["outlook"].Status != types.LibraryStatusUnauthorized {
		t.Fatalf("outlook = %+v", resp.ByService["outlook"])
	}
	if !strings.Contains(resp.ByService["outlook"].Error, "/v1/auth/outlook/login") {
		t.Fatalf("error = %q", resp.ByService["outlook"].Error)
	}
}

func TestHandleLibraryContacts_ErrorService(t *testing.T) {
	cfg := &Config{
		Services: []Service{{ID: "google", Name: "Google", Action: "inject"}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"google": &fakeContactProvider{err: errors.New("upstream boom")},
	}

	req := httptest.NewRequest("GET", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp types.ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["google"].Status != types.LibraryStatusError {
		t.Fatalf("google = %+v", resp.ByService["google"])
	}
	if len(resp.Contacts) != 0 {
		t.Fatalf("contacts = %+v", resp.Contacts)
	}
}

func TestHandleLibraryContacts_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("POST", "/v1/library/contacts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryContacts)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestDataTypeForPath_Contacts(t *testing.T) {
	cases := map[string]string{
		"/me/contacts":      "contacts",
		"/contacts":         "contacts",
		"/contacts/abc":     "contacts",
		"/contacts/abc/x":   "contacts",
		"/me/contacts/":     "contacts",
		"/contact":          "", // singular is not a known type
		"/me/contact-lists": "",
	}
	for path, want := range cases {
		if got := types.DataTypeForPath(path); got != want {
			t.Errorf("types.DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestMentionsContacts(t *testing.T) {
	yes := []string{
		"who is in my contacts?",
		"show my address book",
		"search my contact list for Ada",
		"list my contacts",
	}
	for _, msg := range yes {
		if !LibraryKindByID("contacts").Mentions(msg) {
			t.Errorf("Mentions(%q) = false, want true", msg)
		}
	}
	no := []string{
		"what playlists do I have?",
		"play some jazz",
		"tell me a joke",
		"contact the support team", // "contact" alone is not enough
	}
	for _, msg := range no {
		if LibraryKindByID("contacts").Mentions(msg) {
			t.Errorf("Mentions(%q) = true, want false", msg)
		}
	}
}

func TestPreRequest_ContactsWithoutServiceName(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "google", Name: "Google", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"google": &fakeContactProvider{},
	}

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{RequestID: "req-contacts", LoginName: "alice"},
		UserMessage: "Who is in my contacts?",
		RequestBody: mustJSON(t, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "Who is in my contacts?"}},
		}),
	}

	req := httptest.NewRequest("POST", "/v1/pre-request", strings.NewReader(string(mustJSON(t, payload))))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != ActionModify {
		t.Fatalf("action = %s, want modify", resp.Action)
	}
	if !strings.Contains(string(resp.RequestBody), "/v1/library/contacts") {
		t.Fatalf("request_body missing contacts library endpoint: %s", resp.RequestBody)
	}
}

func TestHasContactProviders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{}
	if kindHasProviders("contacts", srv) {
		t.Fatal("empty connectors should have no contact providers")
	}
	srv.connectors["google"] = &fakeContactProvider{}
	if !kindHasProviders("contacts", srv) {
		t.Fatal("fake provider should register as contact provider")
	}
}
