package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

type fakeMessageProvider struct {
	messages  []kinds.Message
	err       error
	truncated bool
}

func (f *fakeMessageProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakeMessageProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeMessageProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeMessageProvider) ListMessages(context.Context) ([]kinds.Message, bool, error) {
	return f.messages, f.truncated, f.err
}

func TestHandleLibraryMessages_AggregatesServices(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-gmail", Name: "Gmail", Action: "inject"},
		{ID: "outlook-mail", Name: "Outlook Mail", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	a := newMessage("google-gmail", "Gmail", "g1", kinds.MessageChannelEmail, "Standup notes", "notes", "2024-06-01T09:00:00Z")
	a.EntityID = "standup@example.com"
	from := kinds.Contact{Emails: []string{"ada@example.com"}}
	a.From = &from
	b := newMessage("outlook-mail", "Outlook Mail", "o1", kinds.MessageChannelEmail, "Standup notes", "notes", "2024-06-01T09:00:00Z")
	b.EntityID = "standup@example.com"
	c := newMessage("google-gmail", "Gmail", "g2", kinds.MessageChannelEmail, "Other", "", "2024-06-02T10:00:00Z")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{messages: []kinds.Message{a, c}},
		"outlook-mail": &fakeMessageProvider{messages: []kinds.Message{b}},
	}

	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("messages"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d messages=%+v", resp.Total, resp.Messages)
	}
	if resp.ByService["google-gmail"].Count != 2 || resp.ByService["outlook-mail"].Count != 1 {
		t.Fatalf("by_service = %+v", resp.ByService)
	}
	var standup *kinds.Message
	for i := range resp.Messages {
		if resp.Messages[i].Subject == "Standup notes" {
			standup = &resp.Messages[i]
		}
	}
	if standup == nil || len(standup.Sources) != 2 || standup.Match != "message_id" {
		t.Fatalf("standup = %+v", standup)
	}
}

func TestHandleLibraryMessages_CanonicalStamp(t *testing.T) {
	cfg := &Config{Services: []Service{
		{
			ID:           "google-gmail",
			Name:         "Gmail",
			Action:       "inject",
			CanonicalFor: []string{"messages"},
		},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{messages: []kinds.Message{
			newMessage("google-gmail", "Gmail", "g1", kinds.MessageChannelEmail, "Hello", "", "2024-06-01T09:00:00Z"),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("messages"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != 1 || len(resp.Messages[0].Sources) != 1 || !resp.Messages[0].Sources[0].Canonical {
		t.Fatalf("messages = %+v", resp.Messages)
	}
}

func TestHandleLibraryMessages_NoProviders(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("messages"))(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleLibraryMessages_DeniedByPermission(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"messages": {Default: shared.AccessNone}},
		Services: []Service{{
			ID:     "google-gmail",
			Name:   "Gmail",
			Action: "inject",
		}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{messages: []kinds.Message{
			newMessage("google-gmail", "Gmail", "g1", kinds.MessageChannelEmail, "Hello", "", "2024-06-01T09:00:00Z"),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("messages"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryMessages_UnauthorizedService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "google-gmail", Name: "Gmail", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{err: kinds.ErrUnauthorized},
	}
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("messages"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["google-gmail"].Status != kinds.LibraryStatusUnauthorized {
		t.Fatalf("status = %+v", resp.ByService)
	}
}

func TestHandleLibraryMessages_ErrorService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "google-gmail", Name: "Gmail", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{err: errors.New("boom")},
	}
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("messages"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["google-gmail"].Status != kinds.LibraryStatusError {
		t.Fatalf("by_service = %+v", resp.ByService)
	}
}

func TestHandleLibraryMessages_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("POST", "/v1/library/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("messages"))(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestLibraryKindMessages_Registered(t *testing.T) {
	k := LibraryKindByID("messages")
	if k == nil {
		t.Fatal("messages library kind not registered")
	}
	if k.Path != "/v1/library/messages" || k.PermType != "messages" {
		t.Fatalf("kind = %+v", k)
	}
	if !k.Mentions("check my email") {
		t.Fatal("expected messages keyword match")
	}
	handlers := (&Server{}).libraryRouteHandlers()
	if _, ok := handlers["/v1/library/messages"]; !ok {
		t.Fatal("messages route not registered")
	}
}

func TestLibraryMessagesInstructions_MentionEndpoint(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	instr := srv.libraryInstructionsFor("messages")
	for _, want := range []string{
		"/v1/library/messages",
		"messages",
		"canonical",
		"sort=asc",
		"include_body",
		"/v1/library/messages/{sources[].service}/{sources[].id}",
	} {
		if !strings.Contains(instr, want) {
			t.Fatalf("missing %q in instructions:\n%s", want, instr)
		}
	}
	// One search path and one fetch path (issue #311): the retired split
	// surface must not be advertised.
	for _, gone := range []string{"/v1/mail/", "/v1/services/"} {
		if strings.Contains(instr, gone) {
			t.Fatalf("instructions still name %q:\n%s", gone, instr)
		}
	}
}

func TestStampMessageCanonical_OnlyWhenConfigured(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-gmail", Name: "Gmail", Action: "inject", CanonicalFor: []string{"messages"}},
		{ID: "other-mail", Name: "Other", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	items := []kinds.Message{
		{Sources: []kinds.MessageSource{{Service: "google-gmail"}}},
		{Sources: []kinds.MessageSource{{Service: "other-mail"}}},
	}
	mark, _ := kinds.Stamps(srv, "messages", forEachMessageSource)
	mark(items, "google-gmail")
	mark(items, "other-mail")
	if !items[0].Sources[0].Canonical {
		t.Fatal("google-gmail should be canonical when configured")
	}
	if items[1].Sources[0].Canonical {
		t.Fatal("other-mail should not be canonical without config")
	}
	if items[0].CanonicalService() != "google-gmail" {
		t.Fatalf("CanonicalService = %q", items[0].CanonicalService())
	}
}

func TestMentionsMessages_Keyword(t *testing.T) {
	if !LibraryKindByID("messages").Mentions("what emails have I received?") {
		t.Fatal("expected email keyword match")
	}
	if LibraryKindByID("messages").Mentions("play some jazz") {
		t.Fatal("unexpected match")
	}
}

func TestHasMessageProviders_UsesRegistry(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{ID: "google-gmail", Name: "Gmail", Action: "inject"}}}, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{},
	}
	if !kindHasProviders("messages", srv) {
		t.Fatal("expected message provider")
	}
	if !kindReadable("messages", srv) {
		t.Fatal("expected readable")
	}
}
