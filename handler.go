package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
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
	apiKey string
	config *Config
	cache  *Cache
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
		"provider", payload.Metadata.Provider,
	)

	matched := s.config.FindMatchingRules(
		payload.Metadata.Model,
		payload.Metadata.LoginName,
		payload.UserMessage,
	)

	if len(matched) == 0 {
		logger.Debug("no rules matched, allowing")
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	ruleIDs := make([]string, len(matched))
	for i, r := range matched {
		ruleIDs[i] = r.ID
	}
	logger.Info("rules matched", "rules", ruleIDs)

	// Check if any rule requires blocking.
	for _, rule := range matched {
		if rule.Action == "block" {
			logger.Warn("request blocked", "rule", rule.ID, "reason", rule.Description)
			writeGuardrailResponse(w, GuardrailResponse{
				Action:  ActionBlock,
				Message: fmt.Sprintf("blocked by rule %s: %s", rule.ID, rule.Description),
			})
			return
		}
	}

	// Collect data from sources referenced by matching rules.
	var enrichments []string
	for _, rule := range matched {
		if rule.Action != "inject" && rule.Action != "" {
			continue
		}
		for _, sourceID := range rule.Sources {
			data, err := s.fetchFromSource(sourceID)
			if err != nil {
				logger.Error("source fetch failed", "source", sourceID, "error", err)
				continue
			}
			logger.Debug("source fetched", "source", sourceID, "bytes", len(data))
			enrichments = append(enrichments, fmt.Sprintf("[%s] %s", sourceID, data))
		}
	}

	if len(enrichments) == 0 {
		logger.Debug("no enrichments produced, allowing")
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	// Inject enrichment data into the request body.
	modified, err := injectSystemMessage(payload.RequestBody, strings.Join(enrichments, "\n\n"))
	if err != nil {
		logger.Error("failed to inject system message", "error", err)
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	logger.Info("request modified", "enrichments", len(enrichments))
	writeGuardrailResponse(w, GuardrailResponse{
		Action: ActionModify,
		Body:   modified,
	})
}

func (s *Server) fetchFromSource(sourceID string) (string, error) {
	if cached, ok := s.cache.Get(sourceID); ok {
		slog.Debug("cache hit", "source", sourceID)
		return cached, nil
	}

	src := s.config.GetSource(sourceID)
	if src == nil {
		return "", fmt.Errorf("source %q not found", sourceID)
	}

	var data string
	var err error

	switch src.Type {
	case "api":
		data, err = s.fetchAPI(src)
	default:
		return "", fmt.Errorf("unsupported source type: %s", src.Type)
	}

	if err != nil {
		return "", err
	}

	ttl := src.CacheTTL.Duration
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	s.cache.Set(sourceID, data, ttl)
	slog.Debug("cache set", "source", sourceID, "ttl", ttl, "bytes", len(data))
	return data, nil
}

func (s *Server) fetchAPI(src *Source) (string, error) {
	req, err := http.NewRequest("GET", src.URL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range src.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	slog.Debug("api fetch complete", "source", src.ID, "status", resp.StatusCode, "bytes", len(data))
	return string(data), nil
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
