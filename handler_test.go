package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestInjectRequestInstructions_Anthropic(t *testing.T) {
	original := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 1024,
		"system":     "You are helpful.",
		"messages": []map[string]string{
			{"role": "user", "content": "play Spotify"},
		},
	}
	origBytes, _ := json.Marshal(original)

	res, err := injectRequestInstructions(origBytes, "USE AEGIS PROXY", "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("injectRequestInstructions: %v", err)
	}
	if res.Shape != bodyShapeAnthropic {
		t.Fatalf("shape = %q, want anthropic", res.Shape)
	}
	if res.AlreadyPresent {
		t.Fatal("expected fresh injection")
	}

	var result map[string]any
	if err := json.Unmarshal(res.Body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sys, _ := result["system"].(string)
	if !strings.Contains(sys, "USE AEGIS PROXY") || !strings.Contains(sys, "You are helpful.") {
		t.Fatalf("system = %q", sys)
	}
	// Anthropic must not grow a role:system entry inside messages.
	msgs := result["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages[0] = %v", msgs[0])
	}
}

func TestInjectRequestInstructions_AnthropicSystemBlocks(t *testing.T) {
	original := map[string]any{
		"model": "claude-sonnet-4-5",
		"system": []map[string]string{
			{"type": "text", "text": "Existing system."},
		},
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}
	origBytes, _ := json.Marshal(original)

	res, err := injectRequestInstructions(origBytes, "USE AEGIS PROXY", "", "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("injectRequestInstructions: %v", err)
	}
	if res.Shape != bodyShapeAnthropic {
		t.Fatalf("shape = %q, want anthropic", res.Shape)
	}

	var result map[string]any
	if err := json.Unmarshal(res.Body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	blocks, ok := result["system"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("system blocks = %#v", result["system"])
	}
	first := blocks[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "USE AEGIS PROXY" {
		t.Fatalf("first block = %v", first)
	}
}

func TestInjectRequestInstructions_Responses(t *testing.T) {
	original := map[string]any{
		"model": "gpt-4o",
		"input": []map[string]string{
			{"role": "user", "content": "play Spotify"},
		},
	}
	origBytes, _ := json.Marshal(original)

	res, err := injectRequestInstructions(origBytes, "USE AEGIS PROXY", "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("injectRequestInstructions: %v", err)
	}
	if res.Shape != bodyShapeResponses {
		t.Fatalf("shape = %q, want responses", res.Shape)
	}

	var result map[string]any
	if err := json.Unmarshal(res.Body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items := result["input"].([]any)
	if len(items) != 2 {
		t.Fatalf("input len = %d, want 2", len(items))
	}
	sys := items[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "USE AEGIS PROXY" {
		t.Fatalf("input[0] = %v", sys)
	}
	if _, ok := result["messages"]; ok {
		t.Fatal("responses body must not gain a messages key")
	}
}

func TestInjectRequestInstructions_ResponsesStringInput(t *testing.T) {
	original := map[string]any{
		"model": "gpt-4o",
		"input": "play Spotify",
	}
	origBytes, _ := json.Marshal(original)

	res, err := injectRequestInstructions(origBytes, "USE AEGIS PROXY", "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("injectRequestInstructions: %v", err)
	}
	if res.Shape != bodyShapeResponses {
		t.Fatalf("shape = %q, want responses", res.Shape)
	}

	var result map[string]any
	if err := json.Unmarshal(res.Body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items := result["input"].([]any)
	if len(items) != 2 {
		t.Fatalf("input len = %d, want 2", len(items))
	}
	if items[0].(map[string]any)["role"] != "system" {
		t.Fatalf("input[0] = %v", items[0])
	}
	user := items[1].(map[string]any)
	if user["role"] != "user" || user["content"] != "play Spotify" {
		t.Fatalf("input[1] = %v", user)
	}
}

func TestInjectRequestInstructions_ResponsesNoInputUsesInstructions(t *testing.T) {
	original := map[string]any{
		"model":                "gpt-4o",
		"previous_response_id": "resp_123",
	}
	origBytes, _ := json.Marshal(original)

	res, err := injectRequestInstructions(origBytes, "USE AEGIS PROXY", "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("injectRequestInstructions: %v", err)
	}
	if res.Shape != bodyShapeResponses {
		t.Fatalf("shape = %q, want responses", res.Shape)
	}
	var result map[string]any
	if err := json.Unmarshal(res.Body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["instructions"] != "USE AEGIS PROXY" {
		t.Fatalf("instructions = %v", result["instructions"])
	}
}

func TestInjectRequestInstructions_Idempotent(t *testing.T) {
	original := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "play Spotify"},
		},
	}
	origBytes, _ := json.Marshal(original)
	const content = "USE AEGIS PROXY"

	first, err := injectRequestInstructions(origBytes, content, "", "")
	if err != nil {
		t.Fatalf("first inject: %v", err)
	}
	if first.AlreadyPresent {
		t.Fatal("first inject should modify")
	}

	second, err := injectRequestInstructions(first.Body, content, "", "")
	if err != nil {
		t.Fatalf("second inject: %v", err)
	}
	if !second.AlreadyPresent {
		t.Fatal("second inject should detect already-present instructions")
	}
	if string(second.Body) != string(first.Body) {
		t.Fatalf("second inject mutated body:\n%s\n%s", second.Body, first.Body)
	}

	// Anthropic idempotency
	anth := map[string]any{
		"model":    "claude-sonnet-4-5",
		"system":   content,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	anthBytes, _ := json.Marshal(anth)
	again, err := injectRequestInstructions(anthBytes, content, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("anthropic re-inject: %v", err)
	}
	if !again.AlreadyPresent {
		t.Fatal("anthropic re-inject should be idempotent")
	}
}

func TestInjectRequestInstructions_UnrecognizedShape(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"foo":"bar"}`,
		`[1,2,3]`,
		`"just a string"`,
	} {
		res, err := injectRequestInstructions([]byte(body), "USE AEGIS PROXY", "openai", "gpt-4o")
		if !errors.Is(err, errUnrecognizedBodyShape) {
			t.Fatalf("body %s: err = %v, want errUnrecognizedBodyShape", body, err)
		}
		if res.Shape != bodyShapeUnknown {
			t.Fatalf("body %s: shape = %q", body, res.Shape)
		}
	}
}

func TestDetectBodyShape(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		provider string
		model    string
		want     string
	}{
		{"chat", `{"messages":[{"role":"user","content":"hi"}]}`, "", "gpt-4", bodyShapeChat},
		{"anthropic_system", `{"system":"s","messages":[]}`, "anthropic", "claude-x", bodyShapeAnthropic},
		{"anthropic_meta", `{"messages":[]}`, "anthropic", "claude-x", bodyShapeAnthropic},
		{"anthropic_model_only", `{"messages":[]}`, "", "claude-3-5-sonnet", bodyShapeAnthropic},
		{"responses", `{"input":"hi"}`, "openai", "gpt-4o", bodyShapeResponses},
		{"responses_prev", `{"previous_response_id":"resp_1"}`, "openai", "gpt-4o", bodyShapeResponses},
		{"responses_instructions", `{"instructions":"sys"}`, "openai", "gpt-4o", bodyShapeResponses},
		{"unknown", `{"foo":1}`, "", "", bodyShapeUnknown},
		{"unknown_model_only", `{"model":"gpt-4o"}`, "openai", "gpt-4o", bodyShapeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var full map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &full); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := detectBodyShape(full, tc.provider, tc.model)
			if got != tc.want {
				t.Fatalf("detectBodyShape = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHandlePreRequest_AnthropicBody(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata: RequestMetadata{
			LoginName: "alice",
			Model:     "claude-sonnet-4-5",
			Provider:  "anthropic",
			RequestID: "req-anthropic",
		},
		UserMessage: "play Spotify",
		RequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"play Spotify"}]}`),
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
	sys, _ := modified["system"].(string)
	if !strings.Contains(sys, "/v1/services/spotify") {
		t.Fatalf("anthropic system missing proxy URL: %q", sys)
	}
	msgs := modified["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1 (no system role in Anthropic messages)", len(msgs))
	}
}

func TestHandlePreRequest_ResponsesBody(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata: RequestMetadata{
			LoginName: "alice",
			Model:     "gpt-4o",
			Provider:  "openai",
			RequestID: "req-responses",
		},
		UserMessage: "play Spotify",
		RequestBody: []byte(`{"model":"gpt-4o","input":[{"role":"user","content":"play Spotify"}]}`),
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
	items := modified["input"].([]any)
	sys := items[0].(map[string]any)
	content, _ := sys["content"].(string)
	if sys["role"] != "system" || !strings.Contains(content, "/v1/services/spotify") {
		t.Fatalf("input[0] = %v", sys)
	}
}

func TestHandlePreRequest_UnrecognizedShapeAllowsAndRecords(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "m", RequestID: "req-unknown"},
		UserMessage: "play Spotify",
		RequestBody: []byte(`{"foo":1}`),
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.HandlePreRequest(w, req)

	var resp GuardrailResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Action != ActionAllow {
		t.Fatalf("action = %q, want allow (fail-open)", resp.Action)
	}
	if !strings.Contains(resp.Message, "unrecognized") {
		t.Fatalf("message = %q, want unrecognized shape diagnostic", resp.Message)
	}

	rep := srv.hookHealth.Snapshot("https://aegis.example.com")
	if rep.UnrecognizedShapes == 0 {
		t.Fatal("expected unrecognized_shapes count in hook health")
	}
	found := false
	for _, is := range rep.Issues {
		if is.Code == "unrecognized_body_shape" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v", rep.Issues)
	}
}

func TestHandlePreRequest_RepeatInjectionIdempotent(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-idem"},
		UserMessage: "play Spotify",
		RequestBody: []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"play Spotify"}]}`),
	}
	body, _ := json.Marshal(payload)

	do := func() GuardrailResponse {
		req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		srv.HandlePreRequest(w, req)
		var resp GuardrailResponse
		json.NewDecoder(w.Body).Decode(&resp)
		return resp
	}

	first := do()
	if first.Action != ActionModify {
		t.Fatalf("first action = %q, want modify", first.Action)
	}

	// Feed the already-modified body back through the hook.
	payload.RequestBody = first.RequestBody
	body, _ = json.Marshal(payload)
	second := do()
	if second.Action != ActionAllow {
		t.Fatalf("second action = %q, want allow (already injected)", second.Action)
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
