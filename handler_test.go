package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// testServer creates a Server with the given config and cache for testing.
func testServer(cfg *Config) *Server {
	return &Server{
		apiKey: "test-key",
		config: cfg,
		cache:  NewCache(),
	}
}

func TestAuthMiddleware_ValidBearer(t *testing.T) {
	srv := testServer(&Config{})
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
	srv := testServer(&Config{})
	called := false
	inner := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
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
	srv := testServer(&Config{})
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

func TestHandlePreRequest_NoRulesMatch(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{Keywords: []string{"patient"}}, Action: "inject"},
		},
	}
	srv := testServer(cfg)

	payload := PreRequestPayload{
		Metadata: RequestMetadata{
			LoginName: "alice",
			Model:     "gpt-4",
			RequestID: "req-1",
		},
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

func TestHandlePreRequest_BlockRule(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "pii-block", Match: Match{Keywords: []string{"social security"}}, Action: "block", Description: "PII guard"},
		},
	}
	srv := testServer(cfg)

	payload := PreRequestPayload{
		Metadata: RequestMetadata{
			LoginName: "alice",
			Model:     "gpt-4",
			RequestID: "req-2",
		},
		UserMessage: "What is the social security number for this patient?",
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

func TestHandlePreRequest_InjectRule(t *testing.T) {
	// Set up a mock API source.
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "Patient data: John Doe, age 45")
	}))
	defer mockAPI.Close()

	cfg := &Config{
		Sources: []Source{
			{ID: "kb", Type: "api", URL: mockAPI.URL, CacheTTL: Duration{Duration: 0}},
		},
		Rules: []Rule{
			{ID: "enrich", Match: Match{Keywords: []string{"patient"}}, Sources: []string{"kb"}, Action: "inject"},
		},
	}
	srv := testServer(cfg)

	originalReq := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "Tell me about the patient record"},
		},
	}
	reqBody, _ := json.Marshal(originalReq)

	payload := PreRequestPayload{
		Metadata: RequestMetadata{
			LoginName: "alice",
			Model:     "gpt-4",
			RequestID: "req-3",
		},
		UserMessage: "Tell me about the patient record",
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

	// Verify the modified body has the injected system message.
	var modified map[string]any
	if err := json.Unmarshal(resp.Body, &modified); err != nil {
		t.Fatalf("failed to parse modified body: %v", err)
	}

	msgs, ok := modified["messages"].([]any)
	if !ok {
		t.Fatal("messages not found in modified body")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("first message role = %q, want %q", first["role"], "system")
	}
	content := first["content"].(string)
	if content == "" {
		t.Fatal("injected system message content is empty")
	}
}

func TestHandlePreRequest_SourceFetchError(t *testing.T) {
	// Source points to a dead server — should fail open (allow).
	cfg := &Config{
		Sources: []Source{
			{ID: "dead", Type: "api", URL: "http://127.0.0.1:1"},
		},
		Rules: []Rule{
			{ID: "r1", Match: Match{Keywords: []string{"test"}}, Sources: []string{"dead"}, Action: "inject"},
		},
	}
	srv := testServer(cfg)

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-4"},
		UserMessage: "test message",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Action != ActionAllow {
		t.Fatalf("action = %q, want %q (fail open)", resp.Action, ActionAllow)
	}
}

func TestHandlePreRequest_InvalidPayload(t *testing.T) {
	srv := testServer(&Config{})

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader([]byte("{bad json")))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Action != ActionAllow {
		t.Fatalf("action = %q, want %q (fail open on bad payload)", resp.Action, ActionAllow)
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

	// Verify system message is first.
	msgs := result["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	sysMsg := msgs[0].(map[string]any)
	if sysMsg["role"] != "system" {
		t.Fatalf("first message role = %q, want %q", sysMsg["role"], "system")
	}
	if sysMsg["content"] != "injected context" {
		t.Fatalf("content = %q, want %q", sysMsg["content"], "injected context")
	}

	// Original user message preserved.
	userMsg := msgs[1].(map[string]any)
	if userMsg["role"] != "user" {
		t.Fatalf("second message role = %q, want %q", userMsg["role"], "user")
	}

	// Other fields preserved.
	if result["model"] != "gpt-4" {
		t.Fatalf("model = %q, want %q", result["model"], "gpt-4")
	}
	if result["temperature"] != 0.7 {
		t.Fatalf("temperature = %v, want 0.7", result["temperature"])
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
		t.Fatalf("content-type = %q, want application/json", ct)
	}
}

func TestWriteGuardrailResponse_Block(t *testing.T) {
	w := httptest.NewRecorder()
	writeGuardrailResponse(w, GuardrailResponse{Action: ActionBlock, Message: "denied"})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
