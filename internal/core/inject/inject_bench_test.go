package inject

import (
	"encoding/json"
	"strings"
	"testing"
)

// Pre-request hook injection benchmarks (issue #75 measurement plan). The
// hook sits on the chat latency path (docs/configuring-aperture.md), and
// injection cost is dominated by the full-body JSON decode/encode round
// trip rather than the instruction merge itself. Run with:
//
//	go test -bench 'BenchmarkRequestInstructions' -benchtime=2s ./internal/core/inject/

const benchFiller = "hook benchmark filler sentence for a realistic message body. "

func benchChatBody(b *testing.B, turns int) []byte {
	b.Helper()
	msgs := make([]map[string]string, 0, turns)
	for i := 0; i < turns; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]string{
			"role":    role,
			"content": strings.Repeat(benchFiller, 60),
		})
	}
	raw, err := json.Marshal(map[string]any{
		"model":       "gpt-4",
		"messages":    msgs,
		"temperature": 0.7,
	})
	if err != nil {
		b.Fatalf("marshal chat body: %v", err)
	}
	return raw
}

func benchAnthropicBody(b *testing.B, turns int) []byte {
	b.Helper()
	msgs := make([]map[string]string, 0, turns)
	for i := 0; i < turns; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]string{
			"role":    role,
			"content": strings.Repeat(benchFiller, 60),
		})
	}
	raw, err := json.Marshal(map[string]any{
		"model":  "claude-sonnet-4-5",
		"system": strings.Repeat(benchFiller, 20),
		"messages": msgs,
		"max_tokens": 1024,
	})
	if err != nil {
		b.Fatalf("marshal anthropic body: %v", err)
	}
	return raw
}

// BenchmarkRequestInstructionsChat injects into an OpenAI-style chat
// envelope with multi-turn content.
func BenchmarkRequestInstructionsChat(b *testing.B) {
	body := benchChatBody(b, 8)
	instructions := strings.Repeat("Aegis library instruction line. ", 40)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := RequestInstructions(body, instructions, "", "")
		if err != nil {
			b.Fatalf("inject chat: %v", err)
		}
		if res.Shape == "" {
			b.Fatal("expected a detected body shape")
		}
	}
}

// BenchmarkRequestInstructionsChatAlreadyInjected is the dedupe path: the
// instruction block is already in the body, so the injector must scan the
// carrier text and leave the body alone.
func BenchmarkRequestInstructionsChatAlreadyInjected(b *testing.B) {
	instructions := strings.Repeat("Aegis library instruction line. ", 40)
	body := benchChatBody(b, 8)
	first, err := RequestInstructions(body, instructions, "", "")
	if err != nil {
		b.Fatalf("seed inject: %v", err)
	}
	b.SetBytes(int64(len(first.Body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := RequestInstructions(first.Body, instructions, "", "")
		if err != nil {
			b.Fatalf("re-inject chat: %v", err)
		}
		if !res.AlreadyPresent {
			b.Fatal("expected AlreadyPresent on the second pass")
		}
	}
}

// BenchmarkRequestInstructionsAnthropic injects into the Anthropic shape
// (top-level system carrier).
func BenchmarkRequestInstructionsAnthropic(b *testing.B) {
	body := benchAnthropicBody(b, 8)
	instructions := strings.Repeat("Aegis library instruction line. ", 40)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := RequestInstructions(body, instructions, "anthropic", "claude-sonnet-4-5"); err != nil {
			b.Fatalf("inject anthropic: %v", err)
		}
	}
}
