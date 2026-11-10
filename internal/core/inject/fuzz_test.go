package inject

import (
	"encoding/json"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

// Fuzz targets for the Aperture hook payload decode + injection path
// (issue #107). Hostile LLM request bodies must never panic the hook.

func FuzzInjectRequestInstructions(f *testing.F) {
	// Seed common wire shapes plus hostile JSON.
	f.Add([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`), "Aegis instructions", "openai", "gpt-4o")
	f.Add([]byte(`{"model":"claude-3","system":"you are helpful","messages":[]}`), "SYS", "anthropic", "claude-3")
	f.Add([]byte(`{"model":"gpt-4o","input":"hello"}`), "SYS", "openai", "gpt-4o")
	f.Add([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), "SYS", "", "")
	f.Add([]byte(`{}`), "SYS", "", "")
	f.Add([]byte(`[]`), "SYS", "", "")
	f.Add([]byte(`null`), "SYS", "", "")
	f.Add([]byte(`{"messages":"not-an-array"}`), "SYS", "", "")
	f.Add([]byte(`{"messages":[1,2,3]}`), "SYS", "", "")
	f.Add([]byte(`{"system":[{"type":"text","text":"old"}],"messages":[]}`), "NEW", "anthropic", "claude")
	f.Add([]byte(`{"input":[{"role":"user","content":"x"}]}`), "NEW", "", "")
	f.Add([]byte(`{"instructions":"old","input":"hi"}`), "NEW", "", "")
	f.Add([]byte("\x00\xff{\"messages\":[]}"), "SYS", "", "")
	f.Fuzz(func(t *testing.T, original []byte, content, provider, model string) {
		res, err := RequestInstructions(original, content, provider, model)
		if err != nil {
			return
		}
		// Success must produce parseable JSON when a body is returned.
		if len(res.Body) > 0 {
			var check map[string]json.RawMessage
			if jsonErr := json.Unmarshal(res.Body, &check); jsonErr != nil {
				// injectSystemMessage path returns the marshaled map; if shape
				// was recognized, body should be a JSON object.
				if res.Shape != shared.BodyShapeUnknown {
					t.Fatalf("successful inject returned non-object JSON: %v", jsonErr)
				}
			}
		}

		// Idempotence: a second inject of the same content must not grow
		// another copy of the instructions (AlreadyPresent or stable body).
		if content == "" {
			return
		}
		var base []byte
		if len(res.Body) > 0 {
			base = res.Body
		} else {
			base = original
		}
		second, err2 := RequestInstructions(base, content, provider, model)
		if err2 != nil {
			return
		}
		if !second.AlreadyPresent {
			t.Fatalf("injection not idempotent for shape %q (first already=%v)", second.Shape, res.AlreadyPresent)
		}
	})
}

func FuzzDetectBodyShape(f *testing.F) {
	f.Add([]byte(`{"messages":[]}`), "", "")
	f.Add([]byte(`{"system":"x","messages":[]}`), "anthropic", "claude-3")
	f.Add([]byte(`{"input":[]}`), "", "")
	f.Add([]byte(`{"instructions":"x"}`), "", "")
	f.Add([]byte(`{}`), "anthropic", "claude")
	f.Add([]byte(`[1]`), "", "")
	f.Add([]byte(`{"messages":"bad"}`), "", "")
	f.Fuzz(func(t *testing.T, body []byte, provider, model string) {
		var full map[string]json.RawMessage
		if err := json.Unmarshal(body, &full); err != nil {
			return
		}
		shape := detectBodyShape(full, provider, model)
		switch shape {
		case shared.BodyShapeChat, shared.BodyShapeAnthropic, shared.BodyShapeResponses, shared.BodyShapeUnknown:
		default:
			t.Fatalf("unexpected shape %q", shape)
		}
		// Carrier text extraction must be total on any decoded object.
		_ = instructionCarrierText(full, shape)
	})
}
