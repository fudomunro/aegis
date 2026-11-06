package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
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

	host := newTestHostWithService(GoogleContactsServiceID, upstream.URL, "https://aegis.example.com")
	host.setToken(GoogleContactsServiceID, "test-token")

	conn := NewGoogleContactsConnector(host)
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
	if src.Service != GoogleContactsServiceID || src.ServiceName != GoogleContactsServiceName {
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
	host := newTestHostWithService(GoogleContactsServiceID, "", "https://aegis.example.com")
	conn := NewGoogleContactsConnector(host)
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
		// Always emit another page so the connector hits shared.LibraryPageCap.
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

	host := newTestHostWithService(GoogleContactsServiceID, upstream.URL, "https://aegis.example.com")
	host.setToken(GoogleContactsServiceID, "test-token")

	conn := NewGoogleContactsConnector(host)
	contacts, truncated, err := conn.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if !truncated {
		t.Fatal("expected truncated after page cap")
	}
	if len(contacts) != shared.LibraryPageCap {
		t.Fatalf("got %d contacts, want %d", len(contacts), shared.LibraryPageCap)
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

	host := newTestHostWithService(GoogleContactsServiceID, upstream.URL, "https://aegis.example.com")
	host.setToken(GoogleContactsServiceID, "test-token")
	conn := NewGoogleContactsConnector(host)

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

	host := newTestHostWithService(GoogleContactsServiceID, upstream.URL, "https://aegis.example.com")
	host.setToken(GoogleContactsServiceID, "test-token")
	conn := NewGoogleContactsConnector(host)

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
		if got := NormalizeGooglePeopleResourceName(in); got != want {
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
		got, ok := ContactIDFromProxyPath(path)
		if !ok || got != want {
			t.Errorf("ContactIDFromProxyPath(%q) = (%q,%v), want (%q,true)", path, got, ok, want)
		}
	}
	if _, ok := ContactIDFromProxyPath("/contacts/"); ok {
		t.Error("empty id should not match")
	}
}

func TestGoogleContactsConnector_SystemInstructions(t *testing.T) {
	conn := NewGoogleContactsConnector(newTestHost("https://aegis.example.com"))
	instr := conn.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(instr, "/v1/library/contacts") {
		t.Fatalf("missing library endpoint: %s", instr)
	}
	if !strings.Contains(instr, "Do not call Google People API directly") {
		t.Fatalf("missing direct-API warning: %s", instr)
	}
	if !strings.Contains(instr, "sources[]") {
		t.Fatalf("missing sources provenance note: %s", instr)
	}
}

func TestGoogleContactsConnector_ImplementsContactProvider(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	var _ types.ContactProvider = NewGoogleContactsConnector(host)
	var _ ServiceConnector = NewGoogleContactsConnector(host)
}
