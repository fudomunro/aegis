package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// Aperture request payload for pre_request hooks.
type PreRequestPayload struct {
	Metadata    RequestMetadata `json:"metadata"`
	RequestBody json.RawMessage `json:"request_body,omitempty"`
	UserMessage string          `json:"user_message,omitempty"`
}

type RequestMetadata struct {
	LoginName string `json:"login_name"`
	UserAgent string `json:"user_agent"`
	URL       string `json:"url"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Tailnet   string `json:"tailnet_name"`
	NodeID    string `json:"stable_node_id"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
}

// Guardrail response actions.
const (
	ActionAllow  = "allow"
	ActionBlock  = "block"
	ActionModify = "modify"
)

type GuardrailResponse struct {
	Action  string          `json:"action"`
	Body    json.RawMessage `json:"body,omitempty"`    // replacement request body for "modify"
	Message string          `json:"message,omitempty"` // human-readable reason
}

type Server struct {
	apiKey      string
	baseURL     string
	config      *Config
	cache       *Cache
	tokens      *TokenStore
	oauthStates *StateStore
	connectors  map[string]ServiceConnector
}

// ServiceConnector is an optional interface for services that provide
// intelligent proxying beyond simple request forwarding.
type ServiceConnector interface {
	// Capabilities returns what data types and operations this connector supports.
	Capabilities() ServiceCapabilities
	// SystemInstructions returns the system message for agents referencing this service.
	SystemInstructions(baseURL string) string
	// HandleProxy handles a proxied request for this service.
	HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string)
}

// ServiceCapabilities describes what a connector can do.
type ServiceCapabilities struct {
	DataTypes []DataTypeCapability `json:"data_types"`
}

// DataTypeCapability describes supported operations for a specific data type.
type DataTypeCapability struct {
	Name        string      `json:"name"`        // e.g. "tracks", "playlists", "search"
	Description string      `json:"description"` // human-readable description
	Operations  []Operation `json:"operations"`
}

// Operation describes a single callable operation on a data type.
type Operation struct {
	Name        string       `json:"name"`                  // "read_saved", "search", "get_by_id"
	Description string       `json:"description"`           // human-readable
	Method      string       `json:"method"`                // "GET", "POST", etc.
	Path        string       `json:"path"`                  // "/me/tracks"
	Parameters  []Parameter  `json:"parameters,omitempty"`
	Response    ResponseSpec `json:"response"`
}

// Parameter describes a query or path parameter for an operation.
type Parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`          // "query" or "path"
	Type        string `json:"type"`        // "string", "integer", "boolean"
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ResponseSpec describes what the operation returns.
type ResponseSpec struct {
	Description string `json:"description"`
	Fields      []string `json:"fields,omitempty"` // top-level field names in the response
}

func (s *Server) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+s.apiKey {
			apiKey := r.Header.Get("X-Api-Key")
			if apiKey != s.apiKey {
				slog.Warn("unauthorized request", "remote", r.RemoteAddr)
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) HandlePreRequest(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("failed to read request body", "error", err)
		writeGuardrailResponse(w, GuardrailResponse{
			Action:  ActionAllow,
			Message: "failed to read request body",
		})
		return
	}

	var payload PreRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		slog.Error("failed to parse payload", "error", err)
		writeGuardrailResponse(w, GuardrailResponse{
			Action:  ActionAllow,
			Message: "failed to parse payload",
		})
		return
	}

	logger := slog.With(
		"request_id", payload.Metadata.RequestID,
		"user", payload.Metadata.LoginName,
		"model", payload.Metadata.Model,
	)

	matched := s.config.FindMatchingServices(payload.UserMessage)

	if len(matched) == 0 {
		logger.Debug("no services matched, allowing")
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	serviceIDs := make([]string, len(matched))
	for i, svc := range matched {
		serviceIDs[i] = svc.ID
	}
	logger.Info("services matched", "services", serviceIDs)

	// If any matched service is blocked, reject the request.
	for _, svc := range matched {
		if svc.Action == "block" {
			logger.Warn("request blocked by service policy", "service", svc.ID, "reason", svc.Description)
			writeGuardrailResponse(w, GuardrailResponse{
				Action:  ActionBlock,
				Message: fmt.Sprintf("blocked: access to %s is not allowed. %s", svc.Name, svc.Description),
			})
			return
		}
	}

	// Build instructions for the agent to use Guardian's proxy endpoints.
	var instructions []string
	for _, svc := range matched {
		if svc.Action != "inject" {
			continue
		}
		// Use connector-specific instructions if available.
		if connector, ok := s.connectors[svc.ID]; ok {
			instructions = append(instructions, connector.SystemInstructions(s.baseURL))
		} else {
			proxyURL := fmt.Sprintf("%s/v1/services/%s", strings.TrimRight(s.baseURL, "/"), svc.ID)
			instruction := fmt.Sprintf(
				"When working with %s, you MUST use the Guardian proxy endpoint at %s "+
					"instead of calling %s directly. All requests through this proxy are "+
					"authenticated, cached, and audited.",
				svc.Name, proxyURL, svc.Name,
			)
			instructions = append(instructions, instruction)
		}
	}

	if len(instructions) == 0 {
		logger.Debug("no injectable services, allowing")
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	// Inject instructions into the request body.
	systemContent := strings.Join(instructions, "\n\n")
	modified, err := injectSystemMessage(payload.RequestBody, systemContent)
	if err != nil {
		logger.Error("failed to inject system message", "error", err)
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	logger.Info("request modified with proxy instructions", "services", len(instructions))
	writeGuardrailResponse(w, GuardrailResponse{
		Action: ActionModify,
		Body:   modified,
	})
}

// injectSystemMessage inserts a system message into an OpenAI-compatible request body.
func injectSystemMessage(original []byte, content string) (json.RawMessage, error) {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(original, &req); err != nil {
		return nil, err
	}

	sysMsg, _ := json.Marshal(map[string]string{
		"role":    "system",
		"content": content,
	})

	// Prepend the system message.
	req.Messages = append([]json.RawMessage{sysMsg}, req.Messages...)

	// Rebuild the full body, preserving all other top-level fields.
	var full map[string]json.RawMessage
	if err := json.Unmarshal(original, &full); err != nil {
		return nil, err
	}
	full["messages"], _ = json.Marshal(req.Messages)
	return json.Marshal(full)
}

func writeGuardrailResponse(w http.ResponseWriter, resp GuardrailResponse) {
	w.Header().Set("Content-Type", "application/json")
	if resp.Action == ActionBlock {
		w.WriteHeader(http.StatusForbidden)
	}
	json.NewEncoder(w).Encode(resp)
}
