package core

import (
	"github.com/fudomunro/aegis/internal/types"

	"github.com/fudomunro/aegis/internal/connectors"

	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func preRequestWithMessage(t *testing.T, srv *Server, userMessage string) GuardrailResponse {
	t.Helper()
	originalReq := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": userMessage},
		},
	}
	reqBody, err := json.Marshal(originalReq)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-photo"},
		UserMessage: userMessage,
		RequestBody: reqBody,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func injectedSystemText(t *testing.T, resp GuardrailResponse) string {
	t.Helper()
	if resp.Action != ActionModify {
		t.Fatalf("action = %q, want %q (message=%q)", resp.Action, ActionModify, resp.Message)
	}
	var modified map[string]any
	if err := json.Unmarshal(resp.RequestBody, &modified); err != nil {
		t.Fatalf("unmarshal modified body: %v", err)
	}
	msgs, ok := modified["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatal("modified body missing messages")
	}
	sysMsg, ok := msgs[0].(map[string]any)
	if !ok || sysMsg["role"] != "system" {
		t.Fatalf("first message = %#v, want system", msgs[0])
	}
	content, _ := sysMsg["content"].(string)
	return content
}

func photoTestServer() *Server {
	cfg := &Config{
		Services: []Service{
			{
				ID:     "immich",
				Name:   "Immich",
				Action: "inject",
				Aliases: []string{
					"immich photos", "self-hosted photos", "immich",
					"photo", "photos", "picture", "pictures", "photo library",
				},
			},
			{
				ID:      "google-photos",
				Name:    "Google Photos",
				Action:  "inject",
				Aliases: []string{"photos", "photo", "pictures", "picture", "google photos library"},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["immich"] = connectors.NewImmichConnector(srv)
	srv.connectors["google-photos"] = connectors.NewGooglePhotosConnector(srv)
	return srv
}

// Issue #125: singular "photo" previously matched neither service aliases nor
// library keywords, so the hook fail-opened with zero Aegis guidance.
func TestHandlePreRequest_OldestPhotoInCollection(t *testing.T) {
	srv := photoTestServer()
	resp := preRequestWithMessage(t, srv, "What's the oldest photo in my collection?")
	content := injectedSystemText(t, resp)

	for _, want := range []string{
		"/v1/library/photos",
		"/v1/services/immich",
		types.PhotoSourceDiscipline(),
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("system message missing %q:\n%s", want, content)
		}
	}
}

func TestHandlePreRequest_DescribeMyOldestPhoto(t *testing.T) {
	srv := photoTestServer()
	resp := preRequestWithMessage(t, srv, "Describe my oldest photo.")
	content := injectedSystemText(t, resp)

	if !strings.Contains(content, "/v1/library/photos") {
		t.Fatalf("system message missing library photos endpoint:\n%s", content)
	}
	if !strings.Contains(content, "Photos Library.photoslibrary") {
		t.Fatalf("system message missing photo source discipline:\n%s", content)
	}
}

func TestHandlePreRequest_PhotoLibraryHintsAlongsideServiceMatch(t *testing.T) {
	srv := photoTestServer()
	resp := preRequestWithMessage(t, srv, "How many photos do I have?")
	content := injectedSystemText(t, resp)

	if !strings.Contains(content, "https://aegis.example.com/v1/services/immich") {
		t.Fatalf("expected Immich proxy instructions:\n%s", content)
	}
	if !strings.Contains(content, "https://aegis.example.com/v1/library/photos") {
		t.Fatalf("expected library photos hints even when a service matched:\n%s", content)
	}
}

func TestPhotoSourceDiscipline_InConnectorInstructions(t *testing.T) {
	srv := photoTestServer()
	discipline := types.PhotoSourceDiscipline()
	if !strings.Contains(srv.connectors["immich"].SystemInstructions("https://aegis.example.com"), discipline) {
		t.Fatal("Immich instructions missing photo source discipline")
	}
	if !strings.Contains(srv.connectors["google-photos"].SystemInstructions("https://aegis.example.com"), discipline) {
		t.Fatal("Google Photos instructions missing photo source discipline")
	}
}

func TestPhotoSourceDiscipline_MentionsBypassPaths(t *testing.T) {
	s := types.PhotoSourceDiscipline()
	for _, want := range []string{
		"Photos Library.photoslibrary",
		"Photo Booth",
		":2283",
		"Docker/Postgres",
		"Aegis owns photo credentials",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("photoSourceDiscipline missing %q: %s", want, s)
		}
	}
}

func TestImmichSetup_DefaultAliasesMatchPhotoQuestions(t *testing.T) {
	srv := photoTestServer()
	// Mirrors immich_setup.go entry aliases used when saving from the admin UI.
	cfg := &Config{Services: []Service{{
		ID:     connectors.ImmichServiceID,
		Name:   "Immich",
		Action: "inject",
		Aliases: []string{
			"immich photos", "self-hosted photos", "immich",
			"photo", "photos", "picture", "pictures", "photo library",
		},
	}}}
	srv.config = cfg
	for _, msg := range []string{
		"What's the oldest photo in my collection?",
		"Describe my oldest photo.",
		"How many photos do I have?",
	} {
		matched := cfg.FindMatchingServices(msg)
		if len(matched) == 0 {
			t.Fatalf("message %q matched no services", msg)
		}
	}
}

func TestLibraryKind_PhotosMentionsSingularForms(t *testing.T) {
	k := LibraryKindByID("photos")
	if k == nil {
		t.Fatal("photos kind not registered")
	}
	for _, msg := range []string{
		"What's the oldest photo in my collection?",
		"Describe my oldest photo.",
		"How many photos do I have?",
		"list my pictures",
	} {
		if !k.Mentions(msg) {
			t.Fatalf("photos kind failed to mention %q", msg)
		}
	}
}
