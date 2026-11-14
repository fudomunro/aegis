package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

func TestMicrosoftContacts_ImplementsContactProvider(t *testing.T) {
	var _ kinds.ContactProvider = NewMicrosoftContactsConnector(newTestHost(""))
	var _ kinds.ContactWriteProvider = NewMicrosoftContactsConnector(newTestHost(""))
}

func TestMicrosoftContacts_ListUserContacts_MapsNeutral(t *testing.T) {
	page := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		page++
		if page == 1 {
			// First page with a nextLink to page two (Graph sends absolute links).
			next := "http://" + r.Host + "/me/contacts?$skiptoken=2"
			_, _ = w.Write([]byte(`{"value":[
				{"id":"c1","@odata.etag":"W/\"e1\"","displayName":"Ada Lovelace",
				 "emailAddresses":[{"name":"Ada","address":"ada@example.com"}],
				 "businessPhones":["+1 555 0100"],"mobilePhone":"+1 555 0101",
				 "companyName":"Analytical Engines"},
				{"id":"c2","displayName":"","givenName":"Grace","surname":"Hopper",
				 "emailAddresses":[{"address":"grace@example.com"}]}
			],"@odata.nextLink":"` + next + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[
			{"id":"c3","displayName":"Alan Turing","homePhones":["+44 20 7946 0000"]}
		]}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftContactsServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftContactsServiceID, "tok")
	c := NewMicrosoftContactsConnector(th)

	contacts, truncated, err := c.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if truncated {
		t.Fatal("expected truncated=false")
	}
	if len(contacts) != 3 {
		t.Fatalf("contacts = %d, want 3: %+v", len(contacts), contacts)
	}
	ada := contacts[0]
	if ada.Name != "Ada Lovelace" {
		t.Fatalf("ada name = %q", ada.Name)
	}
	if len(ada.Emails) != 1 || ada.Emails[0] != "ada@example.com" {
		t.Fatalf("ada emails = %v", ada.Emails)
	}
	if ada.Organization != "Analytical Engines" {
		t.Fatalf("ada org = %q", ada.Organization)
	}
	if len(ada.Phones) < 2 {
		t.Fatalf("ada phones = %v", ada.Phones)
	}
	if ada.Sources[0].ID != "c1" || ada.Sources[0].Service != MicrosoftContactsServiceID {
		t.Fatalf("ada source = %+v", ada.Sources[0])
	}
	if ada.Sources[0].Extra["etag"] != `W/"e1"` {
		t.Fatalf("ada etag = %v", ada.Sources[0].Extra)
	}
	// givenName+surname fills the display name.
	if contacts[1].Name != "Grace Hopper" {
		t.Fatalf("grace name = %q", contacts[1].Name)
	}
}

func TestMicrosoftContacts_ListUserContacts_Unauthorized(t *testing.T) {
	th := newTestHostWithService(MicrosoftContactsServiceID, "https://graph.example.com", "https://aegis.example.com")
	c := NewMicrosoftContactsConnector(th)
	if _, _, err := c.ListUserContacts(context.Background()); err != kinds.ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestMicrosoftContacts_HandleProxy_NeutralList(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"c1","displayName":"Ada Lovelace","emailAddresses":[{"address":"ada@example.com"}]}]}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftContactsServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftContactsServiceID, "tok")
	c := NewMicrosoftContactsConnector(th)

	req := httptest.NewRequest("GET", "/v1/services/microsoft-contacts/me/contacts", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/me/contacts")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var list kinds.ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(list.Contacts) != 1 || list.Contacts[0].Name != "Ada Lovelace" {
		t.Fatalf("list = %+v", list.Contacts)
	}
}

func TestMicrosoftContacts_SystemInstructions_MentionLibraryOnly(t *testing.T) {
	c := NewMicrosoftContactsConnector(newTestHost(""))
	s := c.SystemInstructions("https://aegis.example.com")
	for _, want := range []string{"/v1/library/contacts", "Microsoft"} {
		if !strings.Contains(s, want) {
			t.Fatalf("instructions missing %q: %s", want, s)
		}
	}
	for _, forbidden := range []string{"#145", "github.com"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("instructions leak %q: %s", forbidden, s)
		}
	}
}

func TestMapMicrosoftContact_PrefersDisplayName(t *testing.T) {
	ct := MapMicrosoftContact(microsoftContact{
		ID:          "c9",
		DisplayName: "Katherine Johnson",
		CompanyName: "NASA",
		EmailAddresses: []struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		}{{Address: "kj@nasa.example.com"}},
	})
	if ct.Name != "Katherine Johnson" || ct.Organization != "NASA" {
		t.Fatalf("contact = %+v", ct)
	}
	if len(ct.Sources) != 1 || ct.Sources[0].ID != "c9" {
		t.Fatalf("sources = %+v", ct.Sources)
	}
}

func TestMicrosoftContacts_ListUserContactsDelta(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Query().Get("$deltatoken") == "t1":
			// Resumed feed: one more change and a new resume link.
			_, _ = w.Write([]byte(`{"value":[
				{"id":"c3","displayName":"Alan Turing","@odata.etag":"W/\"e3\""}
			],"@odata.deltaLink":"DELTA2"}`))
		case r.URL.RawQuery == "":
			// The recorded resume link is followed as-is.
			_, _ = w.Write([]byte(`{"value":[],"@odata.deltaLink":"DELTA3"}`))
		default:
			// Fresh feed: two changes, one deletion, then a second page.
			next := "http://" + r.Host + "/me/contacts/delta?%24skiptoken=2"
			if r.URL.Query().Get("$skiptoken") == "2" {
				_, _ = w.Write([]byte(`{"value":[
					{"id":"c9","displayName":"Deleted Person","@removed":{"reason":"deleted"}}
				],"@odata.deltaLink":"http://` + r.Host + `/me/contacts/delta?%24deltatoken=t1"}`))
				return
			}
			_, _ = w.Write([]byte(`{"value":[
				{"id":"c1","displayName":"Ada Lovelace","@odata.etag":"W/\"e1\"","emailAddresses":[{"address":"ada@example.com"}]},
				{"id":"c2","displayName":"Grace Hopper","@odata.etag":"W/\"e2\""}
			],"@odata.nextLink":"` + next + `"}`))
		}
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftContactsServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftContactsServiceID, "tok")
	c := NewMicrosoftContactsConnector(th)

	// Fresh feed walks pages, drops the deletion, and returns the token.
	contacts, next, truncated, err := c.ListUserContactsDelta(context.Background(), "")
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	if truncated || !strings.Contains(next, "deltatoken=t1") {
		t.Fatalf("next = %q truncated = %v", next, truncated)
	}
	if len(contacts) != 2 {
		t.Fatalf("contacts = %+v, want 2 (deletion dropped)", contacts)
	}

	// Resumed feed: the recorded token is followed as a URL.
	contacts, next, _, err = c.ListUserContactsDelta(context.Background(), next)
	if err != nil {
		t.Fatalf("resumed delta: %v", err)
	}
	if len(contacts) != 1 || contacts[0].Name != "Alan Turing" {
		t.Fatalf("resumed contacts = %+v", contacts)
	}
	if next != "DELTA2" {
		t.Fatalf("resumed next = %q", next)
	}
}

func TestMicrosoftContacts_Delta410RestartsFeed(t *testing.T) {
	freshServed := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.RawQuery == "" {
			// The stale recorded link: expired.
			w.WriteHeader(http.StatusGone)
			return
		}
		freshServed = true
		_, _ = w.Write([]byte(`{"value":[{"id":"c1","displayName":"Ada"}],"@odata.deltaLink":"NEW"}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftContactsServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftContactsServiceID, "tok")
	c := NewMicrosoftContactsConnector(th)

	contacts, next, _, err := c.ListUserContactsDelta(context.Background(), ts.URL+"/me/contacts/delta")
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	if !freshServed || next != "NEW" || len(contacts) != 1 {
		t.Fatalf("fresh=%v next=%q contacts=%+v", freshServed, next, contacts)
	}
}
