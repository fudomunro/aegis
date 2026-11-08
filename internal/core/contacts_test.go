package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"

	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

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
	srv.connectors = map[string]connectors.ServiceConnector{
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
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
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
				GrantLimits: policy.Permissions{"contacts": policy.AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
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
	srv.connectors = map[string]connectors.ServiceConnector{
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
	srv.connectors = map[string]connectors.ServiceConnector{
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
	srv.connectors = map[string]connectors.ServiceConnector{
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
	srv.connectors = map[string]connectors.ServiceConnector{}
	if kindHasProviders("contacts", srv) {
		t.Fatal("empty connectors should have no contact providers")
	}
	srv.connectors["google"] = &fakeContactProvider{}
	if !kindHasProviders("contacts", srv) {
		t.Fatal("fake provider should register as contact provider")
	}
}
