package inject

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

func TestRequestInstructions_Chat(t *testing.T) {
	original := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"temperature": 0.7,
	}
	origBytes, _ := json.Marshal(original)

	injected, err := RequestInstructions(origBytes, "injected context", "", "")
	if err != nil {
		t.Fatalf("RequestInstructions: %v", err)
	}
	modified := injected.Body

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

func TestRequestInstructions_InvalidJSON(t *testing.T) {
	_, err := RequestInstructions([]byte("{bad"), "test", "", "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestRequestInstructions_Anthropic(t *testing.T) {
	original := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 1024,
		"system":     "You are helpful.",
		"messages": []map[string]string{
			{"role": "user", "content": "play Spotify"},
		},
	}
	origBytes, _ := json.Marshal(original)

	res, err := RequestInstructions(origBytes, "USE AEGIS PROXY", "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("RequestInstructions: %v", err)
	}
	if res.Shape != shared.BodyShapeAnthropic {
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

func TestRequestInstructions_AnthropicSystemBlocks(t *testing.T) {
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

	res, err := RequestInstructions(origBytes, "USE AEGIS PROXY", "", "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("RequestInstructions: %v", err)
	}
	if res.Shape != shared.BodyShapeAnthropic {
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

func TestRequestInstructions_Responses(t *testing.T) {
	original := map[string]any{
		"model": "gpt-4o",
		"input": []map[string]string{
			{"role": "user", "content": "play Spotify"},
		},
	}
	origBytes, _ := json.Marshal(original)

	res, err := RequestInstructions(origBytes, "USE AEGIS PROXY", "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("RequestInstructions: %v", err)
	}
	if res.Shape != shared.BodyShapeResponses {
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

func TestRequestInstructions_ResponsesStringInput(t *testing.T) {
	original := map[string]any{
		"model": "gpt-4o",
		"input": "play Spotify",
	}
	origBytes, _ := json.Marshal(original)

	res, err := RequestInstructions(origBytes, "USE AEGIS PROXY", "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("RequestInstructions: %v", err)
	}
	if res.Shape != shared.BodyShapeResponses {
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

func TestRequestInstructions_ResponsesNoInputUsesInstructions(t *testing.T) {
	original := map[string]any{
		"model":                "gpt-4o",
		"previous_response_id": "resp_123",
	}
	origBytes, _ := json.Marshal(original)

	res, err := RequestInstructions(origBytes, "USE AEGIS PROXY", "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("RequestInstructions: %v", err)
	}
	if res.Shape != shared.BodyShapeResponses {
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

func TestRequestInstructions_Idempotent(t *testing.T) {
	original := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "play Spotify"},
		},
	}
	origBytes, _ := json.Marshal(original)
	const content = "USE AEGIS PROXY"

	first, err := RequestInstructions(origBytes, content, "", "")
	if err != nil {
		t.Fatalf("first inject: %v", err)
	}
	if first.AlreadyPresent {
		t.Fatal("first inject should modify")
	}

	second, err := RequestInstructions(first.Body, content, "", "")
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
	again, err := RequestInstructions(anthBytes, content, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("anthropic re-inject: %v", err)
	}
	if !again.AlreadyPresent {
		t.Fatal("anthropic re-inject should be idempotent")
	}
}

func TestRequestInstructions_UnrecognizedShape(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"foo":"bar"}`,
		`[1,2,3]`,
		`"just a string"`,
	} {
		res, err := RequestInstructions([]byte(body), "USE AEGIS PROXY", "openai", "gpt-4o")
		if !errors.Is(err, ErrUnrecognizedBodyShape) {
			t.Fatalf("body %s: err = %v, want ErrUnrecognizedBodyShape", body, err)
		}
		if res.Shape != shared.BodyShapeUnknown {
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
		{"chat", `{"messages":[{"role":"user","content":"hi"}]}`, "", "gpt-4", shared.BodyShapeChat},
		{"anthropic_system", `{"system":"s","messages":[]}`, "anthropic", "claude-x", shared.BodyShapeAnthropic},
		{"anthropic_meta", `{"messages":[]}`, "anthropic", "claude-x", shared.BodyShapeAnthropic},
		{"anthropic_model_only", `{"messages":[]}`, "", "claude-3-5-sonnet", shared.BodyShapeAnthropic},
		{"responses", `{"input":"hi"}`, "openai", "gpt-4o", shared.BodyShapeResponses},
		{"responses_prev", `{"previous_response_id":"resp_1"}`, "openai", "gpt-4o", shared.BodyShapeResponses},
		{"responses_instructions", `{"instructions":"sys"}`, "openai", "gpt-4o", shared.BodyShapeResponses},
		{"unknown", `{"foo":1}`, "", "", shared.BodyShapeUnknown},
		{"unknown_model_only", `{"model":"gpt-4o"}`, "openai", "gpt-4o", shared.BodyShapeUnknown},
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
