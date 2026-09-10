package main

import (
	"context"
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

// AuthMode selects how Guardian authenticates inbound API requests.
const (
	// AuthModeAuto accepts Tailscale identity when present, otherwise an API key.
	// Recommended default for tailnet-only deployments that also keep a key
	// for external bridges.
	AuthModeAuto = "auto"
	// AuthModeTailnet requires Tailscale identity (Serve headers or WhoIs).
	// API key is not used. This is the recommended mode when Guardian is
	// only reachable inside the tailnet.
	AuthModeTailnet = "tailnet"
	// AuthModeAPIKey requires the shared API key only (legacy).
	AuthModeAPIKey = "api-key"
)

type Server struct {
	apiKey string
	// authMode is AuthModeAuto, AuthModeTailnet, or AuthModeAPIKey.
	authMode string
	// allowedLogins, when non-empty, restricts which tailnet identities may
	// call the API. Empty means any authenticated tailnet peer is allowed.
	allowedLogins []string
	// identity resolves Tailscale peer identity. Optional in api-key mode.
	identity    IdentityResolver
	baseURL     string
	config      *Config
	cache       *Cache
	tokens      *TokenStore
	oauthStates *StateStore
	connectors  map[string]ServiceConnector
}

// AuthMiddleware authenticates requests according to the configured auth mode.
// Successful tailnet identity is attached to the request context for logging.
func (s *Server) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, idErr := s.resolveIdentity(r)

		switch s.authMode {
		case AuthModeAPIKey:
			if !s.checkAPIKey(r) {
				s.deny(w, r, nil, "missing or invalid API key")
				return
			}
			next(w, r)
			return
		case AuthModeTailnet:
			if id == nil {
				if idErr != nil {
					slog.Warn("tailnet identity resolve failed", "error", idErr, "remote", r.RemoteAddr)
				}
				s.deny(w, r, nil, "tailnet identity required — connect through Tailscale Serve or run on a tailscaled host")
				return
			}
			if !loginAllowed(id, s.allowedLogins) {
				s.deny(w, r, id, "identity not in allowed logins")
				return
			}
			next(w, withPeerIdentity(r, id))
			return
		default: // AuthModeAuto
			if id != nil && loginAllowed(id, s.allowedLogins) {
				next(w, withPeerIdentity(r, id))
				return
			}
			if s.apiKey != "" && s.checkAPIKey(r) {
				next(w, r)
				return
			}
			if id != nil {
				s.deny(w, r, id, "identity not in allowed logins")
				return
			}
			s.deny(w, r, nil, "unauthorized: provide a valid API key or connect from the tailnet")
			return
		}
	}
}

func (s *Server) resolveIdentity(r *http.Request) (*PeerIdentity, error) {
	if s.identity == nil {
		return nil, nil
	}
	return s.identity.Resolve(r.Context(), r)
}

func (s *Server) checkAPIKey(r *http.Request) bool {
	if s.apiKey == "" {
		return false
	}
	if r.Header.Get("Authorization") == "Bearer "+s.apiKey {
		return true
	}
	return r.Header.Get("X-Api-Key") == s.apiKey
}

func (s *Server) deny(w http.ResponseWriter, r *http.Request, id *PeerIdentity, reason string) {
	attrs := []any{"remote", r.RemoteAddr, "path", r.URL.Path, "reason", reason}
	if id != nil {
		attrs = append(attrs, "login", id.LoginName, "identity_source", id.Source)
	}
	slog.Warn("unauthorized request", attrs...)
	writeJSONError(w, http.StatusUnauthorized, "unauthorized")
}

type peerIdentityKey struct{}

func withPeerIdentity(r *http.Request, id *PeerIdentity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), peerIdentityKey{}, id))
}

// PeerIdentityFrom returns the Tailscale identity attached by AuthMiddleware,
// or nil when the request was authenticated with an API key.
func PeerIdentityFrom(r *http.Request) *PeerIdentity {
	id, _ := r.Context().Value(peerIdentityKey{}).(*PeerIdentity)
	return id
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
	Name        string       `json:"name"`        // "read_saved", "search", "get_by_id"
	Description string       `json:"description"` // human-readable
	Method      string       `json:"method"`      // "GET", "POST", etc.
	Path        string       `json:"path"`        // "/me/tracks"
	Parameters  []Parameter  `json:"parameters,omitempty"`
	Response    ResponseSpec `json:"response"`
}

// Parameter describes a query or path parameter for an operation.
type Parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`   // "query" or "path"
	Type        string `json:"type"` // "string", "integer", "boolean"
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ResponseSpec describes what the operation returns.
type ResponseSpec struct {
	Description string   `json:"description"`
	Fields      []string `json:"fields,omitempty"` // top-level field names in the response
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

	// Users often ask for "my playlists" without naming a service. Surface the
	// unified library endpoint in that case so agents can return every service.
	libraryOnly := len(matched) == 0 && mentionsPlaylists(payload.UserMessage) && s.hasPlaylistProviders()

	if len(matched) == 0 && !libraryOnly {
		logger.Debug("no services matched, allowing")
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	serviceIDs := make([]string, len(matched))
	for i, svc := range matched {
		serviceIDs[i] = svc.ID
	}
	if len(matched) > 0 {
		logger.Info("services matched", "services", serviceIDs)
	}

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

	if libraryOnly {
		instructions = append(instructions, s.librarySystemInstructions())
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

// hasPlaylistProviders reports whether any registered connector can list
// neutral playlists for the library endpoint.
func (s *Server) hasPlaylistProviders() bool {
	for _, connector := range s.connectors {
		if _, ok := connector.(PlaylistProvider); ok {
			return true
		}
	}
	return false
}

// librarySystemInstructions tells agents about the cross-service library endpoint.
func (s *Server) librarySystemInstructions() string {
	root := strings.TrimRight(s.baseURL, "/")
	return fmt.Sprintf(
		"Unified music library (use when the user asks about playlists or their music library):\n"+
			"GET %s/v1/library/playlists — all user playlists from every connected service in one "+
			"neutral list (name, description, owner, track_count, sources[]).\n"+
			"Same-named playlists are synced into one entry with multiple sources[]. "+
			"Prefer this endpoint for questions like \"what playlists do I have?\" so the answer includes "+
			"Spotify, YouTube Music, Amazon Music, and any other connected service.",
		root,
	)
}

// mentionsPlaylists detects library-oriented questions that don't name a service.
func mentionsPlaylists(msg string) bool {
	lower := strings.ToLower(msg)
	for _, keyword := range []string{"playlist", "play list", "music library", "my library", "saved playlists"} {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}
