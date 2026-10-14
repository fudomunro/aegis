package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestGoogleContactsConnector_ListUserContacts_MockAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/people/me/connections" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "personFields=") {
			t.Errorf("expected personFields query, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"connections": []map[string]any{
				{
					"resourceName": "people/c123",
					"names": []map[string]any{
						{"displayName": "Ada Lovelace"},
					},
					"emailAddresses": []map[string]any{
						{"value": "Ada@Example.com"},
					},
					"phoneNumbers": []map[string]any{
						{"value": "+1 (555) 123-4567"},
					},
					"organizations": []map[string]any{
						{"name": "Analytical Engines"},
					},
				},
				{
					"resourceName": "people/c456",
					"names": []map[string]any{
						{"givenName": "Grace", "familyName": "Hopper"},
					},
					"emailAddresses": []map[string]any{
						{"value": "grace@example.com"},
					},
				},
			},
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID:       connectors.GoogleContactsServiceID,
		Name:     connectors.GoogleContactsServiceName,
		Action:   "inject",
		Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleContactsServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGoogleContactsConnector(srv)
	contacts, truncated, err := conn.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(contacts) != 2 {
		t.Fatalf("got %d contacts, want 2", len(contacts))
	}
	ada := contacts[0]
	if ada.Name != "Ada Lovelace" {
		t.Fatalf("ada name = %q", ada.Name)
	}
	if len(ada.Emails) != 1 || ada.Emails[0] != "ada@example.com" {
		t.Fatalf("ada emails = %v", ada.Emails)
	}
	if len(ada.Phones) != 1 || ada.Phones[0] != "+15551234567" {
		t.Fatalf("ada phones = %v", ada.Phones)
	}
	if ada.Organization != "Analytical Engines" {
		t.Fatalf("ada org = %q", ada.Organization)
	}
	if len(ada.Sources) != 1 {
		t.Fatalf("ada sources = %+v", ada.Sources)
	}
	src := ada.Sources[0]
	if src.Service != connectors.GoogleContactsServiceID || src.ServiceName != connectors.GoogleContactsServiceName {
		t.Fatalf("ada source identity = %+v", src)
	}
	if src.ID != "people/c123" {
		t.Fatalf("ada source id = %q", src.ID)
	}
	if src.Extra["resource_name"] != "people/c123" {
		t.Fatalf("ada resource_name extra = %v", src.Extra)
	}
	grace := contacts[1]
	if grace.Name != "Grace Hopper" {
		t.Fatalf("grace name = %q", grace.Name)
	}
}

func TestGoogleContactsConnector_ListUserContacts_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{
		ID: connectors.GoogleContactsServiceID, Name: connectors.GoogleContactsServiceName, Action: "inject",
	}}}, "https://aegis.example.com")
	conn := connectors.NewGoogleContactsConnector(srv)
	_, _, err := conn.ListUserContacts(context.Background())
	if !errors.Is(err, types.ErrUnauthorized) {
		t.Fatalf("err = %v, want types.ErrUnauthorized", err)
	}
}

func TestGoogleContactsConnector_ListUserContacts_PaginatesAndTruncates(t *testing.T) {
	var pages int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		// Always emit another page so the connector hits connectors.LibraryPageCap.
		json.NewEncoder(w).Encode(map[string]any{
			"connections": []map[string]any{
				{
					"resourceName":   fmt.Sprintf("people/p%d", pages),
					"names":          []map[string]any{{"displayName": "Person"}},
					"emailAddresses": []map[string]any{{"value": fmt.Sprintf("p%d@example.com", pages)}},
				},
			},
			"nextPageToken": "more",
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: connectors.GoogleContactsServiceID, Name: connectors.GoogleContactsServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleContactsServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})

	conn := connectors.NewGoogleContactsConnector(srv)
	contacts, truncated, err := conn.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if !truncated {
		t.Fatal("expected truncated after page cap")
	}
	if len(contacts) != connectors.LibraryPageCap {
		t.Fatalf("got %d contacts, want %d", len(contacts), connectors.LibraryPageCap)
	}
}

func TestGoogleContactsConnector_HandleProxy_List(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"connections": []map[string]any{
				{
					"resourceName":   "people/c9",
					"names":          []map[string]any{{"displayName": "Ada"}},
					"emailAddresses": []map[string]any{{"value": "ada@example.com"}},
				},
			},
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: connectors.GoogleContactsServiceID, Name: connectors.GoogleContactsServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleContactsServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleContactsConnector(srv)

	req := httptest.NewRequest("GET", "/me/contacts", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/me/contacts")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || resp.Contacts[0].Name != "Ada" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestGoogleContactsConnector_HandleProxy_GetByID(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"resourceName":   "people/c9",
			"names":          []map[string]any{{"displayName": "Ada"}},
			"emailAddresses": []map[string]any{{"value": "ada@example.com"}},
		})
	}))
	defer upstream.Close()

	cfg := &Config{Services: []Service{{
		ID: connectors.GoogleContactsServiceID, Name: connectors.GoogleContactsServiceName, Action: "inject", Endpoint: upstream.URL,
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save(connectors.GoogleContactsServiceID, &OAuthToken{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewGoogleContactsConnector(srv)

	req := httptest.NewRequest("GET", "/contacts/people/c9", nil)
	w := httptest.NewRecorder()
	conn.HandleProxy(w, req, "/contacts/people/c9")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if gotPath != "/people/c9" {
		t.Fatalf("upstream path = %q, want /people/c9", gotPath)
	}
}

func TestNormalizeGooglePeopleResourceName(t *testing.T) {
	cases := map[string]string{
		"people/c123": "people/c123",
		"c123":        "people/c123",
		"/people/c9":  "people/c9",
		"":            "",
	}
	for in, want := range cases {
		if got := connectors.NormalizeGooglePeopleResourceName(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContactIDFromProxyPath(t *testing.T) {
	cases := map[string]string{
		"/contacts/people/c123": "people/c123",
		"/contacts/c123":        "c123",
		"/me/contacts/people/z": "people/z",
	}
	for path, want := range cases {
		got, ok := connectors.ContactIDFromProxyPath(path)
		if !ok || got != want {
			t.Errorf("connectors.ContactIDFromProxyPath(%q) = (%q,%v), want (%q,true)", path, got, ok, want)
		}
	}
	if _, ok := connectors.ContactIDFromProxyPath("/contacts/"); ok {
		t.Error("empty id should not match")
	}
}

func TestGoogleContactsConnector_SystemInstructions(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewGoogleContactsConnector(srv)
	instr := conn.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(instr, "/v1/library/contacts") {
		t.Fatalf("missing library endpoint: %s", instr)
	}
	if !strings.Contains(instr, "/v1/services/google") {
		t.Fatalf("missing proxy base: %s", instr)
	}
	if !strings.Contains(instr, "Aegis authenticates you") {
		t.Fatalf("missing authenticates wording: %s", instr)
	}
	if !strings.Contains(instr, "Photo.people") {
		t.Fatalf("missing Photo.people cross-type note: %s", instr)
	}
}

func TestGoogleContactsConnector_ImplementsContactProvider(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	var _ types.ContactProvider = connectors.NewGoogleContactsConnector(srv)
	var _ connectors.ServiceConnector = connectors.NewGoogleContactsConnector(srv)
}

func TestMergePhotoPeople_UnionsSharedIdentity(t *testing.T) {
	a := types.Contact{
		Name:    "Ada Lovelace",
		Emails:  []string{"ada@example.com"},
		Sources: []types.ContactSource{{Service: "google", ServiceName: "Google Contacts", ID: "people/c1"}},
	}
	b := types.Contact{
		Name:   "Ada L.",
		Emails: []string{"ADA@example.com"},
		Phones: []string{"+15551234567"},
		Sources: []types.ContactSource{
			{Service: "google-photos", ServiceName: "Google Photos", ID: "tag1"},
		},
	}
	merged := mergePhotoPeople([]types.Contact{a}, []types.Contact{b})
	if len(merged) != 1 {
		t.Fatalf("want 1 person, got %+v", merged)
	}
	if merged[0].Name != "Ada Lovelace" {
		t.Fatalf("name = %q", merged[0].Name)
	}
	if len(merged[0].Emails) != 1 || merged[0].Emails[0] != "ada@example.com" {
		t.Fatalf("emails = %v", merged[0].Emails)
	}
	if len(merged[0].Phones) != 1 {
		t.Fatalf("phones = %v", merged[0].Phones)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestMergePhotoPeople_DistinctPeopleStaySeparate(t *testing.T) {
	a := types.Contact{Name: "Alex Kim", Emails: []string{"a@example.com"}}
	b := types.Contact{Name: "Alex Kim", Emails: []string{"b@example.com"}}
	merged := mergePhotoPeople([]types.Contact{a}, []types.Contact{b})
	if len(merged) != 2 {
		t.Fatalf("distinct emails must not merge by name: %+v", merged)
	}
}

func TestMergePhotos_PreservesPeopleAcrossSources(t *testing.T) {
	p1 := types.NewPhoto("google-photos", "Google Photos", "p1", "a.jpg", "image/jpeg", "2024-01-01T00:00:00Z", 10, 10)
	p1.People = []types.Contact{{Name: "Ada", Emails: []string{"ada@example.com"}}}
	p2 := types.NewPhoto("icloud", "iCloud Photos", "p2", "a.jpg", "image/jpeg", "2024-01-01T00:00:00Z", 10, 10)
	p2.People = []types.Contact{
		{Name: "Ada", Emails: []string{"ada@example.com"}, Phones: []string{"+15550001111"}},
		{Name: "Grace", Emails: []string{"grace@example.com"}},
	}

	merged := MergePhotos([]types.Photo{p1, p2})
	if len(merged) != 1 {
		t.Fatalf("want 1 photo, got %d", len(merged))
	}
	people := merged[0].People
	if len(people) != 2 {
		t.Fatalf("people = %+v", people)
	}
}

func TestDataTypeForPath_PeopleAPI(t *testing.T) {
	cases := map[string]string{
		"/people/me/connections": "contacts",
		"/people/c123":           "contacts",
		"/people:searchContacts": "contacts",
	}
	for path, want := range cases {
		if got := types.DataTypeForPath(path); got != want {
			t.Errorf("types.DataTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestHasContactProviders_GoogleContacts(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		connectors.GoogleContactsServiceID: connectors.NewGoogleContactsConnector(srv),
	}
	if !kindHasProviders("contacts", srv) {
		t.Fatal("Google Contacts connector should register as ContactProvider")
	}
}
