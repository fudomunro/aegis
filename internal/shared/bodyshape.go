package shared

// Request body wire shapes Aegis can augment (issue #103). The injection
// engine (internal/core/inject) produces these tokens and the hook-health
// engine (internal/core/hookhealth) records them on HookObservation.BodyShape,
// so the vocabulary lives here as seam vocabulary — the shape two internal
// packages exchange to agree on a contract.
const (
	BodyShapeChat      = "chat"      // OpenAI chat completions: top-level messages[]
	BodyShapeAnthropic = "anthropic" // Anthropic Messages API: top-level system + messages[]
	BodyShapeResponses = "responses" // OpenAI Responses API: top-level input
	BodyShapeUnknown   = "unknown"
)
