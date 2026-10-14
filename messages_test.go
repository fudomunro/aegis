package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

type fakeMessageProvider struct {
	messages  []types.Message
	err       error
	truncated bool
}

func (f *fakeMessageProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakeMessageProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeMessageProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeMessageProvider) ListMessages(context.Context) ([]types.Message, bool, error) {
	return f.messages, f.truncated, f.err
}

func TestMessageIdentityKey_PrefersMessageID(t *testing.T) {
	m := newMessage("google-gmail", "Gmail", "id1", types.MessageChannelEmail, "Hi", "body", "2024-06-01T09:00:00Z")
	m.EntityID = "<AbC@example.com>"
	if got := messageIdentityKey(m); got != "msgid:abc@example.com" {
		t.Fatalf("key = %q", got)
	}
}

func TestMessageIdentityKey_SubjectFromTimeFallback(t *testing.T) {
	m := newMessage("google-gmail", "Gmail", "id1", types.MessageChannelEmail, "  Hello  ", "", "2024-06-01T09:00:00+00:00")
	from := types.NewContact(connectors.GoogleGmailServiceID, connectors.GoogleGmailServiceName, "", "Ada", []string{"Ada@Example.com"}, nil)
	m.From = &from
	if got := messageIdentityKey(m); got != "subject:hello|from:ada@example.com|sent:2024-06-01T09:00:00Z" {
		t.Fatalf("key = %q", got)
	}
}

func TestMergeMessages_ByMessageIDAndSubjectFrom(t *testing.T) {
	a := newMessage("google-gmail", "Gmail", "a1", types.MessageChannelEmail, "Invoice", "s", "2024-06-01T09:00:00Z")
	a.EntityID = "shared@example.com"
	fromA := types.Contact{Name: "Ada", Emails: []string{"ada@example.com"}}
	a.From = &fromA
	b := newMessage("outlook-mail", "Outlook Mail", "b1", types.MessageChannelEmail, "Different", "s", "2024-06-01T09:00:00Z")
	b.EntityID = "shared@example.com"
	c := newMessage("google-gmail", "Gmail", "c1", types.MessageChannelEmail, "Planning", "", "2024-06-02T14:00:00Z")
	fromC := types.Contact{Name: "Grace", Emails: []string{"grace@example.com"}}
	c.From = &fromC
	d := newMessage("outlook-mail", "Outlook Mail", "d1", types.MessageChannelEmail, "Planning", "", "2024-06-02T14:00:00+00:00")
	fromD := types.Contact{Name: "Grace L", Emails: []string{"GRACE@example.com"}, Phones: []string{"+1 555 0100"}}
	d.From = &fromD

	merged := MergeMessages([]types.Message{a, b, c, d})
	if len(merged) != 2 {
		t.Fatalf("merged = %d messages: %+v", len(merged), merged)
	}
	// Newest first: Planning (June 2) then Invoice (June 1).
	if merged[0].Subject != "Planning" || len(merged[0].Sources) != 2 || merged[0].Match != "subject_from_time" {
		t.Fatalf("first = %+v", merged[0])
	}
	if merged[0].From == nil || len(merged[0].From.Emails) != 1 || merged[0].From.Emails[0] != "grace@example.com" {
		t.Fatalf("merged from = %+v", merged[0].From)
	}
	if merged[1].Subject != "Invoice" || len(merged[1].Sources) != 2 || merged[1].Match != "message_id" {
		t.Fatalf("second = %+v", merged[1])
	}
}

func TestMergeMessages_DistinctWhenDifferentIdentity(t *testing.T) {
	a := newMessage("google-gmail", "Gmail", "a", types.MessageChannelEmail, "Meeting", "", "2024-06-01T09:00:00Z")
	fromA := types.Contact{Emails: []string{"a@example.com"}}
	a.From = &fromA
	b := newMessage("google-gmail", "Gmail", "b", types.MessageChannelEmail, "Meeting", "", "2024-06-01T09:00:00Z")
	fromB := types.Contact{Emails: []string{"b@example.com"}}
	b.From = &fromB
	merged := MergeMessages([]types.Message{a, b})
	if len(merged) != 2 {
		t.Fatalf("merged = %d, want 2", len(merged))
	}
}

func TestHandleLibraryMessages_AggregatesServices(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-gmail", Name: "Gmail", Action: "inject"},
		{ID: "outlook-mail", Name: "Outlook Mail", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	a := newMessage("google-gmail", "Gmail", "g1", types.MessageChannelEmail, "Standup notes", "notes", "2024-06-01T09:00:00Z")
	a.EntityID = "standup@example.com"
	from := types.Contact{Emails: []string{"ada@example.com"}}
	a.From = &from
	b := newMessage("outlook-mail", "Outlook Mail", "o1", types.MessageChannelEmail, "Standup notes", "notes", "2024-06-01T09:00:00Z")
	b.EntityID = "standup@example.com"
	c := newMessage("google-gmail", "Gmail", "g2", types.MessageChannelEmail, "Other", "", "2024-06-02T10:00:00Z")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{messages: []types.Message{a, c}},
		"outlook-mail": &fakeMessageProvider{messages: []types.Message{b}},
	}

	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMessages)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d messages=%+v", resp.Total, resp.Messages)
	}
	if resp.ByService["google-gmail"].Count != 2 || resp.ByService["outlook-mail"].Count != 1 {
		t.Fatalf("by_service = %+v", resp.ByService)
	}
	var standup *types.Message
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
		"google-gmail": &fakeMessageProvider{messages: []types.Message{
			newMessage("google-gmail", "Gmail", "g1", types.MessageChannelEmail, "Hello", "", "2024-06-01T09:00:00Z"),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMessages)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.MessageList
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
	srv.AuthMiddleware(srv.HandleLibraryMessages)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleLibraryMessages_DeniedByPermission(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "google-gmail",
		Name:        "Gmail",
		Action:      "inject",
		Permissions: types.Permissions{"messages": types.AccessNone},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{messages: []types.Message{
			newMessage("google-gmail", "Gmail", "g1", types.MessageChannelEmail, "Hello", "", "2024-06-01T09:00:00Z"),
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMessages)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleLibraryMessages_UnauthorizedService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "google-gmail", Name: "Gmail", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"google-gmail": &fakeMessageProvider{err: types.ErrUnauthorized},
	}
	req := httptest.NewRequest("GET", "/v1/library/messages", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMessages)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["google-gmail"].Status != types.LibraryStatusUnauthorized {
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
	srv.AuthMiddleware(srv.HandleLibraryMessages)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.MessageList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["google-gmail"].Status != types.LibraryStatusError {
		t.Fatalf("by_service = %+v", resp.ByService)
	}
}

func TestHandleLibraryMessages_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("POST", "/v1/library/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryMessages)(w, req)
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
	} {
		if !strings.Contains(instr, want) {
			t.Fatalf("missing %q in instructions:\n%s", want, instr)
		}
	}
}

func TestStampMessageCanonical_OnlyWhenConfigured(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-gmail", Name: "Gmail", Action: "inject", CanonicalFor: []string{"messages"}},
		{ID: "other-mail", Name: "Other", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	items := []types.Message{
		{Sources: []types.MessageSource{{Service: "google-gmail"}}},
		{Sources: []types.MessageSource{{Service: "other-mail"}}},
	}
	srv.stampMessageCanonical(items, "google-gmail")
	srv.stampMessageCanonical(items, "other-mail")
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
