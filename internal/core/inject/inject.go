// Package inject inserts system-level instructions into LLM request bodies:
// pure JSON surgery over the Aperture hook payload's request_body, with no
// *Server or *Config references. The hub wires this engine into the
// pre-request hook (issue #346).
package inject

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
)

// ErrUnrecognizedBodyShape is returned when the request body is not one of the
// supported LLM wire formats. Callers fail open (allow) and record diagnostics.
var ErrUnrecognizedBodyShape = errors.New("unrecognized request body shape")

// Result is the outcome of an instruction-injection attempt.
type Result struct {
	Body json.RawMessage
	// Shape is shared.BodyShapeChat, shared.BodyShapeAnthropic,
	// shared.BodyShapeResponses, or shared.BodyShapeUnknown.
	Shape string
	// AlreadyPresent is true when the instruction block was already in the body.
	AlreadyPresent bool
}

// jsonSafeText matches encoding/json's UTF-8 handling: invalid bytes become
// U+FFFD. Injection must dedupe against the text that will actually be
// written, or a second pass fails to see its own instructions (issue #107).
func jsonSafeText(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out string
	if err := json.Unmarshal(b, &out); err != nil {
		return s
	}
	return out
}

// RequestInstructions inserts system-level instructions into an LLM request
// body, choosing the correct carrier for the detected wire shape. Repeated calls
// with the same content are idempotent. provider/model come from Aperture
// metadata and are used when the body alone is ambiguous.
func RequestInstructions(original []byte, content, provider, model string) (Result, error) {
	if len(original) == 0 {
		return Result{Shape: shared.BodyShapeUnknown}, ErrUnrecognizedBodyShape
	}
	// Normalize to the form encoding/json will persist so containsContent
	// sees the same bytes a prior inject wrote.
	content = jsonSafeText(content)
	var full map[string]json.RawMessage
	if err := json.Unmarshal(original, &full); err != nil {
		// Non-object JSON (or corrupt bytes) is not a wire shape we can augment.
		return Result{Shape: shared.BodyShapeUnknown}, ErrUnrecognizedBodyShape
	}
	shape := detectBodyShape(full, provider, model)
	res := Result{Shape: shape}

	if containsContent(instructionCarrierText(full, shape), content) {
		res.Body = original
		res.AlreadyPresent = true
		return res, nil
	}

	var err error
	switch shape {
	case shared.BodyShapeChat:
		err = injectChatMessages(full, content)
	case shared.BodyShapeAnthropic:
		err = injectAnthropicSystem(full, content)
	case shared.BodyShapeResponses:
		err = injectResponsesInput(full, content)
	default:
		return res, ErrUnrecognizedBodyShape
	}
	if err != nil {
		return res, err
	}
	out, err := json.Marshal(full)
	if err != nil {
		return res, err
	}
	res.Body = out
	return res, nil
}

// detectBodyShape prefers explicit Aperture metadata, then sniffs body keys.
func detectBodyShape(full map[string]json.RawMessage, provider, model string) string {
	_, hasMessages := full["messages"]
	_, hasInput := full["input"]
	_, hasSystem := full["system"]
	_, hasInstructions := full["instructions"]
	_, hasPrevResponse := full["previous_response_id"]

	metaAnthropic := metaLooksAnthropic(provider, model)

	// Responses API is the only supported shape that uses `input` (or
	// `instructions` / `previous_response_id`) as conversation carriers and
	// typically has no `messages` array.
	if (hasInput || hasInstructions || hasPrevResponse) && !hasMessages {
		return shared.BodyShapeResponses
	}
	if hasMessages {
		// Top-level `system` is Anthropic's system prompt slot; OpenAI chat
		// keeps system content inside messages[]. Metadata disambiguates
		// Anthropic bodies that omit system on first turn.
		if hasSystem || metaAnthropic {
			return shared.BodyShapeAnthropic
		}
		return shared.BodyShapeChat
	}
	if metaAnthropic {
		return shared.BodyShapeAnthropic
	}
	if hasInput {
		return shared.BodyShapeResponses
	}
	return shared.BodyShapeUnknown
}

func metaLooksAnthropic(provider, model string) bool {
	p := strings.ToLower(provider)
	m := strings.ToLower(model)
	return strings.Contains(p, "anthropic") ||
		strings.Contains(p, "claude") ||
		strings.Contains(m, "claude")
}

// containsContent reports whether needle is already carried in the instruction text.
func containsContent(haystack, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return true
	}
	return strings.Contains(haystack, needle)
}

// instructionCarrierText extracts existing system/instruction text for dedupe.
func instructionCarrierText(full map[string]json.RawMessage, shape string) string {
	switch shape {
	case shared.BodyShapeAnthropic:
		return contentText(full["system"])
	case shared.BodyShapeResponses:
		parts := []string{contentText(full["instructions"])}
		parts = append(parts, inputCarrierText(full["input"])...)
		return strings.Join(parts, "\n")
	default:
		raw, ok := full["messages"]
		if !ok {
			return ""
		}
		var msgs []json.RawMessage
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return ""
		}
		var parts []string
		for _, m := range msgs {
			var obj struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(m, &obj); err != nil {
				continue
			}
			if obj.Role == "system" || obj.Role == "developer" {
				parts = append(parts, contentText(obj.Content))
			}
		}
		return strings.Join(parts, "\n")
	}
}

func inputCarrierText(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return nil // user text, not instruction carrier
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var parts []string
	for _, item := range items {
		var obj struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			continue
		}
		if obj.Role == "system" || obj.Role == "developer" {
			parts = append(parts, contentText(obj.Content))
		}
	}
	return parts
}

// contentText flattens a JSON string or content-block array into plain text.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		var obj struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			continue
		}
		if obj.Text != "" {
			parts = append(parts, obj.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func injectChatMessages(full map[string]json.RawMessage, content string) error {
	raw, ok := full["messages"]
	if !ok {
		return ErrUnrecognizedBodyShape
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return ErrUnrecognizedBodyShape
	}
	sysMsg, err := json.Marshal(map[string]string{
		"role":    "system",
		"content": content,
	})
	if err != nil {
		return err
	}
	msgs = append([]json.RawMessage{sysMsg}, msgs...)
	out, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	full["messages"] = out
	return nil
}

func injectAnthropicSystem(full map[string]json.RawMessage, content string) error {
	raw, ok := full["system"]
	if !ok {
		out, err := json.Marshal(content)
		if err != nil {
			return err
		}
		full["system"] = out
		return nil
	}

	// Preserve Anthropic's system shapes: plain string or content-block array.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		merged := content
		if s != "" {
			merged = content + "\n\n" + s
		}
		out, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		full["system"] = out
		return nil
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ErrUnrecognizedBodyShape
	}
	sysBlock, err := json.Marshal(map[string]string{
		"type": "text",
		"text": content,
	})
	if err != nil {
		return err
	}
	blocks = append([]json.RawMessage{sysBlock}, blocks...)
	out, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	full["system"] = out
	return nil
}

func injectResponsesInput(full map[string]json.RawMessage, content string) error {
	sysMsg, err := json.Marshal(map[string]string{
		"role":    "system",
		"content": content,
	})
	if err != nil {
		return err
	}

	raw, ok := full["input"]
	if !ok {
		// No conversation yet: instructions field is the Responses system slot.
		if err := mergeInstructions(full, content); err != nil {
			return err
		}
		return nil
	}

	// String input is shorthand for a single user message — expand to items.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		userMsg, err := json.Marshal(map[string]string{
			"role":    "user",
			"content": s,
		})
		if err != nil {
			return err
		}
		items := []json.RawMessage{sysMsg, userMsg}
		out, err := json.Marshal(items)
		if err != nil {
			return err
		}
		full["input"] = out
		return nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err == nil {
		items = append([]json.RawMessage{sysMsg}, items...)
		out, err := json.Marshal(items)
		if err != nil {
			return err
		}
		full["input"] = out
		return nil
	}

	// Single input item object.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ErrUnrecognizedBodyShape
	}
	items = []json.RawMessage{sysMsg, raw}
	out, err := json.Marshal(items)
	if err != nil {
		return err
	}
	full["input"] = out
	return nil
}

// mergeInstructions sets or prepends the Responses API `instructions` field.
func mergeInstructions(full map[string]json.RawMessage, content string) error {
	raw, ok := full["instructions"]
	if !ok {
		out, err := json.Marshal(content)
		if err != nil {
			return err
		}
		full["instructions"] = out
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ErrUnrecognizedBodyShape
	}
	merged := content
	if s != "" {
		merged = content + "\n\n" + s
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	full["instructions"] = out
	return nil
}
