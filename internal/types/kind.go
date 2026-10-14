package types

import (
	"fmt"
	"strings"
)

// LibraryKind describes one unified library dataset (type-layer metadata).
// Runtime visibility predicates (HasProviders / Readable) live on the core
// registration wrapper, not on this data struct (issue #15).
type LibraryKind struct {
	// ID is the short name ("tracks") used in cache keys and logs.
	ID string
	// Path is the agent-facing endpoint ("/v1/library/tracks").
	Path string
	// PermType is the permissions map key and path classifier type.
	PermType string
	// Label is the human name used in messages ("liked tracks").
	Label string
	// Usage is how agents should think about the dataset (instructions).
	Usage string
	// Summary describes the neutral list shape.
	Summary string
	// SyncRule explains cross-service merge behavior.
	SyncRule string
	// ExampleQuestion is a canonical user prompt.
	ExampleQuestion string
	// Keywords detect service-less questions that should inject this endpoint.
	Keywords []string
	// EmptyMsg / DenyMsg / FailMsg are API error strings.
	EmptyMsg string
	DenyMsg  string
	FailMsg  string
}

// Mentions reports whether the user message looks like a question about this
// dataset without naming a service.
func (k LibraryKind) Mentions(msg string) bool {
	lower := strings.ToLower(msg)
	for _, keyword := range k.Keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// Instructions builds the system-message hint for this library endpoint.
func (k LibraryKind) Instructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(
		"Unified %s library (use when the user asks about %s):\n"+
			"GET %s%s — %s.\n"+
			"%s Prefer this endpoint for questions like %q so the answer includes "+
			"every connected service.\n"+
			"Check response.truncated (and by_service[].truncated) before claiming the list is complete; "+
			"when true, say so and note that some items may still be missing upstream.\n"+
			"sources[].canonical marks the authoritative non-Aegis location for that item "+
			"(issue #16); prefer that service for writes or mirrors.",
		k.Label, k.Usage, root, k.Path, k.Summary, k.SyncRule, k.ExampleQuestion,
	)
}

// CacheKey is the in-memory cache key for the aggregate response.
func (k LibraryKind) CacheKey() string { return "library:" + k.ID }
