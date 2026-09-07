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

	// Collect data from injectable services.
	var enrichments []string
	for _, svc := range matched {
		if svc.Action != "inject" {
			continue
		}
		data, err := s.fetchServiceData(svc)
		if err != nil {
			logger.Error("service data fetch failed", "service", svc.ID, "error", err)
			continue
		}
		logger.Debug("service data fetched", "service", svc.ID, "bytes", len(data))
		enrichments = append(enrichments, fmt.Sprintf("[%s] %s", svc.Name, data))
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

// fetchServiceData fetches data from a service's endpoint, using the cache when available.
func (s *Server) fetchServiceData(svc Service) (string, error) {
	if cached, ok := s.cache.Get(svc.ID); ok {
		slog.Debug("cache hit", "service", svc.ID)
		return cached, nil
	}

	if svc.Endpoint == "" {
		return "", fmt.Errorf("service %q has no endpoint configured", svc.ID)
	}

	req, err := http.NewRequest("GET", svc.Endpoint, nil)
	if err != nil {
		return "", err
	}
	for k, v := range svc.Headers {
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

	slog.Debug("service fetch complete", "service", svc.ID, "status", resp.StatusCode, "bytes", len(data))

	ttl := svc.CacheTTL.Duration
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	s.cache.Set(svc.ID, string(data), ttl)
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
