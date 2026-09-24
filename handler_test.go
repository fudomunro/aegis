package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthMiddleware_ValidBearer(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	called := false
	inner := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}

	handler := srv.AuthMiddleware(inner)
	req := httptest.NewRequest("POST", "/v1/pre-request", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	handler(w, req)

	if !called {
		t.Fatal("expected inner handler to be called")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestAuthMiddleware_ValidAPIKey(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	called := false
	inner := func(w http.ResponseWriter, r *http.Request) {
		called = true
	}

	handler := srv.AuthMiddleware(inner)
	req := httptest.NewRequest("POST", "/v1/pre-request", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()

	handler(w, req)

	if !called {
		t.Fatal("expected inner handler to be called")
	}
}

func TestAuthMiddleware_InvalidAuth(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	inner := func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	}

	handler := srv.AuthMiddleware(inner)
	req := httptest.NewRequest("POST", "/v1/pre-request", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandlePreRequest_NoServiceMatch(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-1"},
		UserMessage: "Hello, how are you?",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Action != ActionAllow {
		t.Fatalf("action = %q, want %q", resp.Action, ActionAllow)
	}
}

func TestHandlePreRequest_BlockService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "tidal", Name: "Tidal", Action: "block", Description: "not permitted"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-2"},
		UserMessage: "Play my Tidal playlist",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Action != ActionBlock {
		t.Fatalf("action = %q, want %q", resp.Action, ActionBlock)
	}
	if resp.Message == "" {
		t.Fatal("expected non-empty block message")
	}
}

func TestHandlePreRequest_InjectService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	originalReq := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "Play some jazz on Spotify"},
		},
	}
	reqBody, _ := json.Marshal(originalReq)

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-3"},
		UserMessage: "Play some jazz on Spotify",
		RequestBody: reqBody,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Action != ActionModify {
		t.Fatalf("action = %q, want %q", resp.Action, ActionModify)
	}

	// Verify the modified body has proxy instructions.
	var modified map[string]any
	json.Unmarshal(resp.RequestBody, &modified)

	msgs := modified["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	sysMsg := msgs[0].(map[string]any)
	if sysMsg["role"] != "system" {
		t.Fatalf("first message role = %q, want %q", sysMsg["role"], "system")
	}
	content := sysMsg["content"].(string)
	if !strings.Contains(content, "https://aegis.example.com/v1/services/spotify") {
		t.Fatalf("system message missing proxy URL: %q", content)
	}
	if !strings.Contains(content, "Spotify") {
		t.Fatalf("system message missing service name: %q", content)
	}
}

func TestHandlePreRequest_AliasMatch(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "ytm", Name: "YouTube Music", Aliases: []string{"yt music", "ytmusic"}, Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-4"},
		UserMessage: "find my yt music playlists",
		RequestBody: []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"find my yt music playlists"}]}`),
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Action != ActionModify {
		t.Fatalf("action = %q, want %q", resp.Action, ActionModify)
	}

	var modified map[string]any
	json.Unmarshal(resp.RequestBody, &modified)
	sysMsg := modified["messages"].([]any)[0].(map[string]any)
	content := sysMsg["content"].(string)
	if !strings.Contains(content, "/v1/services/ytm") {
		t.Fatalf("system message missing ytm proxy URL: %q", content)
	}
}

func TestHandlePreRequest_InjectNoEndpoint(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-5"},
		UserMessage: "play Spotify",
		RequestBody: []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"play Spotify"}]}`),
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	// Even without an endpoint, the handler injects proxy instructions
	// (the proxy endpoint itself will return 502 if no upstream is configured).
	if resp.Action != ActionModify {
		t.Fatalf("action = %q, want %q", resp.Action, ActionModify)
	}
}

func TestHandlePreRequest_InvalidPayload(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Action != ActionAllow {
		t.Fatalf("action = %q, want %q", resp.Action, ActionAllow)
	}
}

func TestHandlePreRequest_BlockTakesPriorityOverInject(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "block"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-6"},
		UserMessage: "play Spotify",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandlePreRequest_MultipleServices(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "ytm", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-7"},
		UserMessage: "compare my Spotify and YouTube Music libraries",
		RequestBody: []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"compare"}]}`),
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Action != ActionModify {
		t.Fatalf("action = %q, want %q", resp.Action, ActionModify)
	}

	var modified map[string]any
	json.Unmarshal(resp.RequestBody, &modified)
	sysMsg := modified["messages"].([]any)[0].(map[string]any)
	content := sysMsg["content"].(string)

	if !strings.Contains(content, "/v1/services/spotify") {
		t.Fatalf("missing spotify proxy URL: %q", content)
	}
	if !strings.Contains(content, "/v1/services/ytm") {
		t.Fatalf("missing ytm proxy URL: %q", content)
	}
}

func TestInjectSystemMessage(t *testing.T) {
	original := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"temperature": 0.7,
	}
	origBytes, _ := json.Marshal(original)

	modified, err := injectSystemMessage(origBytes, "injected context")
	if err != nil {
		t.Fatalf("injectSystemMessage: %v", err)
	}

	var result map[string]any
	json.Unmarshal(modified, &result)

	msgs := result["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	sysMsg := msgs[0].(map[string]any)
	if sysMsg["role"] != "system" || sysMsg["content"] != "injected context" {
		t.Fatalf("system message = %v", sysMsg)
	}

	userMsg := msgs[1].(map[string]any)
	if userMsg["role"] != "user" {
		t.Fatalf("user message role = %q", userMsg["role"])
	}

	if result["model"] != "gpt-4" {
		t.Fatalf("model = %q", result["model"])
	}
	if result["temperature"] != 0.7 {
		t.Fatalf("temperature = %v", result["temperature"])
	}
}

func TestInjectSystemMessage_InvalidJSON(t *testing.T) {
	_, err := injectSystemMessage([]byte("{bad"), "test")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestWriteGuardrailResponse_Allow(t *testing.T) {
	w := httptest.NewRecorder()
	writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestWriteGuardrailResponse_Block(t *testing.T) {
	w := httptest.NewRecorder()
	writeGuardrailResponse(w, GuardrailResponse{Action: ActionBlock, Message: "denied"})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandlePreRequest_IncludesPermissionNotice(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID: "spotify", Name: "Spotify", Action: "inject",
				Permissions: Permissions{"default": AccessRead, "player": AccessNone, "recommendations": AccessNone},
			},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	originalReq := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "Play some jazz on Spotify"},
		},
	}
	reqBody, _ := json.Marshal(originalReq)
	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-perm"},
		UserMessage: "Play some jazz on Spotify",
		RequestBody: reqBody,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Action != ActionModify {
		t.Fatalf("action = %q, want modify", resp.Action)
	}
	var modified map[string]any
	json.Unmarshal(resp.RequestBody, &modified)
	msgs := modified["messages"].([]any)
	sys := msgs[0].(map[string]any)
	content := sys["content"].(string)
	if !strings.Contains(content, "Access policy:") {
		t.Fatalf("system message missing access policy notice:\n%s", content)
	}
	if !strings.Contains(content, "player") || !strings.Contains(content, "recommendations") {
		t.Fatalf("notice should list denied types:\n%s", content)
	}
}
